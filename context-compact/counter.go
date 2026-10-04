package contextcompact

import (
	"container/list"
	"sync"
	"sync/atomic"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/usage"
)

const defaultCacheEntries = 1024

type cacheEntry struct {
	key    string
	result usage.CountResult
}

type flight struct {
	done   chan struct{}
	result usage.CountResult
	err    error
}

type inputCounter struct {
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

func newCounter(resolver model.RequestResolver, selected profile, capacity int) *inputCounter {
	instance := &inputCounter{
		resolver: resolver,
		profile:  selected,
		cache:    make(map[string]*list.Element, capacity),
		recent:   list.New(),
		inflight: make(map[string]*flight),
	}
	instance.config.Store(&counterConfig{capacity: capacity})
	return instance
}

func (c *inputCounter) applyConfig(capacity int) {
	c.config.Store(&counterConfig{capacity: capacity})
	c.mu.Lock()
	clear(c.cache)
	c.recent.Init()
	c.mu.Unlock()
}

func (c *inputCounter) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	clear(c.cache)
	c.recent.Init()
}
