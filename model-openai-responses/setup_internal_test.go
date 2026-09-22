package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
)

type setupTestScope struct{ dir string }

func (s setupTestScope) Dir() string { return s.dir }

type setupTestHTTP struct{}

func (setupTestHTTP) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}

type setupTestAssets struct{}

func (setupTestAssets) Stat(context.Context, asset.Reference) (asset.Info, error) {
	return asset.Info{}, errors.New("not used")
}

func (setupTestAssets) Open(context.Context, asset.Reference) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

type setupTestChannel struct {
	request interaction.Request
	respond func(interaction.Request) (interaction.Response, error)
}

func (c *setupTestChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	return c.respond(request)
}

func (*setupTestChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupTestChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupTestChannel) Clear(context.Context, string) error           { return nil }

func TestSetupRenamePreservesSecretAndHiddenProviderFields(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	current := Config{Providers: []ProviderConfig{{
		Name: "old", BaseURL: "https://example.test/v1", APIKey: "secret", ReasoningEfforts: []string{"low", "high"},
		Organization: "org", Project: "project", Models: []string{"m"},
		DefaultHeaders:   map[string]string{"X-Tenant": "one"},
		MaxResponseBytes: 123, MaxErrorBodyBytes: 124, MaxAssetBytes: 125, AssetConcurrency: 2,
	}}}
	if err := saveConfig(scope.Dir(), current); err != nil {
		t.Fatal(err)
	}
	inspected := errors.New("request inspected")
	var captured *http.Request
	client := liveTestHTTP(func(_ context.Context, request *http.Request) (*http.Response, error) {
		captured = request
		return nil, inspected
	})
	exports, _, err := New(context.Background(), Dependencies{HTTP: client, Assets: setupTestAssets{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupTestChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		providers := findSetupField(t, request.Fields, "providers")
		item := providers.Default.Items[0]
		setObjectString(t, &item, "name", "renamed")
		return interaction.Response{Values: []interaction.Answer{{Name: "providers", Value: interaction.ListValue([]interaction.Value{item})}}}, nil
	}}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	want := current.Providers[0]
	want.Name = "renamed"
	if !reflect.DeepEqual(stored.Providers, []ProviderConfig{want}) {
		t.Fatalf("stored providers = %#v, want %#v", stored.Providers, []ProviderConfig{want})
	}
	var output struct {
		Providers       int  `json:"providers"`
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Providers != 1 || output.RestartRequired {
		t.Fatalf("output = %#v", output)
	}
	entries := liveEntries(t, exports)
	if _, err := entries[0].Complete(context.Background(), model.Request{Model: "m"}); !errors.Is(err, inspected) {
		t.Fatalf("renamed provider request error = %v", err)
	}
	if entries[0].Name != "renamed" || captured.Header.Get("Authorization") != "Bearer secret" || captured.Header.Get("X-Tenant") != "one" {
		t.Fatal("renamed provider did not retain its active credentials")
	}

	providersField := findSetupField(t, channel.request.Fields, "providers")
	apiKey := findSetupField(t, providersField.Element.Fields, "api_key")
	if !apiKey.Sensitive || apiKey.Default != nil {
		t.Fatalf("api_key field = %#v", apiKey)
	}
	defaultHeaders := findSetupField(t, providersField.Element.Fields, "default_headers")
	headerSource := findSetupField(t, defaultHeaders.Element.Fields, "source")
	headerValue := findSetupField(t, defaultHeaders.Element.Fields, "value")
	if headerSource.Kind != interaction.FieldString || len(headerSource.Options) != 1 || !headerValue.Sensitive || headerValue.Default != nil {
		t.Fatalf("default header fields = source %#v value %#v", headerSource, headerValue)
	}
	if valueContainsString(*providersField.Default, "secret") || valueContainsString(*providersField.Default, "one") {
		t.Fatalf("provider default exposes a secret: %#v", providersField.Default.Items[0])
	}
}

func TestSetupPersistsReasoningEffortMultiChoice(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	current := Config{Providers: []ProviderConfig{{
		Name: "p", BaseURL: "https://example.test", Models: []string{"inherited", "disabled", "high-only"},
	}}}
	if err := saveConfig(scope.Dir(), current); err != nil {
		t.Fatal(err)
	}
	exports, _, err := New(context.Background(), Dependencies{HTTP: setupTestHTTP{}, Assets: setupTestAssets{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupTestChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		providers := findSetupField(t, request.Fields, "providers")
		item := providers.Default.Items[0]
		if efforts := findObjectEntry(t, item, "reasoning_efforts"); efforts.Kind != interaction.ValueStrings || len(efforts.Strings) != 0 {
			t.Fatalf("reasoning effort default = %#v", efforts)
		}
		setObjectValue(t, &item, "reasoning_efforts", interaction.StringsValue([]string{"low", "high"}))
		setObjectValue(t, &item, "reasoning_effort_overrides", interaction.ListValue([]interaction.Value{
			interaction.ObjectValue([]interaction.Entry{
				{Name: "model", Value: interaction.StringValue("disabled")},
				{Name: "reasoning_efforts", Value: interaction.StringsValue([]string{})},
			}),
			interaction.ObjectValue([]interaction.Entry{
				{Name: "model", Value: interaction.StringValue("high-only")},
				{Name: "reasoning_efforts", Value: interaction.StringsValue([]string{"high"})},
			}),
		}))
		return interaction.Response{Values: []interaction.Answer{{Name: "providers", Value: interaction.ListValue([]interaction.Value{item})}}}, nil
	}}
	if _, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Providers[0].ReasoningEfforts; !reflect.DeepEqual(got, []string{"low", "high"}) {
		t.Fatalf("stored reasoning efforts = %v", got)
	}
	wantOverrides := []ReasoningEffortOverride{
		{Model: "disabled", ReasoningEfforts: []string{}},
		{Model: "high-only", ReasoningEfforts: []string{"high"}},
	}
	if got := stored.Providers[0].ReasoningEffortOverrides; !reflect.DeepEqual(got, wantOverrides) {
		t.Fatalf("stored reasoning effort overrides = %#v, want %#v", got, wantOverrides)
	}
	entries := liveEntries(t, exports)
	wantEfforts := [][]model.ReasoningEffort{
		{model.ReasoningEffortLow, model.ReasoningEffortHigh},
		nil,
		{model.ReasoningEffortHigh},
	}
	for i, want := range wantEfforts {
		if got := entries[0].Models[i].ReasoningEfforts; !reflect.DeepEqual(got, want) {
			t.Fatalf("published reasoning efforts for %q = %v, want %v", entries[0].Models[i].Name, got, want)
		}
	}
}

func TestSetupManagesDefaultHeadersAndProviderLimits(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	current := Config{Providers: []ProviderConfig{{
		Name: "p", BaseURL: "https://example.test", DefaultHeaders: map[string]string{"X-Secret": "old-value"},
		MaxResponseBytes: 100, MaxErrorBodyBytes: 101, MaxAssetBytes: 102, AssetConcurrency: 2,
	}}}
	if err := saveConfig(scope.Dir(), current); err != nil {
		t.Fatal(err)
	}
	var captured *http.Request
	client := liveTestHTTP(func(_ context.Context, request *http.Request) (*http.Response, error) {
		captured = request
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}" + strings.Repeat(" ", 148)))}, nil
	})
	exports, _, err := New(context.Background(), Dependencies{HTTP: client, Assets: setupTestAssets{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := liveEntries(t, exports)[0].Complete(context.Background(), model.Request{Model: "m"}); !errors.Is(err, ErrResponseLimit) {
		t.Fatalf("original response limit error = %v", err)
	}
	channel := &setupTestChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		providers := findSetupField(t, request.Fields, "providers")
		item := providers.Default.Items[0]
		setObjectInteger(t, &item, "max_response_bytes", 200)
		headers := findObjectEntry(t, item, "default_headers")
		header := headers.Items[0]
		setObjectString(t, &header, "name", "X-Renamed")
		setObjectString(t, &header, "action", headerValueReplace)
		header.Entries = append(header.Entries, interaction.Entry{Name: "value", Value: interaction.StringValue("new-value")})
		setObjectValue(t, &item, "default_headers", interaction.ListValue([]interaction.Value{header}))
		return interaction.Response{Values: []interaction.Answer{{Name: "providers", Value: interaction.ListValue([]interaction.Value{item})}}}, nil
	}}
	_, err = exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	saved := stored.Providers[0]
	if saved.MaxResponseBytes != 200 || saved.MaxErrorBodyBytes != 101 || saved.MaxAssetBytes != 102 || saved.AssetConcurrency != 2 {
		t.Fatalf("stored limits = %#v", saved)
	}
	if !reflect.DeepEqual(saved.DefaultHeaders, map[string]string{"X-Renamed": "new-value"}) {
		t.Fatalf("stored headers = %#v", saved.DefaultHeaders)
	}
	if _, err := liveEntries(t, exports)[0].Complete(context.Background(), model.Request{Model: "m"}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("updated response limit should allow parsing: %v", err)
	}
	if captured.Header.Get("X-Renamed") != "new-value" || captured.Header.Get("X-Secret") != "" {
		t.Fatal("saved headers were not published")
	}
}

func TestSetupAppliesFullConstructionValidationBeforeSave(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	current := Config{Providers: []ProviderConfig{{Name: "p", BaseURL: "https://example.test"}}}
	if err := saveConfig(scope.Dir(), current); err != nil {
		t.Fatal(err)
	}
	exports, _, err := New(context.Background(), Dependencies{HTTP: setupTestHTTP{}, Assets: setupTestAssets{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupTestChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		providers := findSetupField(t, request.Fields, "providers")
		item := providers.Default.Items[0]
		setObjectString(t, &item, "base_url", "file:///tmp/not-http")
		return interaction.Response{Values: []interaction.Answer{{Name: "providers", Value: interaction.ListValue([]interaction.Value{item})}}}, nil
	}}
	_, err = exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Providers) != 1 || stored.Providers[0].Name != "p" || stored.Providers[0].BaseURL != "https://example.test" {
		t.Fatalf("stored = %#v, want original provider", stored)
	}
}

func findSetupField(t *testing.T, fields []interaction.Field, name string) interaction.Field {
	t.Helper()
	for _, field := range fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("field %q not found", name)
	return interaction.Field{}
}

func setObjectString(t *testing.T, object *interaction.Value, name, value string) {
	t.Helper()
	for i := range object.Entries {
		if object.Entries[i].Name == name {
			object.Entries[i].Value = interaction.StringValue(value)
			return
		}
	}
	t.Fatalf("entry %q not found", name)
}

func setObjectInteger(t *testing.T, object *interaction.Value, name string, value int64) {
	t.Helper()
	setObjectValue(t, object, name, interaction.IntegerValue(value))
}

func setObjectValue(t *testing.T, object *interaction.Value, name string, value interaction.Value) {
	t.Helper()
	for i := range object.Entries {
		if object.Entries[i].Name == name {
			object.Entries[i].Value = value
			return
		}
	}
	t.Fatalf("entry %q not found", name)
}

func findObjectEntry(t *testing.T, object interaction.Value, name string) interaction.Value {
	t.Helper()
	for _, entry := range object.Entries {
		if entry.Name == name {
			return entry.Value
		}
	}
	t.Fatalf("entry %q not found", name)
	return interaction.Value{}
}

func valueContainsString(value interaction.Value, needle string) bool {
	if value.Kind == interaction.ValueString && strings.Contains(value.String, needle) {
		return true
	}
	for _, item := range value.Items {
		if valueContainsString(item, needle) {
			return true
		}
	}
	for _, entry := range value.Entries {
		if valueContainsString(entry.Value, needle) {
			return true
		}
	}
	return false
}
