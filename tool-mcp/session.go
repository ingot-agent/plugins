package toolmcp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/workspace"
)

const (
	backoffInitial = time.Second
	backoffMaximum = 60 * time.Second

	// maxSessionsPerServer bounds how many concurrent MCP sessions one server
	// may carry. A stdio session owns a child process, so this is a resource
	// bound rather than a policy knob: it is not configurable, and reaching it
	// fails closed instead of sharing a session across callers, because a shared
	// session would expose one Session's state to another.
	maxSessionsPerServer = 8
)

const (
	cwdSourceWorkspace = "session-workspace"
	cwdSourceConfig    = "server-cwd"
	cwdSourceProcess   = "runtime-process"
	cwdSourceDiscovery = "discovery"
)

// connectionKey identifies one MCP session. State isolation is per Session, not
// per server: sharing a session would let one Session observe another's tabs,
// cwd, or server-side state, and the caller's Session is the only identity that
// can be attributed to a request.
type connectionKey struct {
	server  string
	session string
}

// serverSlots bounds concurrent calls for one server across all its sessions.
// Many stdio servers are single-threaded, so the bound protects the server
// rather than only the caller.
type serverSlots struct {
	mu     sync.Mutex
	inUse  int
	notify chan struct{}
}

// platform holds the shared plugin state: the live configuration, injected
// capabilities, and the lazy connectors keyed by server and Session.
type platform struct {
	stateDir    string
	http        ingotabi.Optional[httpx.Client]
	workspace   ingotabi.Optional[workspace.Resolver]
	logger      logging.Logger
	observation observation.Consumer

	config atomic.Pointer[Config]
	// generation advances on every configuration publication, so a connection
	// dialled with an older configuration is never installed.
	generation atomic.Uint64
	// closed is set by Cleanup; late dials observe it and close their session.
	closed atomic.Bool

	mu         sync.Mutex
	runtimes   map[connectionKey]*serverRuntime
	slots      map[string]*serverSlots
	discoverMu sync.Mutex
	// children holds every live child process, including one whose handshake is
	// still running, so Cleanup can terminate it instead of leaving it orphaned.
	children map[*exec.Cmd]struct{}
}

func newPlatform(
	stateDir string,
	httpClient ingotabi.Optional[httpx.Client],
	resolver ingotabi.Optional[workspace.Resolver],
	logger logging.Logger,
	observer observation.Consumer,
	config Config,
) *platform {
	instance := &platform{
		stateDir:    stateDir,
		http:        httpClient,
		workspace:   resolver,
		logger:      logger,
		observation: observer,
		runtimes:    make(map[connectionKey]*serverRuntime),
		slots:       make(map[string]*serverSlots),
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

func (p *platform) currentConfig() Config {
	if loaded := p.config.Load(); loaded != nil {
		return *loaded
	}
	return Config{}
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

func (p *platform) currentGeneration() uint64 { return p.generation.Load() }

// slotsFor returns the shared concurrency limiter of one server.
func (p *platform) slotsFor(name string) *serverSlots {
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.slots[name]; ok {
		return existing
	}
	created := &serverSlots{}
	p.slots[name] = created
	return created
}

func (s *serverSlots) acquire(ctx context.Context, limit int) (func(), error) {
	for {
		s.mu.Lock()
		if s.inUse < limit {
			s.inUse++
			s.mu.Unlock()
			return s.release, nil
		}
		if s.notify == nil {
			s.notify = make(chan struct{})
		}
		freed := s.notify
		s.mu.Unlock()

		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *serverSlots) release() {
	s.mu.Lock()
	if s.inUse > 0 {
		s.inUse--
	}
	if s.notify != nil {
		close(s.notify)
		s.notify = nil
	}
	s.mu.Unlock()
}

func (s *serverSlots) inFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse
}

// workspaceRoot resolves the working directory for one connection. An empty
// result means "inherit the Runtime working directory"; the second return value
// records where the decision came from, which is reported by mcp_status so a
// user can see why files land where they do.
func (p *platform) workspaceRoot(ctx context.Context, server ServerConfig, scope execution.Scope) (string, string, error) {
	if server.Transport == transportHTTP {
		return "", "", nil
	}
	if strings.TrimSpace(server.Cwd) != "" {
		return server.Cwd, cwdSourceConfig, nil
	}
	if !p.workspace.Valid {
		// The composition has no workspace concept at all, so no Session can be
		// bound. Inheriting is then the only available answer.
		return "", cwdSourceProcess, nil
	}
	binding, err := p.workspace.Value.Resolve(ctx, scope)
	if errors.Is(err, workspace.ErrNotAssigned) {
		// Assigning a workspace later would leave a connection cached in the
		// wrong directory forever, silently, so this fails instead.
		return "", "", fmt.Errorf(
			"session has no workspace assigned yet; open or assign a workspace before calling MCP tools: %w", err)
	}
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace: %w", err)
	}
	return binding.Root, cwdSourceWorkspace, nil
}

// acquire returns a lease on the live MCP session of one Session, connecting
// when necessary. The caller must release the lease when the call finishes.
func (p *platform) acquire(ctx context.Context, name string, scope execution.Scope) (*lease, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("tool.mcp: plugin is shutting down")
	}
	config := p.currentConfig()
	server, ok := config.server(name)
	if !ok {
		return nil, fmt.Errorf("MCP server %q is not configured", name)
	}
	if !server.enabled() {
		return nil, fmt.Errorf("MCP server %q is disabled", name)
	}

	root, cwdSource, err := p.workspaceRoot(ctx, server, scope)
	if err != nil {
		return nil, fmt.Errorf("MCP server %q: %w", name, err)
	}

	key := connectionKey{server: name, session: string(scope.SessionID)}
	runtime, err := p.runtimeFor(key, server, root, cwdSource)
	if err != nil {
		return nil, err
	}
	return runtime.acquire(ctx, p, server, p.currentGeneration(), config.MaxResultBytes, root, cwdSource)
}

// runtimeFor returns the connector for one Session. When the server is already
// at its session limit it evicts an idle sibling; if every sibling is serving a
// call it fails rather than sharing a session across callers.
func (p *platform) runtimeFor(key connectionKey, server ServerConfig, root, cwdSource string) (*serverRuntime, error) {
	for {
		p.mu.Lock()
		if existing, ok := p.runtimes[key]; ok {
			p.mu.Unlock()
			return existing, nil
		}
		if p.countLocked(key.server) >= maxSessionsPerServer {
			evicted := p.evictIdleLocked(key.server, key)
			if evicted == nil {
				p.mu.Unlock()
				return nil, fmt.Errorf(
					"MCP server %q already has %d active sessions and every one is busy; retry when a call finishes",
					key.server, maxSessionsPerServer)
			}
			// Close outside the lock, then re-check capacity: another caller may
			// have taken the freed slot in the meantime.
			p.mu.Unlock()
			evicted.close()
			continue
		}
		created := &serverRuntime{name: key.server, session: key.session, root: root, cwdSource: cwdSource}
		p.runtimes[key] = created
		p.mu.Unlock()
		return created, nil
	}
}

func (p *platform) countLocked(server string) int {
	count := 0
	for key := range p.runtimes {
		if key.server == server {
			count++
		}
	}
	return count
}

// evictIdleLocked removes the least recently used connector that is not serving
// a call. It reports the evicted connector so the caller can close it after
// releasing p.mu, and returns nil when every connector is busy.
func (p *platform) evictIdleLocked(server string, keep connectionKey) *serverRuntime {
	var candidateKey connectionKey
	var candidate *serverRuntime
	for key, runtime := range p.runtimes {
		if key.server != server || key == keep {
			continue
		}
		if runtime.inUse() {
			continue
		}
		if candidate == nil || runtime.lastUsedTime().Before(candidate.lastUsedTime()) {
			candidateKey, candidate = key, runtime
		}
	}
	if candidate == nil {
		return nil
	}
	delete(p.runtimes, candidateKey)
	return candidate
}

// invalidateLease retires the connector of one Session after a transport
// failure. It never creates a connector: a fresh one would consume a capacity
// slot and carry no resolved working directory.
func (p *platform) invalidateLease(key connectionKey, handle *lease, cause error) bool {
	p.mu.Lock()
	runtime, ok := p.runtimes[key]
	p.mu.Unlock()
	if !ok {
		handle.retire()
		return false
	}
	return runtime.invalidate(handle, cause)
}

// markStale retires every live session. A session still used by an in-flight
// call is closed when that call releases it, so reconfiguration never aborts a
// call that already started; later calls connect with the new configuration.
func (p *platform) markStale() {
	p.mu.Lock()
	runtimes := make([]*serverRuntime, 0, len(p.runtimes))
	for _, runtime := range p.runtimes {
		runtimes = append(runtimes, runtime)
	}
	p.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.markStale()
	}
}

// closeAll terminates every session and waits, within a bound, for in-flight
// handshakes to finish terminating their child processes.
func (p *platform) closeAll() {
	p.mu.Lock()
	p.closed.Store(true)
	runtimes := make([]*serverRuntime, 0, len(p.runtimes))
	for _, runtime := range p.runtimes {
		runtimes = append(runtimes, runtime)
	}
	p.runtimes = make(map[connectionKey]*serverRuntime)
	children := make([]*exec.Cmd, 0, len(p.children))
	for command := range p.children {
		children = append(children, command)
	}
	p.mu.Unlock()

	closed := make(chan struct{}, len(runtimes))
	for _, runtime := range runtimes {
		go func(runtime *serverRuntime) {
			runtime.close()
			closed <- struct{}{}
		}(runtime)
	}
	for range runtimes {
		<-closed
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
	Connections       int     `json:"connections"`
	InFlightCalls     int     `json:"in_flight_calls"`
	ConsecutiveErrors int     `json:"consecutive_errors"`
	RetryAfterSeconds float64 `json:"retry_after_seconds,omitempty"`
	LastError         string  `json:"last_error,omitempty"`
	Calls             int64   `json:"calls"`
	// CwdSource explains where the working directory of a connection came from,
	// so "my files landed elsewhere" is answerable without reading code.
	CwdSource string `json:"cwd_source,omitempty"`
}

// statuses aggregates every connection of every configured server.
func (p *platform) statuses() []serverStatus {
	config := p.currentConfig()
	p.mu.Lock()
	grouped := make(map[string][]*serverRuntime)
	for key, runtime := range p.runtimes {
		grouped[key.server] = append(grouped[key.server], runtime)
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
		status.InFlightCalls = p.slotsFor(server.Name).inFlight()
		runtimes := grouped[server.Name]
		status.Connections = len(runtimes)
		if len(runtimes) == 0 {
			result = append(result, status)
			continue
		}
		// One server can carry several connections in different states. Report
		// any live connection, otherwise the most severe failure, so a single
		// broken Session is visible instead of averaged away.
		merged := serverStatus{}
		for index, runtime := range runtimes {
			current := runtime.status(serverStatus{Name: server.Name, Transport: server.Transport})
			if index == 0 {
				merged = current
			}
			merged.Calls += current.Calls
			merged.Connected = merged.Connected || current.Connected
			if current.ConsecutiveErrors > merged.ConsecutiveErrors {
				merged.ConsecutiveErrors = current.ConsecutiveErrors
			}
			if merged.LastError == "" {
				merged.LastError = current.LastError
			}
			if merged.RetryAfterSeconds == 0 {
				merged.RetryAfterSeconds = current.RetryAfterSeconds
			}
			if merged.CwdSource == "" {
				merged.CwdSource = current.CwdSource
			}
			if statusRank(current.Status) > statusRank(merged.Status) {
				merged.Status = current.Status
			}
		}
		merged.Name = server.Name
		merged.Transport = server.Transport
		merged.Connections = len(runtimes)
		merged.InFlightCalls = status.InFlightCalls
		result = append(result, merged)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// statusRank orders connection states by how much attention they need.
func statusRank(status string) int {
	switch status {
	case "connected":
		return 3
	case "backoff":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}

// serverRuntime owns the MCP session of one (server, Session) pair.
type serverRuntime struct {
	name    string
	session string
	// root is the working directory this connection was created with, and
	// cwdSource records why.
	root      string
	cwdSource string

	mu        sync.Mutex
	handle    *lease
	failures  int
	retryAt   time.Time
	lastError string
	lastUsed  time.Time
	calls     int64

	connectMu sync.Mutex
}

func (r *serverRuntime) markStale() {
	r.mu.Lock()
	handle := r.handle
	r.handle = nil
	r.mu.Unlock()
	if handle != nil {
		handle.retire()
	}
}

func (r *serverRuntime) close() {
	r.mu.Lock()
	handle := r.handle
	r.handle = nil
	r.mu.Unlock()
	if handle != nil {
		handle.forceClose()
	}
}

// inUse reports whether a call currently uses this connection.
func (r *serverRuntime) inUse() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handle == nil {
		return false
	}
	return r.handle.inUse()
}

func (r *serverRuntime) lastUsedTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastUsed
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
// hold a lease. Only instance cleanup and eviction use it.
func (l *lease) forceClose() {
	l.mu.Lock()
	l.retired = true
	l.mu.Unlock()
	_ = l.conn.Close()
}

func (l *lease) retire() {
	l.mu.Lock()
	l.retired = true
	last := l.refs <= 0
	l.mu.Unlock()
	if last {
		_ = l.conn.Close()
	}
}

// acquire returns a lease on the live connection, connecting at most once
// concurrently. The caller must release the lease.
func (r *serverRuntime) acquire(
	ctx context.Context,
	p *platform,
	server ServerConfig,
	generation uint64,
	maxEventBytes int,
	root, cwdSource string,
) (*lease, error) {
	for {
		r.mu.Lock()
		handle := r.handle
		retryBlocked := handle == nil && !r.retryAt.IsZero() && time.Now().Before(r.retryAt)
		if handle != nil && !r.idleExpired(server) && handle.acquireRef() {
			r.lastUsed = time.Now()
			r.calls++
			r.mu.Unlock()
			return handle, nil
		}
		if handle != nil {
			r.handle = nil
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
	if handle := r.handle; handle != nil && handle.acquireRef() {
		r.lastUsed = time.Now()
		r.calls++
		r.mu.Unlock()
		return handle, nil
	}
	r.mu.Unlock()

	timeout := time.Duration(server.ConnectTimeoutSeconds) * time.Second
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	connection, err := p.dial(connectCtx, server, maxEventBytes, root, cwdSource)
	if err != nil {
		r.recordFailure(err)
		p.logger.Warn(connectCtx, "MCP server connection failed",
			logging.String("server", server.Name),
			logging.String("error", sanitizeMessage(err.Error())))
		return nil, err
	}

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
	previous := r.handle
	r.handle = handle
	r.root = root
	r.cwdSource = cwdSource
	r.failures = 0
	r.retryAt = time.Time{}
	r.lastError = ""
	r.lastUsed = time.Now()
	r.calls++
	r.mu.Unlock()
	if previous != nil {
		previous.retire()
	}

	if p.closed.Load() {
		r.mu.Lock()
		if r.handle == handle {
			r.handle = nil
		}
		r.mu.Unlock()
		handle.forceClose()
		return nil, fmt.Errorf("tool.mcp: plugin is shutting down")
	}

	p.logger.Info(ctx, "MCP server connected",
		logging.String("server", server.Name),
		logging.String("cwd_source", cwdSource))
	return handle, nil
}

// invalidate reports that a live session is unusable after a transport failure,
// so the next call reconnects and the backoff and status state reflect the
// outage.
func (r *serverRuntime) invalidate(handle *lease, cause error) bool {
	r.mu.Lock()
	attached := r.handle == handle
	if attached {
		r.handle = nil
	}
	r.mu.Unlock()
	handle.retire()
	if attached && cause != nil {
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
	if server.IdleTimeoutSeconds <= 0 || r.handle == nil || r.handle.inUse() {
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

func (r *serverRuntime) backoffErrorLocked() error {
	remaining := time.Until(r.retryAt)
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Errorf("MCP server %q is unavailable, retrying in %s: %s",
		r.name, remaining.Round(time.Second), r.lastError)
}

func (r *serverRuntime) status(base serverStatus) serverStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	base.Connected = r.handle != nil
	base.ConsecutiveErrors = r.failures
	base.LastError = r.lastError
	base.Calls = r.calls
	base.CwdSource = r.cwdSource
	switch {
	case r.handle != nil:
		base.Status = "connected"
	case !r.retryAt.IsZero() && time.Now().Before(r.retryAt):
		base.Status = "backoff"
		base.RetryAfterSeconds = time.Until(r.retryAt).Round(time.Second).Seconds()
	default:
		base.Status = "idle"
	}
	return base
}
