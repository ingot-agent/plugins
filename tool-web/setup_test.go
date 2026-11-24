package toolweb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

func TestSetupReplacesKeyAndPublishesLiveConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(apiKeyEnv, "")
	client := &fakeHTTP{responses: []fakeHTTPResponse{{status: 200, body: `{"results":[]}`}}}
	exports, _, err := New(context.Background(), Dependencies{HTTP: client, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	channel := &fakeInteraction{answers: map[string]interaction.Response{
		setupOperationName: {Values: []interaction.Answer{
			{Name: "api_key_action", Value: interaction.StringValue(apiKeyReplace)},
			{Name: "base_url", Value: interaction.StringValue("https://proxy.example/api")},
			{Name: "request_timeout_seconds", Value: interaction.IntegerValue(30)},
			{Name: "max_response_bytes", Value: interaction.IntegerValue(1 << 20)},
		}},
		setupOperationName + ".api-key": {Values: []interaction.Answer{
			{Name: "api_key", Value: interaction.StringValue("stored-key")},
		}},
	}}
	output, err := setupOperationFor(t, exports).Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Configured      bool   `json:"configured"`
		APIKeySource    string `json:"api_key_source"`
		RestartRequired bool   `json:"restart_required"`
	}
	if err := json.Unmarshal(output.Output, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Configured || result.APIKeySource != "config" || result.RestartRequired {
		t.Fatalf("output = %#v", result)
	}
	if channel.requestCount() != 2 {
		t.Fatalf("interaction requests = %d", channel.requestCount())
	}
	if field := channel.requests[0].Fields[0]; field.Default == nil || field.Default.String != apiKeyReplace {
		t.Fatalf("unconfigured default action = %#v", field.Default)
	}
	if !channel.requests[1].Fields[0].Sensitive {
		t.Fatalf("API key field is not sensitive: %#v", channel.requests[1].Fields[0])
	}

	stored, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "stored-key" || stored.BaseURL != "https://proxy.example/api" || stored.RequestTimeoutSeconds != 30 || stored.MaxResponseBytes != 1<<20 {
		t.Fatalf("stored = %#v", stored)
	}
	if _, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	request := client.request(0)
	if request.url != "https://proxy.example/api/v1/tools/search" || request.authorization != "Bearer stored-key" {
		t.Fatalf("live request = %#v", request)
	}
}

func TestSetupKeepPreservesKeyAndAvoidsKeyPrompt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(apiKeyEnv, "")
	if err := saveConfig(dir, Config{APIKey: "existing", BaseURL: "https://api.deepseek.com"}); err != nil {
		t.Fatal(err)
	}
	exports, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	channel := &fakeInteraction{answers: map[string]interaction.Response{
		setupOperationName: {Values: []interaction.Answer{
			{Name: "api_key_action", Value: interaction.StringValue(apiKeyKeep)},
			{Name: "request_timeout_seconds", Value: interaction.IntegerValue(45)},
		}},
	}}
	if _, err := setupOperationFor(t, exports).Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	if channel.requestCount() != 1 {
		t.Fatalf("interaction requests = %d", channel.requestCount())
	}
	stored, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "existing" || stored.RequestTimeoutSeconds != 45 {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestSetupClearFallsBackToEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(apiKeyEnv, "environment-key")
	if err := saveConfig(dir, Config{APIKey: "stored-key"}); err != nil {
		t.Fatal(err)
	}
	exports, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	channel := &fakeInteraction{answers: map[string]interaction.Response{
		setupOperationName: {Values: []interaction.Answer{
			{Name: "api_key_action", Value: interaction.StringValue(apiKeyClear)},
		}},
	}}
	output, err := setupOperationFor(t, exports).Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Configured   bool   `json:"configured"`
		APIKeySource string `json:"api_key_source"`
	}
	if err := json.Unmarshal(output.Output, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Configured || result.APIKeySource != "environment" {
		t.Fatalf("output = %#v", result)
	}
	stored, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "" {
		t.Fatalf("stored API key = %q", stored.APIKey)
	}
}

func TestSetupReplaceRequiresNonEmptyKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(apiKeyEnv, "")
	exports, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	channel := &fakeInteraction{answers: map[string]interaction.Response{
		setupOperationName: {Values: []interaction.Answer{
			{Name: "api_key_action", Value: interaction.StringValue(apiKeyReplace)},
		}},
		setupOperationName + ".api-key": {Values: []interaction.Answer{
			{Name: "api_key", Value: interaction.StringValue("   ")},
		}},
	}}
	if _, err := setupOperationFor(t, exports).Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, configFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config file should not be written: %v", err)
	}
}

func TestSetupRejectsInvalidValuesWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(apiKeyEnv, "")
	exports, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	channel := &fakeInteraction{answers: map[string]interaction.Response{
		setupOperationName: {Values: []interaction.Answer{
			{Name: "api_key_action", Value: interaction.StringValue(apiKeyKeep)},
			{Name: "base_url", Value: interaction.StringValue("ftp://example.com")},
		}},
	}}
	if _, err := setupOperationFor(t, exports).Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, configFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config file should not be written: %v", err)
	}
}

func TestSetupDetectsConcurrentConfigurationChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(apiKeyEnv, "")
	if err := saveConfig(dir, Config{APIKey: "original"}); err != nil {
		t.Fatal(err)
	}
	exports, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	channel := &fakeInteraction{
		answers: map[string]interaction.Response{
			setupOperationName: {Values: []interaction.Answer{
				{Name: "api_key_action", Value: interaction.StringValue(apiKeyKeep)},
			}},
		},
		before: func(interaction.Request) {
			if err := saveConfig(dir, Config{APIKey: "someone-else"}); err != nil {
				t.Fatal(err)
			}
		},
	}
	if _, err := setupOperationFor(t, exports).Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupPreservesInteractionErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(apiKeyEnv, "")
	exports, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	channel := &fakeInteraction{err: interaction.ErrUnavailable}
	if _, err := setupOperationFor(t, exports).Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, interaction.ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupRequiresInteractionAndState(t *testing.T) {
	dir := t.TempDir()
	exports, _, err := New(context.Background(), Dependencies{HTTP: &fakeHTTP{}, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setupOperationFor(t, exports).Invoke(context.Background(), operation.Request{}); err == nil || !strings.Contains(err.Error(), "interaction channel") {
		t.Fatalf("error = %v", err)
	}
	invalid := &setupOperation{configuration: &configState{}}
	if _, err := invalid.Invoke(context.Background(), operation.Request{Interaction: &fakeInteraction{}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v", err)
	}
}
