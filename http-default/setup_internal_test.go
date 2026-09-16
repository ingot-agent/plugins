package httpdefault

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

type setupTestScope struct{ dir string }

func (s setupTestScope) Dir() string { return s.dir }

type setupTestChannel struct {
	t       *testing.T
	secret  string
	replace string
	seen    []string
}

func (c *setupTestChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.seen = append(c.seen, request.Name)
	if containsRequestText(request, c.secret) {
		c.t.Fatalf("request %q exposes stored proxy credentials", request.Name)
	}
	switch request.Name {
	case setupOperationName:
		return interaction.Response{Values: []interaction.Answer{{Name: "proxy_mode", Value: interaction.StringValue("url")}}}, nil
	case setupOperationName + ".proxy-url-action":
		action := proxyURLKeep
		if c.replace != "" {
			action = proxyURLReplace
		}
		return interaction.Response{Values: []interaction.Answer{{Name: "proxy_url_action", Value: interaction.StringValue(action)}}}, nil
	case setupOperationName + ".proxy-url":
		if len(request.Fields) != 1 || request.Fields[0].Name != "proxy_url" || !request.Fields[0].Sensitive || request.Fields[0].Default != nil {
			c.t.Fatalf("proxy URL field = %#v", request.Fields)
		}
		return interaction.Response{Values: []interaction.Answer{{Name: "proxy_url", Value: interaction.StringValue(c.replace)}}}, nil
	default:
		c.t.Fatalf("unexpected request %q", request.Name)
		return interaction.Response{}, nil
	}
}

func (*setupTestChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupTestChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupTestChannel) Clear(context.Context, string) error           { return nil }

func TestSetupKeepsStoredProxyURLWithoutProjectingIt(t *testing.T) {
	secret := "http://user:stored-secret@proxy.example"
	current := Config{ProxyMode: "url", ProxyURL: secret}
	scope := setupTestScope{dir: t.TempDir()}
	if err := saveConfig(scope.Dir(), current); err != nil {
		t.Fatal(err)
	}
	active, err := effectiveConfiguration(current)
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupTestChannel{t: t, secret: "stored-secret"}
	result, err := (&setupOperation{scope: scope, active: active}).Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProxyURL != secret {
		t.Fatalf("stored proxy URL = %q", stored.ProxyURL)
	}
	var output struct {
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.RestartRequired {
		t.Fatal("unchanged proxy URL requires restart")
	}
	if got := strings.Join(channel.seen, ","); got != "config,config.proxy-url-action" {
		t.Fatalf("requests = %q", got)
	}
}

func TestSetupReplacesProxyURLThroughSensitiveRequest(t *testing.T) {
	current := Config{ProxyMode: "url", ProxyURL: "http://user:old-secret@proxy.example"}
	scope := setupTestScope{dir: t.TempDir()}
	if err := saveConfig(scope.Dir(), current); err != nil {
		t.Fatal(err)
	}
	active, err := effectiveConfiguration(current)
	if err != nil {
		t.Fatal(err)
	}
	replacement := "http://user:new-secret@proxy.example"
	channel := &setupTestChannel{t: t, secret: "old-secret", replace: replacement}
	result, err := (&setupOperation{scope: scope, active: active}).Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProxyURL != replacement {
		t.Fatalf("stored proxy URL = %q", stored.ProxyURL)
	}
	if !strings.Contains(string(result.Output), `"restart_required":true`) {
		t.Fatalf("output = %s", result.Output)
	}
}

func TestProxyValidationDoesNotEchoCredentials(t *testing.T) {
	_, err := normalizeConfig(Config{ProxyMode: "url", ProxyURL: "http://user:do-not-echo@%zz"})
	if !strings.Contains(err.Error(), "proxy_url") || strings.Contains(err.Error(), "do-not-echo") {
		t.Fatalf("error = %q", err)
	}
}

func containsRequestText(request interaction.Request, text string) bool {
	if text == "" {
		return false
	}
	data, _ := json.Marshal(request)
	return strings.Contains(string(data), text)
}
