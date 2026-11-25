package appbackend

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestConfigNormalizeDefaults(t *testing.T) {
	cfg, err := (Config{}).Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Address != "127.0.0.1:7316" || cfg.ReplayCapacity != 1024 || cfg.SubscriberBuffer != 64 || cfg.Heartbeat != 15*time.Second {
		t.Fatalf("defaults = %#v", cfg)
	}
}

func TestConfigRejectsNegativeHeartbeat(t *testing.T) {
	_, err := (Config{Backend: BackendConfig{HeartbeatIntervalSeconds: -1}}).Normalize()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Normalize error = %v, want ErrInvalidConfig", err)
	}
}

func TestConfigRejectsHeartbeatOverflow(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("all positive int values fit in a duration on this architecture")
	}
	seconds := int64(1<<63-1)/int64(time.Second) + 1
	_, err := (Config{Backend: BackendConfig{HeartbeatIntervalSeconds: int(seconds)}}).Normalize()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("overflowing heartbeat error = %v", err)
	}
}

func TestConfigRejectsInvalidOperationAndAssetLimits(t *testing.T) {
	for _, cfg := range []BackendConfig{{OperationRetention: -1}, {MaxAssetBytes: -1}} {
		if _, err := (Config{Backend: cfg}).Normalize(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid limits = %v", err)
		}
	}
}

func TestConfigDefaultWorkspaceMustBeAbsolute(t *testing.T) {
	if _, err := (Config{Backend: BackendConfig{DefaultWorkspace: "relative/workspace"}}).Normalize(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("relative default workspace error = %v, want ErrInvalidConfig", err)
	}
	cfg, err := (Config{Backend: BackendConfig{DefaultWorkspace: "/srv/agent-work"}}).Normalize()
	if err != nil || cfg.DefaultWorkspace != "/srv/agent-work" {
		t.Fatalf("absolute default workspace = %#v, %v", cfg, err)
	}
}

func TestConfigRoundTripsDefaultWorkspace(t *testing.T) {
	dir := t.TempDir()
	want := Config{Backend: BackendConfig{DefaultWorkspace: "/srv/agent-work"}}
	if err := SaveConfig(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend.DefaultWorkspace != want.Backend.DefaultWorkspace {
		t.Fatalf("default workspace = %q, want %q", got.Backend.DefaultWorkspace, want.Backend.DefaultWorkspace)
	}
}
