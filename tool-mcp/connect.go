package toolmcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	pluginImplementationName = "ingot-tool-mcp"
	pluginVersion            = "0.1.0"

	stderrRingBytes   = 8 << 10
	errorMessageLimit = 512
)

// connection is one live MCP session plus the resources it owns.
type connection struct {
	session   *mcp.ClientSession
	command   *exec.Cmd
	stderr    *ringBuffer
	closeOnce sync.Once
	closeErr  error
}

// Close terminates the session. For stdio servers the child process tree is
// terminated as well; the SDK already closes stdin, waits, and escalates
// signals for the direct child, so the process group is only swept when that
// escalation failed.
func (c *connection) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		var closeErrors []error
		if c.session != nil {
			if err := c.session.Close(); err != nil {
				closeErrors = append(closeErrors, err)
				killProcessGroup(c.command)
			}
		} else {
			killProcessGroup(c.command)
		}
		c.closeErr = errors.Join(closeErrors...)
	})
	return c.closeErr
}

// dial opens a new session for server. Construction of the client is cheap;
// the transport performs the handshake under ctx.
func (p *platform) dial(ctx context.Context, server ServerConfig, maxEventBytes int) (*connection, error) {
	expanded, err := expandServer(server)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: pluginImplementationName, Version: pluginVersion},
		&mcp.ClientOptions{
			ProgressNotificationHandler: func(ctx context.Context, request *mcp.ProgressNotificationClientRequest) {
				p.emitProgress(ctx, server.Name, request)
			},
		},
	)
	switch expanded.Transport {
	case transportStdio:
		return p.dialStdio(ctx, client, expanded)
	case transportHTTP:
		return p.dialHTTP(ctx, client, expanded, maxEventBytes)
	default:
		return nil, fmt.Errorf("server %q: unsupported transport %q", server.Name, server.Transport)
	}
}

func (p *platform) dialStdio(ctx context.Context, client *mcp.Client, server ServerConfig) (*connection, error) {
	environment, err := buildEnvironment(server)
	if err != nil {
		return nil, err
	}
	command := exec.Command(server.Command, server.Args...)
	command.Env = environment
	if server.Cwd != "" {
		command.Dir = server.Cwd
	}
	stderr := newRingBuffer(stderrRingBytes)
	command.Stderr = stderr
	configureProcess(command)

	transport := &mcp.CommandTransport{
		Command:           command,
		TerminateDuration: time.Duration(server.ShutdownTimeoutSeconds) * time.Second,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		killProcessGroup(command)
		return nil, fmt.Errorf("connect to stdio server %q: %w", server.Name, err)
	}
	return &connection{session: session, command: command, stderr: stderr}, nil
}

func (p *platform) dialHTTP(ctx context.Context, client *mcp.Client, server ServerConfig, maxEventBytes int) (*connection, error) {
	httpClient, err := p.httpClient(server)
	if err != nil {
		return nil, err
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:   server.URL,
		HTTPClient: httpClient,
		// Retries are owned by this Plugin so that a failed call is surfaced
		// instead of being silently repeated with side effects.
		MaxRetries: -1,
		// Tool discovery is snapshot based, so the standalone server-sent
		// event stream buys nothing and keeps an unnecessary connection open.
		DisableStandaloneSSE: true,
		MaxEventSize:         maxEventBytes,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to http server %q: %w", server.Name, err)
	}
	return &connection{session: session}, nil
}

// httpClient builds a bounded HTTP client for one server. Configured headers
// are injected on every request, including the transport handshake.
func (p *platform) httpClient(server ServerConfig) (*http.Client, error) {
	headers := make(map[string]string, len(server.Headers))
	for name, raw := range server.Headers {
		value, err := expandVariables(raw)
		if err != nil {
			return nil, fmt.Errorf("server %q: header %s: %w", server.Name, name, err)
		}
		headers[name] = value
	}
	client := &http.Client{
		Transport: &headerRoundTripper{base: p.roundTripper(), headers: headers},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if request.URL.Scheme != "https" &&
				!(request.URL.Scheme == "http" && isLoopbackHost(request.URL.Hostname())) {
				return fmt.Errorf("redirect to unsupported scheme %q", request.URL.Scheme)
			}
			if request.URL.Host != via[0].URL.Host {
				return fmt.Errorf("cross-host redirect from %q to %q rejected", via[0].URL.Host, request.URL.Host)
			}
			return nil
		},
	}
	return client, nil
}

// headerRoundTripper injects per-server headers and enforces the transport
// scheme policy on every request.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(request.URL.Hostname()) {
			return nil, fmt.Errorf("plaintext http is only allowed for loopback hosts")
		}
	default:
		return nil, fmt.Errorf("unsupported scheme %q", request.URL.Scheme)
	}
	if len(t.headers) == 0 {
		return t.base.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	for name, value := range t.headers {
		clone.Header.Set(name, value)
	}
	return t.base.RoundTrip(clone)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// roundTripper selects the injected host HTTP capability when one is
// composed, so that proxy, timeout, and diagnostics policy stay centralized.
func (p *platform) roundTripper() http.RoundTripper {
	if p.http.Valid && p.http.Value != nil {
		client := p.http.Value
		return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return client.Do(request.Context(), request)
		})
	}
	return http.DefaultTransport
}

// emitProgress forwards MCP progress notifications to the observation
// capability. The server name is the logical progress channel.
func (p *platform) emitProgress(ctx context.Context, server string, request *mcp.ProgressNotificationClientRequest) {
	if p.observation == nil || request == nil || request.Params == nil {
		return
	}
	message := strings.TrimSpace(request.Params.Message)
	if message == "" {
		return
	}
	if request.Params.Total > 0 {
		message = fmt.Sprintf("%s (%.0f/%.0f)", message, request.Params.Progress, request.Params.Total)
	}
	p.observation.Emit(ctx, observation.ToolProgress{
		Progress: tool.Progress{Channel: server, Content: content.FromText(message)},
	})
}

// ringBuffer retains the most recent stderr bytes of a stdio server. It exists
// so the child never blocks on a full pipe, and it is bounded so a noisy
// server cannot exhaust memory.
type ringBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func newRingBuffer(limit int) *ringBuffer {
	return &ringBuffer{limit: limit}
}

func (b *ringBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

// String returns the retained bytes with invalid UTF-8 replaced, ready to be
// surfaced in diagnostics.
func (b *ringBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(string(b.data), string('\uFFFD'))
}

// sanitizeMessage bounds an error message and flattens newlines so it can be
// reported through diagnostics or the status tool.
func sanitizeMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > errorMessageLimit {
		message = message[:errorMessageLimit] + "..."
	}
	return message
}
