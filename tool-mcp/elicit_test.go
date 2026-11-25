package toolmcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordingBinder binds every Session to one scripted channel and remembers the
// requested scope.
type recordingBinder struct {
	channel *scriptedChannel
	mu      sync.Mutex
	scopes  []execution.Scope
	err     error
}

func (b *recordingBinder) Bind(scope execution.Scope) (interaction.Channel, error) {
	b.mu.Lock()
	b.scopes = append(b.scopes, scope)
	b.mu.Unlock()
	if b.err != nil {
		return nil, b.err
	}
	return b.channel, nil
}

func (b *recordingBinder) boundScopes() []execution.Scope {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]execution.Scope(nil), b.scopes...)
}

func schemaOf(t *testing.T, document string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(document), &value); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return value
}

func TestElicitFieldsMapsEverySupportedType(t *testing.T) {
	fields, err := elicitFields(schemaOf(t, `{
		"type": "object",
		"properties": {
			"name": {"type": "string", "title": "Name", "description": "Your name", "default": "anon", "maxLength": 20},
			"count": {"type": "integer", "minimum": 1, "maximum": 5, "default": 2},
			"ratio": {"type": "number", "default": 0.5},
			"flag": {"type": "boolean", "default": true},
			"color": {"type": "string", "title": "Colour", "enum": ["red", "green"], "enumNames": ["Red", "Green"], "default": "green"},
			"tags": {"type": "array", "items": {"type": "string", "enum": ["a", "b"]}, "minItems": 1, "default": ["a"]},
			"secret": {"type": "string", "format": "password"}
		},
		"required": ["name", "count"]
	}`))
	if err != nil {
		t.Fatalf("elicitFields: %v", err)
	}
	if len(fields) != 7 {
		t.Fatalf("fields = %d", len(fields))
	}
	byName := make(map[string]interaction.Field, len(fields))
	for _, field := range fields {
		byName[field.Name] = field
	}

	if field := byName["name"]; field.Kind != interaction.FieldString || !field.Required ||
		field.Label != "Name" || field.Description != "Your name At most 20 characters." ||
		field.Default == nil || field.Default.String != "anon" {
		t.Fatalf("name field = %#v", field)
	}
	if field := byName["count"]; field.Kind != interaction.FieldInteger || !field.Required ||
		field.Default == nil || field.Default.Integer != 2 ||
		!strings.Contains(field.Description, "Minimum: 1") || !strings.Contains(field.Description, "Maximum: 5") {
		t.Fatalf("count field = %#v", field)
	}
	if field := byName["ratio"]; field.Kind != interaction.FieldNumber || field.Default == nil || field.Default.Number != 0.5 {
		t.Fatalf("ratio field = %#v", field)
	}
	if field := byName["flag"]; field.Kind != interaction.FieldBoolean || field.Default == nil || !field.Default.Boolean {
		t.Fatalf("flag field = %#v", field)
	}
	choice := byName["color"]
	if choice.Kind != interaction.FieldChoice || len(choice.Options) != 2 ||
		choice.Options[1].Value != "green" || choice.Options[1].Label != "Green" ||
		choice.Default == nil || choice.Default.String != "green" {
		t.Fatalf("color field = %#v", choice)
	}
	multi := byName["tags"]
	if multi.Kind != interaction.FieldMultiChoice || len(multi.Options) != 2 ||
		multi.Default == nil || len(multi.Default.Strings) != 1 ||
		!strings.Contains(multi.Description, "Select at least 1") {
		t.Fatalf("tags field = %#v", multi)
	}
	if !byName["secret"].Sensitive {
		t.Fatal("a password format must mark the field sensitive")
	}
	// The form order must not depend on map iteration order.
	for index := 1; index < len(fields); index++ {
		if fields[index-1].Name > fields[index].Name {
			t.Fatalf("fields are not sorted: %v", []string{fields[0].Name, fields[1].Name})
		}
	}
}

func TestElicitFieldsRejectsUnsupportedSchemas(t *testing.T) {
	cases := map[string]string{
		"nested object": `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}}}}}`,
		"free array":    `{"type":"object","properties":{"a":{"type":"array","items":{"type":"string"}}}}`,
		"null type":     `{"type":"object","properties":{"a":{}}}`,
		"no properties": `{"type":"object","properties":{}}`,
		"not object":    `{"type":"array"}`,
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := elicitFields(schemaOf(t, document)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := elicitFields(nil); err == nil {
		t.Fatal("a missing schema must be rejected")
	}
}

func TestElicitFieldsRejectsOversizedSchemas(t *testing.T) {
	properties := make(map[string]any, maxElicitFields+1)
	for index := 0; index <= maxElicitFields; index++ {
		properties[string(rune('a'+index))] = map[string]any{"type": "string"}
	}
	document := map[string]any{"type": "object", "properties": properties}
	if _, err := elicitFields(document); err == nil {
		t.Fatal("too many properties must be rejected")
	}

	values := make([]string, maxElicitOptions+1)
	for index := range values {
		values[index] = "v"
	}
	tooMany := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string", "enum": values}},
	}
	if _, err := elicitFields(tooMany); err == nil {
		t.Fatal("too many enum options must be rejected")
	}
}

func TestElicitContentMapsAnswers(t *testing.T) {
	fields := []interaction.Field{
		{Name: "name", Kind: interaction.FieldString, Required: true},
		{Name: "count", Kind: interaction.FieldInteger},
		{Name: "ratio", Kind: interaction.FieldNumber},
		{Name: "flag", Kind: interaction.FieldBoolean},
		{Name: "color", Kind: interaction.FieldChoice, Options: []interaction.Option{{Value: "red"}}},
		{Name: "tags", Kind: interaction.FieldMultiChoice, Options: []interaction.Option{{Value: "a"}, {Value: "b"}}},
	}
	response := interaction.Response{Values: []interaction.Answer{
		{Name: "name", Value: interaction.StringValue("widget")},
		{Name: "count", Value: interaction.IntegerValue(3)},
		{Name: "ratio", Value: interaction.NumberValue(1.5)},
		{Name: "flag", Value: interaction.BooleanValue(true)},
		{Name: "color", Value: interaction.StringValue("red")},
		{Name: "tags", Value: interaction.StringsValue([]string{"a", "b"})},
	}}
	content, ok := elicitContent(fields, response)
	if !ok {
		t.Fatal("elicitContent rejected a complete answer")
	}
	if content["name"] != "widget" || content["count"] != int64(3) || content["ratio"] != 1.5 || content["flag"] != true {
		t.Fatalf("content = %#v", content)
	}
	tags, ok := content["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" {
		t.Fatalf("tags = %#v", content["tags"])
	}
}

func TestElicitContentRejectsIncompleteOrInvalidAnswers(t *testing.T) {
	required := []interaction.Field{{Name: "name", Kind: interaction.FieldString, Required: true}}
	if _, ok := elicitContent(required, interaction.Response{}); ok {
		t.Fatal("a missing required answer must be rejected")
	}
	if _, ok := elicitContent(required, interaction.Response{Values: []interaction.Answer{
		{Name: "name", Value: interaction.IntegerValue(1)},
	}}); ok {
		t.Fatal("an answer of the wrong kind must be rejected")
	}

	choice := []interaction.Field{{Name: "c", Kind: interaction.FieldChoice, Options: []interaction.Option{{Value: "a"}}}}
	if _, ok := elicitContent(choice, interaction.Response{Values: []interaction.Answer{
		{Name: "c", Value: interaction.StringValue("z")},
	}}); ok {
		t.Fatal("a value outside the declared options must be rejected")
	}

	optional := []interaction.Field{
		{Name: "name", Kind: interaction.FieldString, Required: true},
		{Name: "extra", Kind: interaction.FieldString},
	}
	content, ok := elicitContent(optional, interaction.Response{Values: []interaction.Answer{
		{Name: "name", Value: interaction.StringValue("x")},
	}})
	if !ok || len(content) != 1 {
		t.Fatalf("an omitted optional field must be accepted: %#v ok=%v", content, ok)
	}

	duplicate := []interaction.Field{{Name: "name", Kind: interaction.FieldString}}
	if _, ok := elicitContent(duplicate, interaction.Response{Values: []interaction.Answer{
		{Name: "name", Value: interaction.StringValue("a")},
		{Name: "name", Value: interaction.StringValue("b")},
	}}); ok {
		t.Fatal("duplicate answers must be rejected")
	}
}

func TestElicitValueCoercesIntegerAndNumber(t *testing.T) {
	if value, ok := elicitValue(interaction.Field{Kind: interaction.FieldInteger},
		interaction.NumberValue(4)); !ok || value != int64(4) {
		t.Fatalf("integral float should become an integer: %#v ok=%v", value, ok)
	}
	if _, ok := elicitValue(interaction.Field{Kind: interaction.FieldInteger},
		interaction.NumberValue(4.5)); ok {
		t.Fatal("a fractional float must not become an integer")
	}
	if value, ok := elicitValue(interaction.Field{Kind: interaction.FieldNumber},
		interaction.IntegerValue(2)); !ok || value != float64(2) {
		t.Fatalf("an integer should widen to a number: %#v ok=%v", value, ok)
	}
	// A multi-select answer may arrive as a repeated list instead of a string set.
	field := interaction.Field{Kind: interaction.FieldMultiChoice, Options: []interaction.Option{{Value: "a"}}}
	if value, ok := elicitValue(field, interaction.ListValue([]interaction.Value{interaction.StringValue("a")})); !ok {
		t.Fatalf("a list answer should be accepted: %#v", value)
	}
}

func TestElicitDescriptionAttributesTheServer(t *testing.T) {
	description := elicitDescription("playwrite", "Please confirm the deployment.")
	if !strings.Contains(description, `"playwrite"`) || !strings.Contains(description, "Please confirm") {
		t.Fatalf("description = %q", description)
	}
	long := elicitDescription("playwrite", strings.Repeat("x", maxElicitMessageBytes*2))
	if len(long) > maxElicitMessageBytes+128 {
		t.Fatalf("description is %d bytes", len(long))
	}
	if !strings.Contains(elicitDescription("srv", "  "), `"srv"`) {
		t.Fatal("an empty message must still identify the server")
	}
}

// elicitPlatform builds a platform whose helper connections can ask the user.
func elicitPlatform(t *testing.T, dir string, binder interaction.ExecutionBinder) *platform {
	t.Helper()
	config, err := normalizeConfig(helperConfig(t))
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	return newPlatform(dir, ingotabiNone(), ingotabi.None[workspace.Resolver](),
		ingotabi.Some[interaction.ExecutionBinder](binder), discardLogger(), nil, config)
}

// TestElicitationRoundTrip drives the whole path: the server asks, the request
// reaches the Session's channel, and the answer returns to the server.
func TestElicitationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	channel := newScriptedChannel(map[string]interaction.Value{
		"confirm": interaction.BooleanValue(true),
		"note":    interaction.StringValue("staging"),
	})
	binder := &recordingBinder{channel: channel}
	instance := elicitPlatform(t, dir, binder)
	defer instance.closeAll()

	entry := &mcpTool{
		platform: instance,
		server:   "helper",
		wireName: "elicit",
		definition: tool.Definition{
			Name:        "mcp_helper_elicit",
			Description: "elicit",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	}
	result, err := entry.Invoke(context.Background(), tool.Invocation{
		Scope: execution.Scope{SessionID: sessionIDOf("session-a")},
		Call:  tool.Call{ID: "e1", Name: "mcp_helper_elicit", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	text, ok := content.TextOnly(result.Content)
	if !ok {
		t.Fatalf("result = %#v", result.Content)
	}
	var outcome struct {
		Action  string         `json:"action"`
		Content map[string]any `json:"content"`
	}
	if err := json.Unmarshal([]byte(text), &outcome); err != nil {
		t.Fatalf("decode outcome %q: %v", text, err)
	}
	if outcome.Action != elicitAccept {
		t.Fatalf("action = %q, want accept", outcome.Action)
	}
	if outcome.Content["confirm"] != true || outcome.Content["note"] != "staging" {
		t.Fatalf("content = %#v", outcome.Content)
	}

	// The question must reach the Session that owns the connection.
	scopes := binder.boundScopes()
	if len(scopes) != 1 || scopes[0].SessionID != sessionIDOf("session-a") {
		t.Fatalf("bound scopes = %#v", scopes)
	}
	if len(channel.requests) != 1 {
		t.Fatalf("requests = %d", len(channel.requests))
	}
	request := channel.requests[0]
	if request.Name != elicitRequestName {
		t.Fatalf("request name = %q", request.Name)
	}
	if !strings.Contains(request.Description, `"helper"`) {
		t.Fatalf("the form must attribute the server: %q", request.Description)
	}
	if len(request.Fields) != 2 {
		t.Fatalf("fields = %#v", request.Fields)
	}
	if request.Fields[0].Name != "confirm" || request.Fields[0].Kind != interaction.FieldBoolean || !request.Fields[0].Required {
		t.Fatalf("confirm field = %#v", request.Fields[0])
	}
	if request.Fields[1].Name != "note" || request.Fields[1].Default == nil || request.Fields[1].Default.String != "none" {
		t.Fatalf("note field = %#v", request.Fields[1])
	}
}

// TestElicitationDeclinedWhenUserProvidesNothing covers a host that answers with
// no values: the server must see a refusal, not a partial accept.
func TestElicitationDeclinedWhenUserProvidesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	instance := elicitPlatform(t, dir, &recordingBinder{channel: newScriptedChannel(nil)})
	defer instance.closeAll()

	entry := &mcpTool{
		platform: instance, server: "helper", wireName: "elicit",
		definition: tool.Definition{Name: "mcp_helper_elicit", Description: "e", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	result, err := entry.Invoke(context.Background(), tool.Invocation{
		Scope: execution.Scope{SessionID: sessionIDOf("session-a")},
		Call:  tool.Call{ID: "e1", Name: "mcp_helper_elicit", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	text, _ := content.TextOnly(result.Content)
	if !strings.Contains(text, `"action":"decline"`) {
		t.Fatalf("outcome = %q", text)
	}
}

// TestElicitationNeverAdvertisedWithoutASession pins the discovery guarantee: a
// connection without a Session must not tell the server it can ask questions.
func TestElicitationNeverAdvertisedWithoutASession(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	channel := newScriptedChannel(map[string]interaction.Value{"confirm": interaction.BooleanValue(true)})
	binder := &recordingBinder{channel: channel}

	config, err := normalizeConfig(helperConfig(t))
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(dir, ingotabiNone(), ingotabi.None[workspace.Resolver](),
		ingotabi.Some[interaction.ExecutionBinder](binder), discardLogger(), nil, config)
	defer instance.closeAll()

	// A discovery-style connection has no Session.
	connection, err := instance.dial(context.Background(), config.Servers[0], config.MaxResultBytes, dir, cwdSourceDiscovery, execution.Scope{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer connection.Close()
	if _, err := connection.session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if _, err := connection.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "elicit", Arguments: map[string]any{},
	}); err == nil {
		t.Fatal("a connection without a Session must not be able to elicit")
	}
	if len(binder.boundScopes()) != 0 {
		t.Fatalf("no channel should have been bound: %#v", binder.boundScopes())
	}
	if len(channel.requests) != 0 {
		t.Fatalf("the user should never have been asked: %d requests", len(channel.requests))
	}
}

// TestElicitDeclinesWhenTheHostCannotAsk covers a composition without an
// interaction facility.
func TestElicitDeclinesWhenTheHostCannotAsk(t *testing.T) {
	instance := testPlatform(t, Config{})
	result, err := instance.elicit(context.Background(), execution.Scope{SessionID: sessionIDOf("s")},
		ServerConfig{Name: "demo"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
			Mode:            "form",
			Message:         "hi",
			RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
		}})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	if result.Action != elicitDecline {
		t.Fatalf("action = %q", result.Action)
	}
}

// TestElicitURLAsksWithTheFullAddress covers the core promise of URL mode: the
// user sees the exact address before acting, never a shortened one.
func TestElicitURLAsksWithTheFullAddress(t *testing.T) {
	channel := newScriptedChannel(map[string]interaction.Value{
		elicitActionField: interaction.StringValue(elicitAccept),
	})
	instance := elicitPlatform(t, t.TempDir(), &recordingBinder{channel: channel})

	result, err := instance.elicit(context.Background(), execution.Scope{SessionID: sessionIDOf("s")},
		ServerConfig{Name: "deployer"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
			Mode:          "url",
			Message:       "Authorise the deployment.",
			URL:           "https://example.test/authorize?session=abc123",
			ElicitationID: "elic-1",
		}})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	if result.Action != elicitAccept {
		t.Fatalf("action = %q, want accept", result.Action)
	}
	if len(channel.requests) != 1 {
		t.Fatalf("requests = %d", len(channel.requests))
	}
	request := channel.requests[0]
	if !strings.Contains(request.Description, "<https://example.test/authorize?session=abc123>") {
		t.Fatalf("the full address must be shown verbatim: %q", request.Description)
	}
	if !strings.Contains(request.Description, `"deployer"`) {
		t.Fatalf("the form must attribute the server: %q", request.Description)
	}
	if len(request.Fields) != 1 || request.Fields[0].Name != elicitActionField {
		t.Fatalf("fields = %#v", request.Fields)
	}
	if request.Fields[0].Kind != interaction.FieldChoice || len(request.Fields[0].Options) != 2 {
		t.Fatalf("confirmation field = %#v", request.Fields[0])
	}

	// Accept only records consent, so the call is tracked until the server
	// reports completion.
	instance.mu.Lock()
	_, tracked := instance.elicitations["elic-1"]
	instance.mu.Unlock()
	if !tracked {
		t.Fatal("an accepted URL elicitation must stay tracked")
	}
}

func TestElicitURLCancelAndDecline(t *testing.T) {
	cases := map[string]struct {
		answer interaction.Value
		want   string
	}{
		"declines when asked to cancel": {interaction.StringValue(elicitDecline), elicitDecline},
		"cancels when dismissed":        {interaction.StringValue(""), elicitCancel},
		"cancels on an unknown answer":  {interaction.StringValue("maybe"), elicitCancel},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			channel := newScriptedChannel(map[string]interaction.Value{elicitActionField: testCase.answer})
			instance := elicitPlatform(t, t.TempDir(), &recordingBinder{channel: channel})
			result, err := instance.elicit(context.Background(), execution.Scope{SessionID: sessionIDOf("s")},
				ServerConfig{Name: "deployer"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
					Mode: "url", Message: "go", URL: "https://example.test/x", ElicitationID: "elic-2",
				}})
			if err != nil {
				t.Fatalf("elicit: %v", err)
			}
			if result.Action != testCase.want {
				t.Fatalf("action = %q, want %q", result.Action, testCase.want)
			}
			instance.mu.Lock()
			_, tracked := instance.elicitations["elic-2"]
			instance.mu.Unlock()
			if tracked {
				t.Fatal("a refused URL elicitation must not stay tracked")
			}
		})
	}
}

// TestElicitURLRejectsUnsafeAddresses covers the checks the host actually owns:
// the scheme the browser will follow and whether ambient credentials ride along.
func TestElicitURLRejectsUnsafeAddresses(t *testing.T) {
	cases := map[string]string{
		"javascript":       "javascript:alert(1)",
		"data":             "data:text/html,<h1>x</h1>",
		"file":             "file:///etc/passwd",
		"remote plaintext": "http://example.test/authorize",
		"credentials":      "https://user:pass@example.test/x",
		"relative":         "/authorize",
		"empty":            "",
		"angle bracket":    "https://example.test/<x>",
		"whitespace":       "https://example.test/a b",
	}
	for name, address := range cases {
		t.Run(name, func(t *testing.T) {
			channel := newScriptedChannel(nil)
			instance := elicitPlatform(t, t.TempDir(), &recordingBinder{channel: channel})
			result, err := instance.elicit(context.Background(), execution.Scope{SessionID: sessionIDOf("s")},
				ServerConfig{Name: "deployer"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
					Mode: "url", Message: "go", URL: address, ElicitationID: "elic-3",
				}})
			if err != nil {
				t.Fatalf("elicit: %v", err)
			}
			if result.Action != elicitDecline {
				t.Fatalf("action = %q, want decline", result.Action)
			}
			if len(channel.requests) != 0 {
				t.Fatalf("an unusable address must not reach the user: %#v", channel.requests)
			}
		})
	}

	// Loopback plaintext is allowed, matching the rest of the plugin.
	channel := newScriptedChannel(map[string]interaction.Value{elicitActionField: interaction.StringValue(elicitAccept)})
	instance := elicitPlatform(t, t.TempDir(), &recordingBinder{channel: channel})
	if _, err := instance.elicit(context.Background(), execution.Scope{SessionID: sessionIDOf("s")},
		ServerConfig{Name: "deployer"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
			Mode: "url", Message: "go", URL: "http://127.0.0.1:8080/authorize",
		}}); err != nil {
		t.Fatalf("elicit: %v", err)
	}
	if len(channel.requests) != 1 {
		t.Fatal("a loopback address should be presentable")
	}
}

// TestElicitURLEscapesServerMarkup keeps a server from forging a link inside the
// markdown-rendered description.
func TestElicitURLEscapesServerMarkup(t *testing.T) {
	channel := newScriptedChannel(map[string]interaction.Value{
		elicitActionField: interaction.StringValue(elicitAccept),
	})
	instance := elicitPlatform(t, t.TempDir(), &recordingBinder{channel: channel})
	_, err := instance.elicit(context.Background(), execution.Scope{SessionID: sessionIDOf("s")},
		ServerConfig{Name: "deployer"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
			Mode:    "url",
			Message: "[Click here](https://evil.test/steal) to authorise",
			URL:     "https://example.test/authorize",
		}})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	description := channel.requests[0].Description
	if strings.Contains(description, "](https://evil.test") {
		t.Fatalf("server markup survived into the description: %q", description)
	}
	if !strings.Contains(description, "\\[Click here\\]") {
		t.Fatalf("server text should be escaped, not dropped: %q", description)
	}
}

// TestElicitCompleteNotifiesTheSession covers the asynchronous tail: the server
// reports the out-of-band step finished, and the owning Session learns about it.
func TestElicitCompleteNotifiesTheSession(t *testing.T) {
	channel := newScriptedChannel(map[string]interaction.Value{
		elicitActionField: interaction.StringValue(elicitAccept),
	})
	instance := elicitPlatform(t, t.TempDir(), &recordingBinder{channel: channel})
	if _, err := instance.elicit(context.Background(), execution.Scope{SessionID: sessionIDOf("s")},
		ServerConfig{Name: "deployer"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
			Mode: "url", Message: "go", URL: "https://example.test/x", ElicitationID: "elic-4",
		}}); err != nil {
		t.Fatalf("elicit: %v", err)
	}

	instance.completeElicitation(context.Background(), ServerConfig{Name: "deployer"}, "elic-4")
	channel.mu.Lock()
	events := append([]interaction.Event(nil), channel.events...)
	channel.mu.Unlock()
	if len(events) != 1 || events[0].Name != elicitCompleteEvent {
		t.Fatalf("events = %#v", events)
	}
	if !strings.Contains(events[0].Message, `"deployer"`) {
		t.Fatalf("event must attribute the server: %q", events[0].Message)
	}

	// A repeated or unknown identifier is ignored.
	instance.completeElicitation(context.Background(), ServerConfig{Name: "deployer"}, "elic-4")
	instance.completeElicitation(context.Background(), ServerConfig{Name: "deployer"}, "unknown")
	channel.mu.Lock()
	after := len(channel.events)
	channel.mu.Unlock()
	if after != 1 {
		t.Fatalf("unknown or repeated identifiers must be ignored, events = %d", after)
	}
}

// TestElicitDeclinesUnsupportedSchema covers a server that ignores the protocol
// restriction instead of failing the whole call.
func TestElicitDeclinesUnsupportedSchema(t *testing.T) {
	channel := newScriptedChannel(nil)
	instance := elicitPlatform(t, t.TempDir(), &recordingBinder{channel: channel})
	result, err := instance.elicit(context.Background(), execution.Scope{SessionID: sessionIDOf("s")},
		ServerConfig{Name: "demo"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
			Mode:            "form",
			Message:         "nested",
			RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "object"}}},
		}})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	if result.Action != elicitDecline {
		t.Fatalf("action = %q", result.Action)
	}
	if len(channel.requests) != 0 {
		t.Fatal("an unsupported schema must not reach the user")
	}
}

// TestElicitReportsCancellation covers the caller giving up while the user is
// being asked.
func TestElicitReportsCancellation(t *testing.T) {
	channel := newScriptedChannel(nil)
	instance := elicitPlatform(t, t.TempDir(), &recordingBinder{channel: channel})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := instance.elicit(ctx, execution.Scope{SessionID: sessionIDOf("s")},
		ServerConfig{Name: "demo"}, &mcp.ElicitRequest{Params: &mcp.ElicitParams{
			Mode:            "form",
			Message:         "hi",
			RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
		}})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	if result.Action != elicitCancel {
		t.Fatalf("action = %q, want cancel", result.Action)
	}
}

// TestElicitationURLRoundTrip drives URL mode end to end through the protocol's
// input-request path, which is the only shape allowed while a server is serving
// a request on the current protocol version.
func TestElicitationURLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	channel := newScriptedChannel(map[string]interaction.Value{
		elicitActionField: interaction.StringValue(elicitAccept),
	})
	instance := elicitPlatform(t, dir, &recordingBinder{channel: channel})
	defer instance.closeAll()

	entry := &mcpTool{
		platform: instance, server: "helper", wireName: "authorize",
		definition: tool.Definition{Name: "mcp_helper_authorize", Description: "a", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	result, err := entry.Invoke(context.Background(), tool.Invocation{
		Scope: execution.Scope{SessionID: sessionIDOf("session-a")},
		Call:  tool.Call{ID: "a1", Name: "mcp_helper_authorize", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	text, ok := content.TextOnly(result.Content)
	if !ok {
		t.Fatalf("result = %#v", result.Content)
	}
	if !strings.Contains(text, `"action":"accept"`) {
		t.Fatalf("the server should have received consent: %q", text)
	}
	if len(channel.requests) != 1 {
		t.Fatalf("requests = %d", len(channel.requests))
	}
	if !strings.Contains(channel.requests[0].Description, "https://example.test/authorize?session=abc123") {
		t.Fatalf("the address never reached the user: %q", channel.requests[0].Description)
	}
}
