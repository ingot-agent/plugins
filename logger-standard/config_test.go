package loggerstandard

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeConfigSelectsDefaults(t *testing.T) {
	t.Parallel()
	config, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	want := Config{
		Level:          defaultLevel,
		Format:         defaultFormat,
		Output:         defaultOutput,
		Path:           defaultPath,
		MaxRecordBytes: defaultMaxRecordBytes,
		MaxSizeBytes:   defaultMaxSizeBytes,
		MaxBackups:     defaultMaxBackups,
	}
	if config != want {
		t.Fatalf("config = %+v, want %+v", config, want)
	}
}

func TestNormalizeConfigPreservesEffectiveValues(t *testing.T) {
	t.Parallel()
	input := Config{Level: " WARN ", Format: "JSON", Output: "File", Path: " custom/app.log ",
		MaxRecordBytes: 1, MaxSizeBytes: 2, MaxBackups: 3}
	config, err := normalizeConfig(input)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if config.Level != "warn" || config.Format != "json" || config.Output != "file" {
		t.Fatalf("config = %+v", config)
	}
	if config.Path != "custom/app.log" {
		t.Fatalf("path = %q", config.Path)
	}
	if config.MaxRecordBytes != 1 || config.MaxSizeBytes != 2 || config.MaxBackups != 3 {
		t.Fatalf("bounds = %+v", config)
	}
}

func TestNormalizeConfigRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	for _, config := range []Config{
		{Level: "trace"},
		{Format: "yaml"},
		{Output: "stdout"},
		{MaxRecordBytes: -1},
		{MaxSizeBytes: -1},
		{MaxBackups: -1},
	} {
		if _, err := normalizeConfig(config); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("normalizeConfig(%+v) error = %v, want ErrInvalidConfig", config, err)
		}
	}
}

func TestLoadConfigMissingFileIsUnconfigured(t *testing.T) {
	t.Parallel()
	config, err := loadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if config != (Config{}) {
		t.Fatalf("config = %+v, want zero value", config)
	}
}

func TestSaveAndLoadConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := Config{Level: "warn", Format: "json", Output: "file", Path: "logs/app.log",
		MaxRecordBytes: 2048, MaxSizeBytes: 4096, MaxBackups: 1}
	if err := saveConfig(dir, want); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	got, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions = %v, want 0600", info.Mode().Perm())
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("unknown = 1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := loadConfig(dir); err == nil {
		t.Fatal("loadConfig accepted an unknown field")
	}
}

func TestSaveConfigRequiresScope(t *testing.T) {
	t.Parallel()
	if err := saveConfig("", Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("saveConfig error = %v, want ErrInvalidConfig", err)
	}
}
