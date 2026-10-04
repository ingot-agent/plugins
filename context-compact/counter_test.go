package contextcompact

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type resolverFunc func(context.Context, model.Request) (model.Request, error)

func (f resolverFunc) ResolveRequest(ctx context.Context, request model.Request) (model.Request, error) {
	return f(ctx, request)
}

type testProfile struct {
	source   string
	accuracy usage.Accuracy
	count    int64
	err      error
	calls    atomic.Int32
	entered  chan struct{}
	release  chan struct{}
}

func (p *testProfile) CountInput(ctx context.Context, _ model.Request) (int64, error) {
	p.calls.Add(1)
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return p.count, p.err
}

func (p *testProfile) Accuracy() usage.Accuracy { return p.accuracy }
func (p *testProfile) Source() string           { return p.source }

func passthroughResolver(_ context.Context, request model.Request) (model.Request, error) {
	return cloneRequest(request), nil
}

func TestUnicodeEstimateProfileFixedVector(t *testing.T) {
	t.Parallel()
	profile := unicodeEstimateProfile{}
	count, err := profile.CountInput(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: textContent("hello世界")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// reply priming 3 + message framing 4 + role 1 + "hello" 2 + CJK 2
	if count != 12 {
		t.Fatalf("count=%d, want 12", count)
	}
	if profile.Accuracy() != usage.AccuracyEstimate || profile.Source() != unicodeEstimateSource {
		t.Fatalf("accuracy=%q source=%q", profile.Accuracy(), profile.Source())
	}
}

func TestUnicodeEstimateToolEnvelopeFixedVector(t *testing.T) {
	t.Parallel()
	count, err := (unicodeEstimateProfile{}).CountInput(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: model.RoleAssistant, Name: "agent", Content: textContent("ok"), ToolCalls: []tool.Call{
				{ID: "call", Name: "read", Arguments: json.RawMessage(`{"path":"世界"}`)},
			}},
			{Role: model.RoleTool, ToolCallID: "call", Content: textContent("done")},
		},
		Tools: []tool.Definition{{Name: "read", Description: "Read file", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	// Reply priming 3, assistant envelope and call 25, tool result 7, definition 21.
	if err != nil || count != 56 {
		t.Fatalf("tool envelope count = %d, %v; want 56", count, err)
	}
}

func TestCountSkipsNonTextParts(t *testing.T) {
	counter := newCounter(resolverFunc(passthroughResolver), unicodeEstimateProfile{}, 1)
	inline := []byte("image")
	request := usage.CountRequest{Invocation: model.Request{
		Provider: "p", Model: "model",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: content.Content{
				content.Text("hello"),
				content.Inline(content.KindImage, "image/png", "x.png", inline),
				content.Text("世界"),
			}},
			{Role: model.RoleUser, Content: content.Content{
				content.Inline(content.KindAudio, "audio/wav", "clip.wav", []byte("audio")),
			}},
		},
	}}
	result, err := counter.CountInput(context.Background(), request)
	if err != nil || result.InputTokens != 17 || result.Source != unicodeEstimateSource {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if string(inline) != "image" {
		t.Fatalf("caller media was mutated: %q", inline)
	}
}

func TestMediaDoesNotChangeTextEstimateCacheKey(t *testing.T) {
	profile := &testProfile{source: unicodeEstimateSource, accuracy: usage.AccuracyEstimate, count: 10}
	counter := newCounter(resolverFunc(passthroughResolver), profile, 2)
	for _, payload := range []string{"first", "second"} {
		request := usage.CountRequest{Invocation: model.Request{Provider: "p", Model: "model", Messages: []model.Message{{
			Role: model.RoleUser,
			Content: content.Content{
				content.Text("same"),
				content.Inline(content.KindImage, "image/png", "x.png", []byte(payload)),
			},
		}}}}
		if _, err := counter.CountInput(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if profile.calls.Load() != 1 {
		t.Fatalf("profile called %d times, want 1", profile.calls.Load())
	}
}

func TestCacheHitEvictionAndClosedState(t *testing.T) {
	t.Parallel()
	profile := &testProfile{source: "test-v1", accuracy: usage.AccuracyExact, count: 42}
	counter := newCounter(resolverFunc(passthroughResolver), profile, 1)
	first := usage.CountRequest{Invocation: validRequest("one")}
	result, err := counter.CountInput(context.Background(), first)
	if err != nil || result.InputTokens != 42 || result.Accuracy != usage.AccuracyExact {
		t.Fatalf("first result=%#v error=%v", result, err)
	}
	if _, err = counter.CountInput(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if profile.calls.Load() != 1 {
		t.Fatalf("cache calls=%d", profile.calls.Load())
	}
	if _, err = counter.CountInput(context.Background(), usage.CountRequest{Invocation: validRequest("two")}); err != nil {
		t.Fatal(err)
	}
	if _, err = counter.CountInput(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if profile.calls.Load() != 3 {
		t.Fatalf("eviction calls=%d", profile.calls.Load())
	}
	counter.close()
	if _, err = counter.CountInput(context.Background(), first); !errors.Is(err, errCounterClosed) {
		t.Fatalf("closed error=%v", err)
	}
}

func TestProfileFailuresAndNegativeCountsAreClassified(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("tokenizer failed")
	for _, profile := range []*testProfile{
		{source: "error-v1", accuracy: usage.AccuracyEstimate, err: wantErr},
		{source: "negative-v1", accuracy: usage.AccuracyEstimate, count: -1},
	} {
		counter := newCounter(resolverFunc(passthroughResolver), profile, 1)
		_, err := counter.CountInput(context.Background(), usage.CountRequest{Invocation: validRequest("secret-prompt")})
		if !errors.Is(err, ErrInvalidCount) {
			t.Fatalf("source=%q error=%v", profile.source, err)
		}
		if profile.err != nil && !errors.Is(err, wantErr) {
			t.Fatalf("source=%q lost profile error: %v", profile.source, err)
		}
		if strings.Contains(err.Error(), "secret-prompt") {
			t.Fatalf("source=%q leaked prompt: %v", profile.source, err)
		}
	}
}

func TestSameKeySingleFlightAndCanceledWaiter(t *testing.T) {
	t.Parallel()
	profile := &testProfile{
		source: "blocking-v1", accuracy: usage.AccuracyEstimate, count: 7,
		entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	counter := newCounter(resolverFunc(passthroughResolver), profile, 2)
	request := usage.CountRequest{Invocation: validRequest("same")}
	firstDone := make(chan error, 1)
	go func() {
		_, err := counter.CountInput(context.Background(), request)
		firstDone <- err
	}()
	select {
	case <-profile.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("profile did not start")
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	waitDone := make(chan error, 1)
	go func() {
		_, err := counter.CountInput(waitCtx, request)
		waitDone <- err
	}()
	cancel()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter did not return")
	}
	close(profile.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if profile.calls.Load() != 1 {
		t.Fatalf("profile calls=%d", profile.calls.Load())
	}
}

func TestDifferentKeysCountConcurrently(t *testing.T) {
	t.Parallel()
	profile := &testProfile{source: "parallel-v1", accuracy: usage.AccuracyEstimate, count: 1, entered: make(chan struct{}, 2), release: make(chan struct{})}
	counter := newCounter(resolverFunc(passthroughResolver), profile, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for _, content := range []string{"one", "two"} {
		go func() {
			defer wait.Done()
			_, _ = counter.CountInput(context.Background(), usage.CountRequest{Invocation: validRequest(content)})
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-profile.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("different keys did not count concurrently")
		}
	}
	close(profile.release)
	wait.Wait()
	if profile.calls.Load() != 2 {
		t.Fatalf("profile calls=%d", profile.calls.Load())
	}
}

func validRequest(content string) model.Request {
	return model.Request{Provider: "p", Model: "model", Messages: []model.Message{{Role: model.RoleUser, Content: textContent(content)}}}
}
