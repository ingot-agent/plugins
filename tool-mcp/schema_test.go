package toolmcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeSchema(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode normalized schema: %v", err)
	}
	return document
}

func TestNormalizeInputSchemaHandlesEmptyInput(t *testing.T) {
	schema, err := normalizeInputSchema(nil)
	if err != nil {
		t.Fatalf("normalizeInputSchema: %v", err)
	}
	if string(schema) != `{"type":"object"}` {
		t.Fatalf("schema = %s", schema)
	}
}

func TestNormalizeInputSchemaRewritesDraft07(t *testing.T) {
	raw := []byte(`{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"$id": "https://example.test/schema",
		"type": "object",
		"definitions": {"name": {"type": "string"}},
		"properties": {
			"name": {"type": "string", "nullable": true},
			"count": {"type": "integer", "exclusiveMinimum": true, "minimum": 0},
			"tags": {"type": "array", "items": [{"type": "string"}, {"type": "integer"}]}
		},
		"dependencies": {"name": ["count"], "count": {"required": ["name"]}}
	}`)
	schema, err := normalizeInputSchema(raw)
	if err != nil {
		t.Fatalf("normalizeInputSchema: %v", err)
	}
	document := decodeSchema(t, schema)
	for _, removed := range []string{"$schema", "$id", "definitions", "dependencies"} {
		if _, exists := document[removed]; exists {
			t.Fatalf("%s should be rewritten", removed)
		}
	}
	if _, exists := document["$defs"]; !exists {
		t.Fatal("definitions should become $defs")
	}
	properties := document["properties"].(map[string]any)
	name := properties["name"].(map[string]any)
	types, ok := name["type"].([]any)
	if !ok || len(types) != 2 || types[1] != "null" {
		t.Fatalf("nullable type rewrite failed: %#v", name)
	}
	count := properties["count"].(map[string]any)
	if _, exists := count["minimum"]; exists {
		t.Fatal("minimum should be folded into exclusiveMinimum")
	}
	if count["exclusiveMinimum"] != float64(0) {
		t.Fatalf("exclusiveMinimum = %#v", count["exclusiveMinimum"])
	}
	tags := properties["tags"].(map[string]any)
	if _, exists := tags["items"]; exists {
		t.Fatal("tuple items should become prefixItems")
	}
	if _, exists := tags["prefixItems"]; !exists {
		t.Fatal("prefixItems missing")
	}
	dependentRequired := document["dependentRequired"].(map[string]any)
	if _, exists := dependentRequired["name"]; !exists {
		t.Fatal("dependentRequired missing")
	}
	if _, exists := document["dependentSchemas"]; !exists {
		t.Fatal("dependentSchemas missing")
	}
	reference := decodeSchema(t, mustNormalize(t, `{"type":"object","properties":{"x":{"$ref":"#/$defs/x","nullable":true}},"$defs":{"x":{"type":"string"}}}`))
	referenceProperties := reference["properties"].(map[string]any)
	nullableReference := referenceProperties["x"].(map[string]any)
	if _, exists := nullableReference["anyOf"]; !exists {
		t.Fatalf("nullable $ref should become anyOf: %#v", nullableReference)
	}
	if err := compileSchema("test", schema); err != nil {
		t.Fatalf("normalized schema does not compile: %v", err)
	}
}

func TestNormalizeInputSchemaHandlesBoolean(t *testing.T) {
	allow, err := normalizeInputSchema([]byte(`true`))
	if err != nil {
		t.Fatalf("normalizeInputSchema: %v", err)
	}
	if string(allow) != `{"type":"object"}` {
		t.Fatalf("boolean true schema = %s", allow)
	}
	deny, err := normalizeInputSchema([]byte(`false`))
	if err != nil {
		t.Fatalf("normalizeInputSchema: %v", err)
	}
	if err := compileSchema("deny", deny); err != nil {
		t.Fatalf("boolean false schema does not compile: %v", err)
	}
}

func TestNormalizeInputSchemaRejectsNonObject(t *testing.T) {
	if _, err := normalizeInputSchema([]byte(`"text"`)); err == nil {
		t.Fatal("expected an error for a string schema")
	}
	if _, err := normalizeInputSchema([]byte(`[1,2]`)); err == nil {
		t.Fatal("expected an error for an array schema")
	}
}

func TestCompileSchemaRejectsUnresolvableReference(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"#/$defs/missing"}}}`)
	if err := compileSchema("broken", schema); err == nil {
		t.Fatal("expected an error for an unresolvable reference")
	}
}

func TestDescribeToolPrefixesOrigin(t *testing.T) {
	description := describeTool("github", "create_issue", "Create an issue.")
	if !strings.Contains(description, `"github"`) {
		t.Fatalf("description lost the origin: %q", description)
	}
	fallback := describeTool("github", "create_issue", "")
	if !strings.Contains(fallback, `"create_issue"`) {
		t.Fatalf("fallback description = %q", fallback)
	}
}

func mustNormalize(t *testing.T, raw string) []byte {
	t.Helper()
	schema, err := normalizeInputSchema([]byte(raw))
	if err != nil {
		t.Fatalf("normalizeInputSchema: %v", err)
	}
	return schema
}
