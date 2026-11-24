package toolmcp

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

// TestConfigOperationRejectsConcurrentWrite covers the lost-update guard: a
// configuration written while the interaction is open must not be overwritten
// by the answer collected against the older state.
func TestConfigOperationRejectsConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	defer cleanup()

	channel := newScriptedChannel(map[string]interaction.Value{
		"tool_name_prefix": interaction.StringValue("racy"),
	})
	channel.duringRequest = func() {
		// Another writer changes the stored configuration mid-interaction.
		concurrent, err := normalizeConfig(Config{
			ToolNamePrefix: "other",
			Servers:        helperConfig(t).Servers,
		})
		if err != nil {
			t.Errorf("normalizeConfig: %v", err)
			return
		}
		if err := saveConfig(dir, concurrent); err != nil {
			t.Errorf("saveConfig: %v", err)
		}
	}

	configOperation := findOperation(t, exports.Operations, setupOperationName)
	_, err := configOperation.Invoke(context.Background(), operation.Request{Interaction: channel})
	if !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("error = %v, want ErrConfigConflict", err)
	}

	stored, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if stored.ToolNamePrefix != "other" {
		t.Fatalf("the concurrent write was lost: prefix = %q", stored.ToolNamePrefix)
	}
}

// TestConfigOperationRequiresInteraction documents that configuration is never
// applied without an explicit host interaction.
func TestConfigOperationRequiresInteraction(t *testing.T) {
	dir := t.TempDir()
	exports, cleanup := gateway(t, dir)
	defer cleanup()

	configOperation := findOperation(t, exports.Operations, setupOperationName)
	if _, err := configOperation.Invoke(context.Background(), operation.Request{}); err == nil {
		t.Fatal("expected an error without an interaction channel")
	}
}

// TestConfigOperationRejectsInvalidCandidate verifies the candidate
// configuration is validated before anything is persisted.
func TestConfigOperationRejectsInvalidCandidate(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	defer cleanup()

	channel := newScriptedChannel(map[string]interaction.Value{
		"tool_name_prefix": interaction.StringValue("INVALID PREFIX"),
	})
	configOperation := findOperation(t, exports.Operations, setupOperationName)
	if _, err := configOperation.Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}

	stored, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if stored.ToolNamePrefix != defaultToolNamePrefix {
		t.Fatalf("an invalid candidate was persisted: %q", stored.ToolNamePrefix)
	}
}
