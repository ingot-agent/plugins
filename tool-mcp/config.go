package toolmcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

const (
	// configFileName is the Plugin-owned configuration file inside this
	// Plugin's Runtime state scope. The Runtime never reads or decodes it.
	configFileName = "config.toml"

	configByteLimit = 1 << 20

	defaultToolNamePrefix       = "mcp"
	defaultMaxResultBytes       = 256 << 10
	defaultMaxToolsTotal        = 128
	defaultMaxConcurrentCalls   = 4
	defaultConnectTimeoutSecond = 30
	defaultCallTimeoutSecond    = 120
	defaultShutdownTimeSecond   = 5

	maxToolNameBytes   = 64
	maxServers         = 32
	maxToolsPerServer  = 512
	maxResultByteLimit = 16 << 20
	maxConfigTimeout   = 3600
)

var (
	// ErrInvalidDependencies indicates missing construction dependencies.
	ErrInvalidDependencies = errors.New("invalid tool.mcp dependencies")
	// ErrInvalidConfig indicates malformed or invalid plugin configuration.
	ErrInvalidConfig = errors.New("invalid tool.mcp configuration")
	// ErrInvalidArguments indicates malformed tool arguments.
	ErrInvalidArguments = errors.New("invalid tool.mcp arguments")
	// ErrConfigConflict indicates that persisted configuration changed while an
	// interactive update was in progress.
	ErrConfigConflict = errors.New("tool.mcp configuration changed during interaction")
)

var (
	serverNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	toolPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	envNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envRefPattern     = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// Config is this Plugin's persisted configuration. It is a private contract
// between this module and the state file; the Runtime never interprets it.
type Config struct {
	SnapshotVersion  int            `toml:"snapshot_version"`
	ToolNamePrefix   string         `toml:"tool_name_prefix"`
	ExposeStatusTool *bool          `toml:"expose_status_tool"`
	MaxResultBytes   int            `toml:"max_result_bytes"`
	MaxToolsTotal    int            `toml:"max_tools_total"`
	Servers          []ServerConfig `toml:"servers"`
}

// ServerConfig configures one MCP server connection.
type ServerConfig struct {
	Name      string `toml:"name"`
	Transport string `toml:"transport"`
	Enabled   *bool  `toml:"enabled"`

	// stdio transport.
	Command    string            `toml:"command"`
	Args       []string          `toml:"args"`
	Env        map[string]string `toml:"env"`
	Cwd        string            `toml:"cwd"`
	InheritEnv []string          `toml:"inherit_env"`

	// Streamable HTTP transport.
	URL     string            `toml:"url"`
	Headers map[string]string `toml:"headers"`

	EnabledTools  []string `toml:"enabled_tools"`
	DisabledTools []string `toml:"disabled_tools"`

	MaxConcurrentCalls     int `toml:"max_concurrent_calls"`
	ConnectTimeoutSeconds  int `toml:"connect_timeout_seconds"`
	CallTimeoutSeconds     int `toml:"call_timeout_seconds"`
	ShutdownTimeoutSeconds int `toml:"shutdown_timeout_seconds"`
	IdleTimeoutSeconds     int `toml:"idle_timeout_seconds"`
}

const (
	transportStdio = "stdio"
	transportHTTP  = "http"
)

// configCommitMu serializes configuration persistence so that two concurrent
// Operations cannot interleave a read-modify-write cycle.
var configCommitMu sync.Mutex

// enabled reports whether the server participates in discovery and calls.
func (s ServerConfig) enabled() bool { return s.Enabled == nil || *s.Enabled }

func (c Config) exposeStatusTool() bool {
	return c.ExposeStatusTool == nil || *c.ExposeStatusTool
}

// server returns the configuration for name.
func (c Config) server(name string) (ServerConfig, bool) {
	for _, server := range c.Servers {
		if server.Name == name {
			return server, true
		}
	}
	return ServerConfig{}, false
}

// enabledServers returns the enabled servers in canonical (name) order.
func (c Config) enabledServers() []ServerConfig {
	result := make([]ServerConfig, 0, len(c.Servers))
	for _, server := range c.Servers {
		if server.enabled() {
			result = append(result, server)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// loadConfig reads the Plugin-owned configuration. A missing file is the
// normal unconfigured state; any other read or decode failure is reported
// instead of silently discarding persisted settings.
func loadConfig(scope string) (Config, error) {
	var config Config
	if strings.TrimSpace(scope) == "" {
		return config, nil
	}
	path := filepath.Join(scope, configFileName)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, fmt.Errorf("read plugin config: %w", err)
	}
	if info.Size() > configByteLimit {
		return config, fmt.Errorf("config file exceeds %d bytes: %w", configByteLimit, ErrInvalidConfig)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("read plugin config: %w", err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode plugin config: %w: %w", ErrInvalidConfig, err)
	}
	return config, nil
}

// saveConfig atomically writes the Plugin-owned configuration with owner-only
// permissions. The Host never participates.
func saveConfig(scope string, config Config) error {
	if strings.TrimSpace(scope) == "" {
		return fmt.Errorf("state scope is required: %w", ErrInvalidConfig)
	}
	if err := os.MkdirAll(scope, 0o700); err != nil {
		return fmt.Errorf("create state scope: %w", err)
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode plugin config: %w", err)
	}
	temporary, err := os.CreateTemp(scope, configFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(scope, configFileName)); err != nil {
		return fmt.Errorf("replace plugin config: %w", err)
	}
	return nil
}

// normalizeConfig validates config and returns a canonical copy with every
// default applied. Servers are sorted by name so that repeated normalization
// and hashing are stable.
func normalizeConfig(config Config) (Config, error) {
	result := config

	switch result.SnapshotVersion {
	case 0:
		result.SnapshotVersion = 1
	case 1:
	default:
		return Config{}, fmt.Errorf("unsupported snapshot_version %d: %w", result.SnapshotVersion, ErrInvalidConfig)
	}

	result.ToolNamePrefix = strings.TrimSpace(result.ToolNamePrefix)
	if result.ToolNamePrefix == "" {
		result.ToolNamePrefix = defaultToolNamePrefix
	}
	if !toolPrefixPattern.MatchString(result.ToolNamePrefix) {
		return Config{}, fmt.Errorf("tool_name_prefix must match %s: %w", toolPrefixPattern, ErrInvalidConfig)
	}

	if result.ExposeStatusTool == nil {
		value := true
		result.ExposeStatusTool = &value
	}

	if result.MaxResultBytes == 0 {
		result.MaxResultBytes = defaultMaxResultBytes
	}
	if result.MaxResultBytes < 1024 || result.MaxResultBytes > maxResultByteLimit {
		return Config{}, fmt.Errorf("max_result_bytes must be within [1024, %d]: %w", maxResultByteLimit, ErrInvalidConfig)
	}

	if result.MaxToolsTotal == 0 {
		result.MaxToolsTotal = defaultMaxToolsTotal
	}
	if result.MaxToolsTotal < 1 || result.MaxToolsTotal > 1024 {
		return Config{}, fmt.Errorf("max_tools_total must be within [1, 1024]: %w", ErrInvalidConfig)
	}

	if len(result.Servers) > maxServers {
		return Config{}, fmt.Errorf("at most %d servers are supported: %w", maxServers, ErrInvalidConfig)
	}

	seen := make(map[string]bool, len(result.Servers))
	for index := range result.Servers {
		server, err := normalizeServer(result.Servers[index])
		if err != nil {
			return Config{}, err
		}
		if seen[server.Name] {
			return Config{}, fmt.Errorf("duplicate server name %q: %w", server.Name, ErrInvalidConfig)
		}
		seen[server.Name] = true
		result.Servers[index] = server
	}
	sort.Slice(result.Servers, func(i, j int) bool {
		return result.Servers[i].Name < result.Servers[j].Name
	})
	return result, nil
}

func normalizeServer(server ServerConfig) (ServerConfig, error) {
	result := server
	result.Name = strings.TrimSpace(result.Name)
	if !serverNamePattern.MatchString(result.Name) {
		return ServerConfig{}, fmt.Errorf("server name %q must match %s: %w", result.Name, serverNamePattern, ErrInvalidConfig)
	}

	result.Transport = strings.TrimSpace(strings.ToLower(result.Transport))
	if result.Transport == "" {
		result.Transport = transportStdio
	}
	switch result.Transport {
	case transportStdio:
	case transportHTTP:
	default:
		return ServerConfig{}, fmt.Errorf("server %q: unsupported transport %q: %w", result.Name, result.Transport, ErrInvalidConfig)
	}

	if result.Enabled == nil {
		value := true
		result.Enabled = &value
	}

	if err := normalizeLimits(&result); err != nil {
		return ServerConfig{}, err
	}
	if err := normalizeFilters(&result); err != nil {
		return ServerConfig{}, err
	}

	switch result.Transport {
	case transportStdio:
		if err := normalizeStdio(&result); err != nil {
			return ServerConfig{}, err
		}
		if result.URL != "" || len(result.Headers) != 0 {
			return ServerConfig{}, fmt.Errorf("server %q: url and headers are only valid for the http transport: %w", result.Name, ErrInvalidConfig)
		}
	case transportHTTP:
		if err := normalizeHTTP(&result); err != nil {
			return ServerConfig{}, err
		}
		if result.Command != "" || len(result.Args) != 0 || len(result.Env) != 0 || result.Cwd != "" || len(result.InheritEnv) != 0 {
			return ServerConfig{}, fmt.Errorf("server %q: command, args, env, cwd and inherit_env are only valid for the stdio transport: %w", result.Name, ErrInvalidConfig)
		}
	}
	return result, nil
}

func normalizeLimits(server *ServerConfig) error {
	if server.MaxConcurrentCalls == 0 {
		server.MaxConcurrentCalls = defaultMaxConcurrentCalls
	}
	if server.MaxConcurrentCalls < 1 || server.MaxConcurrentCalls > 64 {
		return fmt.Errorf("server %q: max_concurrent_calls must be within [1, 64]: %w", server.Name, ErrInvalidConfig)
	}
	if server.ConnectTimeoutSeconds == 0 {
		server.ConnectTimeoutSeconds = defaultConnectTimeoutSecond
	}
	if server.ConnectTimeoutSeconds < 1 || server.ConnectTimeoutSeconds > maxConfigTimeout {
		return fmt.Errorf("server %q: connect_timeout_seconds must be within [1, %d]: %w", server.Name, maxConfigTimeout, ErrInvalidConfig)
	}
	if server.CallTimeoutSeconds == 0 {
		server.CallTimeoutSeconds = defaultCallTimeoutSecond
	}
	if server.CallTimeoutSeconds < 1 || server.CallTimeoutSeconds > maxConfigTimeout {
		return fmt.Errorf("server %q: call_timeout_seconds must be within [1, %d]: %w", server.Name, maxConfigTimeout, ErrInvalidConfig)
	}
	if server.ShutdownTimeoutSeconds == 0 {
		server.ShutdownTimeoutSeconds = defaultShutdownTimeSecond
	}
	if server.ShutdownTimeoutSeconds < 1 || server.ShutdownTimeoutSeconds > 60 {
		return fmt.Errorf("server %q: shutdown_timeout_seconds must be within [1, 60]: %w", server.Name, ErrInvalidConfig)
	}
	if server.IdleTimeoutSeconds < 0 || server.IdleTimeoutSeconds > 86400 {
		return fmt.Errorf("server %q: idle_timeout_seconds must be within [0, 86400]: %w", server.Name, ErrInvalidConfig)
	}
	return nil
}

func normalizeFilters(server *ServerConfig) error {
	server.EnabledTools = compactStrings(server.EnabledTools)
	server.DisabledTools = compactStrings(server.DisabledTools)
	if len(server.EnabledTools) == 0 {
		server.EnabledTools = []string{"*"}
	}
	for _, pattern := range server.EnabledTools {
		if !validToolFilter(pattern) {
			return fmt.Errorf("server %q: invalid enabled_tools entry %q: %w", server.Name, pattern, ErrInvalidConfig)
		}
	}
	for _, pattern := range server.DisabledTools {
		if !validToolFilter(pattern) {
			return fmt.Errorf("server %q: invalid disabled_tools entry %q: %w", server.Name, pattern, ErrInvalidConfig)
		}
	}
	return nil
}

func normalizeStdio(server *ServerConfig) error {
	server.Command = strings.TrimSpace(server.Command)
	if server.Command == "" {
		return fmt.Errorf("server %q: stdio transport requires command: %w", server.Name, ErrInvalidConfig)
	}
	if strings.ContainsRune(server.Command, 0) {
		return fmt.Errorf("server %q: command must not contain NUL: %w", server.Name, ErrInvalidConfig)
	}
	for index := range server.Args {
		if !utf8.ValidString(server.Args[index]) || strings.ContainsRune(server.Args[index], 0) {
			return fmt.Errorf("server %q: args[%d] is not a valid argument: %w", server.Name, index, ErrInvalidConfig)
		}
	}
	if server.Cwd != "" {
		if !filepath.IsAbs(server.Cwd) {
			return fmt.Errorf("server %q: cwd must be an absolute path: %w", server.Name, ErrInvalidConfig)
		}
		if strings.ContainsRune(server.Cwd, 0) {
			return fmt.Errorf("server %q: cwd must not contain NUL: %w", server.Name, ErrInvalidConfig)
		}
	}
	if err := validateAssignments(server.Name, "env", server.Env, envNamePattern); err != nil {
		return err
	}
	server.InheritEnv = compactStrings(server.InheritEnv)
	for _, name := range server.InheritEnv {
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("server %q: invalid inherit_env entry %q: %w", server.Name, name, ErrInvalidConfig)
		}
	}
	return nil
}

func normalizeHTTP(server *ServerConfig) error {
	server.URL = strings.TrimSpace(server.URL)
	if server.URL == "" {
		return fmt.Errorf("server %q: http transport requires url: %w", server.Name, ErrInvalidConfig)
	}
	parsed, err := url.Parse(server.URL)
	if err != nil {
		return fmt.Errorf("server %q: invalid url: %w: %w", server.Name, ErrInvalidConfig, err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("server %q: url must be absolute: %w", server.Name, ErrInvalidConfig)
	}
	if parsed.User != nil {
		return fmt.Errorf("server %q: url must not embed credentials; use headers: %w", server.Name, ErrInvalidConfig)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return fmt.Errorf("server %q: plaintext http is only allowed for loopback hosts: %w", server.Name, ErrInvalidConfig)
		}
	default:
		return fmt.Errorf("server %q: unsupported url scheme %q: %w", server.Name, parsed.Scheme, ErrInvalidConfig)
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("server %q: url must not contain a fragment: %w", server.Name, ErrInvalidConfig)
	}
	return validateHeaders(server)
}

func validateHeaders(server *ServerConfig) error {
	if err := validateAssignments(server.Name, "headers", server.Headers, nil); err != nil {
		return err
	}
	for name, value := range server.Headers {
		if !validHeaderName(name) {
			return fmt.Errorf("server %q: invalid header name %q: %w", server.Name, name, ErrInvalidConfig)
		}
		if !validHeaderValue(value) {
			return fmt.Errorf("server %q: header %q has an invalid value: %w", server.Name, name, ErrInvalidConfig)
		}
	}
	return nil
}

func validateAssignments(server, field string, values map[string]string, namePattern *regexp.Regexp) error {
	for name, value := range values {
		if namePattern != nil && !namePattern.MatchString(name) {
			return fmt.Errorf("server %q: invalid %s name %q: %w", server, field, name, ErrInvalidConfig)
		}
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("server %q: %s %q has an invalid value: %w", server, field, name, ErrInvalidConfig)
		}
		if err := validateVarReferences(value); err != nil {
			return fmt.Errorf("server %q: %s %q: %w", server, field, name, err)
		}
	}
	return nil
}

// validateVarReferences checks ${NAME} syntax without resolving values, so that
// configuration can be validated before the referenced environment exists.
func validateVarReferences(value string) error {
	remainder := value
	for {
		index := strings.Index(remainder, "${")
		if index < 0 {
			return nil
		}
		remainder = remainder[index+2:]
		end := strings.IndexByte(remainder, '}')
		if end < 0 {
			return fmt.Errorf("unterminated ${ reference: %w", ErrInvalidConfig)
		}
		name := remainder[:end]
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("invalid ${%s} reference: %w", name, ErrInvalidConfig)
		}
		remainder = remainder[end+1:]
	}
}

func validToolFilter(pattern string) bool {
	if pattern == "*" {
		return true
	}
	core := strings.TrimSuffix(pattern, "*")
	if core == "" || strings.ContainsRune(core, '*') {
		return false
	}
	for _, r := range core {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for _, r := range value {
		if r == '\r' || r == '\n' || r == 0 {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

// matchesToolFilter reports whether wireName is exposed by server. Disabled
// patterns take precedence over enabled patterns.
func matchesToolFilter(server ServerConfig, wireName string) bool {
	for _, pattern := range server.DisabledTools {
		if matchToolPattern(pattern, wireName) {
			return false
		}
	}
	for _, pattern := range server.EnabledTools {
		if matchToolPattern(pattern, wireName) {
			return true
		}
	}
	return false
}

func matchToolPattern(pattern, wireName string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(wireName, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == wireName
}

// configHash hashes exactly the configuration that determines the discovered
// tool set. Runtime-only settings such as timeouts, concurrency, result limits,
// and secret values are excluded so that changing them never invalidates a
// snapshot or demands a restart.
func (c Config) configHash() (string, error) {
	type hashServer struct {
		Name          string   `json:"name"`
		Transport     string   `json:"transport"`
		Command       string   `json:"command,omitempty"`
		Args          []string `json:"args,omitempty"`
		Cwd           string   `json:"cwd,omitempty"`
		URL           string   `json:"url,omitempty"`
		EnabledTools  []string `json:"enabled_tools,omitempty"`
		DisabledTools []string `json:"disabled_tools,omitempty"`
	}
	type hashConfig struct {
		ToolNamePrefix   string       `json:"tool_name_prefix"`
		ExposeStatusTool bool         `json:"expose_status_tool"`
		MaxToolsTotal    int          `json:"max_tools_total"`
		Servers          []hashServer `json:"servers"`
	}

	document := hashConfig{
		ToolNamePrefix:   c.ToolNamePrefix,
		ExposeStatusTool: c.exposeStatusTool(),
		MaxToolsTotal:    c.MaxToolsTotal,
	}
	for _, server := range c.enabledServers() {
		document.Servers = append(document.Servers, hashServer{
			Name:          server.Name,
			Transport:     server.Transport,
			Command:       server.Command,
			Args:          server.Args,
			Cwd:           server.Cwd,
			URL:           server.URL,
			EnabledTools:  server.EnabledTools,
			DisabledTools: server.DisabledTools,
		})
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode configuration digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func sortedKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
