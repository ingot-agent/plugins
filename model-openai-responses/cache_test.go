package openairesponses_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	openairesponses "github.com/ingot-agent/plugins/model-openai-responses"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/session"
)

func TestSessionCacheAffinitySurvivesTurnsAndProviderReconstruction(t *testing.T) {
	type scope struct {
		session, turn string
		round         int
	}
	scopes := []scope{{"a", "turn-1", 0}, {"a", "turn-1", 1}, {"a", "turn-2", 0}, {"b", "turn-1", 0}, {"", "turn-1", 0}}
	keys := make([]string, len(scopes))
	for i, scope := range scopes {
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(_ context.Context, r *http.Request) (*http.Response, error) {
			var body struct {
				PromptCacheKey string `json:"prompt_cache_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			keys[i] = body.PromptCacheKey
			if r.Header.Get("Session-Id") != body.PromptCacheKey || r.Header.Get("Thread-Id") != body.PromptCacheKey {
				t.Fatal("body/header affinity keys differ")
			}
			response := `{"object":"response","model":"m","status":"completed","output":[]}`
			if i%2 == 1 {
				response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
			}
			return httpResponse(200, response), nil
		}), assetResolver{})
		ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: session.ID(scope.session), TurnID: observation.ID(scope.turn), RoundIndex: scope.round})
		var err error
		if i%2 == 0 {
			_, err = provider.Complete(ctx, model.Request{Model: "m"})
		} else {
			_, err = provider.Stream(ctx, model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(keys[0]) != 64 || keys[0] != keys[1] || keys[0] != keys[2] || keys[0] == keys[3] || keys[4] != "" {
		t.Fatalf("unexpected session partitions: %v", keys)
	}
}

func TestSessionCacheAffinityConcurrentRequestsAreIsolated(t *testing.T) {
	var mu sync.Mutex
	keys := map[string]string{}
	provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(ctx context.Context, r *http.Request) (*http.Response, error) {
		var body struct {
			PromptCacheKey string `json:"prompt_cache_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		scope, _ := observation.CorrelationFromContext(ctx)
		key := r.Header.Get("Session-Id")
		if len(key) != 64 || key != body.PromptCacheKey {
			return nil, fmt.Errorf("invalid affinity key")
		}
		mu.Lock()
		defer mu.Unlock()
		id := string(scope.SessionID)
		if previous, ok := keys[id]; ok && previous != key {
			return nil, fmt.Errorf("session key changed")
		}
		keys[id] = key
		return httpResponse(200, `{"object":"response","model":"m","status":"completed","output":[]}`), nil
	}), assetResolver{})
	var group sync.WaitGroup
	for i := range 24 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: session.ID(fmt.Sprintf("session-%d", i%4)), RoundIndex: i})
			if _, err := provider.Complete(ctx, model.Request{Model: "m"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	group.Wait()
	distinct := map[string]bool{}
	for _, key := range keys {
		distinct[key] = true
	}
	if len(distinct) != 4 {
		t.Fatal("concurrent sessions share affinity keys")
	}
}

func TestCacheHeadersPreserveExplicitConfigurationAndUnscopedCalls(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test", DefaultHeaders: map[string]string{"session-id": "configured-session", "thread-id": "configured-thread"}}, clientFunc(func(_ context.Context, r *http.Request) (*http.Response, error) {
			if r.Header.Get("Session-Id") != "configured-session" || r.Header.Get("Thread-Id") != "configured-thread" {
				t.Fatal("explicit session headers changed")
			}
			var body map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["prompt_cache_key"]; ok != scoped {
				t.Fatal("unexpected cache key presence")
			}
			return httpResponse(200, `{"object":"response","model":"m","status":"completed","output":[]}`), nil
		}), assetResolver{})
		ctx := context.Background()
		if scoped {
			ctx = observation.WithCorrelation(ctx, observation.Correlation{SessionID: "custom\nidentity"})
		}
		if _, err := provider.Complete(ctx, model.Request{Model: "m"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiagnosticsReportCachedInputTokens(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, cached := range []string{"missing", "0", "4096"} {
			t.Run(fmt.Sprintf("stream=%v/cached=%s", stream, cached), func(t *testing.T) {
				logs := captureDiagnostics(t)
				details := ""
				if cached != "missing" {
					details = `,"input_tokens_details":{"cached_tokens":` + cached + `}`
				}
				response := `{"object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":5000,"output_tokens":0,"total_tokens":5000` + details + `}}`
				if stream {
					response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
				}
				provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(response), assetResolver{})
				ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: "s", TurnID: "t"})
				var err error
				if stream {
					_, err = provider.Stream(ctx, model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
				} else {
					_, err = provider.Complete(ctx, model.Request{Model: "m"})
				}
				if err != nil {
					t.Fatal(err)
				}
				records := diagnosticRecords(t, logs)
				last := records[len(records)-1]
				if records[0]["cache_session_key_present"] != true || last["input_tokens"] != float64(5000) {
					t.Fatal("missing cache diagnostics")
				}
				count, present := last["cached_input_tokens"]
				if cached == "missing" {
					if present {
						t.Fatal("missing usage was reported as zero")
					}
				} else if fmt.Sprint(count) != cached {
					t.Fatalf("cached input=%v", count)
				}
				if strings.Contains(logs.String(), "prompt_cache_key") {
					t.Fatal("diagnostics exposed cache key")
				}
			})
		}
	}
}
