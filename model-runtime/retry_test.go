package modelruntime_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	modelruntime "github.com/ingot-agent/plugins/model-runtime"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
)

type retryableError struct{ delay time.Duration }

func (e retryableError) Error() string                     { return "temporary" }
func (e retryableError) RetryDelay() (time.Duration, bool) { return e.delay, true }

func retryExports(t *testing.T, entry model.ProviderEntry, deps modelruntime.Dependencies) modelruntime.Exports {
	t.Helper()
	deps.ProviderSources = []model.ProviderSource{fixedProviderSource(entry)}
	exports, _, err := modelruntime.New(context.Background(), withState(t, modelruntime.Config{DefaultModel: "m"}, deps))
	if err != nil {
		t.Fatal(err)
	}
	return exports
}
func goodResponse() model.Response {
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("ok")}}
}

func TestCompleteRetriesOnlyProviderHint(t *testing.T) {
	for _, tc := range []struct {
		name        string
		providerErr error
		wantCalls   int
	}{
		{"temporary", retryableError{}, 3},
		{"permanent", errors.New("permanent"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			intercepts := 0
			exports := retryExports(t, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) {
				calls++
				if calls < 3 {
					return model.Response{}, tc.providerErr
				}
				return goodResponse(), nil
			}}, modelruntime.Dependencies{Interceptors: []model.Interceptor{interceptorFunc(func(ctx context.Context, req model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
				intercepts++
				return next(ctx, req)
			})}})
			_, err := exports.Runtime.Complete(context.Background(), model.Request{})
			if calls != tc.wantCalls || intercepts != 1 || (err != nil) != (tc.wantCalls == 1) {
				t.Fatalf("calls=%d intercepts=%d err=%v", calls, intercepts, err)
			}
		})
	}
}

func TestCompleteRetryKeepsRequestAndStopsAtLimit(t *testing.T) {
	calls := 0
	var observed []string
	temporary := retryableError{}
	exports := retryExports(t, model.ProviderEntry{Name: "p", Complete: func(_ context.Context, request model.Request) (model.Response, error) {
		calls++
		observed = append(observed, request.Messages[0].Content[0].Text)
		request.Messages[0].Content[0].Text = "mutated"
		return model.Response{}, temporary
	}}, modelruntime.Dependencies{})
	_, err := exports.Runtime.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("original")}}})
	if !errors.Is(err, temporary) || calls != 3 || !reflect.DeepEqual(observed, []string{"original", "original", "original"}) {
		t.Fatalf("calls=%d observed=%v err=%v", calls, observed, err)
	}
}

func TestStreamRetryBeforeFirstEventOnly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		emit      bool
		wantCalls int
	}{
		{"before", false, 2}, {"after start", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			events := 0
			exports := retryExports(t, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { return goodResponse(), nil }, Stream: func(_ context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
				calls++
				if calls == 1 {
					if tc.emit {
						if err := handler(model.StreamEvent{Kind: model.StreamPartStart, PartKind: content.KindText}); err != nil {
							return model.Response{}, err
						}
					}
					return model.Response{}, retryableError{}
				}
				for _, e := range []model.StreamEvent{{Kind: model.StreamPartStart, PartKind: content.KindText}, {Kind: model.StreamPartDelta, TextDelta: "ok"}, {Kind: model.StreamPartEnd}} {
					if err := handler(e); err != nil {
						return model.Response{}, err
					}
				}
				return goodResponse(), nil
			}}, modelruntime.Dependencies{})
			_, err := exports.Streaming.Stream(context.Background(), model.Request{}, func(model.StreamEvent) error { events++; return nil })
			if calls != tc.wantCalls || (err != nil) != tc.emit || (tc.emit && events != 1) || (!tc.emit && events != 3) {
				t.Fatalf("calls=%d events=%d err=%v", calls, events, err)
			}
		})
	}
}

func TestRetryWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	exports := retryExports(t, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) {
		calls++
		cancel()
		return model.Response{}, retryableError{delay: time.Hour}
	}}, modelruntime.Dependencies{})
	_, err := exports.Runtime.Complete(ctx, model.Request{})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestStreamHandlerErrorNeverRetries(t *testing.T) {
	calls := 0
	consumerErr := retryableError{}
	exports := retryExports(t, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { return goodResponse(), nil }, Stream: func(_ context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
		calls++
		return model.Response{}, handler(model.StreamEvent{Kind: model.StreamPartStart, PartKind: content.KindText})
	}}, modelruntime.Dependencies{})
	_, err := exports.Streaming.Stream(context.Background(), model.Request{}, func(model.StreamEvent) error { return consumerErr })
	if !errors.Is(err, consumerErr) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
