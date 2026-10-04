package contextcompact

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/usage"
)

func TestPluginInputChangesInvalidateCheckpointPrefix(t *testing.T) {
	ctx := context.Background()
	request := firstToolRound()
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	c := newTokenTestCompactor(t, Config{}, &canonicalTokenCounter{}, &fakeModel{}, store).(*compactor)
	layout, err := inspectRequest(request, 0)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := messageDigest(layout.conversation)
	if err != nil {
		t.Fatal(err)
	}
	policy := "sha256:" + strings.Repeat("a", 64)
	checkpoint := persistedCheckpoint{
		Sequence: 1, Mode: checkpointModeSegment, PolicyDigest: policy,
		CoveredMessages: len(layout.conversation), SourceDigest: digest,
		Summary: "old context", Revision: 1, Provider: request.Provider, Model: request.Model,
	}
	if err := c.appendCheckpoint(ctx, "s", checkpoint); err != nil {
		t.Fatal(err)
	}
	// Opaque plugin records are not checkpoints and must not be decoded here.
	if err := store.Append(ctx, "s", session.Entry{Kind: "agent.plugin_input", Version: 99, Payload: []byte("opaque")}); err != nil {
		t.Fatal(err)
	}
	chain, err := c.loadChain(ctx, "s", policy, request.Provider, request.Model, layout)
	if err != nil || chain.covered != 3 || chain.lastSequence != 1 {
		t.Fatalf("original chain=%+v err=%v", chain, err)
	}
	plugin := model.Message{Role: model.RoleUser, Content: textContent("<system source=\"plugin\" plugin=\"example.index\">\nnew context\n</system>")}
	changed := cloneRequest(request)
	changed.Messages = append(cloneMessages(request.Messages[:2]), append([]model.Message{plugin}, cloneMessages(request.Messages[2:])...)...)
	changedLayout, err := inspectRequest(changed, 0)
	if err != nil {
		t.Fatal(err)
	}
	chain, err = c.loadChain(ctx, "s", policy, request.Provider, request.Model, changedLayout)
	if err != nil || chain.covered != 0 || chain.lastSequence != 0 || chain.maxSequence != 1 {
		t.Fatalf("changed prefix reused checkpoint: %+v err=%v", chain, err)
	}
	unchangedPrefix := cloneRequest(request)
	unchangedPrefix.Messages = append(unchangedPrefix.Messages, plugin)
	tailLayout, err := inspectRequest(unchangedPrefix, 0)
	if err != nil {
		t.Fatal(err)
	}
	chain, err = c.loadChain(ctx, "s", policy, request.Provider, request.Model, tailLayout)
	if err != nil || chain.covered != 3 || chain.lastSequence != 1 {
		t.Fatalf("unchanged prefix lost checkpoint: %+v err=%v", chain, err)
	}
}

func TestPluginInputIsIncludedInCompactionBudget(t *testing.T) {
	request := firstToolRound()
	plugin := model.Message{Role: model.RoleUser, Content: textContent("<system source=\"plugin\" plugin=\"example.index\">\ncontext\n</system>")}
	request.Messages = append(request.Messages, plugin)
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	called := false
	counter := counterFunc(func(_ context.Context, got usage.CountRequest) (usage.CountResult, error) {
		called = true
		if !reflect.DeepEqual(got.Invocation.Messages, request.Messages) {
			t.Fatal("plugin envelope was omitted from counted invocation")
		}
		return counted(10), nil
	})
	models := &fakeModel{err: errors.New("unexpected summary")}
	c := newTokenTestCompactor(t, Config{TriggerInputTokens: 20, TargetInputTokens: 15}, counter, models, store)
	result, err := c.Compact(context.Background(), contextwindow.CompactionRequest{RootSessionID: "s", SessionID: "s", Invocation: request})
	if err != nil || !called || result.Changed || !reflect.DeepEqual(result.Messages, request.Messages) || len(models.requests) != 0 {
		t.Fatalf("result=%+v counted=%v err=%v", result, called, err)
	}
}
