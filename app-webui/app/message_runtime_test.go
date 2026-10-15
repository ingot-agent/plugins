package appcomponent

import (
	"context"
	agentdefault "github.com/ingot-agent/plugins/agent-default"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"testing"
	"time"
)

type webTestModel struct{ agent *testAgent }

func (m webTestModel) Complete(ctx context.Context, root, id session.ID, request model.Request) (model.Response, error) {
	input := agent.Input{}
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == model.RoleUser {
			for _, p := range request.Messages[i].Content {
				if p.Kind == content.KindText {
					input.Text += p.Text
				} else {
					a := content.Attachment{Kind: p.Kind, Media: p.Media}
					input.Attachments = append(input.Attachments, agent.InputAttachment{Media: &a})
				}
			}
			break
		}
	}
	execution, err := m.agent.Run(ctx, agent.Turn{RootSessionID: root, SessionID: id, Inputs: []agent.Input{input}})
	output := content.Content(nil)
	if execution.Result != nil {
		output = execution.Result.Output
	}
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: output}}, err
}

type webTestTools struct{}

func (webTestTools) Definitions() []tool.Definition { return nil }
func (webTestTools) Call(context.Context, tool.Invocation) (tool.Result, error) {
	panic("unexpected tool")
}

type webTestPrompt struct{}

func (webTestPrompt) Render(_ context.Context, request prompt.Request) ([]model.Message, error) {
	return append(request.History, model.Message{Role: model.RoleUser, Content: request.Input}), nil
}

type webTestAssets struct{ *browserAssets }

func (webTestAssets) Stat(context.Context, asset.Reference) (asset.Info, error) {
	return asset.Info{}, nil
}

// SDK-focused HTTP fakes use arbitrary session labels.
type webTestSessions struct {
	session.Manager
	store session.Store
}

func (m webTestSessions) Get(ctx context.Context, id session.ID) (session.Metadata, error) {
	_, err := m.store.Load(ctx, id)
	return session.Metadata{ID: id}, err
}

func initTestAgent(t *testing.T, a *testAgent, store session.Store) {
	t.Helper()
	if a.Runtime != nil {
		return
	}
	if store == nil {
		store = &testStore{}
	}
	exports, cleanup, err := agentdefault.New(context.Background(), agentdefault.Dependencies{
		State: testStateScope{}, Model: webTestModel{a}, Tools: webTestTools{}, Store: store, Sessions: webTestSessions{store: store}, Assets: webTestAssets{&browserAssets{items: make(map[string][]byte)}}, Prompt: webTestPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Runtime, a.Control, a.engineHistory, a.cleanup = exports.Runtime, exports.Control, exports.History, cleanup
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := cleanup(ctx); err != nil {
			t.Error(err)
		}
	})
}
func testController(t *testing.T, a *testAgent) agentController {
	t.Helper()
	initTestAgent(t, a, nil)
	controller, err := newAgentController(a, a, a)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}
func replaceTestController(a *application, controller agentController) {
	a.turns.stop()
	<-a.turns.done
	a.agent = controller
	a.turns = newTurnRegistry(context.Background(), controller, a.backend.Events())
}
func turnMedia(turn agent.Turn) []content.Attachment {
	var media []content.Attachment
	for _, a := range turn.Inputs[0].Attachments {
		if a.Media != nil {
			media = append(media, *a.Media)
		}
	}
	return media
}
