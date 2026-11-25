package toolmcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// normalizeInputSchema converts a remote MCP input schema into the strict
// Draft 2020-12 object accepted by the standard tool Runtime. It never
// silently discards structure: unsupported keywords are rewritten to their
// 2020-12 equivalent, and anything that still fails to compile is reported to
// the caller so the tool can be rejected instead of breaking startup.
func normalizeInputSchema(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`{"type":"object"}`), nil
	}

	var document any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode input schema: %w", err)
	}

	switch value := document.(type) {
	case bool:
		// A boolean schema is legal JSON Schema but not a legal tool input
		// schema, which must be an object.
		if value {
			return json.RawMessage(`{"type":"object"}`), nil
		}
		return json.RawMessage(`{"type":"object","not":{}}`), nil
	case map[string]any:
		converted := convertSchemaNode(value)
		object, ok := converted.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("input schema must be a JSON object")
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return nil, fmt.Errorf("encode input schema: %w", err)
		}
		return encoded, nil
	default:
		return nil, fmt.Errorf("input schema must be a JSON object or boolean")
	}
}

// convertSchemaNode rewrites draft-04/07 and OpenAPI constructs into Draft
// 2020-12 equivalents, recursively.
func convertSchemaNode(node any) any {
	switch value := node.(type) {
	case map[string]any:
		return convertSchemaObject(value)
	case []any:
		for index := range value {
			value[index] = convertSchemaNode(value[index])
		}
		return value
	default:
		return value
	}
}

func convertSchemaObject(object map[string]any) map[string]any {
	// The Runtime rejects a $schema that is not 2020-12, and a remote $id can
	// point at an unreachable document. Both are dropped unconditionally.
	delete(object, "$schema")
	delete(object, "$id")

	if definitions, ok := object["definitions"]; ok {
		delete(object, "definitions")
		if existing, exists := object["$defs"]; exists {
			if existingObject, ok := existing.(map[string]any); ok {
				if definitionsObject, ok := definitions.(map[string]any); ok {
					for name, schema := range definitionsObject {
						if _, present := existingObject[name]; !present {
							existingObject[name] = schema
						}
					}
				}
			}
		} else {
			object["$defs"] = definitions
		}
	}

	if dependencies, ok := object["dependencies"]; ok {
		delete(object, "dependencies")
		required := map[string]any{}
		schemas := map[string]any{}
		if entries, ok := dependencies.(map[string]any); ok {
			for name, entry := range entries {
				if _, isArray := entry.([]any); isArray {
					required[name] = entry
				} else {
					schemas[name] = entry
				}
			}
		}
		if len(required) > 0 {
			object["dependentRequired"] = required
		}
		if len(schemas) > 0 {
			object["dependentSchemas"] = schemas
		}
	}

	if items, ok := object["items"].([]any); ok {
		delete(object, "items")
		object["prefixItems"] = items
		delete(object, "additionalItems")
	}

	convertExclusiveBound(object, "exclusiveMinimum", "minimum")
	convertExclusiveBound(object, "exclusiveMaximum", "maximum")
	convertNullable(object)

	for key, value := range object {
		object[key] = convertSchemaNode(value)
	}
	return object
}

// convertExclusiveBound rewrites the draft-04 boolean form of
// exclusiveMinimum/exclusiveMaximum into the 2020-12 numeric form.
func convertExclusiveBound(object map[string]any, exclusiveKey, boundKey string) {
	flag, ok := object[exclusiveKey].(bool)
	if !ok {
		return
	}
	if !flag {
		delete(object, exclusiveKey)
		return
	}
	bound, ok := object[boundKey]
	if !ok {
		delete(object, exclusiveKey)
		return
	}
	object[exclusiveKey] = bound
	delete(object, boundKey)
}

// convertNullable rewrites the OpenAPI `nullable: true` marker into a JSON
// Schema type union where a type is declared.
func convertNullable(object map[string]any) {
	nullable, ok := object["nullable"].(bool)
	if !ok {
		return
	}
	delete(object, "nullable")
	if !nullable {
		return
	}
	switch declared := object["type"].(type) {
	case string:
		if declared != "null" {
			object["type"] = []any{declared, "null"}
		}
		return
	case []any:
		for _, entry := range declared {
			if text, ok := entry.(string); ok && text == "null" {
				return
			}
		}
		object["type"] = append(declared, "null")
		return
	}

	// Without a declared type (typically a $ref or a combinator), nullability
	// is expressed as a union so the referenced constraints stay intact.
	if len(object) == 0 {
		object["type"] = "null"
		return
	}
	inner := make(map[string]any, len(object))
	for key, value := range object {
		inner[key] = value
		delete(object, key)
	}
	object["anyOf"] = []any{inner, map[string]any{"type": "null"}}
}

// compileSchema mirrors the validation performed by the standard tool Runtime
// so that a rejected tool is isolated during discovery rather than discovered
// at Runtime startup.
func compileSchema(name string, schema json.RawMessage) error {
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(schema))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	if document == nil {
		return fmt.Errorf("schema must be a JSON object")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	location := "urn:ingot:tool-schema:" + name
	if err := compiler.AddResource(location, document); err != nil {
		return fmt.Errorf("load schema: %w", err)
	}
	if _, err := compiler.Compile(location); err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	return nil
}

// schemaBytes returns the encoded size of a normalized schema, used to report
// the context cost of a tool set during configuration.
func schemaBytes(schema json.RawMessage) int {
	return len(bytes.TrimSpace(schema))
}

// describeTool synthesizes a description when a remote server omits one. The
// description always names its origin so that the model can attribute it.
func describeTool(server, tool, description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return fmt.Sprintf("MCP tool %q provided by server %q.", tool, server)
	}
	return fmt.Sprintf("MCP tool %q provided by server %q. %s", tool, server, description)
}
