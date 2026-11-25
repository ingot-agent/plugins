package toolmcp

import (
	"context"
	"encoding/json"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/workspace"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

// discoverWithFilter runs one discovery pass for a helper server with the given
// filter and limit settings and returns the resulting snapshot.
func discoverWithFilter(t *testing.T, mutate func(*Config)) (*Snapshot, refreshOutput) {
	t.Helper()
	config := helperConfig(t)
	mutate(&config)
	config, err := normalizeConfig(config)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	dir := t.TempDir()
	if err := saveConfig(dir, config); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	defer cleanup()

	refresh := findOperation(t, exports.Operations, refreshOperationName)
	result, err := refresh.Invoke(context.Background(), operation.Request{Interaction: interaction.Unavailable()})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var output refreshOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	snapshot, err := loadSnapshot(dir)
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	return snapshot, output
}

func TestDiscoveryAppliesEnabledTools(t *testing.T) {
	snapshot, _ := discoverWithFilter(t, func(config *Config) {
		config.Servers[0].EnabledTools = []string{"echo"}
	})
	names := snapshot.toolNames()
	if len(names) != 1 || names[0] != "mcp_helper_echo" {
		t.Fatalf("tools = %v", names)
	}
}

func TestDiscoveryAppliesDisabledTools(t *testing.T) {
	snapshot, _ := discoverWithFilter(t, func(config *Config) {
		config.Servers[0].DisabledTools = []string{"fail"}
	})
	names := snapshot.toolNames()
	if containsString(names, "mcp_helper_fail") {
		t.Fatalf("disabled tool exported: %v", names)
	}
	if !containsString(names, "mcp_helper_echo") || !containsString(names, "mcp_helper_image") {
		t.Fatalf("tools = %v", names)
	}
}

func TestDiscoveryAppliesPrefixFilters(t *testing.T) {
	snapshot, _ := discoverWithFilter(t, func(config *Config) {
		config.Servers[0].EnabledTools = []string{"ec*"}
	})
	names := snapshot.toolNames()
	if len(names) != 1 || names[0] != "mcp_helper_echo" {
		t.Fatalf("tools = %v", names)
	}
}

func TestDiscoverySkipsServerOverToolLimit(t *testing.T) {
	snapshot, output := discoverWithFilter(t, func(config *Config) {
		config.MaxToolsTotal = 2
	})
	if len(snapshot.toolNames()) != 0 {
		t.Fatalf("tools = %v, want none", snapshot.toolNames())
	}
	if len(output.Servers) != 1 || output.Servers[0].Status != serverStatusSkipped {
		t.Fatalf("servers = %#v", output.Servers)
	}
	if output.Servers[0].Error == "" {
		t.Fatal("a skipped server must explain why")
	}
}

func TestDiscoveryHonoursDisabledServer(t *testing.T) {
	config := helperConfig(t)
	disabled := false
	config.Servers[0].Enabled = &disabled
	config, err := normalizeConfig(config)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(t.TempDir(), ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), nil, config)
	snapshot := instance.discover(context.Background(), config, nil)
	if len(snapshot.Servers) != 0 {
		t.Fatalf("a disabled server was discovered: %#v", snapshot.Servers)
	}
}

func TestHeaderValidation(t *testing.T) {
	cases := map[string]bool{
		"Authorization": true,
		"X-Api-Key":     true,
		"x_custom":      true,
		"":              false,
		"Bad Header":    false,
		"Bad:Header":    false,
		"Ünicode":       false,
	}
	for name, want := range cases {
		if got := validHeaderName(name); got != want {
			t.Fatalf("validHeaderName(%q) = %v, want %v", name, got, want)
		}
	}
	if validHeaderValue("line\nbreak") {
		t.Fatal("a newline in a header value must be rejected")
	}
	if validHeaderValue("carriage\rreturn") {
		t.Fatal("a carriage return in a header value must be rejected")
	}
	if !validHeaderValue("Bearer ${TOKEN}") {
		t.Fatal("a reference value should be valid")
	}
}

func TestNormalizeHTTPRejectsInvalidHeaders(t *testing.T) {
	cases := []map[string]string{
		{"Bad Header": "value"},
		{"Authorization": "line\nbreak"},
		{"Authorization": "bad\x00value"},
	}
	for _, headers := range cases {
		if _, err := normalizeConfig(Config{Servers: []ServerConfig{{
			Name: "remote", Transport: transportHTTP, URL: "https://example.test/mcp", Headers: headers,
		}}}); err == nil {
			t.Fatalf("expected an error for headers %#v", headers)
		}
	}
}
