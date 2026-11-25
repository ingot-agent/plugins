package toolmcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	toolruntime "github.com/ingot-agent/plugins/tool-runtime"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain doubles as the entry point of the fake MCP server used by the
// end-to-end tests. Re-executing the test binary keeps the suite hermetic: no
// network, no second build, and no dependency on an installed npx or uvx.
func TestMain(m *testing.M) {
	if os.Getenv("TOOLMCP_TEST_SERVER") == "1" {
		if err := runHelperServer(); err != nil {
			fmt.Fprintln(os.Stderr, "helper MCP server:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// helperNonce is unique per helper process, which is how the tests tell one
// Session's MCP process from another's.
var helperNonce = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

func runHelperServer() error {
	server := mcp.NewServer(&mcp.Implementation{Name: "toolmcp-helper", Version: "1.0.0"}, nil)

	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "Echo the input text.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
			},
			"required": []any{"text"},
		},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + arguments.Text}},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "fail",
		Description: "Always reports a tool error.",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "boom"}},
		}, nil
	})

	// "elicit" asks the client for a value and echoes the outcome. The current
	// protocol forbids a server-initiated elicitation while it is serving a
	// request, so the server returns input requests and is invoked again with
	// the answers, which is exactly the path the client middleware drives.
	server.AddTool(&mcp.Tool{
		Name:        "elicit",
		Description: "Ask the client for a value, then report the outcome.",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		response, answered := request.Params.InputResponses["confirm"]
		if !answered {
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{
				"confirm": &mcp.ElicitParams{
					Mode:    "form",
					Message: "Please confirm the deployment.",
					RequestedSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"confirm": map[string]any{"type": "boolean", "title": "Confirm"},
							"note":    map[string]any{"type": "string", "title": "Note", "default": "none"},
						},
						"required": []any{"confirm"},
					},
				},
			}}, nil
		}
		// Echo the client's answer exactly as the protocol delivered it.
		payload, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil
	})

	// "authorize" drives URL mode through the same input-request path.
	server.AddTool(&mcp.Tool{
		Name:        "authorize",
		Description: "Ask the client to open an authorisation address.",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		response, answered := request.Params.InputResponses["auth"]
		if !answered {
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{
				"auth": &mcp.ElicitParams{
					Mode:          "url",
					Message:       "Authorise the deployment.",
					URL:           "https://example.test/authorize?session=abc123",
					ElicitationID: "elic-e2e",
				},
			}}, nil
		}
		payload, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "where",
		Description: "Report the working directory and a per-process nonce.",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(map[string]string{"cwd": dir, "nonce": helperNonce})
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "slow",
		Description: "Answers after a short delay.",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "slow-done"}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "progress",
		Description: "Reports one progress notification before answering.",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if token := request.Params.GetProgressToken(); token != nil {
			if err := request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      1,
				Total:         2,
				Message:       "halfway",
			}); err != nil {
				return nil, err
			}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
	})

	imagePayload := []byte("png-payload")
	server.AddTool(&mcp.Tool{
		Name:        "image",
		Description: "Returns a small image.",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ImageContent{
				MIMEType: "image/png",
				Data:     []byte(base64.StdEncoding.EncodeToString(imagePayload)),
			}},
		}, nil
	})

	return server.Run(context.Background(), &mcp.StdioTransport{})
}

func helperConfig(t *testing.T) Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name:                  "helper",
		Transport:             transportStdio,
		Command:               executable,
		Env:                   map[string]string{"TOOLMCP_TEST_SERVER": "1"},
		ConnectTimeoutSeconds: 30,
		CallTimeoutSeconds:    30,
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	return config
}

func gateway(t *testing.T, dir string) (Exports, func()) {
	t.Helper()
	exports, cleanup, err := New(context.Background(), Dependencies{State: testScope(dir)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return exports, func() {
		if cleanup != nil {
			_ = cleanup(context.Background())
		}
	}
}

func toolNames(tools []tool.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, entry := range tools {
		names = append(names, entry.Definition().Name)
	}
	return names
}

func findOperation(t *testing.T, operations []operation.Operation, name string) operation.Operation {
	t.Helper()
	for _, entry := range operations {
		if entry.Definition().Name == name {
			return entry
		}
	}
	t.Fatalf("operation %q not found", name)
	return nil
}

func runDiscovery(t *testing.T, exports Exports) {
	t.Helper()
	refresh := findOperation(t, exports.Operations, refreshOperationName)
	if _, err := refresh.Invoke(context.Background(), operation.Request{Interaction: interaction.Unavailable()}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}

func callTool(t *testing.T, runtime tool.Runtime, name, arguments string) (tool.Result, error) {
	t.Helper()
	return runtime.Call(context.Background(), tool.Invocation{
		Call: tool.Call{ID: "call-1", Name: name, Arguments: json.RawMessage(arguments)},
	})
}

func TestConstructionNeitherConnectsNorBlocks(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	start := time.Now()
	exports, cleanup := gateway(t, dir)
	defer cleanup()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("construction took %s", elapsed)
	}
	names := toolNames(exports.Tools)
	if len(names) != 1 || names[0] != statusToolName {
		t.Fatalf("tools = %v, want only the status tool before discovery", names)
	}
}

func TestDiscoveryAndInvocation(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	exports, cleanup = gateway(t, dir)
	defer cleanup()

	want := []string{"mcp_helper_authorize", "mcp_helper_echo", "mcp_helper_elicit", "mcp_helper_fail", "mcp_helper_image", "mcp_helper_progress", "mcp_helper_slow", "mcp_helper_where", statusToolName}
	if got := toolNames(exports.Tools); !equalStrings(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}

	runtimeExports, runtimeCleanup, err := toolruntime.New(context.Background(), toolruntime.Dependencies{
		Tools: exports.Tools,
		State: testScope(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("tool.runtime construction: %v", err)
	}
	defer cleanupNow(runtimeCleanup)

	result, err := callTool(t, runtimeExports.Runtime, "mcp_helper_echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("echo call: %v", err)
	}
	if text, ok := content.TextOnly(result.Content); !ok || text != "echo:hi" {
		t.Fatalf("echo result = %q", text)
	}

	result, err = callTool(t, runtimeExports.Runtime, "mcp_helper_fail", `{}`)
	if err != nil {
		t.Fatalf("a tool-reported error must not be a Go error: %v", err)
	}
	if text, ok := content.TextOnly(result.Content); !ok || !strings.Contains(text, "boom") {
		t.Fatalf("failure result = %q", text)
	}

	result, err = callTool(t, runtimeExports.Runtime, "mcp_helper_image", `{}`)
	if err != nil {
		t.Fatalf("image call: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Kind != content.KindImage {
		t.Fatalf("image result = %#v", result.Content)
	}
	if string(result.Content[0].Media.Source.Data) != "png-payload" {
		t.Fatalf("image bytes = %q", result.Content[0].Media.Source.Data)
	}

	// The standard Runtime validates arguments against the snapshot schema, so
	// a type error is rejected before any remote call happens.
	if _, err := callTool(t, runtimeExports.Runtime, "mcp_helper_echo", `{"text":5}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("invalid arguments error = %v", err)
	}

	result, err = callTool(t, runtimeExports.Runtime, statusToolName, `{}`)
	if err != nil {
		t.Fatalf("status call: %v", err)
	}
	statusText, ok := content.TextOnly(result.Content)
	if !ok {
		t.Fatalf("status result = %#v", result.Content)
	}
	if !strings.Contains(statusText, `"helper"`) || !strings.Contains(statusText, `"connected"`) {
		t.Fatalf("status = %s", statusText)
	}
}

func TestConfigOperationReportsNoRestartWhenToolSetIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	exports, cleanup = gateway(t, dir)
	defer cleanup()

	configOperation := findOperation(t, exports.Operations, setupOperationName)
	channel := newScriptedChannel(map[string]interaction.Value{
		"refresh_now": interaction.BooleanValue(true),
	})
	result, err := configOperation.Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var output setupOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.RestartRequired {
		t.Fatalf("unexpected restart: %v", output.RestartReasons)
	}
	if len(channel.requests) != 1 {
		t.Fatalf("requests = %d", len(channel.requests))
	}
	if len(channel.requests[0].Fields) == 0 {
		t.Fatal("the configuration form declared no fields")
	}
	if output.ApprovalHint == nil {
		t.Fatal("approval hints should be an empty list, not null")
	}
}

func TestConfigOperationReportsRestartWhenToolSetChanges(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	exports, cleanup = gateway(t, dir)
	defer cleanup()

	stored, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	storedConfig, err := normalizeConfig(stored)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	server := storedConfig.Servers[0]
	server.DisabledTools = []string{"fail"}

	configOperation := findOperation(t, exports.Operations, setupOperationName)
	channel := newScriptedChannel(map[string]interaction.Value{
		"refresh_now": interaction.BooleanValue(true),
		"servers":     interaction.ListValue([]interaction.Value{serverValue(server)}),
	})
	result, err := configOperation.Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var output setupOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !output.RestartRequired {
		t.Fatal("disabling a tool must require a restart")
	}
	if !containsString(output.Removed, "mcp_helper_fail") {
		t.Fatalf("removed = %v", output.Removed)
	}

	// The live instance keeps its snapshot until it is reconstructed.
	if names := toolNames(exports.Tools); !containsString(names, "mcp_helper_fail") {
		t.Fatalf("running image changed its tool set: %v", names)
	}

	cleanup()
	exports, cleanup = gateway(t, dir)
	defer cleanup()
	if names := toolNames(exports.Tools); containsString(names, "mcp_helper_fail") {
		t.Fatalf("restarted image still exports the disabled tool: %v", names)
	}
}

func TestUnreadableSnapshotDegradesInsteadOfFailing(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	writeFile(t, dir, snapshotFileName, "{not json")
	exports, cleanup := gateway(t, dir)
	defer cleanup()
	if names := toolNames(exports.Tools); len(names) != 1 || names[0] != statusToolName {
		t.Fatalf("tools = %v", names)
	}
}

func TestMalformedConfigFailsConstruction(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, configFileName, "servers = 5\n")
	if _, _, err := New(context.Background(), Dependencies{State: testScope(dir)}); err == nil {
		t.Fatal("expected construction to fail on malformed configuration")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// cleanupNow tolerates a nil Cleanup, which components without owned
// resources may return.
func cleanupNow(cleanup ingotabi.Cleanup) {
	if cleanup != nil {
		_ = cleanup(context.Background())
	}
}

func TestFailedDiscoveryRetainsPreviousTools(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	broken := helperConfig(t)
	broken.Servers[0].Command = "definitely-not-a-real-executable"
	if err := saveConfig(dir, broken); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	exports, cleanup = gateway(t, dir)
	defer cleanup()
	if names := toolNames(exports.Tools); !containsString(names, "mcp_helper_echo") {
		t.Fatalf("the previous snapshot should still be exported: %v", names)
	}

	refresh := findOperation(t, exports.Operations, refreshOperationName)
	result, err := refresh.Invoke(context.Background(), operation.Request{Interaction: interaction.Unavailable()})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var output refreshOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(output.Servers) != 1 || output.Servers[0].Status != serverStatusError {
		t.Fatalf("servers = %#v", output.Servers)
	}
	snapshot, err := loadSnapshot(dir)
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if !containsString(snapshot.toolNames(), "mcp_helper_echo") {
		t.Fatalf("a failed discovery deleted the previous tool set: %v", snapshot.toolNames())
	}
	if len(output.Removed) != 0 {
		t.Fatalf("a failed discovery must not report removals: %v", output.Removed)
	}
}

func TestConfigOperationWithoutRefreshDoesNotDemandRestart(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	exports, cleanup = gateway(t, dir)
	defer cleanup()

	configOperation := findOperation(t, exports.Operations, setupOperationName)
	channel := newScriptedChannel(map[string]interaction.Value{
		"refresh_now": interaction.BooleanValue(false),
	})
	result, err := configOperation.Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var output setupOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.RestartRequired {
		t.Fatalf("an unchanged configuration must not require a restart: %v", output.RestartReasons)
	}
}
