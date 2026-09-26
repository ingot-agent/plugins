// Package usagedefault estimates model input tokens with a Unicode heuristic
// and bounded in-memory caching.
package usagedefault

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/usage"
)

const defaultCacheEntries = 1024

var (
	// ErrInvalidConfig indicates invalid cache limits or dependencies.
	ErrInvalidConfig = errors.New("invalid usage.default config")
	// ErrInvalidRequest indicates malformed model invocation data.
	ErrInvalidRequest = errors.New("invalid usage count request")
	// ErrUnsupportedModel aliases the SDK classification for unsupported content.
	ErrUnsupportedModel = usage.ErrUnsupportedModel
	// ErrCountFailed indicates that a selected profile could not produce a
	// valid count.
	ErrCountFailed = errors.New("usage count failed")
	// ErrClosed indicates that the component instance has been cleaned up.
	ErrClosed = errors.New("usage counter is closed")
)

// Config controls the bounded result cache. Routes is read only for
// compatibility with existing plugin state and is ignored by the counter.
type Config struct {
	Routes       []Route `toml:"routes"`
	CacheEntries int     `toml:"cache_entries"`
}

// Route is the legacy on-disk route format.
type Route struct {
	Provider     string `toml:"provider"`
	ModelPattern string `toml:"model_pattern"`
	Profile      string `toml:"profile"`
}

// Dependencies contains the request resolver used to materialize model
// runtime defaults.
type Dependencies struct {
	Resolver        model.RequestResolver
	ProviderSources []model.ProviderSource
	State           state.Scope
}

// Exports contains the model input counter.
type Exports struct {
	Counter    usage.Counter
	Operations []operation.Operation
}

type cacheEntry struct {
	key    string
	result usage.CountResult
}

type flight struct {
	done   chan struct{}
	result usage.CountResult
	err    error
}

type counter struct {
	resolver model.RequestResolver
	profile  profile
	config   atomic.Pointer[counterConfig]

	mu       sync.Mutex
	closed   bool
	cache    map[string]*list.Element
	recent   *list.List
	inflight map[string]*flight
}

type counterConfig struct {
	capacity int
}

// New loads this Plugin's own configuration and constructs a Unicode-estimate
// counter for every resolved provider and model.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct usage.default: nil context: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Resolver) || isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("resolver and state dependencies are required: %w", ErrInvalidConfig)
	}
	for i, source := range deps.ProviderSources {
		if isNil(source) {
			return Exports{}, nil, fmt.Errorf("provider_sources[%d] is nil: %w", i, ErrInvalidConfig)
		}
	}
	cfg, err := loadConfig(deps.State.Dir())
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct usage.default: %w: %w", err, ErrInvalidConfig)
	}
	capacity := cfg.CacheEntries
	if capacity < 0 {
		return Exports{}, nil, fmt.Errorf("cache_entries must not be negative: %w", ErrInvalidConfig)
	}
	if capacity == 0 {
		capacity = defaultCacheEntries
	}
	instance := newCounter(deps.Resolver, unicodeEstimateProfile{}, capacity)
	cleanup := ingotabi.Cleanup(func(cleanupCtx context.Context) error {
		if cleanupCtx == nil {
			return errors.New("cleanup usage.default: nil context")
		}
		if err := cleanupCtx.Err(); err != nil {
			return err
		}
		instance.close()
		return nil
	})
	return Exports{Counter: instance, Operations: []operation.Operation{&setupOperation{scope: deps.State, counter: instance}}}, cleanup, nil
}

func newCounter(resolver model.RequestResolver, selected profile, capacity int) *counter {
	instance := &counter{
		resolver: resolver,
		profile:  selected,
		cache:    make(map[string]*list.Element, capacity),
		recent:   list.New(),
		inflight: make(map[string]*flight),
	}
	instance.config.Store(&counterConfig{capacity: capacity})
	return instance
}

func (c *counter) applyConfig(capacity int) {
	c.config.Store(&counterConfig{capacity: capacity})
	c.mu.Lock()
	clear(c.cache)
	c.recent.Init()
	c.mu.Unlock()
}

func (c *counter) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	clear(c.cache)
	c.recent.Init()
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ usage.Counter = (*counter)(nil)
