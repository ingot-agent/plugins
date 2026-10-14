package appcomponent

// This opt-in fixture serves the real HTTP/SSE boundary with deterministic SDK
// adapters. It is used by browser regression tests without credentials or model
// network calls, and is excluded from the distributed runtime.
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	agentdefault "github.com/ingot-agent/plugins/agent-default"
	agentobservation "github.com/ingot-agent/plugins/agent-default/observation"
	hostcomponent "github.com/ingot-agent/plugins/app-webui/host"
	"github.com/ingot-agent/plugins/app-webui/modelselection"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type browserAgent struct {
	interaction interaction.ExecutionBinder
	tokenUsage  session.TokenUsageStore
}

func (b *browserAgent) Complete(ctx context.Context, root, id session.ID, request model.Request) (model.Response, error) {
	return b.Stream(ctx, root, id, request, nil)
}
func (b *browserAgent) Stream(ctx context.Context, root, id session.ID, request model.Request, handler model.StreamHandler) (model.Response, error) {
	input, lastUser, attachments := "", -1, false
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == model.RoleUser {
			lastUser = i
			for _, part := range request.Messages[i].Content {
				if part.Kind == content.KindText {
					input += part.Text
				} else {
					attachments = true
				}
			}
			break
		}
	}
	if lastUser > 0 {
		for _, part := range request.Messages[lastUser-1].Content {
			if part.Kind == content.KindText && strings.Contains(part.Text, "所上传的文件") {
				attachments = true
			}
		}
	}
	rounds := 0
	for _, m := range request.Messages[lastUser+1:] {
		if m.Role == model.RoleAssistant && len(m.ToolCalls) > 0 {
			rounds++
		}
	}
	if strings.Contains(input, "fail") {
		return model.Response{}, fmt.Errorf("fixture execution failed")
	}
	emit := func(semantic model.StreamSemantic, text string) error {
		if handler == nil {
			return nil
		}
		return handler(model.StreamEvent{Kind: model.StreamPartDelta, Semantic: semantic, TextDelta: text})
	}
	reasoning := "Checking the request."
	timeline := strings.Contains(input, "timeline")
	if timeline && rounds == 1 {
		reasoning = "Reviewing the inspection. Planning a verification."
	} else if timeline && rounds > 1 {
		reasoning = "Preparing the final response."
	}
	if err := emit(model.StreamSemanticReasoning, reasoning); err != nil {
		return model.Response{}, err
	}
	toolRounds := 0
	if strings.Contains(input, "ask") || strings.Contains(input, "approve") {
		toolRounds = 1
	}
	if timeline {
		toolRounds = 2
	}
	if strings.Contains(input, "tool-only") {
		toolRounds = 3
	}
	if rounds < toolRounds {
		names := []string{"workspace.inspect"}
		text := ""
		if timeline {
			if rounds == 0 {
				text = "I'll inspect the workspace first."
			} else {
				names = []string{"workspace.verify"}
				text = "The first check is complete. I'll verify the result."
			}
		}
		if strings.Contains(input, "tool-only") {
			names = [][]string{{"edit_file", "shell_exec"}, {"edit_file"}, {"shell_exec"}}[rounds]
			if rounds == 0 {
				text = "Checking the workspace before editing."
			}
		}
		if err := emit(model.StreamSemanticContent, text); err != nil {
			return model.Response{}, err
		}
		args, _ := json.Marshal(map[string]any{"input": input, "stage": rounds})
		calls := make([]tool.Call, 0, len(names))
		for i, name := range names {
			calls = append(calls, tool.Call{ID: fmt.Sprintf("fixture-%d-%d-%d", lastUser, rounds, i), Name: name, Arguments: args})
		}
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromInput(text, nil), ToolCalls: calls}}, nil
	}
	if strings.Contains(input, "burst") {
		for range 500 {
			if err := emit(model.StreamSemanticContent, "字"); err != nil {
				return model.Response{}, err
			}
		}
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	if strings.Contains(input, "hold") {
		if err := emit(model.StreamSemanticContent, "Partial response"); err != nil {
			return model.Response{}, err
		}
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	output := "Your workspace is ready. We can take the next step together."
	if attachments {
		output = "Attachment received."
	}
	for _, chunk := range strings.SplitAfter(output, " ") {
		select {
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
		if err := emit(model.StreamSemanticContent, chunk); err != nil {
			return model.Response{}, err
		}
	}
	channel, err := b.interaction.Bind(execution.Scope{SessionID: id})
	if err != nil {
		return model.Response{}, err
	}
	correlation, _ := observation.CorrelationFromContext(ctx)
	_ = channel.Set(ctx, interaction.State{Name: "context-compact.session-context/" + string(id), Values: []interaction.Entry{
		{Name: "sessionId", Value: interaction.StringValue(string(id))}, {Name: "inputTokens", Value: interaction.IntegerValue(int64(len(request.Messages) * 64))},
		{Name: "accuracy", Value: interaction.StringValue("estimate")}, {Name: "source", Value: interaction.StringValue("fixture-estimate")}, {Name: "provider", Value: interaction.StringValue("fixture")}, {Name: "model", Value: interaction.StringValue("chat")},
		{Name: "turnId", Value: interaction.StringValue(string(correlation.TurnID))}, {Name: "roundIndex", Value: interaction.IntegerValue(int64(rounds))},
	}})
	metadata, err := b.tokenUsage.AddTotalTokens(ctx, []session.ID{root, id}, 72)
	if err != nil {
		return model.Response{}, err
	}
	for _, item := range metadata {
		ch, err := b.interaction.Bind(execution.Scope{SessionID: item.ID})
		if err != nil {
			return model.Response{}, err
		}
		_ = ch.Set(ctx, interaction.State{Name: "model-runtime.session-usage/" + string(item.ID), Values: []interaction.Entry{{Name: "sessionId", Value: interaction.StringValue(string(item.ID))}, {Name: "totalToken", Value: interaction.IntegerValue(item.TotalToken)}}})
	}
	return model.Response{Provider: "fixture", Model: "chat", Message: model.Message{Role: model.RoleAssistant, Content: content.FromText(output)}, Usage: model.Usage{InputTokens: 48, OutputTokens: 24, TotalTokens: 72, Reported: true}}, nil
}

type browserTools struct{ interaction interaction.ExecutionBinder }

func (browserTools) Definitions() []tool.Definition {
	var definitions []tool.Definition
	for _, name := range []string{"workspace.inspect", "workspace.verify", "edit_file", "shell_exec"} {
		definitions = append(definitions, tool.Definition{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return definitions
}
func (b browserTools) Call(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		Input string
		Stage int
	}
	if err := json.Unmarshal(invocation.Call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	channel, err := b.interaction.Bind(execution.Scope{SessionID: invocation.Scope.SessionID})
	if err != nil {
		return tool.Result{}, err
	}
	ask := invocation.Call.Name == "workspace.verify" || args.Stage == 0 && (strings.Contains(args.Input, "ask") || strings.Contains(args.Input, "approve"))
	if ask {
		field := interaction.Field{Name: "answer", Label: "Answer", Kind: interaction.FieldString, Required: true}
		description := "How would you like to continue?"
		name := "confirmation"
		if invocation.Call.Name == "workspace.verify" {
			description = "Continue with verification?"
			name = "verification"
		} else if strings.Contains(args.Input, "approve") {
			field.Kind = interaction.FieldChoice
			field.Options = []interaction.Option{{Value: "yes", Label: "Yes"}, {Value: "no", Label: "No"}}
			description = "Allow workspace inspection?"
		}
		if _, err := channel.Request(ctx, interaction.Request{Name: name, Description: description, Fields: []interaction.Field{field}}); err != nil {
			return tool.Result{}, err
		}
	}
	text := "Workspace is ready."
	if invocation.Call.Name == "workspace.verify" {
		text = "Verification complete."
	}
	return tool.Result{Content: content.FromText(text)}, nil
}

type browserAssets struct {
	mu    sync.Mutex
	items map[string][]byte
}

func (s *browserAssets) Put(_ context.Context, request asset.PutRequest) (asset.Reference, asset.Info, error) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return asset.Reference{}, asset.Info{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("asset-%d", len(s.items)+1)
	s.items[id] = data
	return asset.Reference{ID: id}, asset.Info{Size: uint64(len(data))}, nil
}
func (s *browserAssets) Stat(_ context.Context, reference asset.Reference) (asset.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.items[reference.ID]
	if !ok {
		return asset.Info{}, fs.ErrNotExist
	}
	return asset.Info{Size: uint64(len(data))}, nil
}
func (s *browserAssets) Open(_ context.Context, reference asset.Reference) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.items[reference.ID]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func TestBrowserFixture(t *testing.T) {
	address := os.Getenv("INGOT_WEBUI_FIXTURE_ADDR")
	if address == "" {
		t.Skip("opt-in browser fixture")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a := testApplication(t)
	a.config.MaxAssetBytes = 64 << 20
	a.config.Heartbeat = time.Second
	// Construct host once so request scope and observation share the same hub.
	store := a.sessions.(*defaultSessionController).store.(*testStore)
	inputs := testPluginInputs{store: store}
	host, _, err := hostcomponent.New(ctx, hostcomponent.Dependencies{State: testStateScope{dir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	a.backend = host.Runtime
	b := &browserAgent{interaction: host.ExecutionInteractions, tokenUsage: store}
	streaming := ingotabi.Some[model.StreamingRuntime](b)
	if os.Getenv("INGOT_WEBUI_FIXTURE_RUN_ONLY") == "1" {
		streaming = ingotabi.None[model.StreamingRuntime]()
	}
	observations, closeObservation, err := agentobservation.New(ctx, agentobservation.Dependencies{Observers: []observation.Observer{host.Observer}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeObservation(context.Background()) })
	a.assets = &browserAssets{items: make(map[string][]byte)}
	engine, closeAgent, err := agentdefault.New(ctx, agentdefault.Dependencies{State: testStateScope{}, Model: b, Streaming: streaming, Tools: browserTools{host.ExecutionInteractions}, Store: store, Sessions: store, Assets: a.assets, Prompt: webTestPrompt{}, Observation: observations.Consumer, PluginInputs: ingotabi.Some[agent.PluginInputProjector](inputs)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeAgent(context.Background()) })
	a.agent, err = newAgentController(engine.Runtime, engine.Control, engine.History)
	if err != nil {
		t.Fatal(err)
	}
	a.turns.stop()
	<-a.turns.done
	a.turns = newTurnRegistry(ctx, a.agent, host.Runtime.Events())
	a.filePicker = browserFilePicker{}
	pixel, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aO2sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.defaultWorkspace, "pixel.png"), pixel, 0o600); err != nil {
		t.Fatal(err)
	}
	documents := filepath.Join(a.defaultWorkspace, "documents")
	if err := os.MkdirAll(documents, 0o700); err != nil {
		t.Fatal(err)
	}
	localTestFile(t, documents, "report.pdf", "%PDF-1.4")
	a.modelSelection = &testModelSelection{snapshot: modelselection.Snapshot{
		Revision: "initial", Configured: true,
		Current: modelselection.Selection{Provider: "primary", Model: "chat", ReasoningEffort: "low"},
		Providers: []modelselection.Provider{
			{Name: "primary", Models: []modelselection.Model{{Name: "chat", ReasoningEfforts: []string{"low", "high"}}}},
			{Name: "secondary", Models: []modelselection.Model{{Name: "reasoner", ReasoningEfforts: []string{"medium", "high"}}}},
		},
	}}
	echo := operationFixture("echo")
	echo.definition.InputSchema = json.RawMessage("{\"type\":\"object\",\"properties\":{\"value\":{\"type\":\"integer\"}},\"required\":[\"value\"]}")
	echo.invoke = func(_ context.Context, request operation.Request) (operation.Result, error) {
		return operation.Result{Output: request.Input}, nil
	}
	wait := operationFixture("wait")
	wait.definition.InputSchema = echo.definition.InputSchema
	wait.invoke = func(ctx context.Context, _ operation.Request) (operation.Result, error) {
		<-ctx.Done()
		return operation.Result{}, ctx.Err()
	}
	complex := operationFixture("complex")
	complex.definition.InputSchema = json.RawMessage("{\"type\":\"object\",\"properties\":{\"items\":{\"type\":\"array\"}},\"required\":[\"items\"]}")
	complex.definition.OutputSchema = json.RawMessage("{\"type\":\"object\"}")
	complex.invoke = echo.invoke
	config := operationFixture("config")
	config.definition.Group = "tool-shell"
	config.definition.Description = "Configure the shell fixture."
	config.definition.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
	config.definition.OutputSchema = json.RawMessage(`{"type":"object"}`)
	config.invoke = func(ctx context.Context, request operation.Request) (operation.Result, error) {
		rules := interaction.ListValue([]interaction.Value{interaction.ObjectValue([]interaction.Entry{
			{Name: "tool", Value: interaction.StringValue("workspace.inspect")},
			{Name: "action", Value: interaction.StringValue("ask")},
		})})
		response, err := request.Interaction.Request(ctx, interaction.Request{
			Name: "config", Description: "Edit shell approval rules.",
			Fields: []interaction.Field{{Name: "rules", Label: "Rules", Kind: interaction.FieldList, Default: &rules, Element: &interaction.Field{Name: "rule", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "tool", Label: "Tool", Kind: interaction.FieldString, Required: true},
				{Name: "action", Label: "Action", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{{Value: "ask", Label: "Ask"}, {Value: "allow", Label: "Allow"}}},
			}}}},
		})
		if err != nil {
			return operation.Result{}, err
		}
		count := 0
		for _, value := range response.Values {
			if value.Name == "rules" && value.Value.Kind == interaction.ValueList {
				count = len(value.Value.Items)
			}
		}
		output, err := json.Marshal(map[string]any{"rules": count})
		return operation.Result{Output: output}, err
	}
	providers := operationFixture("config")
	providers.definition.Group = "model-openai-compatible"
	providers.definition.Description = "Review and update the OpenAI-compatible model providers."
	providers.definition.InputSchema = config.definition.InputSchema
	providers.definition.OutputSchema = json.RawMessage(`{"type":"object"}`)
	providers.invoke = func(ctx context.Context, request operation.Request) (operation.Result, error) {
		defaults := interaction.ListValue([]interaction.Value{interaction.ObjectValue([]interaction.Entry{
			{Name: "name", Value: interaction.StringValue("Example provider")},
			{Name: "base_url", Value: interaction.StringValue("https://api.example.com/v1")},
			{Name: "models", Value: interaction.ListValue([]interaction.Value{interaction.StringValue("example-chat"), interaction.StringValue("example-reasoner")})},
			{Name: "default_headers", Value: interaction.ListValue([]interaction.Value{interaction.ObjectValue([]interaction.Entry{
				{Name: "name", Value: interaction.StringValue("X-Tenant")},
				{Name: "source", Value: interaction.StringValue("X-Tenant")},
				{Name: "action", Value: interaction.StringValue("keep")},
			})})},
		})})
		_, err := request.Interaction.Request(ctx, interaction.Request{
			Name: "providers", Description: "One entry per OpenAI-compatible provider.",
			Fields: []interaction.Field{{Name: "providers", Label: "Providers", Kind: interaction.FieldList, Default: &defaults, Element: &interaction.Field{Name: "provider", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "name", Label: "Name", Description: "Stable provider name referenced by model.runtime.", Kind: interaction.FieldString, Required: true},
				{Name: "base_url", Label: "Base URL", Description: "Absolute http/https endpoint.", Kind: interaction.FieldString, Required: true},
				{Name: "api_key", Label: "API key", Kind: interaction.FieldString, Sensitive: true},
				{Name: "models", Label: "Models", Kind: interaction.FieldList, Element: &interaction.Field{Name: "model", Kind: interaction.FieldString}},
				{Name: "default_headers", Label: "Default headers", Kind: interaction.FieldList, Element: &interaction.Field{Name: "header", Kind: interaction.FieldObject, Fields: []interaction.Field{
					{Name: "name", Label: "Name", Kind: interaction.FieldString, Required: true},
					{Name: "source", Label: "Existing header", Kind: interaction.FieldString, Required: true, Options: []interaction.Option{{Value: "X-Tenant", Label: "X-Tenant"}}},
					{Name: "action", Label: "Value action", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{{Value: "keep", Label: "Keep"}, {Value: "replace", Label: "Replace"}, {Value: "clear", Label: "Clear"}}},
					{Name: "value", Label: "Value", Kind: interaction.FieldString, Sensitive: true},
				}}},
			}}}},
		})
		if err != nil {
			return operation.Result{}, err
		}
		return operation.Result{Output: json.RawMessage(`{"saved":true}`)}, nil
	}
	a.operations, err = newOperationController([]operation.Operation{echo, wait, complex, config, providers})
	if err != nil {
		t.Fatal(err)
	}
	a.operationInvocations = newOperationRegistry(ctx, a.operations, a.backend.Interactions(), a.backend.Events(), 128)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	t.Logf("Browser fixture: http://%s", address)
	<-ctx.Done()
}

type browserFilePicker struct{}

func (browserFilePicker) Select(ctx context.Context, initial string) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	entries, err := os.ReadDir(initial)
	if err != nil {
		return nil, false, err
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			paths = append(paths, filepath.Join(initial, entry.Name()))
		}
	}
	return paths, len(paths) == 0, nil
}
