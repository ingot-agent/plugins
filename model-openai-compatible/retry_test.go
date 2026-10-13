package openaicompat_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	openaicompat "github.com/ingot-agent/plugins/model-openai-compatible"
	"github.com/ingot-agent/sdk/model"
)

type retryHint interface{ RetryDelay() (time.Duration, bool) }

func checkRetry(t *testing.T, err error, want bool) time.Duration {
	t.Helper()
	var hint retryHint
	if !errors.As(err, &hint) {
		if want {
			t.Fatalf("error %v has no retry hint", err)
		}
		return 0
	}
	delay, ok := hint.RetryDelay()
	if ok != want {
		t.Fatalf("error %v retry=%v want=%v", err, ok, want)
	}
	return delay
}

func TestHTTP2ResetAfterPartialOutputIsRetryable(t *testing.T) {
	consumed := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-consumed:
		case <-r.Context().Done():
			return
		}
		panic(http.ErrAbortHandler)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	p := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: server.URL}, clientFunc(func(ctx context.Context, request *http.Request) (*http.Response, error) {
		return server.Client().Do(request.WithContext(ctx))
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var once sync.Once
	_, err := p.Stream(ctx, model.Request{Model: "m"}, func(model.StreamEvent) error { once.Do(func() { close(consumed) }); return nil })
	checkRetry(t, err, true)
}

func TestTruncatedStreamIsRetryableButMalformedDataIsNot(t *testing.T) {
	for _, tc := range []struct {
		body  string
		retry bool
	}{
		{"", true},
		{"data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}\n\n", true},
		{"data: {\n\n", false},
	} {
		p := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
			return response(http.StatusOK, tc.body), nil
		}))
		_, err := p.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
		checkRetry(t, err, tc.retry)
	}
}
func TestRetryHintsFromHTTPAndNetwork(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{http.StatusTooManyRequests, true}, {http.StatusBadGateway, true}, {http.StatusServiceUnavailable, true}, {http.StatusRequestTimeout, true}, {http.StatusBadRequest, false}, {http.StatusUnauthorized, false},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) { return response(tc.status, "bad"), nil }))
			_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
			checkRetry(t, err, tc.want)
		})
	}
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) { return nil, syscall.ECONNRESET }))
	_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("lost cause: %v", err)
	}
	checkRetry(t, err, true)
}
func TestRetryAfterAndPreEventReadError(t *testing.T) {
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		res := response(http.StatusTooManyRequests, "slow down")
		res.Header.Set("Retry-After", "2")
		return res, nil
	}))
	_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
	if delay := checkRetry(t, err, true); delay != 2*time.Second {
		t.Fatalf("retry-after=%v", delay)
	}
	provider = newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		res := response(http.StatusOK, "")
		res.Body = io.NopCloser(&errorReader{err: syscall.ECONNRESET})
		return res, nil
	}))
	_, err = provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
	checkRetry(t, err, true)
}

type errorReader struct{ err error }

func (r *errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestCompleteReadNetworkFailureIsRetryable(t *testing.T) {
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		res := response(http.StatusOK, "")
		res.Body = io.NopCloser(&errorReader{err: syscall.ECONNRESET})
		return res, nil
	}))
	_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("lost cause: %v", err)
	}
	checkRetry(t, err, true)
}
