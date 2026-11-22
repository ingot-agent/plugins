package contextcompact

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

// memoryStore is an in-memory session.Store. Only Append and Load matter here.
type memoryStore struct {
	mu      sync.Mutex
	entries map[session.ID][]session.Entry
	appends int
	err     error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{entries: make(map[session.ID][]session.Entry)}
}

func (s *memoryStore) Create(context.Context, session.CreateRequest) (session.Metadata, error) {
	return session.Metadata{}, errors.New("not implemented")
}

func (s *memoryStore) Append(_ context.Context, id session.ID, entries ...session.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.entries[id] = append(s.entries[id], entries...)
	s.appends++
	return nil
}

func (s *memoryStore) Load(_ context.Context, id session.ID) ([]session.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return append([]session.Entry(nil), s.entries[id]...), nil
}

func (s *memoryStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appends
}

// fakeModel answers summarizer calls with scripted responses.
type fakeModel struct {
	mu        sync.Mutex
	responses []model.Response
	errs      []error
	requests  []model.Request
	// onCall can override the scripted behaviour entirely.
	onCall func(request model.Request) (model.Response, error)
}

func (m *fakeModel) Complete(ctx context.Context, _, _ session.ID, request model.Request) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, cloneRequest(request))
	onCall := m.onCall
	var response model.Response
	var err error
	if index < len(m.responses) {
		response = m.responses[index]
	}
	if index < len(m.errs) {
		err = m.errs[index]
	}
	m.mu.Unlock()
	if onCall != nil {
		return onCall(request)
	}
	if err != nil {
		return model.Response{}, err
	}
	response.Provider = request.Provider
	response.Model = request.Model
	return response, nil
}

func (m *fakeModel) calls() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Request(nil), m.requests...)
}

// testStateScope is a Plugin-owned Runtime state scope.
type testStateScope struct{ dir string }

func (s testStateScope) Dir() string { return s.dir }

// writeTestConfig persists a configuration into a fresh state scope.
func writeTestConfig(t *testing.T, cfg Config) string {
	t.Helper()
	dir := t.TempDir()
	if err := saveConfig(dir, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return dir
}

type testDependencies struct {
	Model           model.Runtime
	ProviderSources []model.ProviderSource
	Interactions    ingotabi.Optional[interaction.ExecutionBinder]
	Logger          ingotabi.Optional[logging.Logger]
	Store           session.Store
	State           state.Scope
}

func newTestCompactor(ctx context.Context, deps testDependencies) (Exports, ingotabi.Cleanup, error) {
	return New(ctx, Dependencies{
		Model: deps.Model, ProviderSources: deps.ProviderSources, Interactions: deps.Interactions,
		Logger: deps.Logger, Store: deps.Store, State: deps.State,
	})
}

// text, user, assistant, call and toolResult build message fixtures.
func text(value string) content.Content { return content.FromText(value) }

func userMessage(value string) model.Message {
	return model.Message{Role: model.RoleUser, Content: text(value)}
}

func assistantMessage(value string) model.Message {
	return model.Message{Role: model.RoleAssistant, Content: text(value)}
}

func toolCall(id, name, arguments string) tool.Call {
	return tool.Call{ID: id, Name: name, Arguments: json.RawMessage(arguments)}
}

func toolMessage(id, name, output string) model.Message {
	return model.Message{Role: model.RoleTool, ToolCallID: id, Name: name, Content: text(output)}
}

// simpleRequest builds a request from alternating user and assistant messages.
func simpleRequest(pairs ...string) model.Request {
	messages := []model.Message{{Role: model.RoleSystem, Content: text("system")}}
	for i, value := range pairs {
		if i%2 == 0 {
			messages = append(messages, userMessage(value))
			continue
		}
		messages = append(messages, assistantMessage(value))
	}
	return model.Request{Provider: "p", Model: "m", Messages: messages}
}

// toolRequest builds a request whose rounds each run one tool call.
func toolRequest(rounds int, outputSize int) model.Request {
	messages := []model.Message{{Role: model.RoleSystem, Content: text("system")}}
	output := make([]byte, outputSize)
	for i := range output {
		output[i] = byte('a' + i%26)
	}
	for i := 0; i < rounds; i++ {
		messages = append(messages,
			userMessage("step prompt"),
			model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{toolCall("call_1", "shell", `{"cmd":"go test"}`)}},
			toolMessage("call_1", "shell", string(output)),
			assistantMessage("done"),
		)
	}
	return model.Request{Provider: "p", Model: "m", Messages: messages}
}

// summaryText renders a valid summarizer answer.
func summaryText(summary string) string {
	return "SUMMARY:\n" + summary + "\n\nNOTES:\n- fact"
}

// summaryResponse wraps answer text in an assistant response.
func summaryResponse(answer string) model.Response {
	return model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Content: text(answer)},
		FinishReason: "stop",
	}
}

func cloneRequest(request model.Request) model.Request {
	result := request
	if request.Temperature != nil {
		value := *request.Temperature
		result.Temperature = &value
	}
	if request.MaxTokens != nil {
		value := *request.MaxTokens
		result.MaxTokens = &value
	}
	if request.Messages != nil {
		result.Messages = make([]model.Message, len(request.Messages))
		for i, message := range request.Messages {
			result.Messages[i] = message
			if message.Content != nil {
				result.Messages[i].Content = content.Clone(message.Content)
			}
			if message.ToolCalls != nil {
				calls := make([]tool.Call, len(message.ToolCalls))
				for j, call := range message.ToolCalls {
					calls[j] = call
					calls[j].Arguments = append([]byte(nil), call.Arguments...)
				}
				result.Messages[i].ToolCalls = calls
			}
			result.Messages[i].Replay = message.Replay.Clone()
		}
	}
	if request.Tools != nil {
		result.Tools = make([]tool.Definition, len(request.Tools))
		for i, definition := range request.Tools {
			result.Tools[i] = definition
			result.Tools[i].InputSchema = append([]byte(nil), definition.InputSchema...)
		}
	}
	return result
}
