package toolweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeConfigDefaults(t *testing.T) {
	normalized, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.baseURL != defaultBaseURL {
		t.Fatalf("base URL = %q", normalized.baseURL)
	}
	if normalized.searchURL != "https://api.deepseek.com/v1/tools/search" || normalized.fetchURL != "https://api.deepseek.com/v1/tools/fetch" {
		t.Fatalf("endpoints = %q, %q", normalized.searchURL, normalized.fetchURL)
	}
	if normalized.requestTimeout != 60*time.Second || normalized.maxResponseBytes != defaultMaxResponseBytes {
		t.Fatalf("timeout=%v max=%d", normalized.requestTimeout, normalized.maxResponseBytes)
	}
}

func TestNormalizeConfigBaseURLVariants(t *testing.T) {
	valid := map[string]string{
		"https://api.deepseek.com":          "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/":         "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/v1":       "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/v1/":      "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/v1/tools": "https://api.deepseek.com/v1/tools/search",
		"https://proxy.example/deepseek":    "https://proxy.example/deepseek/v1/tools/search",
		"https://proxy.example/deepseek/v1": "https://proxy.example/deepseek/v1/tools/search",
	}
	for input, expected := range valid {
		t.Run(input, func(t *testing.T) {
			normalized, err := normalizeConfig(Config{BaseURL: input})
			if err != nil {
				t.Fatal(err)
			}
			if normalized.searchURL != expected {
				t.Fatalf("search URL = %q, want %q", normalized.searchURL, expected)
			}
			if normalized.baseURL != strings.TrimRight(input, "/") {
				t.Fatalf("stored base URL = %q", normalized.baseURL)
			}
		})
	}

	invalid := []string{
		"ftp://api.deepseek.com",
		"api.deepseek.com",
		"https://",
		"https://user:pass@api.deepseek.com",
		"https://api.deepseek.com?x=1",
		"https://api.deepseek.com#fragment",
		"https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/v1/tools/fetch",
		"https://api.deepseek.com/a b",
	}
	for _, input := range invalid {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeConfig(Config{BaseURL: input}); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
			}
		})
	}
}

func TestNormalizeConfigLimits(t *testing.T) {
	invalid := []Config{
		{RequestTimeoutSeconds: -1},
		{RequestTimeoutSeconds: minRequestTimeoutSeconds - 1},
		{RequestTimeoutSeconds: maxRequestTimeoutSeconds + 1},
		{MaxResponseBytes: -1},
		{MaxResponseBytes: minMaxResponseBytes - 1},
		{MaxResponseBytes: maxMaxResponseBytes + 1},
		{APIKey: "bad\nkey"},
	}
	for _, config := range invalid {
		if _, err := normalizeConfig(config); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config %#v error = %v", config, err)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	config := Config{APIKey: "key", BaseURL: "https://proxy.example", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20}
	if err := saveConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, configFileName))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", info.Mode().Perm())
		}
	}
	loaded, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != config {
		t.Fatalf("loaded = %#v, want %#v", loaded, config)
	}
}

func TestLoadConfigMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	loaded, err := loadConfig(dir)
	if err != nil || loaded != (Config{}) {
		t.Fatalf("missing config = %#v, %v", loaded, err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("unknown = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(dir); err == nil || !strings.Contains(err.Error(), "decode plugin config") {
		t.Fatalf("malformed config error = %v", err)
	}
}

func TestResolveEffective(t *testing.T) {
	t.Setenv(apiKeyEnv, "environment-key")
	normalized, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := resolveEffective(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if effective.apiKey != "environment-key" || effective.apiKeySource != "environment" || !effective.configured() {
		t.Fatalf("effective = %#v", effective)
	}

	normalized, err = normalizeConfig(Config{APIKey: "config-key"})
	if err != nil {
		t.Fatal(err)
	}
	effective, err = resolveEffective(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if effective.apiKey != "config-key" || effective.apiKeySource != "config" {
		t.Fatalf("effective = %#v", effective)
	}

	t.Setenv(apiKeyEnv, "bad\nkey")
	normalized, err = normalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveEffective(normalized); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid environment error = %v", err)
	}

	t.Setenv(apiKeyEnv, "")
	effective, err = resolveEffective(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if effective.configured() || effective.apiKeySource != "none" {
		t.Fatalf("effective = %#v", effective)
	}
}

func TestNewRejectsInvalidDependencies(t *testing.T) {
	client := &fakeHTTP{}
	scope := testScope(t.TempDir())
	tests := []struct {
		name string
		ctx  context.Context
		deps Dependencies
	}{
		{name: "nil context", ctx: nil, deps: Dependencies{HTTP: client, State: scope}},
		{name: "nil http", ctx: context.Background(), deps: Dependencies{State: scope}},
		{name: "nil state", ctx: context.Background(), deps: Dependencies{HTTP: client}},
		{name: "empty state", ctx: context.Background(), deps: Dependencies{HTTP: client, State: testScope("")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := New(test.ctx, test.deps)
			if !errors.Is(err, ErrInvalidDependencies) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNewReadsPersistedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, Config{APIKey: "stored-key", BaseURL: "https://proxy.example/v1"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(apiKeyEnv, "environment-key")
	client := &fakeHTTP{responses: []fakeHTTPResponse{{status: 200, body: `{"results":[]}`}}}
	exports, _, err := New(context.Background(), Dependencies{HTTP: client, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	request := client.request(0)
	if request.url != "https://proxy.example/v1/tools/search" || request.authorization != "Bearer stored-key" {
		t.Fatalf("request = %#v", request)
	}
}

func TestNewRejectsMalformedPersistedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("base_url = \"ftp://example.com\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(apiKeyEnv, "")
	_, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(dir)})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewRejectsInvalidEnvironmentKey(t *testing.T) {
	t.Setenv(apiKeyEnv, "bad\nkey")
	_, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(t.TempDir())})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginDefinitions(t *testing.T) {
	exports := newTestExports(t, &fakeHTTP{}, Config{APIKey: "secret-key"}, "")
	definitions := map[string]string{}
	for _, candidate := range exports.Tools {
		definition := candidate.Definition()
		if definition.Description == "" || len(definition.InputSchema) == 0 {
			t.Fatalf("empty definition: %#v", definition)
		}
		definitions[definition.Name] = definition.Description
	}
	if len(definitions) != 2 || definitions[searchToolName] == "" || definitions[fetchToolName] == "" {
		t.Fatalf("definitions = %#v", definitions)
	}
	setup := setupOperationFor(t, exports)
	definition := setup.Definition()
	if definition.Name != setupOperationName || definition.Group != setupOperationGroup || len(definition.OutputSchema) == 0 {
		t.Fatalf("setup definition = %#v", definition)
	}
}
