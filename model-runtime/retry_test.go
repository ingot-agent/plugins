package modelruntime_test

import (
	"context"
	"errors"
	"fmt"
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
	exports, _, err := modelruntime.New(context.Background(), withState(t, modelruntime.Config{DefaultModel: "m", RetryMaxAttempts: 3, RetryInitialDelayMS: 1, RetryMaxDelayMS: 1}, deps))
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
			_, err := exports.Runtime.Complete(context.Background(), "s", "s", model.Request{})
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
	_, err := exports.Runtime.Complete(context.Background(), "s", "s", model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("original")}}})
	if !errors.Is(err, temporary) || calls != 3 || !reflect.DeepEqual(observed, []string{"original", "original", "original"}) {
		t.Fatalf("calls=%d observed=%v err=%v", calls, observed, err)
	}
}

func TestRetryCanBeDisabledForCompleteAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		calls := 0
		entry := model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) {
			calls++
			return model.Response{}, retryableError{}
		}}
		entry.Stream = func(context.Context, model.Request, model.StreamHandler) (model.Response, error) {
			calls++
			return model.Response{}, retryableError{}
		}
		exports, _, err := modelruntime.New(context.Background(), withState(t, modelruntime.Config{DefaultModel: "m", RetryMaxAttempts: 1}, modelruntime.Dependencies{ProviderSources: []model.ProviderSource{fixedProviderSource(entry)}}))
		if err != nil {
			t.Fatal(err)
		}
		if stream {
			_, err = exports.Streaming.Stream(context.Background(), "s", "s", model.Request{}, func(model.StreamEvent) error { t.Fatal("unexpected event"); return nil })
		} else {
			_, err = exports.Runtime.Complete(context.Background(), "s", "s", model.Request{})
		}
		if err == nil || calls != 1 {
			t.Fatalf("stream=%v calls=%d err=%v", stream, calls, err)
		}
	}
}

func TestStreamRetryRetractsFirstAttempt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		emit      bool
		wantCalls int
	}{
		{"before", false, 2}, {"after start", true, 2},
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
			_, err := exports.Streaming.Stream(context.Background(), "s", "s", model.Request{}, func(model.StreamEvent) error { events++; return nil })
			if calls != tc.wantCalls || err != nil || (tc.emit && events != 5) || (!tc.emit && events != 4) {
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
	_, err := exports.Runtime.Complete(ctx, "s", "s", model.Request{})
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
	_, err := exports.Streaming.Stream(context.Background(), "s", "s", model.Request{}, func(model.StreamEvent) error { return consumerErr })
	if !errors.Is(err, consumerErr) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestStreamRetriesDiscardPartialContentAndReasoning(t *testing.T) {
	for _, closePart := range []bool{false, true} {
		t.Run(fmt.Sprint(closePart), func(t *testing.T) {
			calls, resets := 0, 0
			var output string
			var observed []string
			exports := retryExports(t, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { return goodResponse(), nil }, Stream: func(_ context.Context, request model.Request, h model.StreamHandler) (model.Response, error) {
				calls++
				observed = append(observed, request.Messages[0].Content[0].Text)
				request.Messages[0].Content[0].Text = "mutated"
				text := "失败🙂"
				if calls == 3 {
					text = "ok"
				}
				for _, semantic := range []model.StreamSemantic{model.StreamSemanticReasoning, model.StreamSemanticContent} {
					events := []model.StreamEvent{{Kind: model.StreamPartStart, PartKind: content.KindText, Semantic: semantic}, {Kind: model.StreamPartDelta, TextDelta: text, Semantic: semantic}}
					if closePart || calls == 3 {
						events = append(events, model.StreamEvent{Kind: model.StreamPartEnd, Semantic: semantic})
					}
					for _, event := range events {
						if err := h(event); err != nil {
							return model.Response{}, err
						}
					}
				}
				if calls < 3 {
					return model.Response{}, retryableError{}
				}
				return goodResponse(), nil
			}}, modelruntime.Dependencies{})
			response, err := exports.Streaming.Stream(context.Background(), "s", "s", model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("original")}}}, func(event model.StreamEvent) error {
				if event.Kind == model.StreamRetry {
					resets++
					if event.Attempt != resets+1 {
						t.Fatalf("retry=%+v", event)
					}
					output = ""
				} else if event.Semantic == model.StreamSemanticContent {
					output += event.TextDelta
				}
				return nil
			})
			if err != nil || calls != 3 || resets != 2 || output != "ok" || response.Message.Content[0].Text != "ok" || !reflect.DeepEqual(observed, []string{"original", "original", "original"}) {
				t.Fatalf("calls=%d resets=%d output=%q observed=%v response=%+v err=%v", calls, resets, output, observed, response, err)
			}
		})
	}
}

func TestStreamRetryCallbackFailureAndCancellation(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelWait), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			exports := retryExports(t, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { return goodResponse(), nil }, Stream: func(context.Context, model.Request, model.StreamHandler) (model.Response, error) {
				calls++
				return model.Response{}, retryableError{delay: time.Hour}
			}}, modelruntime.Dependencies{})
			consumer := errors.New("retry display failed")
			_, err := exports.Streaming.Stream(ctx, "s", "s", model.Request{}, func(e model.StreamEvent) error {
				if e.Kind != model.StreamRetry {
					t.Fatalf("event=%+v", e)
				}
				if cancelWait {
					cancel()
					return nil
				}
				return consumer
			})
			want := consumer
			if cancelWait {
				want = context.Canceled
			}
			if !errors.Is(err, want) || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestStreamConsumerFailureCannotBeHiddenByProvider(t *testing.T) {
	consumer := errors.New("consumer failed")
	for _, providerErr := range []error{nil, retryableError{}} {
		calls := 0
		exports := retryExports(t, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { return goodResponse(), nil }, Stream: func(_ context.Context, _ model.Request, h model.StreamHandler) (model.Response, error) {
			calls++
			_ = h(model.StreamEvent{Kind: model.StreamPartStart, PartKind: content.KindText})
			return goodResponse(), providerErr
		}}, modelruntime.Dependencies{})
		_, err := exports.Streaming.Stream(context.Background(), "s", "s", model.Request{}, func(model.StreamEvent) error { return consumer })
		if !errors.Is(err, consumer) || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	}
}
