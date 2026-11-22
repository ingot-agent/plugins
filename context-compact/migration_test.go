package contextcompact

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ingot-agent/sdk/logging"
)

// TestLegacyConfigMigratesAndPreservesIntendedValues uses the exact file shape
// the previous version wrote. Failing construction on it would leave the runtime
// unable to start after an upgrade, and would hide the settings that still apply.
func TestLegacyConfigMigratesAndPreservesIntendedValues(t *testing.T) {
	dir := t.TempDir()
	legacy := `provider = 'deepseek'
model = ''
trigger_input_tokens = 800000
target_input_tokens = 250000
recent_rounds = 4
summary_chunk_tokens = 65546
summary_input_tokens = 256000
summary_max_tokens = 8192
rollup_max_tokens = 32768
summary_max_bytes = 65536
memory_trigger_tokens = 24576
memory_target_tokens = 12288
state_trigger_tokens = 8192
state_target_tokens = 3072
max_summary_passes = 32
allowed_accuracies = ['exact', 'upper_bound', 'estimate']
token_count_cache_entries = 1024
`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loader, err := newConfigLoader(dir, logging.Discard())
	if err != nil {
		t.Fatalf("legacy config must load: %v", err)
	}
	active := loader.snapshot()
	if active.provider != "deepseek" {
		t.Fatalf("provider = %q", active.provider)
	}
	if active.triggerTokens != 800000 || active.targetTokens != 250000 {
		t.Fatalf("watermarks not preserved: %#v", active)
	}
	// summary_max_tokens keeps its meaning across versions.
	if active.summaryMaxTokens != 8192 {
		t.Fatalf("summary_max_tokens = %d", active.summaryMaxTokens)
	}
	// The file is rewritten in the current shape.
	rewritten, migrated, err := loadConfig(dir)
	if err != nil || migrated {
		t.Fatalf("rewritten file must decode directly: err=%v migrated=%v", err, migrated)
	}
	if rewritten.TriggerTokens != 800000 || rewritten.TargetTokens != 250000 {
		t.Fatalf("rewritten = %#v", rewritten)
	}
	if rewritten.ProtectRecentTokens != defaultProtectRecentTokens {
		t.Fatalf("rewritten file should carry the effective protection budget: %#v", rewritten)
	}
}

func TestLegacyConfigRejectsGenuinelyUnknownField(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("trigger_tokens = 1000\ntarget_token = 500\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newConfigLoader(dir, logging.Discard()); err == nil {
		t.Fatal("a typo must be reported")
	}
}

func TestMigrateLegacyConfigReportsDroppedFields(t *testing.T) {
	_, dropped, err := migrateLegacyConfig([]byte("trigger_input_tokens = 100\nmax_summary_passes = 8\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "max_summary_passes" {
		t.Fatalf("dropped = %v", dropped)
	}
}
