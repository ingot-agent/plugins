package toolmcp

import (
	"errors"
	"testing"
)

func TestNormalizeConfigAppliesDefaults(t *testing.T) {
	config, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if config.SnapshotVersion != 1 {
		t.Fatalf("SnapshotVersion = %d, want 1", config.SnapshotVersion)
	}
	if config.ToolNamePrefix != defaultToolNamePrefix {
		t.Fatalf("ToolNamePrefix = %q, want %q", config.ToolNamePrefix, defaultToolNamePrefix)
	}
	if !config.exposeStatusTool() {
		t.Fatal("status tool should be enabled by default")
	}
	if config.MaxResultBytes != defaultMaxResultBytes {
		t.Fatalf("MaxResultBytes = %d, want %d", config.MaxResultBytes, defaultMaxResultBytes)
	}
	if config.MaxToolsTotal != defaultMaxToolsTotal {
		t.Fatalf("MaxToolsTotal = %d, want %d", config.MaxToolsTotal, defaultMaxToolsTotal)
	}
}

func TestNormalizeServerDefaults(t *testing.T) {
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{Name: "demo", Command: "echo"}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	server := config.Servers[0]
	if server.Transport != transportStdio {
		t.Fatalf("Transport = %q", server.Transport)
	}
	if !server.enabled() {
		t.Fatal("server should be enabled by default")
	}
	if len(server.EnabledTools) != 1 || server.EnabledTools[0] != "*" {
		t.Fatalf("EnabledTools = %v, want [*]", server.EnabledTools)
	}
	if server.MaxConcurrentCalls != defaultMaxConcurrentCalls {
		t.Fatalf("MaxConcurrentCalls = %d", server.MaxConcurrentCalls)
	}
	if server.ConnectTimeoutSeconds != defaultConnectTimeoutSecond {
		t.Fatalf("ConnectTimeoutSeconds = %d", server.ConnectTimeoutSeconds)
	}
	if server.CallTimeoutSeconds != defaultCallTimeoutSecond {
		t.Fatalf("CallTimeoutSeconds = %d", server.CallTimeoutSeconds)
	}
	if server.ShutdownTimeoutSeconds != defaultShutdownTimeSecond {
		t.Fatalf("ShutdownTimeoutSeconds = %d", server.ShutdownTimeoutSeconds)
	}
}

func TestNormalizeConfigSortsServers(t *testing.T) {
	config, err := normalizeConfig(Config{Servers: []ServerConfig{
		{Name: "zeta", Command: "true"},
		{Name: "alpha", Command: "true"},
	}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if config.Servers[0].Name != "alpha" || config.Servers[1].Name != "zeta" {
		t.Fatalf("servers are not sorted: %v", []string{config.Servers[0].Name, config.Servers[1].Name})
	}
}

func TestNormalizeConfigRejectsInvalidInput(t *testing.T) {
	cases := map[string]Config{
		"bad prefix":      {ToolNamePrefix: "MCP"},
		"bad server name": {Servers: []ServerConfig{{Name: "Demo", Command: "true"}}},
		"duplicate server": {Servers: []ServerConfig{
			{Name: "demo", Command: "true"},
			{Name: "demo", Command: "true"},
		}},
		"stdio without command": {Servers: []ServerConfig{{Name: "demo"}}},
		"unknown transport":     {Servers: []ServerConfig{{Name: "demo", Transport: "sse", Command: "true"}}},
		"http remote plaintext": {Servers: []ServerConfig{{Name: "demo", Transport: "http", URL: "http://example.com/mcp"}}},
		"http credentials":      {Servers: []ServerConfig{{Name: "demo", Transport: "http", URL: "https://user:pass@example.com/mcp"}}},
		"stdio with url":        {Servers: []ServerConfig{{Name: "demo", Command: "true", URL: "https://example.com/mcp"}}},
		"http with command":     {Servers: []ServerConfig{{Name: "demo", Transport: "http", URL: "https://example.com/mcp", Command: "true"}}},
		"relative cwd":          {Servers: []ServerConfig{{Name: "demo", Command: "true", Cwd: "relative"}}},
		"bad env name":          {Servers: []ServerConfig{{Name: "demo", Command: "true", Env: map[string]string{"bad name": "x"}}}},
		"unterminated reference": {Servers: []ServerConfig{{Name: "demo", Command: "true", Env: map[string]string{
			"TOKEN": "${UNCLOSED",
		}}}},
		"bad filter":      {Servers: []ServerConfig{{Name: "demo", Command: "true", EnabledTools: []string{"a*b"}}}},
		"too many tools":  {MaxToolsTotal: 2048},
		"too few bytes":   {MaxResultBytes: 10},
		"bad concurrency": {Servers: []ServerConfig{{Name: "demo", Command: "true", MaxConcurrentCalls: 1000}}},
		"bad version":     {SnapshotVersion: 99},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeConfig(config); err == nil {
				t.Fatal("expected an error")
			} else if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error does not wrap ErrInvalidConfig: %v", err)
			}
		})
	}
}

func TestNormalizeConfigAcceptsLoopbackHTTP(t *testing.T) {
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "local", Transport: transportHTTP, URL: "http://127.0.0.1:8080/mcp",
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if config.Servers[0].URL != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("URL = %q", config.Servers[0].URL)
	}
}

func TestConfigHashIgnoresSecretValues(t *testing.T) {
	base := Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true",
		Env: map[string]string{"TOKEN": "${A}"},
	}}}
	first, err := normalizeConfig(base)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	second, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true",
		Env: map[string]string{"TOKEN": "${B}"},
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	firstHash, err := first.configHash()
	if err != nil {
		t.Fatalf("configHash: %v", err)
	}
	secondHash, err := second.configHash()
	if err != nil {
		t.Fatalf("configHash: %v", err)
	}
	if firstHash != secondHash {
		t.Fatal("changing a secret reference must not invalidate the tool snapshot")
	}

	third, err := normalizeConfig(Config{Servers: []ServerConfig{{Name: "demo", Command: "different"}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	thirdHash, err := third.configHash()
	if err != nil {
		t.Fatalf("configHash: %v", err)
	}
	if thirdHash == firstHash {
		t.Fatal("changing the command must change the digest")
	}
}

func TestMatchesToolFilter(t *testing.T) {
	server := ServerConfig{
		EnabledTools:  []string{"read_*", "list"},
		DisabledTools: []string{"read_secret"},
	}
	cases := map[string]bool{
		"read_file":   true,
		"read_secret": false,
		"list":        true,
		"write_file":  false,
	}
	for name, want := range cases {
		if got := matchesToolFilter(server, name); got != want {
			t.Fatalf("matchesToolFilter(%q) = %v, want %v", name, got, want)
		}
	}
	if !matchesToolFilter(ServerConfig{EnabledTools: []string{"*"}}, "anything") {
		t.Fatal("wildcard should match")
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, configFileName, "unknown_field = 1\n")
	if _, err := loadConfig(dir); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestSaveLoadConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "npx", Args: []string{"-y", "server"},
		Env: map[string]string{"TOKEN": "${TOKEN}"},
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if err := saveConfig(dir, config); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	loaded, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	normalized, err := normalizeConfig(loaded)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if len(normalized.Servers) != 1 || normalized.Servers[0].Command != "npx" {
		t.Fatalf("round trip lost the server: %#v", normalized.Servers)
	}
	if normalized.Servers[0].Env["TOKEN"] != "${TOKEN}" {
		t.Fatalf("round trip lost the reference: %#v", normalized.Servers[0].Env)
	}
}

func TestConfigHashIgnoresRuntimeOnlySettings(t *testing.T) {
	first, err := normalizeConfig(Config{Servers: []ServerConfig{{Name: "demo", Command: "true"}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	second, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true",
		CallTimeoutSeconds: 300, MaxConcurrentCalls: 8,
		Env: map[string]string{"TOKEN": "changed"},
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	firstHash, err := first.configHash()
	if err != nil {
		t.Fatalf("configHash: %v", err)
	}
	secondHash, err := second.configHash()
	if err != nil {
		t.Fatalf("configHash: %v", err)
	}
	if firstHash != secondHash {
		t.Fatal("runtime-only settings must not invalidate the tool snapshot")
	}

	third, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true", DisabledTools: []string{"secret"},
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	thirdHash, err := third.configHash()
	if err != nil {
		t.Fatalf("configHash: %v", err)
	}
	if thirdHash == firstHash {
		t.Fatal("tool filters must be part of the digest")
	}
}

func TestConfigHashTracksStatusToolVisibility(t *testing.T) {
	enabled, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	disabled, err := normalizeConfig(Config{ExposeStatusTool: boolPointer(false)})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	enabledHash, _ := enabled.configHash()
	disabledHash, _ := disabled.configHash()
	if enabledHash == disabledHash {
		t.Fatal("status tool visibility changes the exported tool set")
	}
}
