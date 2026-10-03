package contextcompact

import (
	"context"
	"errors"
	"strings"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

type contextBinder func(execution.Scope) (interaction.Channel, error)

func (f contextBinder) Bind(scope execution.Scope) (interaction.Channel, error) { return f(scope) }

type contextChannel struct {
	interaction.Channel
	set func(context.Context, interaction.State) error
}

func (c contextChannel) Set(ctx context.Context, state interaction.State) error {
	return c.set(ctx, state)
}

func TestContextSnapshotsUseCurrentSessionAndCanShrink(t *testing.T) {
	var snapshots []interaction.State
	binder := contextBinder(func(scope execution.Scope) (interaction.Channel, error) {
		if scope.SessionID != "child" {
			t.Fatalf("scope = %+v", scope)
		}
		return contextChannel{set: func(_ context.Context, state interaction.State) error {
			snapshots = append(snapshots, state)
			return nil
		}}, nil
	})
	exports, cleanup, err := New(context.Background(), Dependencies{
		Model: &fakeModel{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{}},
		State:        testStateScope{dir: t.TempDir()},
		Interactions: ingotabi.Some[interaction.ExecutionBinder](binder),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: "child", TurnID: "turn", RoundIndex: 2})
	for _, text := range []string{strings.Repeat("long input ", 100), "short"} {
		_, err := exports.Compactor.Compact(ctx, contextwindow.CompactionRequest{
			RootSessionID: "root", SessionID: "child",
			Invocation: model.Request{Provider: "p", Model: "m", Messages: []model.Message{{Role: model.RoleUser, Content: textContent(text)}}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(snapshots) != 2 || snapshots[0].Name != "context-compact.session-context/child" {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	first, second := snapshots[0].Values, snapshots[1].Values
	if first[0].Value.String != "child" || first[1].Value.Integer <= second[1].Value.Integer || second[1].Value.Integer < 1 ||
		second[2].Value.String != "estimate" || second[3].Value.String != "unicode-estimate-v1" ||
		second[4].Value.String != "p" || second[5].Value.String != "m" || second[6].Value.String != "turn" || second[7].Value.Integer != 2 {
		t.Fatalf("context values = %+v / %+v", first, second)
	}
}

func TestContextPublicationFailureDoesNotFailCompaction(t *testing.T) {
	for _, binder := range []interaction.ExecutionBinder{
		contextBinder(func(execution.Scope) (interaction.Channel, error) { return nil, nil }),
		contextBinder(func(execution.Scope) (interaction.Channel, error) { return nil, errors.New("unavailable") }),
		contextBinder(func(execution.Scope) (interaction.Channel, error) {
			return contextChannel{set: func(context.Context, interaction.State) error { return errors.New("unavailable") }}, nil
		}),
	} {
		exports, cleanup, err := newTestCompactor(context.Background(), withState(t, Config{}, testDependencies{
			Model: &fakeModel{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{}}, Interactions: ingotabi.Some(binder),
		}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{
			RootSessionID: "s", SessionID: "s", Invocation: model.Request{Provider: "p", Model: "m", Messages: []model.Message{{Role: model.RoleUser, Content: textContent("hello")}}},
		})
		cleanup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestContextSnapshotUsesPostCompactionRequest(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	var published int64
	binder := contextBinder(func(execution.Scope) (interaction.Channel, error) {
		return contextChannel{set: func(_ context.Context, state interaction.State) error {
			if len(store.entries["s"]) != 1 {
				t.Fatal("context published before checkpoint commit")
			}
			published = state.Values[1].Value.Integer
			return nil
		}}, nil
	})
	counter := counterFunc(func(_ context.Context, input usage.CountRequest) (usage.CountResult, error) {
		if len(input.Invocation.Tools) == 0 {
			return counted(10), nil
		}
		if strings.Contains(messagesText(input.Invocation.Messages), "[Compacted conversation summary.") {
			return counted(50), nil
		}
		return counted(100), nil
	})
	exports, cleanup, err := newTestCompactor(context.Background(), withState(t, Config{TriggerInputTokens: 100, TargetInputTokens: 50}, testDependencies{
		Model: &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"completed","operations":[]}`)}}, Store: store,
		Counter: ingotabi.Some[usage.Counter](counter), Interactions: ingotabi.Some[interaction.ExecutionBinder](binder),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())
	result, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s", Invocation: model.Request{Provider: "main-provider", Model: "main-model",
			Messages: []model.Message{{Role: model.RoleUser, Content: textContent("u")}, {Role: model.RoleAssistant, Content: textContent("a")}},
			Tools:    []tool.Definition{{Name: "test", InputSchema: []byte(`{"type":"object"}`)}},
		},
	})
	if err != nil || !result.Changed || published != 50 {
		t.Fatalf("changed=%v published=%d error=%v", result.Changed, published, err)
	}
}
