package modelruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	modelruntime "github.com/ingot-agent/plugins/model-runtime"
	"github.com/ingot-agent/sdk/session"

	"github.com/pelletier/go-toml/v2"
)

type memorySessions struct {
	session.Manager
	mu          sync.Mutex
	totals      map[session.ID]int64
	settlements int
	err         error
}

func (s *memorySessions) Get(ctx context.Context, id session.ID) (session.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return session.Metadata{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	total, ok := s.totals[id]
	if !ok {
		return session.Metadata{}, session.ErrNotFound
	}
	return session.Metadata{ID: id, TotalToken: total}, nil
}

func (s *memorySessions) AddTotalTokens(ctx context.Context, ids []session.ID, delta int64) ([]session.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	for _, id := range ids {
		if _, ok := s.totals[id]; !ok {
			return nil, session.ErrNotFound
		}
	}
	s.settlements++
	result := []session.Metadata{}
	seen := map[session.ID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		s.totals[id] += delta
		result = append(result, session.Metadata{ID: id, TotalToken: s.totals[id]})
	}
	return result, nil
}

// testStateScope is a Plugin-owned Runtime state scope rooted at a test
// directory. Tests persist this Plugin's own configuration there exactly as a
// real runtime hands the scope to the Plugin.
type testStateScope struct{ dir string }

func (s testStateScope) Dir() string { return s.dir }

// withState attaches a Runtime state scope carrying cfg to the supplied
// Dependencies. The Host never decodes or injects plugin configuration; each
// Plugin loads its own configuration from its scope. A Dependencies value that
// already carries a scope (a test exercising scope handling directly) is left
// untouched.
func withState(t *testing.T, cfg modelruntime.Config, deps modelruntime.Dependencies) modelruntime.Dependencies {
	t.Helper()
	if deps.Sessions == nil {
		deps.Sessions = &memorySessions{totals: map[session.ID]int64{"s": 0}}
	}
	if deps.TokenUsage == nil {
		deps.TokenUsage = &memorySessions{totals: map[session.ID]int64{"s": 0}}
	}
	dir := writeTestConfig(t, cfg)
	if deps.State != nil {
		// A test supplied its own scope; persist cfg into it instead of
		// replacing the scope the test is exercising.
		persistTestConfig(t, deps.State.Dir(), cfg)
		return deps
	}
	deps.State = testStateScope{dir: dir}
	return deps
}

// writeTestConfig persists cfg as this Plugin's own configuration file and
// returns the state scope directory holding it. A zero Config means
// Unconfigured, so no file is written and the Plugin must apply defaults.
func writeTestConfig(t *testing.T, cfg modelruntime.Config) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	persistTestConfig(t, dir, cfg)
	return dir
}

// persistTestConfig writes cfg as this Plugin's own configuration file inside
// dir. A zero Config means Unconfigured, so no file is written and the Plugin
// must apply its defaults.
func persistTestConfig(t *testing.T, dir string, cfg modelruntime.Config) {
	t.Helper()
	if reflect.ValueOf(cfg).IsZero() {
		return
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
