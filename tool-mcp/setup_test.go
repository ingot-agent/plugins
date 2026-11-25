package toolmcp

import (
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
)

func TestDisplayAssignmentsHidesLiteralSecrets(t *testing.T) {
	lines := displayAssignments(map[string]string{
		"REFERENCE": "${TOKEN}",
		"LITERAL":   "super-secret",
		"PREFIXED":  "Bearer ${TOKEN}",
	})
	joined := ""
	for _, line := range lines {
		joined += line + "\n"
	}
	if joined != "LITERAL=\nPREFIXED=\nREFERENCE=${TOKEN}\n" {
		t.Fatalf("displayed assignments = %q", joined)
	}
}

func TestParseAssignments(t *testing.T) {
	values, err := parseAssignments([]string{"A=1", "B=x=y", "  "}, "env")
	if err != nil {
		t.Fatalf("parseAssignments: %v", err)
	}
	if values["A"] != "1" || values["B"] != "x=y" {
		t.Fatalf("values = %#v", values)
	}
	if _, err := parseAssignments([]string{"NOEQUALS"}, "env"); err == nil {
		t.Fatal("expected an error for a missing '='")
	}
	if _, err := parseAssignments([]string{"A=1", "A=2"}, "env"); err == nil {
		t.Fatal("expected an error for a duplicate key")
	}
}

func TestParseServerValueKeepsStoredSecrets(t *testing.T) {
	current, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true",
		Env: map[string]string{"TOKEN": "stored"},
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	value := objectValue(map[string]interaction.Value{
		"name":           interaction.StringValue("demo"),
		"transport":      interaction.StringValue(transportStdio),
		"command":        interaction.StringValue("true"),
		"env":            interaction.StringsValue([]string{"TOKEN="}),
		"secrets_action": interaction.StringValue(secretActionKeep),
	})
	server, err := parseServerValue(value, current)
	if err != nil {
		t.Fatalf("parseServerValue: %v", err)
	}
	if server.Env["TOKEN"] != "stored" {
		t.Fatalf("stored secret was lost: %#v", server.Env)
	}
}

func TestParseServerValueReplacesSecrets(t *testing.T) {
	current, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true", Env: map[string]string{"TOKEN": "stored"},
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	value := objectValue(map[string]interaction.Value{
		"name":           interaction.StringValue("demo"),
		"transport":      interaction.StringValue(transportStdio),
		"command":        interaction.StringValue("true"),
		"env":            interaction.StringsValue([]string{"TOKEN=${NEW_TOKEN}"}),
		"secrets_action": interaction.StringValue(secretActionReplace),
	})
	server, err := parseServerValue(value, current)
	if err != nil {
		t.Fatalf("parseServerValue: %v", err)
	}
	if server.Env["TOKEN"] != "${NEW_TOKEN}" {
		t.Fatalf("replacement failed: %#v", server.Env)
	}
}

func TestParseServerValueDropsOtherTransportFields(t *testing.T) {
	value := objectValue(map[string]interaction.Value{
		"name":      interaction.StringValue("demo"),
		"transport": interaction.StringValue(transportHTTP),
		"command":   interaction.StringValue("npx"),
		"env":       interaction.StringsValue([]string{"A=1"}),
		"url":       interaction.StringValue("https://example.test/mcp"),
	})
	server, err := parseServerValue(value, Config{})
	if err != nil {
		t.Fatalf("parseServerValue: %v", err)
	}
	if server.Command != "" || len(server.Env) != 0 {
		t.Fatalf("stdio fields survived an http switch: %#v", server)
	}
	if server.URL != "https://example.test/mcp" {
		t.Fatalf("url = %q", server.URL)
	}
}

func TestParseServerValueRejectsUnknownSecretAction(t *testing.T) {
	value := objectValue(map[string]interaction.Value{
		"name":           interaction.StringValue("demo"),
		"secrets_action": interaction.StringValue("maybe"),
	})
	if _, err := parseServerValue(value, Config{}); err == nil {
		t.Fatal("expected an error")
	} else if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error does not wrap ErrInvalidConfig: %v", err)
	}
}

func TestEntryStringsAcceptsScalarAndList(t *testing.T) {
	entries := map[string]interaction.Value{
		"scalar": interaction.StringValue("value"),
		"list":   interaction.StringsValue([]string{"a", "b"}),
		"empty":  interaction.StringValue(""),
	}
	if values, ok := entryStrings(entries, "scalar"); !ok || len(values) != 1 || values[0] != "value" {
		t.Fatalf("scalar = %v, ok=%v", values, ok)
	}
	if values, ok := entryStrings(entries, "list"); !ok || len(values) != 2 {
		t.Fatalf("list = %v, ok=%v", values, ok)
	}
	if values, ok := entryStrings(entries, "empty"); !ok || len(values) != 0 {
		t.Fatalf("empty = %v, ok=%v", values, ok)
	}
}
