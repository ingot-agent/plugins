package toolmcp

import (
	"context"
	"fmt"
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

	mu         sync.Mutex
	servers    map[string]*serverRuntime
	discoverMu sync.Mutex
}

func newPlatform(stateDir string, httpClient ingotabi.Optional[httpx.Client], logger logging.Logger, observer observation.Consumer, config Config) *platform {
	instance := &platform{
		stateDir:    stateDir,
		http:        httpClient,
		logger:      logger,
		observation: observer,
		servers:     make(map[string]*serverRuntime),
	}
	instance.setConfig(config)
	return instance
}

func (p *platform) setConfig(config Config) {
	normalized := config
	p.config.Store(&normalized)
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

// acquire returns a live session for the named server, connecting when
// necessary.
func (p *platform) acquire(ctx context.Context, name string) (*connection, error) {
	config := p.currentConfig()
	server, ok := config.server(name)
	if !ok {
		return nil, fmt.Errorf("MCP server %q is not configured", name)
	}
	if !server.enabled() {
		return nil, fmt.Errorf("MCP server %q is disabled", name)
	}
	return p.runtimeFor(name).acquire(ctx, p, server, config.MaxResultBytes)
}

// markStale forces every live session to reconnect on its next use. In-flight
// calls keep their session; later calls observe the new configuration.
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

// closeAll terminates every session. It is the instance Cleanup.
func (p *platform) closeAll() {
	p.mu.Lock()
	runtimes := make([]*serverRuntime, 0, len(p.servers))
	for _, runtime := range p.servers {
		runtimes = append(runtimes, runtime)
	}
	p.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.close()
	}
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
	session *connection
	stale   bool
	slots   chan struct{}

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
	if limit < 1 {
		limit = 1
	}
	r.mu.Lock()
	if r.slots == nil || cap(r.slots) != limit {
		r.slots = make(chan struct{}, limit)
	}
	slots := r.slots
	r.mu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *serverRuntime) markStale() {
	r.mu.Lock()
	r.stale = true
	r.mu.Unlock()
}

func (r *serverRuntime) close() {
	r.mu.Lock()
	session := r.session
	r.session = nil
	r.stale = false
	r.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}

// acquire returns the live session, connecting at most once concurrently.
func (r *serverRuntime) acquire(ctx context.Context, p *platform, server ServerConfig, maxEventBytes int) (*connection, error) {
	r.mu.Lock()
	if r.session != nil {
		if !r.stale && !r.idleExpired(server) {
			session := r.session
			r.lastUsed = time.Now()
			r.calls++
			r.mu.Unlock()
			return session, nil
		}
		stale := r.session
		r.session = nil
		r.stale = false
		r.mu.Unlock()
		_ = stale.Close()
	} else {
		if !r.retryAt.IsZero() && time.Now().Before(r.retryAt) {
			err := r.backoffErrorLocked()
			r.mu.Unlock()
			return nil, err
		}
		r.mu.Unlock()
	}

	r.connectMu.Lock()
	defer r.connectMu.Unlock()

	r.mu.Lock()
	if r.session != nil && !r.stale {
		session := r.session
		r.lastUsed = time.Now()
		r.calls++
		r.mu.Unlock()
		return session, nil
	}
	r.mu.Unlock()

	timeout := time.Duration(server.ConnectTimeoutSeconds) * time.Second
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	session, err := p.dial(connectCtx, server, maxEventBytes)
	if err != nil {
		r.recordFailure(err)
		p.logger.Warn(connectCtx, "MCP server connection failed",
			logging.String("server", server.Name),
			logging.String("transport", server.Transport),
			logging.String("error", sanitizeMessage(err.Error())))
		return nil, err
	}

	r.mu.Lock()
	r.session = session
	r.stale = false
	r.failures = 0
	r.retryAt = time.Time{}
	r.lastError = ""
	r.lastUsed = time.Now()
	r.calls++
	r.mu.Unlock()

	p.logger.Info(ctx, "MCP server connected",
		logging.String("server", server.Name),
		logging.String("transport", server.Transport))
	return session, nil
}

func (r *serverRuntime) idleExpired(server ServerConfig) bool {
	if server.IdleTimeoutSeconds <= 0 || r.session == nil {
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
