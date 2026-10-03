package contextcompact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

type sessionModelFunc func(context.Context, session.ID, session.ID, model.Request) (model.Response, error)

func (f sessionModelFunc) Complete(ctx context.Context, root, current session.ID, request model.Request) (model.Response, error) {
	return f(ctx, root, current, request)
}

func TestCompactionPreservesSessionIdentityAcrossEverySummaryPath(t *testing.T) {
	for _, current := range []session.ID{"root", "nested-child", "followup"} {
		t.Run(string(current), func(t *testing.T) {
			calls := make(map[string]int)
			obsolete, _ := json.Marshal(strings.Repeat("obsolete", 230))
			models := sessionModelFunc(func(_ context.Context, root, gotCurrent session.ID, request model.Request) (model.Response, error) {
				if root != "root" || gotCurrent != current {
					t.Fatalf("summary identity = (%q, %q), want (root, %q)", root, gotCurrent, current)
				}
				prompt := messageText(request.Messages[0])
				calls[prompt]++
				switch prompt {
				case evidenceSystemPrompt:
					return summaryResponse(`{"summary":"ordered evidence"}`), nil
				case summarySystemPrompt:
					return summaryResponse(`{"summary":"Investigated the build.","operations":[{"op":"set","path":"/obsolete","value":` + string(obsolete) + `}]}`), nil
				case rollupSystemPrompt:
					return summaryResponse(`{"summary":"Investigation remains open.","discard_paths":["/obsolete"]}`), nil
				default:
					t.Fatalf("unexpected summary prompt %q", prompt)
					return model.Response{}, nil
				}
			})
			store := &memoryStore{entries: map[session.ID][]session.Entry{current: {}}}
			cfg := Config{TriggerInputTokens: 7000, TargetInputTokens: 5000, StateTriggerTokens: 1400, StateTargetTokens: 700, SummaryInputTokens: 8000, MaxSummaryPasses: 30}
			compactor := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, models, store)
			result, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{RootSessionID: "root", SessionID: current, Invocation: firstToolRound()})
			if err != nil || !result.Changed {
				t.Fatalf("Compact = %#v, %v", result, err)
			}
			if calls[evidenceSystemPrompt] < 2 || calls[summarySystemPrompt] != 1 || calls[rollupSystemPrompt] != 1 {
				t.Fatalf("summary paths were not all exercised: %#v", calls)
			}
			if len(store.entries) != 1 || len(store.entries[current]) != 2 {
				t.Fatalf("checkpoints must belong to current session: %#v", store.entries)
			}
		})
	}
}
