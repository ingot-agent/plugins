package toolmcp

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/observation"
)

const (
	backoffInitial = time.Second
	backoffMaximum = 60 * time.Second
)

// platform holds the shared plugin state: the live configuration, the injected
// capabilities, and one lazy connector per configured server.
type platform struct {
	stateDir    string
	http        ingotabi.Optional[httpx.Client]
	logger      logging.Logger
	observation observation.Consumer

	config atomic.Pointer[Config]
	// generation advances on every configuration publication, so a connection
	// dialled with an older configuration is never installed.
	generation atomic.Uint64
	// closed is set by Cleanup; late dials observe it and close their session.
	closed atomic.Bool

	mu         sync.Mutex
	servers    map[string]*serverRuntime
	discoverMu sync.Mutex
	// children holds every live child process, including one whose handshake is
	// still running, so Cleanup can terminate it instead of leaving it orphaned.
	children map[*exec.Cmd]struct{}
}

func newPlatform(stateDir string, httpClient ingotabi.Optional[httpx.Client], logger logging.Logger, observer observation.Consumer, config Config) *platform {
	instance := &platform{
		stateDir:    stateDir,
		http:        httpClient,
		logger:      logger,
		observation: observer,
		servers:     make(map[string]*serverRuntime),
		children:    make(map[*exec.Cmd]struct{}),
	}
	instance.setConfig(config)
	return instance
}

func (p *platform) setConfig(config Config) {
	normalized := config
	p.config.Store(&normalized)
	p.generation.Add(1)
}

// registerChild records a child process that Cleanup may need to terminate.
func (p *platform) registerChild(command *exec.Cmd) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return false
	}
	p.children[command] = struct{}{}
	return true
}

// unregisterChild drops the registration once the child is gone or the
// connection owns it.
func (p *platform) unregisterChild(command *exec.Cmd) {
	p.mu.Lock()
	delete(p.children, command)
	p.mu.Unlock()
}

// waitForChildren waits, within a bound, until every registered child is gone.
// Registration is removed by whoever finishes with the process, so this joins
// abandoned handshakes without a second lifecycle mechanism.
func (p *platform) waitForChildren(deadline time.Duration) {
	expired := time.Now().Add(deadline)
	for {
		p.mu.Lock()
		remaining := len(p.children)
		p.mu.Unlock()
		if remaining == 0 || time.Now().After(expired) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// currentGeneration identifies the configuration a decision was made against.
func (p *platform) currentGeneration() uint64 { return p.generation.Load() }

// acquireForDial returns the server configuration and generation to dial with,
// or an error while shutting down.
func (p *platform) acquireForDial(name string) (ServerConfig, uint64, Config, error) {
	if p.closed.Load() {
		return ServerConfig{}, 0, Config{}, fmt.Errorf("tool.mcp: plugin is shutting down")
	}
	config := p.currentConfig()
	server, ok := config.server(name)
	if !ok {
		return ServerConfig{}, 0, Config{}, fmt.Errorf("MCP server %q is not configured", name)
	}
	if !server.enabled() {
		return ServerConfig{}, 0, Config{}, fmt.Errorf("MCP server %q is disabled", name)
	}
	return server, p.currentGeneration(), config, nil
}

func (p *platform) currentConfig() Config {
	if loaded := p.config.Load(); loaded != nil {
		return *loaded
	}
	return Config{}
}

// runtimeFor returns the connector for a server, creating it on first use.
func (p *platform) runtimeFor(name string) *serverRuntime {
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.servers[name]; ok {
		return existing
	}
	created := &serverRuntime{name: name}
	p.servers[name] = created
	return created
}

// acquire returns a live session lease for the named server, connecting when
// necessary. The caller must release the lease when the call finishes.
func (p *platform) acquire(ctx context.Context, name string) (*lease, error) {
	server, generation, config, err := p.acquireForDial(name)
	if err != nil {
		return nil, err
	}
	return p.runtimeFor(name).acquire(ctx, p, server, generation, config.MaxResultBytes)
}

// markStale retires every live session. A session still used by an in-flight
// call is closed when that call releases it, so reconfiguration never aborts a
// call that already started; later calls connect with the new configuration.
func (p *platform) markStale() {
	p.mu.Lock()
	runtimes := make([]*serverRuntime, 0, len(p.servers))
	for _, runtime := range p.servers {
		runtimes = append(runtimes, runtime)
	}
	p.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.markStale()
	}
}

// closeAll terminates every session and waits, within a bound, for in-flight
// handshakes to finish terminating their child processes. It is the instance
// Cleanup, so it must not rely on a later lease release.
func (p *platform) closeAll() {
	p.mu.Lock()
	p.closed.Store(true)
	runtimes := make([]*serverRuntime, 0, len(p.servers))
	for _, runtime := range p.servers {
		runtimes = append(runtimes, runtime)
	}
	children := make([]*exec.Cmd, 0, len(p.children))
	for command := range p.children {
		children = append(children, command)
	}
	p.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.close()
	}
	// A handshake may still be inside Connect; terminate its child directly
	// instead of waiting for its connect timeout.
	for _, command := range children {
		killProcessGroup(command)
	}

	p.waitForChildren(shutdownJoinTimeout)
}

// serverStatus is the secret-free connection state reported by mcp_status.
type serverStatus struct {
	Name              string  `json:"name"`
	Transport         string  `json:"transport"`
	Status            string  `json:"status"`
	Connected         bool    `json:"connected"`
	ConsecutiveErrors int     `json:"consecutive_errors"`
	RetryAfterSeconds float64 `json:"retry_after_seconds,omitempty"`
	LastError         string  `json:"last_error,omitempty"`
	Calls             int64   `json:"calls"`
}

func (p *platform) statuses() []serverStatus {
	config := p.currentConfig()
	p.mu.Lock()
	runtimes := make(map[string]*serverRuntime, len(p.servers))
	for name, runtime := range p.servers {
		runtimes[name] = runtime
	}
	p.mu.Unlock()

	result := make([]serverStatus, 0, len(config.Servers))
	for _, server := range config.Servers {
		status := serverStatus{
			Name:      server.Name,
			Transport: server.Transport,
			Status:    "never",
		}
		if !server.enabled() {
			status.Status = "disabled"
			result = append(result, status)
			continue
		}
		if runtime, ok := runtimes[server.Name]; ok {
			status = runtime.status(status)
		}
		result = append(result, status)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// serverRuntime owns one server's live session and its reconnect policy.
type serverRuntime struct {
	name string

	mu      sync.Mutex
	session *lease
	// slotInUse counts in-flight calls; slotNotify is closed when a slot frees.
	// A dynamic count lets a changed max_concurrent_calls take effect for later
	// calls without replacing a limiter that is already in use.
	slotInUse  int
	slotNotify chan struct{}

	connectMu sync.Mutex

	failures  int
	retryAt   time.Time
	lastError string
	lastUsed  time.Time
	calls     int64
}

// acquireSlot reserves one of the server's concurrency slots. Many stdio
// servers are single-threaded, so this bound is a correctness aid rather than
// only a load limit.
func (r *serverRuntime) acquireSlot(ctx context.Context, limit int) (func(), error) {
	for {
		r.mu.Lock()
		if r.slotInUse < limit {
			r.slotInUse++
			r.mu.Unlock()
			return r.releaseSlot, nil
		}
		if r.slotNotify == nil {
			r.slotNotify = make(chan struct{})
		}
		freed := r.slotNotify
		r.mu.Unlock()

		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// releaseSlot frees one concurrency slot and wakes every waiter.
func (r *serverRuntime) releaseSlot() {
	r.mu.Lock()
	if r.slotInUse > 0 {
		r.slotInUse--
	}
	if r.slotNotify != nil {
		close(r.slotNotify)
		r.slotNotify = nil
	}
	r.mu.Unlock()
}

func (r *serverRuntime) markStale() {
	r.mu.Lock()
	handle := r.session
	r.session = nil
	r.mu.Unlock()
	if handle != nil {
		handle.retire()
	}
}

func (r *serverRuntime) close() {
	r.mu.Lock()
	handle := r.session
	r.session = nil
	r.mu.Unlock()
	if handle != nil {
		handle.forceClose()
	}
}

// lease is one in-flight use of a session. Retiring a session marks it
// unusable for new callers; the underlying connection is closed only after the
// last lease is released.
type lease struct {
	conn *connection
	// onIdle runs when the last lease goes away while the session stays live,
	// which is the only correct moment to start measuring idleness.
	onIdle func()

	mu      sync.Mutex
	refs    int
	retired bool
}

func newLease(conn *connection, onIdle func()) *lease {
	return &lease{conn: conn, onIdle: onIdle, refs: 1}
}

// inUse reports whether a call currently holds this session.
func (l *lease) inUse() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refs > 0
}

func (l *lease) acquireRef() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.retired {
		return false
	}
	l.refs++
	return true
}

// release drops one use and closes the connection once it is retired and
// unused.
func (l *lease) release() {
	l.mu.Lock()
	l.refs--
	idle := l.refs <= 0
	closable := idle && l.retired
	l.mu.Unlock()
	switch {
	case closable:
		_ = l.conn.Close()
	case idle && l.onIdle != nil:
		l.onIdle()
	}
}

// forceClose terminates the connection immediately, even while calls still
// hold a lease. Only instance cleanup uses it: at shutdown there is no later
// release to wait for.
func (l *lease) forceClose() {
	l.mu.Lock()
	l.retired = true
	l.mu.Unlock()
	_ = l.conn.Close()
}

// retire makes the session unusable for new callers.
func (l *lease) retire() {
	l.mu.Lock()
	l.retired = true
	last := l.refs <= 0
	l.mu.Unlock()
	if last {
		_ = l.conn.Close()
	}
}

// acquire returns a lease on the live session, connecting at most once
// concurrently. The caller must release the lease; that is what allows a
// retired session to be closed without interrupting an in-flight call.
func (r *serverRuntime) acquire(ctx context.Context, p *platform, server ServerConfig, generation uint64, maxEventBytes int) (*lease, error) {
	for {
		r.mu.Lock()
		handle := r.session
		retryBlocked := handle == nil && !r.retryAt.IsZero() && time.Now().Before(r.retryAt)
		if handle != nil && !r.idleExpired(server) && handle.acquireRef() {
			r.lastUsed = time.Now()
			r.calls++
			r.mu.Unlock()
			return handle, nil
		}
		if handle != nil {
			// Idle or retired: detach it so the next caller connects.
			r.session = nil
		}
		if retryBlocked {
			err := r.backoffErrorLocked()
			r.mu.Unlock()
			return nil, err
		}
		r.mu.Unlock()
		if handle != nil {
			handle.retire()
		}
		break
	}

	r.connectMu.Lock()
	defer r.connectMu.Unlock()

	r.mu.Lock()
	if handle := r.session; handle != nil && handle.acquireRef() {
		r.lastUsed = time.Now()
		r.calls++
		r.mu.Unlock()
		return handle, nil
	}
	r.mu.Unlock()

	timeout := time.Duration(server.ConnectTimeoutSeconds) * time.Second
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	connection, err := p.dial(connectCtx, server, maxEventBytes)
	if err != nil {
		r.recordFailure(err)
		p.logger.Warn(connectCtx, "MCP server connection failed",
			logging.String("server", server.Name),
			logging.String("transport", server.Transport),
			logging.String("error", sanitizeMessage(err.Error())))
		return nil, err
	}

	// A configuration published while this dial was running makes the new
	// connection already obsolete: it was built from superseded settings. Check
	// before publishing it so no concurrent caller can observe it.
	if p.currentGeneration() != generation {
		_ = connection.Close()
		return nil, fmt.Errorf("MCP server %q: configuration changed during connect; retry the call", server.Name)
	}
	if p.closed.Load() {
		_ = connection.Close()
		return nil, fmt.Errorf("tool.mcp: plugin is shutting down")
	}

	handle := newLease(connection, r.markIdle)

	r.mu.Lock()
	previous := r.session
	r.session = handle
	r.failures = 0
	r.retryAt = time.Time{}
	r.lastError = ""
	r.lastUsed = time.Now()
	r.calls++
	r.mu.Unlock()
	if previous != nil {
		previous.retire()
	}

	// Cleanup may have run between the check above and the publication; the
	// runtime would then never close this connection, so close it here.
	if p.closed.Load() {
		r.mu.Lock()
		if r.session == handle {
			r.session = nil
		}
		r.mu.Unlock()
		handle.forceClose()
		return nil, fmt.Errorf("tool.mcp: plugin is shutting down")
	}

	p.logger.Info(ctx, "MCP server connected",
		logging.String("server", server.Name),
		logging.String("transport", server.Transport))
	return handle, nil
}

// invalidate reports that a live session is unusable after a transport failure,
// so the next call reconnects and the backoff and status state reflect the
// outage. Without this a crashed server would stay "connected" forever.
func (r *serverRuntime) invalidate(handle *lease, cause error) bool {
	r.mu.Lock()
	attached := r.session == handle
	if attached {
		r.session = nil
	}
	r.mu.Unlock()
	handle.retire()
	if attached && cause != nil {
		// Without this a server that dies mid-call would be re-dialled on every
		// call with no backoff, and status would keep reporting no failures.
		r.recordFailure(cause)
	}
	return attached
}

// markIdle records that the last holder of the live session finished its call.
func (r *serverRuntime) markIdle() {
	r.mu.Lock()
	r.lastUsed = time.Now()
	r.mu.Unlock()
}

func (r *serverRuntime) idleExpired(server ServerConfig) bool {
	if server.IdleTimeoutSeconds <= 0 || r.session == nil || r.session.inUse() {
		return false
	}
	return time.Since(r.lastUsed) > time.Duration(server.IdleTimeoutSeconds)*time.Second
}

func (r *serverRuntime) recordFailure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures++
	delay := backoffInitial
	for attempt := 1; attempt < r.failures && delay < backoffMaximum; attempt++ {
		delay *= 2
	}
	if delay > backoffMaximum {
		delay = backoffMaximum
	}
	r.retryAt = time.Now().Add(delay)
	r.lastError = sanitizeMessage(err.Error())
}

// backoffErrorLocked reports a fast failure while the connector waits out its
// backoff window; callers must hold r.mu.
func (r *serverRuntime) backoffErrorLocked() error {
	remaining := time.Until(r.retryAt)
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Errorf("MCP server %q is unavailable, retrying in %s: %s",
		r.name, remaining.Round(time.Second), r.lastError)
}

// status reports the current connection state without touching the network.
func (r *serverRuntime) status(base serverStatus) serverStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	base.Connected = r.session != nil
	base.ConsecutiveErrors = r.failures
	base.LastError = r.lastError
	base.Calls = r.calls
	switch {
	case r.session != nil:
		base.Status = "connected"
	case !r.retryAt.IsZero() && time.Now().Before(r.retryAt):
		base.Status = "backoff"
		base.RetryAfterSeconds = time.Until(r.retryAt).Round(time.Second).Seconds()
	case r.failures > 0:
		base.Status = "idle"
	default:
		base.Status = "idle"
	}
	return base
}
