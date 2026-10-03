package contextcompact

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/usage"
	"github.com/pelletier/go-toml/v2"
)

// Only budget tests substitute counts; production always owns its estimator.
type testDependencies struct {
	Model           model.Runtime
	Resolver        ingotabi.Optional[model.RequestResolver]
	ProviderSources []model.ProviderSource
	Interactions    ingotabi.Optional[interaction.ExecutionBinder]
	Store           session.Store
	State           state.Scope
	Counter         ingotabi.Optional[usage.Counter]
}

func newTestCompactor(ctx context.Context, deps testDependencies) (Exports, ingotabi.Cleanup, error) {
	exports, cleanup, err := New(ctx, Dependencies{Model: deps.Model, Resolver: deps.Resolver, ProviderSources: deps.ProviderSources, Interactions: deps.Interactions, Store: deps.Store, State: deps.State})
	if err == nil && deps.Counter.Valid {
		exports.Compactor.(*compactor).counter = deps.Counter.Value
	}
	return exports, cleanup, err
}

// The serialization length is a deterministic test count, not a tokenizer.
type canonicalTokenCounter struct{}

func (*canonicalTokenCounter) CountInput(ctx context.Context, input usage.CountRequest) (usage.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return usage.CountResult{}, err
	}
	raw, err := canonicalRequestBytes(input.Invocation)
	if err != nil {
		return usage.CountResult{}, err
	}
	provider, modelName := input.Invocation.Provider, input.Invocation.Model
	if provider == "" {
		provider = "main-provider"
	}
	if modelName == "" {
		modelName = "main-model"
	}
	return usage.CountResult{
		InputTokens: int64(len(raw)), Accuracy: usage.AccuracyExact,
		Source: "test-canonical-tokens-v1", Provider: provider, Model: modelName,
	}, nil
}

// testStateScope is a Plugin-owned Runtime state scope rooted at a test
// directory. Tests persist this Plugin's own configuration there exactly as a
// real runtime hands the scope to the Plugin.
type testStateScope struct{ dir string }

func (s testStateScope) Dir() string { return s.dir }

// withState attaches a Runtime state scope carrying cfg to the supplied
// testDependencies. The Host never decodes or injects plugin configuration; each
// Plugin loads its own configuration from its scope. A testDependencies value that
// already carries a scope (a test exercising scope handling directly) is left
// untouched.
func withState(t *testing.T, cfg Config, deps testDependencies) testDependencies {
	t.Helper()
	if !deps.Counter.Valid {
		deps.Counter = ingotabi.Some[usage.Counter](&canonicalTokenCounter{})
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
func writeTestConfig(t *testing.T, cfg Config) string {
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
func persistTestConfig(t *testing.T, dir string, cfg Config) {
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
	if err := os.WriteFile(filepath.Join(dir, configFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
