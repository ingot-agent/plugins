package toolmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// elicitRequestName is the stable protocol identity of an elicitation
	// request, matching the convention used by the other "ask the user" tools.
	elicitRequestName = "mcp_elicit"

	maxElicitMessageBytes = 2 << 10
	maxElicitFields       = 16
	maxElicitOptions      = 64
	maxElicitContentBytes = 64 << 10
	maxElicitStringBytes  = 8 << 10
	maxElicitURLBytes     = 2 << 10

	// elicitActionField is the confirmation control of a URL elicitation.
	elicitActionField = "action"
	// elicitCompleteEvent is emitted when a server reports that an out-of-band
	// interaction finished.
	elicitCompleteEvent = "mcp_elicit_complete"
)

// Elicitation actions defined by the protocol.
const (
	elicitAccept  = "accept"
	elicitDecline = "decline"
	elicitCancel  = "cancel"
)

// elicitServices reports whether this plugin can ask the user anything. A
// composition without an interaction capability never advertises the
// capability, so a conforming server does not send elicitation requests at all.
func (p *platform) elicitServices() bool {
	return p.interaction.Valid && p.interaction.Value != nil
}

// elicit forwards one server-initiated elicitation to the Session that owns this
// connection. Each connection belongs to exactly one Session, which is what
// makes the target unambiguous.
//
// Every path returns a protocol action rather than an error when the request is
// merely unsupported or the user declined: declining is a business outcome the
// server is expected to handle, not a transport failure.
func (p *platform) elicit(
	ctx context.Context,
	scope execution.Scope,
	server ServerConfig,
	request *mcp.ElicitRequest,
) (*mcp.ElicitResult, error) {
	if request == nil || request.Params == nil {
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	}
	params := request.Params

	mode := params.Mode
	if mode == "" {
		mode = "form"
	}
	switch mode {
	case "url":
		return p.elicitURL(ctx, scope, server, params)
	case "form":
	default:
		p.logger.Warn(ctx, "declined an unsupported elicitation mode",
			logging.String("server", server.Name), logging.String("mode", mode))
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	}

	fields, err := elicitFields(params.RequestedSchema)
	if err != nil {
		p.logger.Warn(ctx, "declined an elicitation with an unsupported schema",
			logging.String("server", server.Name), logging.String("error", sanitizeMessage(err.Error())))
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	}
	if len(fields) == 0 {
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	}

	channel, err := p.elicitChannel(scope)
	if err != nil {
		// No host facility, or no Session to route to: the user was never asked.
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	}

	if err := ctx.Err(); err != nil {
		return &mcp.ElicitResult{Action: elicitCancel}, nil
	}
	response, err := channel.Request(ctx, interaction.Request{
		Name:        elicitRequestName,
		Description: elicitDescription(server.Name, params.Message),
		Fields:      fields,
	})
	// A host may answer successfully even though the call was abandoned; the
	// cancellation is authoritative.
	if ctx.Err() != nil {
		return &mcp.ElicitResult{Action: elicitCancel}, nil
	}
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return &mcp.ElicitResult{Action: elicitCancel}, nil
		case errors.Is(err, interaction.ErrUnavailable):
			return &mcp.ElicitResult{Action: elicitDecline}, nil
		default:
			// A host failure is reported to the server as an error so it is
			// visible, rather than disguised as a user refusal.
			return nil, fmt.Errorf("elicit input for server %q: %w", server.Name, err)
		}
	}

	content, ok := elicitContent(fields, response)
	if !ok {
		// An incomplete or ill-typed answer cannot satisfy the schema, and the
		// SDK would reject a partial accept, so treat it as a refusal.
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	}
	return &mcp.ElicitResult{Action: elicitAccept, Content: content}, nil
}

// elicitURL asks the user to complete a step out of band at a server-chosen
// address.
//
// The specification scopes the client's responsibility to presenting the
// address together with its context, so this implementation does exactly that:
// the full URL is shown as a link, never shortened and never opened
// automatically, and the user states explicitly whether they completed the step.
// Whatever the address turns out to be, the user can see it before acting.
func (p *platform) elicitURL(
	ctx context.Context,
	scope execution.Scope,
	server ServerConfig,
	params *mcp.ElicitParams,
) (*mcp.ElicitResult, error) {
	target, err := validateElicitURL(params.URL)
	if err != nil {
		// A URL the host refuses to present is reported as a refusal rather than
		// as a transport failure, so the server can fall back.
		p.logger.Warn(ctx, "declined an unusable elicitation URL",
			logging.String("server", server.Name), logging.String("error", sanitizeMessage(err.Error())))
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	}

	channel, err := p.elicitChannel(scope)
	if err != nil {
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	}
	if err := ctx.Err(); err != nil {
		return &mcp.ElicitResult{Action: elicitCancel}, nil
	}

	if params.ElicitationID != "" {
		p.trackElicitation(params.ElicitationID, scope)
	}

	action := interaction.StringValue("")
	response, err := channel.Request(ctx, interaction.Request{
		Name:        elicitRequestName,
		Description: elicitURLDescription(server.Name, params.Message, target),
		Fields: []interaction.Field{{
			Name:     elicitActionField,
			Label:    "Continue",
			Kind:     interaction.FieldChoice,
			Required: true,
			Default:  &action,
			Options: []interaction.Option{
				{Value: elicitAccept, Label: "I completed this step", Description: "The listed address was opened and the step finished."},
				{Value: elicitDecline, Label: "Cancel", Description: "Do not continue with this request."},
			},
		}},
	})
	if ctx.Err() != nil {
		p.forgetElicitation(params.ElicitationID)
		return &mcp.ElicitResult{Action: elicitCancel}, nil
	}
	if err != nil {
		p.forgetElicitation(params.ElicitationID)
		if errors.Is(err, interaction.ErrUnavailable) {
			return &mcp.ElicitResult{Action: elicitDecline}, nil
		}
		return nil, fmt.Errorf("elicit an address for server %q: %w", server.Name, err)
	}

	answer, ok := answerValue(response, elicitActionField)
	switch {
	case ok && answer.Kind == interaction.ValueString && answer.String == elicitAccept:
		// Per the specification, accept means the user consented to the
		// interaction, not that it finished. Completion arrives as a separate
		// notification, which is surfaced to the Session when it arrives.
		return &mcp.ElicitResult{Action: elicitAccept}, nil
	case ok && answer.Kind == interaction.ValueString && answer.String == elicitDecline:
		p.forgetElicitation(params.ElicitationID)
		return &mcp.ElicitResult{Action: elicitDecline}, nil
	default:
		p.forgetElicitation(params.ElicitationID)
		return &mcp.ElicitResult{Action: elicitCancel}, nil
	}
}

// elicitURLDescription renders the address so the user can inspect it. The
// address itself is emitted as plugin markup, while the server's message is
// escaped.
func elicitURLDescription(server, message string, target *url.URL) string {
	context := sanitizeElicitMessage(message)
	address := target.String()
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("MCP server %q is asking you to open this address:", server))
	builder.WriteString("\n\n")
	builder.WriteString("<" + address + ">")
	if context != "" {
		builder.WriteString("\n\n")
		builder.WriteString("Server message: " + escapeMarkdown(context))
	}
	return builder.String()
}

// validateElicitURL enforces what the host can present safely: a real address,
// no embedded credentials, and an encrypted scheme except on loopback.
func validateElicitURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url is required for url elicitation")
	}
	if len(raw) > maxElicitURLBytes {
		return nil, fmt.Errorf("url exceeds %d bytes", maxElicitURLBytes)
	}
	// A markdown autolink cannot carry these characters, and neither can a
	// trustworthy address.
	for _, r := range raw {
		if r == '<' || r == '>' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return nil, errors.New("url contains characters that cannot be displayed safely")
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("url is not valid: %w", err)
	}
	if parsed.Host == "" {
		return nil, errors.New("url must be absolute")
	}
	if parsed.User != nil {
		// Credentials in the address are never legitimate here and train the
		// user to accept them.
		return nil, errors.New("url must not embed credentials")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return nil, fmt.Errorf("plaintext http is only allowed for loopback hosts")
		}
	default:
		return nil, fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}
	return parsed, nil
}

// trackElicitation records an in-flight out-of-band interaction so a completion
// notification can be attributed to its Session.
func (p *platform) trackElicitation(id string, scope execution.Scope) {
	p.mu.Lock()
	if p.elicitations == nil {
		p.elicitations = make(map[string]execution.Scope)
	}
	p.elicitations[id] = scope
	p.mu.Unlock()
}

func (p *platform) forgetElicitation(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	delete(p.elicitations, id)
	p.mu.Unlock()
}

// completeElicitation reports a finished out-of-band interaction to the Session
// that started it. Unknown or already-completed identifiers are ignored, as the
// specification requires.
func (p *platform) completeElicitation(ctx context.Context, server ServerConfig, id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	scope, ok := p.elicitations[id]
	delete(p.elicitations, id)
	p.mu.Unlock()
	if !ok {
		return
	}
	channel, err := p.elicitChannel(scope)
	if err != nil {
		return
	}
	_ = channel.Emit(ctx, interaction.Event{
		Name:    elicitCompleteEvent,
		Level:   interaction.LevelInfo,
		Message: fmt.Sprintf("MCP server %q reports the requested step is complete.", server.Name),
	})
}

// elicitChannel binds the interaction channel of the owning Session.
func (p *platform) elicitChannel(scope execution.Scope) (interaction.Channel, error) {
	if !p.elicitServices() {
		return nil, interaction.ErrUnavailable
	}
	if strings.TrimSpace(string(scope.SessionID)) == "" {
		return nil, interaction.ErrUnavailable
	}
	channel, err := p.interaction.Value.Bind(scope)
	if err != nil {
		return nil, err
	}
	if isNil(channel) {
		return nil, interaction.ErrUnavailable
	}
	return channel, nil
}

// elicitDescription attributes the question to its origin. The message text is
// server-controlled, so it is flattened, bounded, and stripped of markdown
// syntax before display: the host renders descriptions as markdown, and
// unescaped server text could forge a link or imitate host interface text.
func elicitDescription(server, message string) string {
	message = sanitizeElicitMessage(message)
	if message == "" {
		return fmt.Sprintf("MCP server %q is requesting input.", server)
	}
	return fmt.Sprintf("MCP server %q is requesting input: %s", server, escapeMarkdown(message))
}

// sanitizeElicitMessage flattens and bounds server-controlled text.
func sanitizeElicitMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxElicitMessageBytes {
		message = truncateAtRuneBoundary(message, maxElicitMessageBytes) + "…"
	}
	return message
}

// markdownSpecial lists the characters that can change how markdown renders.
const markdownSpecial = "\\`*_{}[]()#+-.!|<>~"

// escapeMarkdown neutralizes markdown syntax in untrusted text. It is applied
// to server-controlled text only; markup this plugin emits is appended after
// escaping.
func escapeMarkdown(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if strings.ContainsRune(markdownSpecial, r) {
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func truncateAtRuneBoundary(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// elicitFields converts the restricted elicitation schema into interaction
// fields. The protocol allows only a flat object of primitive properties, so any
// other construct is refused instead of being approximated.
func elicitFields(raw any) ([]interaction.Field, error) {
	if raw == nil {
		return nil, errors.New("requestedSchema is required for form elicitation")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode requested schema: %w", err)
	}
	var document struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode requested schema: %w", err)
	}
	if document.Type != "" && document.Type != "object" {
		return nil, fmt.Errorf("requested schema must be an object, found %q", document.Type)
	}
	if len(document.Properties) == 0 {
		return nil, errors.New("requested schema declares no properties")
	}
	if len(document.Properties) > maxElicitFields {
		return nil, fmt.Errorf("requested schema declares %d properties, more than the %d supported",
			len(document.Properties), maxElicitFields)
	}

	required := make(map[string]bool, len(document.Required))
	for _, name := range document.Required {
		required[name] = true
	}

	// Iterate in a stable order so the rendered form does not shuffle between
	// identical requests.
	names := make([]string, 0, len(document.Properties))
	for name := range document.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	fields := make([]interaction.Field, 0, len(names))
	for _, name := range names {
		field, err := elicitField(name, document.Properties[name], required[name])
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

// elicitField converts one property of the restricted schema.
func elicitField(name string, raw json.RawMessage, required bool) (interaction.Field, error) {
	var property struct {
		Type        string          `json:"type"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Format      string          `json:"format"`
		Enum        []string        `json:"enum"`
		EnumNames   []string        `json:"enumNames"`
		Minimum     *json.Number    `json:"minimum"`
		Maximum     *json.Number    `json:"maximum"`
		MinLength   *json.Number    `json:"minLength"`
		MaxLength   *json.Number    `json:"maxLength"`
		MinItems    *json.Number    `json:"minItems"`
		MaxItems    *json.Number    `json:"maxItems"`
		Default     json.RawMessage `json:"default"`
		Items       *struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&property); err != nil {
		return interaction.Field{}, fmt.Errorf("decode property %q: %w", name, err)
	}

	field := interaction.Field{
		Name:        name,
		Label:       strings.TrimSpace(property.Title),
		Description: strings.TrimSpace(property.Description),
		Required:    required,
	}
	if field.Label == "" {
		field.Label = name
	}

	// Format "password" is not part of the restricted schema, but servers use it
	// to mark a value that must not be displayed afterwards.
	if property.Format == "password" {
		field.Sensitive = true
	}

	constraints := elicitationConstraints(property.Format, property.Minimum, property.Maximum,
		property.MinLength, property.MaxLength, property.MinItems, property.MaxItems)
	appendConstraint := func(text string) {
		if text == "" {
			return
		}
		if field.Description == "" {
			field.Description = text
			return
		}
		field.Description += " " + text
	}

	switch property.Type {
	case "string":
		if len(property.Enum) > 0 {
			if len(property.Enum) > maxElicitOptions {
				return interaction.Field{}, fmt.Errorf("property %q declares %d enum values, more than the %d supported",
					name, len(property.Enum), maxElicitOptions)
			}
			field.Kind = interaction.FieldChoice
			field.Options = elicitOptions(property.Enum, property.EnumNames)
			if value, ok := elicitDefaultString(property.Default); ok {
				field.Default = &interaction.Value{Kind: interaction.ValueString, String: value}
			}
			appendConstraint(constraints)
			return field, nil
		}
		field.Kind = interaction.FieldString
		if value, ok := elicitDefaultString(property.Default); ok {
			field.Default = &interaction.Value{Kind: interaction.ValueString, String: value}
		}
		appendConstraint(constraints)
		return field, nil
	case "integer":
		field.Kind = interaction.FieldInteger
		if value, ok := elicitDefaultInteger(property.Default); ok {
			field.Default = &interaction.Value{Kind: interaction.ValueInteger, Integer: value}
		}
		appendConstraint(constraints)
		return field, nil
	case "number":
		field.Kind = interaction.FieldNumber
		if value, ok := elicitDefaultNumber(property.Default); ok {
			field.Default = &interaction.Value{Kind: interaction.ValueNumber, Number: value}
		}
		appendConstraint(constraints)
		return field, nil
	case "boolean":
		field.Kind = interaction.FieldBoolean
		if value, ok := elicitDefaultBoolean(property.Default); ok {
			field.Default = &interaction.Value{Kind: interaction.ValueBoolean, Boolean: value}
		}
		return field, nil
	case "array":
		if property.Items == nil || len(property.Items.Enum) == 0 {
			return interaction.Field{}, fmt.Errorf(
				"property %q: only arrays of enumerated strings are supported", name)
		}
		if len(property.Items.Enum) > maxElicitOptions {
			return interaction.Field{}, fmt.Errorf("property %q declares %d enum values, more than the %d supported",
				name, len(property.Items.Enum), maxElicitOptions)
		}
		field.Kind = interaction.FieldMultiChoice
		field.Options = elicitOptions(property.Items.Enum, nil)
		if values, ok := elicitDefaultStrings(property.Default); ok {
			field.Default = &interaction.Value{Kind: interaction.ValueStrings, Strings: values}
		}
		appendConstraint(constraints)
		return field, nil
	default:
		return interaction.Field{}, fmt.Errorf(
			"property %q has unsupported type %q; only string, integer, number, boolean and enumerated arrays are supported",
			name, property.Type)
	}
}

// elicitationConstraints renders the parts of the schema the interaction
// contract cannot express directly, so the user still sees the expectation.
func elicitationConstraints(
	format string,
	minimum, maximum, minLength, maxLength, minItems, maxItems *json.Number,
) string {
	var parts []string
	if format != "" && format != "password" {
		parts = append(parts, fmt.Sprintf("Format: %s.", format))
	}
	if minimum != nil {
		parts = append(parts, fmt.Sprintf("Minimum: %s.", minimum.String()))
	}
	if maximum != nil {
		parts = append(parts, fmt.Sprintf("Maximum: %s.", maximum.String()))
	}
	if minLength != nil {
		parts = append(parts, fmt.Sprintf("At least %s characters.", minLength.String()))
	}
	if maxLength != nil {
		parts = append(parts, fmt.Sprintf("At most %s characters.", maxLength.String()))
	}
	if minItems != nil {
		parts = append(parts, fmt.Sprintf("Select at least %s.", minItems.String()))
	}
	if maxItems != nil {
		parts = append(parts, fmt.Sprintf("Select at most %s.", maxItems.String()))
	}
	return strings.Join(parts, " ")
}

func elicitOptions(values, labels []string) []interaction.Option {
	options := make([]interaction.Option, 0, len(values))
	for index, value := range values {
		option := interaction.Option{Value: value, Label: value}
		if index < len(labels) && strings.TrimSpace(labels[index]) != "" {
			option.Label = labels[index]
		}
		options = append(options, option)
	}
	return options
}

func elicitDefaultString(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func elicitDefaultInteger(raw json.RawMessage) (int64, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false
	}
	var value json.Number
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	parsed, err := value.Int64()
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func elicitDefaultNumber(raw json.RawMessage) (float64, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false
	}
	var value json.Number
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	parsed, err := value.Float64()
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func elicitDefaultBoolean(raw json.RawMessage) (bool, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func elicitDefaultStrings(raw json.RawMessage) ([]string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	return values, true
}

// elicitContent converts the collected answers into the schema-shaped map the
// server expects. It reports false when a required answer is missing or a value
// cannot satisfy its declared type, because a partial accept would be rejected
// by the protocol layer anyway.
func elicitContent(fields []interaction.Field, response interaction.Response) (map[string]any, bool) {
	answers := make(map[string]interaction.Value, len(response.Values))
	for _, answer := range response.Values {
		if _, duplicate := answers[answer.Name]; duplicate {
			return nil, false
		}
		answers[answer.Name] = answer.Value
	}

	content := make(map[string]any, len(fields))
	total := 0
	for _, field := range fields {
		value, present := answers[field.Name]
		if !present {
			if field.Required {
				return nil, false
			}
			continue
		}
		converted, ok := elicitValue(field, value)
		if !ok {
			if field.Required {
				return nil, false
			}
			continue
		}
		encoded, err := json.Marshal(converted)
		if err != nil {
			return nil, false
		}
		total += len(encoded)
		if total > maxElicitContentBytes {
			return nil, false
		}
		content[field.Name] = converted
	}
	if len(content) == 0 {
		return nil, false
	}
	return content, true
}

// elicitValue converts one answer to the Go type matching its field kind.
func elicitValue(field interaction.Field, value interaction.Value) (any, bool) {
	switch field.Kind {
	case interaction.FieldString:
		if value.Kind != interaction.ValueString {
			return nil, false
		}
		if len(value.String) > maxElicitStringBytes {
			return nil, false
		}
		return value.String, true
	case interaction.FieldChoice:
		if value.Kind != interaction.ValueString {
			return nil, false
		}
		if !hasOption(field.Options, value.String) {
			return nil, false
		}
		return value.String, true
	case interaction.FieldMultiChoice:
		selected, ok := value.Strings, value.Kind == interaction.ValueStrings
		if !ok && value.Kind == interaction.ValueList {
			selected = make([]string, 0, len(value.Items))
			for _, item := range value.Items {
				if item.Kind != interaction.ValueString {
					return nil, false
				}
				selected = append(selected, item.String)
			}
			ok = true
		}
		if !ok {
			return nil, false
		}
		result := make([]any, 0, len(selected))
		for _, choice := range selected {
			if !hasOption(field.Options, choice) {
				return nil, false
			}
			result = append(result, choice)
		}
		return result, true
	case interaction.FieldInteger:
		switch value.Kind {
		case interaction.ValueInteger:
			return value.Integer, true
		case interaction.ValueNumber:
			if value.Number != float64(int64(value.Number)) {
				return nil, false
			}
			return int64(value.Number), true
		default:
			return nil, false
		}
	case interaction.FieldNumber:
		switch value.Kind {
		case interaction.ValueNumber:
			return value.Number, true
		case interaction.ValueInteger:
			return float64(value.Integer), true
		default:
			return nil, false
		}
	case interaction.FieldBoolean:
		if value.Kind != interaction.ValueBoolean {
			return nil, false
		}
		return value.Boolean, true
	default:
		return nil, false
	}
}

func hasOption(options []interaction.Option, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}
