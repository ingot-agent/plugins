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
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	pluginImplementationName = "ingot-tool-mcp"
	pluginVersion            = "0.1.0"

	stderrRingBytes   = 8 << 10
	errorMessageLimit = 512

	// shutdownJoinTimeout bounds how long Cleanup waits for an abandoned
	// handshake to finish terminating its child process tree.
	shutdownJoinTimeout = 25 * time.Second

	// redactionMinimumLength is the shortest configured value treated as a
	// secret when scrubbing server-controlled text.
	redactionMinimumLength = 4
)

// connection is one live MCP session plus the resources it owns.
type connection struct {
	session   *mcp.ClientSession
	command   *exec.Cmd
	stderr    *ringBuffer
	platform  *platform
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
		if c.command != nil && c.platform != nil {
			c.platform.unregisterChild(c.command)
		}
		c.closeErr = errors.Join(closeErrors...)
	})
	return c.closeErr
}

// dial opens a new session for server. Construction of the client is cheap;
// the transport performs the handshake under ctx.
func (p *platform) dial(
	ctx context.Context,
	server ServerConfig,
	maxEventBytes int,
	root, cwdSource string,
	scope execution.Scope,
) (*connection, error) {
	expanded, err := expandServer(server)
	if err != nil {
		return nil, err
	}
	options := &mcp.ClientOptions{
		ProgressNotificationHandler: func(ctx context.Context, request *mcp.ProgressNotificationClientRequest) {
			p.emitProgress(ctx, server.Name, request)
		},
	}
	// Elicitation is advertised only for a connection that belongs to a Session
	// and only when the host can actually ask the user. Discovery has no Session,
	// so it can never prompt, and a composition without an interaction facility
	// fails closed at the protocol level instead of accepting a request nobody
	// can answer.
	if p.elicitServices() && strings.TrimSpace(string(scope.SessionID)) != "" {
		options.ElicitationHandler = func(ctx context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return p.elicit(ctx, scope, server, request)
		}
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: pluginImplementationName, Version: pluginVersion},
		options,
	)
	switch expanded.Transport {
	case transportStdio:
		return p.dialStdio(ctx, client, expanded, root)
	case transportHTTP:
		return p.dialHTTP(ctx, client, expanded, maxEventBytes)
	default:
		return nil, fmt.Errorf("server %q: unsupported transport %q", server.Name, server.Transport)
	}
}

func (p *platform) dialStdio(ctx context.Context, client *mcp.Client, server ServerConfig, root string) (*connection, error) {
	environment, err := buildEnvironment(server)
	if err != nil {
		return nil, err
	}
	command := exec.Command(server.Command, server.Args...)
	command.Env = environment
	// The working directory is fixed at process creation. Nothing ever changes
	// the cwd of a live process: POSIX cwd is per-process, so a later change
	// would race between Sessions sharing a Runtime.
	command.Dir = root
	stderr := newRingBuffer(stderrRingBytes)
	command.Stderr = stderr
	configureProcess(command)

	if !p.registerChild(command) {
		return nil, fmt.Errorf("connect to stdio server %q: plugin is shutting down", server.Name)
	}
	transport := &mcp.CommandTransport{
		Command:           command,
		TerminateDuration: time.Duration(server.ShutdownTimeoutSeconds) * time.Second,
	}
	session, abandoned, err := connectBounded(ctx, client, transport, func() {
		killProcessGroup(command)
		p.unregisterChild(command)
	})
	if err != nil {
		// On abandonment the cleanup goroutine owns the registration; releasing
		// it here would let Cleanup return while the child is still being torn
		// down.
		if !abandoned {
			p.unregisterChild(command)
		}
		return nil, fmt.Errorf("connect to stdio server %q: %w", server.Name, err)
	}
	return &connection{session: session, command: command, stderr: stderr, platform: p}, nil
}

// connectBounded performs the transport handshake but does not make the caller
// wait for post-mortem cleanup. The SDK escalates a failed handshake with two
// shutdown waits, so a synchronous failure would report connect_timeout plus
// twice the shutdown timeout. When the deadline expires the caller gets the
// context error immediately and a background goroutine finishes closing the
// transport and terminating the process tree.
func connectBounded(
	ctx context.Context,
	client *mcp.Client,
	transport mcp.Transport,
	onAbandon func(),
) (*mcp.ClientSession, bool, error) {
	type outcome struct {
		session *mcp.ClientSession
		err     error
	}
	started := time.Now()
	results := make(chan outcome, 1)
	go func() {
		session, err := client.Connect(ctx, transport, nil)
		results <- outcome{session: session, err: err}
	}()

	select {
	case result := <-results:
		return result.session, false, result.err
	case <-ctx.Done():
		go func() {
			result := <-results
			if result.session != nil {
				_ = result.session.Close()
			}
			if onAbandon != nil {
				onAbandon()
			}
		}()
		return nil, true, fmt.Errorf("handshake did not complete within %s: %w",
			time.Since(started).Round(10*time.Millisecond), ctx.Err())
	}
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
	session, _, err := connectBounded(ctx, client, transport, nil)
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
		Transport: &headerRoundTripper{base: p.roundTripper(server, p.logger), headers: headers},
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
		response, err := t.base.RoundTrip(request)
		return t.verifySameOrigin(request, response, err)
	}
	clone := request.Clone(request.Context())
	for name, value := range t.headers {
		clone.Header.Set(name, value)
	}
	response, err := t.base.RoundTrip(clone)
	return t.verifySameOrigin(clone, response, err)
}

// verifySameOrigin fails closed when a lower layer followed a redirect. It is
// defense in depth for a composed host HTTP capability that may have its own
// redirect policy.
func (t *headerRoundTripper) verifySameOrigin(
	request *http.Request,
	response *http.Response,
	err error,
) (*http.Response, error) {
	if err != nil || response == nil || response.Request == nil || response.Request.URL == nil {
		return response, err
	}
	if response.Request.URL.Host != request.URL.Host {
		response.Body.Close()
		return nil, fmt.Errorf("redirect from %q to %q rejected", request.URL.Host, response.Request.URL.Host)
	}
	return response, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// roundTripper selects the transport for one server. A composed host HTTP
// capability is used so that proxy, timeout and diagnostics policy stay
// centralized, except when the server carries configured headers: the injected
// client owns its own redirect loop, which could forward those credentials to
// another host, so such a server uses the plugin's redirect-controlled
// transport instead. HTTPS_PROXY and NO_PROXY still apply there.
func (p *platform) roundTripper(server ServerConfig, logger logging.Logger) http.RoundTripper {
	if p.http.Valid && p.http.Value != nil && len(server.Headers) == 0 {
		client := p.http.Value
		return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return client.Do(request.Context(), request)
		})
	}
	if p.http.Valid && p.http.Value != nil && len(server.Headers) > 0 {
		logger.Debug(context.Background(), "using the plugin transport for a server with configured headers",
			logging.String("server", server.Name))
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
	// Progress text is server-controlled and unbounded; it goes to the
	// observation stream, so it is flattened and capped like any other
	// diagnostic string.
	message = sanitizeMessage(message)
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
