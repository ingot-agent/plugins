package toolmcp

import (
	"context"
	"encoding/json"
	"github.com/ingot-agent/sdk/workspace"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	toolruntime "github.com/ingot-agent/plugins/tool-runtime"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	githubmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// httpHelper is a Streamable HTTP MCP server plus the authorization headers it
// observed. It exists to exercise the transport end to end, including header
// injection and the plaintext-HTTP policy, without leaving the process.
type httpHelper struct {
	server   *httptest.Server
	mu       sync.Mutex
	observed []string
}

func (h *httpHelper) sawHeader(value string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, observed := range h.observed {
		if observed == value {
			return true
		}
	}
	return false
}

func newHTTPHelper(t *testing.T, requireToken string) *httpHelper {
	t.Helper()
	helper := &httpHelper{}
	server := githubmcp.NewServer(&githubmcp.Implementation{Name: "toolmcp-http-helper", Version: "1.0.0"}, nil)
	server.AddTool(&githubmcp.Tool{
		Name:        "shout",
		Description: "Upper-case the input text.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
			"required":   []any{"text"},
		},
	}, func(_ context.Context, request *githubmcp.CallToolRequest) (*githubmcp.CallToolResult, error) {
		var arguments struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
			return nil, err
		}
		return &githubmcp.CallToolResult{Content: []githubmcp.Content{
			&githubmcp.TextContent{Text: strings.ToUpper(arguments.Text)},
		}}, nil
	})

	handler := githubmcp.NewStreamableHTTPHandler(func(*http.Request) *githubmcp.Server { return server }, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(writer http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		helper.mu.Lock()
		helper.observed = append(helper.observed, authorization)
		helper.mu.Unlock()
		if requireToken != "" && authorization != requireToken {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(writer, request)
	})
	helper.server = httptest.NewServer(mux)
	t.Cleanup(helper.server.Close)
	return helper
}

func httpConfig(t *testing.T, url string, headers map[string]string) Config {
	t.Helper()
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name:                  "remote",
		Transport:             transportHTTP,
		URL:                   url,
		Headers:               headers,
		ConnectTimeoutSeconds: 20,
		CallTimeoutSeconds:    20,
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	return config
}

func TestHTTPDiscoveryAndInvocation(t *testing.T) {
	t.Setenv("TOOLMCP_TEST_HTTP_TOKEN", "Bearer from-env")
	helper := newHTTPHelper(t, "Bearer from-env")

	dir := t.TempDir()
	if err := saveConfig(dir, httpConfig(t, helper.server.URL+"/mcp", map[string]string{
		"Authorization": "${TOOLMCP_TEST_HTTP_TOKEN}",
	})); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	exports, cleanup = gateway(t, dir)
	defer cleanup()
	if names := toolNames(exports.Tools); !containsString(names, "mcp_remote_shout") {
		t.Fatalf("tools = %v", names)
	}

	runtimeExports, runtimeCleanup, err := toolruntime.New(context.Background(), toolruntime.Dependencies{
		Tools: exports.Tools,
		State: testScope(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("tool.runtime: %v", err)
	}
	defer cleanupNow(runtimeCleanup)

	result, err := callTool(t, runtimeExports.Runtime, "mcp_remote_shout", `{"text":"quiet"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if text, ok := content.TextOnly(result.Content); !ok || text != "QUIET" {
		t.Fatalf("result = %q", text)
	}
	if !helper.sawHeader("Bearer from-env") {
		t.Fatal("the configured Authorization header was not sent")
	}
}

func TestHTTPDiscoveryFailsOnUnauthorized(t *testing.T) {
	helper := newHTTPHelper(t, "Bearer expected")

	dir := t.TempDir()
	if err := saveConfig(dir, httpConfig(t, helper.server.URL+"/mcp", map[string]string{
		"Authorization": "Bearer wrong",
	})); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	exports, cleanup := gateway(t, dir)
	defer cleanup()
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
	if output.Servers[0].Tools != 0 {
		t.Fatalf("an unauthorized server exported tools: %#v", output.Servers[0])
	}
}

func TestHTTPPlaintextRejectedForNonLoopback(t *testing.T) {
	_, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "remote", Transport: transportHTTP, URL: "http://mcp.example.com/mcp",
	}}})
	if err == nil {
		t.Fatal("plaintext remote HTTP must be rejected")
	}
}

func TestHTTPHeaderRoundTripperRejectsPlaintext(t *testing.T) {
	transport := &headerRoundTripper{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("the transport must not dispatch a rejected request")
		return nil, nil
	}), headers: map[string]string{"Authorization": "secret"}}
	request, err := http.NewRequest(http.MethodPost, "http://mcp.example.com/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("expected a scheme error")
	}
}

func TestHTTPHeaderRoundTripperInjectsHeaders(t *testing.T) {
	var observed string
	transport := &headerRoundTripper{base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		observed = request.Header.Get("Authorization")
		if request.Header.Get("Accept") != "application/json" {
			t.Fatalf("existing headers were dropped: %v", request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}), headers: map[string]string{"Authorization": "Bearer token"}}
	request, err := http.NewRequest(http.MethodPost, "https://mcp.example.com/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Accept", "application/json")
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if observed != "Bearer token" {
		t.Fatalf("Authorization = %q", observed)
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("the caller's request was mutated")
	}
}

func TestHTTPClientRejectsCrossHostRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	// The same machine reached by two different host names: the redirect policy
	// compares the host, not the resolved address.
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	instance := newPlatform(t.TempDir(), ingotabiNone(), ingotabi.None[workspace.Resolver](), ingotabi.None[interaction.ExecutionBinder](), discardLogger(), nil, Config{})
	server, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "remote", Transport: transportHTTP, URL: "https://mcp.example.com/mcp",
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	client, err := instance.httpClient(server.Servers[0])
	if err != nil {
		t.Fatalf("httpClient: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, redirector.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := client.Do(request); err == nil {
		t.Fatal("a cross-host redirect must be rejected")
	}
}

func TestHTTPClientFollowsSameHostRedirect(t *testing.T) {
	var mux http.ServeMux
	mux.HandleFunc("/moved", func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/target", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/target", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	})
	server := httptest.NewServer(&mux)
	defer server.Close()

	instance := newPlatform(t.TempDir(), ingotabiNone(), ingotabi.None[workspace.Resolver](), ingotabi.None[interaction.ExecutionBinder](), discardLogger(), nil, Config{})
	client, err := instance.httpClient(ServerConfig{
		Name: "remote", Transport: transportHTTP, URL: server.URL + "/moved",
	})
	if err != nil {
		t.Fatalf("httpClient: %v", err)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/moved", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("same-host redirect should be followed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestHTTPClientExpandsHeaderReferences(t *testing.T) {
	t.Setenv("TOOLMCP_TEST_HEADER_TOKEN", "expanded")
	var observed string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	instance := newPlatform(t.TempDir(), ingotabiNone(), ingotabi.None[workspace.Resolver](), ingotabi.None[interaction.ExecutionBinder](), discardLogger(), nil, Config{})
	client, err := instance.httpClient(ServerConfig{
		Name: "remote", Transport: transportHTTP, URL: server.URL,
		Headers: map[string]string{"Authorization": "Bearer ${TOOLMCP_TEST_HEADER_TOKEN}"},
	})
	if err != nil {
		t.Fatalf("httpClient: %v", err)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	response.Body.Close()
	if observed != "Bearer expanded" {
		t.Fatalf("Authorization = %q", observed)
	}
}

func TestHTTPClientRejectsUnsetHeaderReference(t *testing.T) {
	instance := newPlatform(t.TempDir(), ingotabiNone(), ingotabi.None[workspace.Resolver](), ingotabi.None[interaction.ExecutionBinder](), discardLogger(), nil, Config{})
	if _, err := instance.httpClient(ServerConfig{
		Name: "remote", Transport: transportHTTP, URL: "https://example.test/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${TOOLMCP_TEST_DEFINITELY_UNSET}"},
	}); err == nil {
		t.Fatal("an unset header reference must fail instead of sending an empty value")
	}
}

// countingHTTPClient records that the composed HTTP capability was used.
type countingHTTPClient struct {
	requests int
}

func (c *countingHTTPClient) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	c.requests++
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}}, nil
}

func TestHTTPClientUsesInjectedCapability(t *testing.T) {
	injected := &countingHTTPClient{}
	instance := newPlatform(t.TempDir(), ingotabi.Some[httpx.Client](injected), ingotabi.None[workspace.Resolver](), ingotabi.None[interaction.ExecutionBinder](), discardLogger(), nil, Config{})
	transport := instance.roundTripper(ServerConfig{Name: "remote"}, discardLogger())
	request, err := http.NewRequest(http.MethodGet, "https://example.test/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if injected.requests != 1 {
		t.Fatalf("the injected HTTP capability was used %d times", injected.requests)
	}
}

// TestHeadersBypassInjectedCapability is the credential-leak guard: the injected
// client owns its own redirect policy, so a server with configured headers uses
// the plugin's redirect-controlled transport instead.
func TestHeadersBypassInjectedCapability(t *testing.T) {
	injected := &countingHTTPClient{}
	instance := newPlatform(t.TempDir(), ingotabi.Some[httpx.Client](injected), ingotabi.None[workspace.Resolver](), ingotabi.None[interaction.ExecutionBinder](), discardLogger(), nil, Config{})
	server := ServerConfig{Name: "remote", Headers: map[string]string{"X-Api-Key": "secret"}}
	if _, ok := instance.roundTripper(server, discardLogger()).(roundTripperFunc); ok {
		t.Fatal("a server with configured headers must not delegate to the injected client")
	}
	if _, err := instance.httpClient(server); err != nil {
		t.Fatalf("httpClient: %v", err)
	}
	if injected.requests != 0 {
		t.Fatalf("the injected client was used %d times", injected.requests)
	}
}

// TestHeaderRoundTripperRejectsCrossHostResponse covers a lower layer that
// followed a redirect on its own.
func TestHeaderRoundTripperRejectsCrossHostResponse(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://mcp.example.com/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	redirected, err := http.NewRequest(http.MethodGet, "https://evil.example.com/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	transport := &headerRoundTripper{
		base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       http.NoBody,
				Request:    redirected,
			}, nil
		}),
		headers: map[string]string{"X-Api-Key": "secret"},
	}
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("a response fetched from another host must be rejected")
	}
}
