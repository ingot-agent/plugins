package agentdefault

import (
	"context"
	"errors"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
)

type identityModel struct {
	calls       [][2]session.ID
	unsupported bool
}

func (m *identityModel) Complete(_ context.Context, root, current session.ID, _ model.Request) (model.Response, error) {
	m.calls = append(m.calls, [2]session.ID{root, current})
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}, nil
}
func (m *identityModel) Stream(_ context.Context, root, current session.ID, _ model.Request, handler model.StreamHandler) (model.Response, error) {
	m.calls = append(m.calls, [2]session.ID{root, current})
	if m.unsupported {
		return model.Response{}, model.ErrStreamingUnsupported
	}
	for _, event := range []model.StreamEvent{{Kind: model.StreamPartStart, PartKind: content.KindText}, {Kind: model.StreamPartDelta, TextDelta: "done"}, {Kind: model.StreamPartEnd}} {
		if err := handler(event); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}, nil
}

type identityCompactor struct {
	requests []contextwindow.CompactionRequest
}

func (c *identityCompactor) Compact(_ context.Context, request contextwindow.CompactionRequest) (contextwindow.CompactionResult, error) {
	c.requests = append(c.requests, request)
	return contextwindow.CompactionResult{Messages: request.Invocation.Messages}, nil
}

func TestSessionIdentityIsExplicitAcrossCompactionStreamAndFallback(t *testing.T) {
	for _, root := range []session.ID{"", "main"} {
		for _, mode := range []string{"complete", "stream", "fallback"} {
			t.Run(string(root)+"/"+mode, func(t *testing.T) {
				models := &identityModel{unsupported: mode == "fallback"}
				compactor := &identityCompactor{}
				deps := Dependencies{Model: models, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Compactor: ingotabi.Some[contextwindow.Compactor](compactor)}
				if mode != "complete" {
					deps.Streaming = ingotabi.Some[model.StreamingRuntime](models)
				}
				exports, cleanup, err := New(context.Background(), withState(t, Config{}, deps))
				if err != nil {
					t.Fatal(err)
				}
				if cleanup != nil {
					defer cleanup(context.Background())
				}
				turn := agent.Turn{RootSessionID: root, SessionID: "s", Input: "hello"}
				if mode == "complete" {
					_, err = exports.Runtime.Run(context.Background(), turn)
				} else {
					_, err = exports.Streaming.Stream(context.Background(), turn, func(agent.StreamEvent) error { return nil })
				}
				if err != nil {
					t.Fatal(err)
				}
				wantRoot := root
				if wantRoot == "" {
					wantRoot = "s"
				}
				wantCalls := 1
				if mode == "fallback" {
					wantCalls = 2
				}
				if len(models.calls) != wantCalls || len(compactor.requests) != 1 {
					t.Fatalf("model calls=%v compactions=%d", models.calls, len(compactor.requests))
				}
				for _, ids := range models.calls {
					if ids != [2]session.ID{wantRoot, "s"} {
						t.Fatalf("ids=%v", ids)
					}
				}
				if got := compactor.requests[0]; got.RootSessionID != wantRoot || got.SessionID != "s" {
					t.Fatalf("compaction identities=%+v", got)
				}
			})
		}
	}
}

func TestTurnInterceptorCannotChangeTokenRoot(t *testing.T) {
	models := &identityModel{}
	exports := newOutcomeRuntime(t, models, &outcomeTools{}, func(deps *Dependencies) {
		deps.Interceptors = []agent.Interceptor{agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
			turn.RootSessionID = "forged"
			return next(ctx, turn)
		})}
	})
	_, err := exports.Runtime.Run(context.Background(), agent.Turn{RootSessionID: "main", SessionID: "s", Input: "hello"})
	if !errors.Is(err, ErrInvalidTurn) || len(models.calls) != 0 {
		t.Fatalf("calls=%v err=%v", models.calls, err)
	}
}
