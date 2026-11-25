package toolmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/operation"
)

const (
	secretActionKeep    = "keep"
	secretActionReplace = "replace"
)

// referenceOnlyPattern matches values that consist only of ${NAME} references
// and punctuation. Such a value carries no secret of its own, so it is safe to
// show in the configuration form.
var referenceOnlyPattern = regexp.MustCompile(`^[^A-Za-z0-9]*(\$\{[A-Za-z_][A-Za-z0-9_]*\}[^A-Za-z0-9]*)+$`)

// setupOperation collects this Plugin's configuration through structured
// Interaction, validates it, persists it atomically in the Plugin-owned state
// scope, and publishes a new immutable snapshot to live tool calls.
type setupOperation struct {
	platform *platform
	// exported is the tool set this image exposes, captured at construction.
	exported []string
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:        setupOperationName,
		Description: "Review and update MCP server connections and tool export settings.",
		Group:       setupOperationGroup,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["restart_required","restart_reasons","added","removed","approval_hint","servers"],"properties":{` +
			`"restart_required":{"type":"boolean"},` +
			`"restart_reasons":{"type":"array","items":{"type":"string"}},` +
			`"added":{"type":"array","items":{"type":"string"}},` +
			`"removed":{"type":"array","items":{"type":"string"}},` +
			`"approval_hint":{"type":"array","items":{"type":"string"}},` +
			`"servers":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["name","transport","status","tools","rejected","schema_bytes"],"properties":{"name":{"type":"string"},"transport":{"type":"string"},"status":{"type":"string"},"tools":{"type":"integer"},"rejected":{"type":"integer"},"schema_bytes":{"type":"integer"},"error":{"type":"string"}}}}` +
			`}}`),
	}
}

type setupOutput struct {
	RestartRequired bool           `json:"restart_required"`
	RestartReasons  []string       `json:"restart_reasons"`
	Added           []string       `json:"added"`
	Removed         []string       `json:"removed"`
	ApprovalHint    []string       `json:"approval_hint"`
	Servers         []serverReport `json:"servers"`
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if ctx == nil {
		return operation.Result{}, fmt.Errorf("tool.mcp.config: nil context")
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o == nil || o.platform == nil || strings.TrimSpace(o.platform.stateDir) == "" {
		return operation.Result{}, fmt.Errorf("tool.mcp.config: platform is required: %w", ErrInvalidDependencies)
	}
	if isNil(request.Interaction) {
		return operation.Result{}, fmt.Errorf("tool.mcp.config: interaction channel is required: %w", ErrInvalidConfig)
	}

	current, err := loadConfig(o.platform.stateDir)
	if err != nil {
		return operation.Result{}, err
	}
	currentConfig, err := normalizeConfig(current)
	if err != nil {
		return operation.Result{}, err
	}

	serversDefault := make([]interaction.Value, 0, len(currentConfig.Servers))
	for _, server := range currentConfig.Servers {
		serversDefault = append(serversDefault, serverValue(server))
	}
	serversValue := interaction.ListValue(serversDefault)
	prefixDefault := interaction.StringValue(currentConfig.ToolNamePrefix)
	exposeDefault := interaction.BooleanValue(currentConfig.exposeStatusTool())
	maxResultDefault := interaction.IntegerValue(int64(currentConfig.MaxResultBytes))
	maxToolsDefault := interaction.IntegerValue(int64(currentConfig.MaxToolsTotal))
	refreshDefault := interaction.BooleanValue(true)

	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "MCP servers, tool naming, and export limits. Each server is one connection to an external MCP server.",
		Fields: []interaction.Field{
			{Name: "tool_name_prefix", Label: "Tool name prefix", Description: "Prefix for every exported tool name.", Kind: interaction.FieldString, Required: true, Default: &prefixDefault},
			{Name: "expose_status_tool", Label: "Expose mcp_status", Description: "Export a diagnostic tool that reports MCP connection state.", Kind: interaction.FieldBoolean, Default: &exposeDefault},
			{Name: "max_result_bytes", Label: "Max result bytes", Description: "Upper bound for one mapped tool result. Zero selects the default.", Kind: interaction.FieldInteger, Default: &maxResultDefault},
			{Name: "max_tools_total", Label: "Max exported tools", Description: "Hard limit across all servers; a server that would exceed it is skipped.", Kind: interaction.FieldInteger, Default: &maxToolsDefault},
			{
				Name:        "servers",
				Label:       "Servers",
				Description: "MCP servers to connect. Environment and header values are hidden; choose Replace to submit new ones.",
				Kind:        interaction.FieldList,
				Default:     &serversValue,
				Element:     &interaction.Field{Name: "server", Kind: interaction.FieldObject, Fields: serverFields()},
			},
			{Name: "refresh_now", Label: "Discover tools now", Description: "Connect to the configured servers during this operation.", Kind: interaction.FieldBoolean, Default: &refreshDefault},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}

	candidate := current
	if value, ok := answerString(response, "tool_name_prefix"); ok {
		candidate.ToolNamePrefix = value
	}
	if value, ok := answerBool(response, "expose_status_tool"); ok {
		candidate.ExposeStatusTool = &value
	}
	if value, ok := answerInteger(response, "max_result_bytes"); ok {
		candidate.MaxResultBytes = int(value)
	}
	if value, ok := answerInteger(response, "max_tools_total"); ok {
		candidate.MaxToolsTotal = int(value)
	}
	if values, ok := answerList(response, "servers"); ok {
		servers := make([]ServerConfig, 0, len(values))
		for index, value := range values {
			server, err := parseServerValue(value, currentConfig)
			if err != nil {
				return operation.Result{}, fmt.Errorf("servers[%d]: %w", index, err)
			}
			servers = append(servers, server)
		}
		candidate.Servers = servers
	}

	normalized, err := normalizeConfig(candidate)
	if err != nil {
		return operation.Result{}, err
	}

	// The commit lock covers only the read-check-write cycle. Discovery below
	// performs network I/O and must not hold it.
	if err := o.commitConfig(ctx, current, normalized); err != nil {
		return operation.Result{}, err
	}

	previous, err := loadSnapshot(o.platform.stateDir)
	if err != nil {
		previous = nil
	}

	o.platform.setConfig(normalized)
	o.platform.markStale()

	refreshNow := true
	if value, ok := answerBool(response, "refresh_now"); ok {
		refreshNow = value
	}

	snapshot := previous
	if refreshNow {
		discovered, discoveryErr := discoverAndSave(ctx, o.platform, normalized, previous)
		if discoveryErr != nil {
			if errors.Is(discoveryErr, ErrConfigConflict) || errors.Is(discoveryErr, context.Canceled) ||
				errors.Is(discoveryErr, context.DeadlineExceeded) {
				return operation.Result{}, fmt.Errorf("tool.mcp.config: %w", discoveryErr)
			}
			o.platform.logger.Warn(ctx, "MCP tool snapshot could not be persisted",
				logging.String("error", sanitizeMessage(discoveryErr.Error())))
		}
		if discovered != nil {
			snapshot = discovered
		}
	}
	if snapshot == nil {
		snapshot = &Snapshot{}
	}

	added, removed := diffOrEmpty(previous, snapshot)
	reasons := restartReasons(snapshot, normalized, added, removed, o.exported)
	output, err := json.Marshal(setupOutput{
		RestartRequired: len(reasons) > 0,
		RestartReasons:  reasons,
		Added:           added,
		Removed:         removed,
		ApprovalHint:    approvalHints(snapshot),
		Servers:         buildServerReports(snapshot),
	})
	if err != nil {
		return operation.Result{}, fmt.Errorf("tool.mcp.config: encode output: %w", err)
	}
	return operation.Result{Output: output}, nil
}

// commitConfig persists the validated configuration, rejecting a write when the
// stored configuration changed since the interaction started.
func (o *setupOperation) commitConfig(ctx context.Context, current, next Config) error {
	configCommitMu.Lock()
	defer configCommitMu.Unlock()

	latest, err := loadConfig(o.platform.stateDir)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(latest, current) {
		return ErrConfigConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return saveConfig(o.platform.stateDir, next)
}

// restartReasons explains why a restart is or is not required. An empty result
// means the running image already matches the persisted configuration.
// restartReasons explains why the running image does not match the persisted
// configuration. The authoritative comparison is the tool set this image
// exports, not the file read at invocation start.
func restartReasons(snapshot *Snapshot, config Config, added, removed, exported []string) []string {
	reasons := []string{}
	discovered := snapshot.toolNames()
	if !equalStringSets(exported, discovered) {
		reasons = append(reasons, fmt.Sprintf(
			"the running image exports %d tools; discovery found %d (+%d -%d)",
			len(exported), len(discovered), len(added), len(removed)))
	}
	hash, err := config.configHash()
	if err == nil && snapshot.ConfigHash != "" && snapshot.ConfigHash != hash {
		reasons = append(reasons, "stored tool snapshot was produced by a different configuration")
	}
	return reasons
}

func serverFields() []interaction.Field {
	stringList := func(name, label, description string) interaction.Field {
		return interaction.Field{
			Name:        name,
			Label:       label,
			Description: description,
			Kind:        interaction.FieldList,
			Element:     &interaction.Field{Name: "entry", Kind: interaction.FieldString},
		}
	}
	// Environment and header entries carry credentials, so the host must treat
	// them as sensitive even though the values are already masked here.
	secretList := func(name, label, description string) interaction.Field {
		field := stringList(name, label, description)
		field.Sensitive = true
		return field
	}
	return []interaction.Field{
		{Name: "name", Label: "Name", Description: "Stable server identifier used in tool names.", Kind: interaction.FieldString, Required: true},
		{Name: "transport", Label: "Transport", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{
			{Value: transportStdio, Label: "stdio"},
			{Value: transportHTTP, Label: "Streamable HTTP"},
		}},
		{Name: "enabled", Label: "Enabled", Kind: interaction.FieldBoolean},
		{Name: "command", Label: "Command", Description: "stdio executable, for example npx.", Kind: interaction.FieldString},
		stringList("args", "Arguments", "One command argument per entry."),
		{Name: "cwd", Label: "Working directory", Description: "Absolute path; empty uses the Runtime working directory.", Kind: interaction.FieldString},
		secretList("env", "Environment", "KEY=VALUE entries. A value may reference ${ENV_VAR}."),
		stringList("inherit_env", "Inherited environment", "Host variables forwarded to the server in addition to the safe default set."),
		{Name: "url", Label: "Endpoint URL", Description: "Streamable HTTP endpoint.", Kind: interaction.FieldString},
		secretList("headers", "Headers", "NAME=VALUE entries. A value may reference ${ENV_VAR}."),
		stringList("enabled_tools", "Enabled tools", "Tool name patterns; a trailing * matches a prefix. Defaults to *."),
		stringList("disabled_tools", "Disabled tools", "Patterns removed after enabled_tools is applied."),
		{Name: "secrets_action", Label: "Environment and headers", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{
			{Value: secretActionKeep, Label: "Keep stored values"},
			{Value: secretActionReplace, Label: "Replace with the submitted entries"},
		}},
		{Name: "max_concurrent_calls", Label: "Max concurrent calls", Kind: interaction.FieldInteger},
		{Name: "connect_timeout_seconds", Label: "Connect timeout (s)", Kind: interaction.FieldInteger},
		{Name: "call_timeout_seconds", Label: "Call timeout (s)", Kind: interaction.FieldInteger},
		{Name: "shutdown_timeout_seconds", Label: "Shutdown timeout (s)", Kind: interaction.FieldInteger},
		{Name: "idle_timeout_seconds", Label: "Idle timeout (s)", Description: "Zero keeps the connection open.", Kind: interaction.FieldInteger},
	}
}

func serverValue(server ServerConfig) interaction.Value {
	entries := []interaction.Entry{
		{Name: "name", Value: interaction.StringValue(server.Name)},
		{Name: "transport", Value: interaction.StringValue(server.Transport)},
		{Name: "enabled", Value: interaction.BooleanValue(server.enabled())},
		{Name: "command", Value: interaction.StringValue(server.Command)},
		{Name: "args", Value: interaction.StringsValue(server.Args)},
		{Name: "cwd", Value: interaction.StringValue(server.Cwd)},
		{Name: "env", Value: interaction.StringsValue(displayAssignments(server.Env))},
		{Name: "inherit_env", Value: interaction.StringsValue(server.InheritEnv)},
		{Name: "url", Value: interaction.StringValue(server.URL)},
		{Name: "headers", Value: interaction.StringsValue(displayAssignments(server.Headers))},
		{Name: "enabled_tools", Value: interaction.StringsValue(server.EnabledTools)},
		{Name: "disabled_tools", Value: interaction.StringsValue(server.DisabledTools)},
		{Name: "secrets_action", Value: interaction.StringValue(secretActionKeep)},
		{Name: "max_concurrent_calls", Value: interaction.IntegerValue(int64(server.MaxConcurrentCalls))},
		{Name: "connect_timeout_seconds", Value: interaction.IntegerValue(int64(server.ConnectTimeoutSeconds))},
		{Name: "call_timeout_seconds", Value: interaction.IntegerValue(int64(server.CallTimeoutSeconds))},
		{Name: "shutdown_timeout_seconds", Value: interaction.IntegerValue(int64(server.ShutdownTimeoutSeconds))},
		{Name: "idle_timeout_seconds", Value: interaction.IntegerValue(int64(server.IdleTimeoutSeconds))},
	}
	return interaction.ObjectValue(entries)
}

// displayAssignments renders configured assignments for the form. Literal
// values are reduced to an empty value so a secret is never sent back to the
// host; ${NAME} references are kept because they reveal no secret.
func displayAssignments(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	lines := make([]string, 0, len(values))
	for _, key := range sortedKeys(values) {
		value := values[key]
		if referenceOnlyPattern.MatchString(value) {
			lines = append(lines, key+"="+value)
			continue
		}
		lines = append(lines, key+"=")
	}
	return lines
}

func parseServerValue(value interaction.Value, current Config) (ServerConfig, error) {
	if value.Kind != interaction.ValueObject {
		return ServerConfig{}, fmt.Errorf("server entry must be an object: %w", ErrInvalidConfig)
	}
	entries := make(map[string]interaction.Value, len(value.Entries))
	for _, entry := range value.Entries {
		entries[entry.Name] = entry.Value
	}

	var server ServerConfig
	if name, ok := entryString(entries, "name"); ok {
		server.Name = name
	}
	if transport, ok := entryString(entries, "transport"); ok {
		server.Transport = transport
	}
	if enabled, ok := entryBool(entries, "enabled"); ok {
		server.Enabled = &enabled
	}
	if command, ok := entryString(entries, "command"); ok {
		server.Command = command
	}
	if cwd, ok := entryString(entries, "cwd"); ok {
		server.Cwd = cwd
	}
	if url, ok := entryString(entries, "url"); ok {
		server.URL = url
	}
	if args, ok := entryStrings(entries, "args"); ok {
		server.Args = args
	}
	if inherit, ok := entryStrings(entries, "inherit_env"); ok {
		server.InheritEnv = inherit
	}
	if enabledTools, ok := entryStrings(entries, "enabled_tools"); ok {
		server.EnabledTools = enabledTools
	}
	if disabledTools, ok := entryStrings(entries, "disabled_tools"); ok {
		server.DisabledTools = disabledTools
	}
	if count, ok := entryInteger(entries, "max_concurrent_calls"); ok {
		server.MaxConcurrentCalls = int(count)
	}
	if seconds, ok := entryInteger(entries, "connect_timeout_seconds"); ok {
		server.ConnectTimeoutSeconds = int(seconds)
	}
	if seconds, ok := entryInteger(entries, "call_timeout_seconds"); ok {
		server.CallTimeoutSeconds = int(seconds)
	}
	if seconds, ok := entryInteger(entries, "shutdown_timeout_seconds"); ok {
		server.ShutdownTimeoutSeconds = int(seconds)
	}
	if seconds, ok := entryInteger(entries, "idle_timeout_seconds"); ok {
		server.IdleTimeoutSeconds = int(seconds)
	}

	envLines, _ := entryStrings(entries, "env")
	headerLines, _ := entryStrings(entries, "headers")
	env, err := parseAssignments(envLines, "env")
	if err != nil {
		return ServerConfig{}, err
	}
	headers, err := parseAssignments(headerLines, "headers")
	if err != nil {
		return ServerConfig{}, err
	}

	action, _ := entryString(entries, "secrets_action")
	if action == "" {
		action = secretActionKeep
	}
	switch action {
	case secretActionKeep:
		if previous, ok := current.server(strings.TrimSpace(server.Name)); ok {
			server.Env = previous.Env
			server.Headers = previous.Headers
			break
		}
		// Nothing is stored for this name, so "keep" would otherwise discard
		// what the user just submitted. Values left blank by the masked form are
		// omitted instead of being stored as empty strings.
		server.Env = withoutEmptyValues(env)
		server.Headers = withoutEmptyValues(headers)
	case secretActionReplace:
		server.Env = env
		server.Headers = headers
	default:
		return ServerConfig{}, fmt.Errorf("secrets_action %q is unsupported: %w", action, ErrInvalidConfig)
	}

	// Switching transport must not fail because of values that belong to the
	// other transport; the form shows both field groups at once.
	switch strings.TrimSpace(strings.ToLower(server.Transport)) {
	case transportHTTP:
		server.Command, server.Args, server.Env, server.Cwd, server.InheritEnv = "", nil, nil, "", nil
	case transportStdio, "":
		server.URL, server.Headers = "", nil
	}
	return server, nil
}

// withoutEmptyValues drops entries whose value is empty. The configuration form
// shows stored values as blank, so a blank entry means "unchanged", never
// "replace the secret with an empty string".
func withoutEmptyValues(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		if value == "" {
			continue
		}
		result[key] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// parseAssignments parses KEY=VALUE entries. A missing '=' is an error rather
// than an empty value so a typo cannot silently drop a variable.
func parseAssignments(lines []string, field string) (map[string]string, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s entry %q must be KEY=VALUE: %w", field, line, ErrInvalidConfig)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s entry %q has an empty key: %w", field, line, ErrInvalidConfig)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate %s key %q: %w", field, key, ErrInvalidConfig)
		}
		result[key] = value
	}
	return result, nil
}

func answerValue(response interaction.Response, name string) (interaction.Value, bool) {
	for _, answer := range response.Values {
		if answer.Name == name {
			return answer.Value, true
		}
	}
	return interaction.Value{}, false
}

func answerString(response interaction.Response, name string) (string, bool) {
	value, ok := answerValue(response, name)
	if !ok || value.Kind != interaction.ValueString {
		return "", false
	}
	return value.String, true
}

func answerBool(response interaction.Response, name string) (bool, bool) {
	value, ok := answerValue(response, name)
	if !ok || value.Kind != interaction.ValueBoolean {
		return false, false
	}
	return value.Boolean, true
}

func answerInteger(response interaction.Response, name string) (int64, bool) {
	value, ok := answerValue(response, name)
	if !ok || value.Kind != interaction.ValueInteger {
		return 0, false
	}
	return value.Integer, true
}

func answerList(response interaction.Response, name string) ([]interaction.Value, bool) {
	value, ok := answerValue(response, name)
	if !ok || value.Kind != interaction.ValueList {
		return nil, false
	}
	return value.Items, true
}

func entryValue(entries map[string]interaction.Value, name string) (interaction.Value, bool) {
	value, ok := entries[name]
	return value, ok
}

func entryString(entries map[string]interaction.Value, name string) (string, bool) {
	value, ok := entryValue(entries, name)
	if !ok || value.Kind != interaction.ValueString {
		return "", false
	}
	return value.String, true
}

func entryBool(entries map[string]interaction.Value, name string) (bool, bool) {
	value, ok := entryValue(entries, name)
	if !ok || value.Kind != interaction.ValueBoolean {
		return false, false
	}
	return value.Boolean, true
}

func entryInteger(entries map[string]interaction.Value, name string) (int64, bool) {
	value, ok := entryValue(entries, name)
	if !ok || value.Kind != interaction.ValueInteger {
		return 0, false
	}
	return value.Integer, true
}

// entryStrings accepts either a repeated string list or a single string, which
// is how hosts commonly return a one-element list.
func entryStrings(entries map[string]interaction.Value, name string) ([]string, bool) {
	value, ok := entryValue(entries, name)
	if !ok {
		return nil, false
	}
	switch value.Kind {
	case interaction.ValueStrings:
		return value.Strings, true
	case interaction.ValueString:
		if value.String == "" {
			return nil, true
		}
		return []string{value.String}, true
	case interaction.ValueList:
		result := make([]string, 0, len(value.Items))
		for _, item := range value.Items {
			if item.Kind != interaction.ValueString {
				return nil, false
			}
			result = append(result, item.String)
		}
		return result, true
	default:
		return nil, false
	}
}
