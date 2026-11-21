package toolweb

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

const (
	configFileName = "config.toml"
	apiKeyEnv      = "DEEPSEEK_API_KEY"

	defaultBaseURL               = "https://api.deepseek.com"
	defaultRequestTimeoutSeconds = 60
	minRequestTimeoutSeconds     = 5
	maxRequestTimeoutSeconds     = 300
	defaultMaxResponseBytes      = 8 << 20
	minMaxResponseBytes          = 64 << 10
	maxMaxResponseBytes          = 64 << 20
	maxBaseURLBytes              = 2048
	maxStoredAPIKeyLength        = 1024
)

// configCommitMu serializes the read-check-write sequence of the config
// Operation. The file remains the authority across restarts; the atomic
// snapshot only serves live tool calls.
var configCommitMu sync.Mutex

// Config is this Plugin's private configuration file. Missing values select
// defaults; a zero request_timeout_seconds or max_response_bytes also selects
// its default.
type Config struct {
	APIKey                string `toml:"api_key"`
	BaseURL               string `toml:"base_url"`
	RequestTimeoutSeconds int    `toml:"request_timeout_seconds"`
	MaxResponseBytes      int    `toml:"max_response_bytes"`
}

// normalizedConfig is a validated configuration with resolved defaults and
// prepared endpoint URLs.
type normalizedConfig struct {
	apiKey           string
	baseURL          string
	searchURL        string
	fetchURL         string
	requestTimeout   time.Duration
	maxResponseBytes int64
}

// effectiveConfig is the immutable snapshot used by one tool invocation. The
// API key can come from the plugin config file or, when that is empty, from
// DEEPSEEK_API_KEY.
type effectiveConfig struct {
	apiKey           string
	apiKeySource     string
	searchURL        string
	fetchURL         string
	requestTimeout   time.Duration
	maxResponseBytes int64
}

func (c effectiveConfig) configured() bool { return c.apiKey != "" }

// loadConfig reads the Plugin-owned configuration. A missing file is the
// normal unconfigured state; read and decode failures are reported instead of
// silently discarding persisted state.
func loadConfig(dir string) (Config, error) {
	var config Config
	if dir == "" {
		return config, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, configFileName))
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, fmt.Errorf("read plugin config: %w", err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode plugin config: %w", err)
	}
	return config, nil
}

// saveConfig atomically writes the Plugin-owned configuration, creating the
// state scope when needed. The file contains the API key and is therefore
// restricted to the owner.
func saveConfig(dir string, config Config) error {
	if dir == "" {
		return fmt.Errorf("state scope is required: %w", ErrInvalidConfig)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state scope: %w", err)
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode plugin config: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".config-")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(dir, configFileName))
}

// normalizeConfig validates both freshly loaded and interactively edited
// configuration. It accepts an empty API key so construction can succeed
// before first-time configuration.
func normalizeConfig(config Config) (normalizedConfig, error) {
	apiKey := strings.TrimSpace(config.APIKey)
	if len(apiKey) > maxStoredAPIKeyLength {
		return normalizedConfig{}, configError("api_key", fmt.Sprintf("must be at most %d bytes", maxStoredAPIKeyLength))
	}
	if !validHeaderValue(apiKey) {
		return normalizedConfig{}, configError("api_key", "contains invalid control characters")
	}

	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	configuredBaseURL := baseURL
	if len(baseURL) > maxBaseURLBytes {
		return normalizedConfig{}, configError("base_url", fmt.Sprintf("must be at most %d bytes", maxBaseURLBytes))
	}
	if !utf8.ValidString(baseURL) || strings.ContainsAny(baseURL, " \t\r\n") {
		return normalizedConfig{}, configError("base_url", "must be a valid URL without whitespace")
	}
	parsed, err := url.Parse(baseURL)
	scheme := ""
	if parsed != nil {
		scheme = strings.ToLower(parsed.Scheme)
	}
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.User != nil ||
		(scheme != "http" && scheme != "https") || parsed.Host == "" {
		return normalizedConfig{}, configError("base_url", "must be an absolute http/https URL with a host and no userinfo")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return normalizedConfig{}, configError("base_url", "must not contain a query or fragment")
	}
	if parsed.RawPath != "" {
		return normalizedConfig{}, configError("base_url", "must not contain percent-encoded path segments")
	}

	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/v1/tools/search"), strings.HasSuffix(path, "/v1/tools/fetch"):
		return normalizedConfig{}, configError("base_url", "must be the API root, not a Search or Fetch endpoint")
	case strings.HasSuffix(path, "/v1/tools"):
		parsed.Path = path
	case strings.HasSuffix(path, "/v1"):
		parsed.Path = path + "/tools"
	default:
		parsed.Path = path + "/v1/tools"
	}
	toolsRoot := strings.TrimRight(parsed.String(), "/")

	timeoutSeconds := config.RequestTimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = defaultRequestTimeoutSeconds
	}
	if timeoutSeconds < minRequestTimeoutSeconds || timeoutSeconds > maxRequestTimeoutSeconds {
		return normalizedConfig{}, configError("request_timeout_seconds", fmt.Sprintf("must be between %d and %d", minRequestTimeoutSeconds, maxRequestTimeoutSeconds))
	}

	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if maxResponseBytes < minMaxResponseBytes || maxResponseBytes > maxMaxResponseBytes {
		return normalizedConfig{}, configError("max_response_bytes", fmt.Sprintf("must be between %d and %d", minMaxResponseBytes, maxMaxResponseBytes))
	}

	return normalizedConfig{
		apiKey:           apiKey,
		baseURL:          configuredBaseURL,
		searchURL:        toolsRoot + "/search",
		fetchURL:         toolsRoot + "/fetch",
		requestTimeout:   time.Duration(timeoutSeconds) * time.Second,
		maxResponseBytes: int64(maxResponseBytes),
	}, nil
}

// resolveEffective applies the environment fallback and validates an
// environment key before it is used in an Authorization header.
func resolveEffective(config normalizedConfig) (effectiveConfig, error) {
	apiKey := config.apiKey
	source := "config"
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv(apiKeyEnv))
		if apiKey == "" {
			source = "none"
		} else {
			source = "environment"
			if len(apiKey) > maxStoredAPIKeyLength || !validHeaderValue(apiKey) {
				return effectiveConfig{}, fmt.Errorf("%s contains invalid characters: %w", apiKeyEnv, ErrInvalidConfig)
			}
		}
	}
	return effectiveConfig{
		apiKey:           apiKey,
		apiKeySource:     source,
		searchURL:        config.searchURL,
		fetchURL:         config.fetchURL,
		requestTimeout:   config.requestTimeout,
		maxResponseBytes: config.maxResponseBytes,
	}, nil
}

func canonicalConfig(config normalizedConfig) Config {
	return Config{
		APIKey:                config.apiKey,
		BaseURL:               config.baseURL,
		RequestTimeoutSeconds: int(config.requestTimeout / time.Second),
		MaxResponseBytes:      int(config.maxResponseBytes),
	}
}

func validHeaderValue(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == '\r' || character == '\n' || character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func configError(field, detail string) error {
	return fmt.Errorf("%s %s: %w", field, detail, ErrInvalidConfig)
}
