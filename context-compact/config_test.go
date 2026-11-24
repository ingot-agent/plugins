package contextcompact

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
)

func TestNormalizeConfigDefaultsAndValidation(t *testing.T) {
	cfg, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.triggerTokens != defaultTriggerTokens || cfg.targetTokens != defaultTargetTokens ||
		cfg.protectRecentTokens != defaultProtectRecentTokens || cfg.summaryMaxTokens != defaultSummaryMaxTokens {
		t.Fatalf("defaults = %#v", cfg)
	}
	if cfg.reasoningEffort != model.ReasoningEffortNone {
		t.Fatalf("reasoning effort = %q, want none", cfg.reasoningEffort)
	}

	for name, input := range map[string]Config{
		"target above trigger":         {TriggerTokens: 1000, TargetTokens: 1000},
		"target above default trigger": {TargetTokens: 900_000},
		"negative trigger":             {TriggerTokens: -1},
		"negative summary max":         {SummaryMaxTokens: -1},
	} {
		if _, err := normalizeConfig(input); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s: error = %v, want ErrInvalidConfig", name, err)
		}
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("trigger_tokens = 1\nunknown_field = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfig(dir); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

// TestConfigLoaderReloadsOnFileChange is the regression for the v1 failure mode
// where editing config.toml had no effect until the runtime restarted.
func TestConfigLoaderReloadsOnFileChange(t *testing.T) {
	dir := writeTestConfig(t, Config{TriggerTokens: 1000, TargetTokens: 500})
	loader, err := newConfigLoader(dir, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if got := loader.load(); got.targetTokens != 500 {
		t.Fatalf("initial target = %d", got.targetTokens)
	}

	if err := saveConfig(dir, Config{TriggerTokens: 2000, TargetTokens: 900, ProtectRecentTokens: 300}); err != nil {
		t.Fatal(err)
	}
	reloaded := loader.load()
	if reloaded.targetTokens != 900 || reloaded.triggerTokens != 2000 || reloaded.protectRecentTokens != 300 {
		t.Fatalf("reload = %#v", reloaded)
	}
}

// TestConfigLoaderKeepsPreviousOnBrokenEdit: a typo must not disable compaction.
func TestConfigLoaderKeepsPreviousOnBrokenEdit(t *testing.T) {
	dir := writeTestConfig(t, Config{TriggerTokens: 1000, TargetTokens: 500})
	loader, err := newConfigLoader(dir, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("target_tokens = \"not a number\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loader.load(); got.targetTokens != 500 {
		t.Fatalf("broken edit changed the active config: %#v", got)
	}
}

func TestConfigLoaderRejectsInvalidFileAtConstruction(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("target_tokens = 1000\ntrigger_tokens = 1000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newConfigLoader(dir, logging.Discard()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}
