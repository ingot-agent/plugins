package loggerstandard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// configFileName is the Plugin-owned configuration file inside this Plugin's
// Runtime state scope. Core never reads or decodes it.
const configFileName = "config.toml"

// configCommitMu serializes read-modify-write sequences across the live
// configuration Operation so a concurrent writer cannot be lost silently.
var configCommitMu sync.Mutex

const (
	defaultLevel          = "info"
	defaultFormat         = "text"
	defaultOutput         = "stderr"
	defaultPath           = "logs/ingot.log"
	defaultMaxRecordBytes = 64 * 1024
	defaultMaxSizeBytes   = 32 * 1024 * 1024
	defaultMaxBackups     = 3

	formatText = "text"
	formatJSON = "json"

	outputStderr = "stderr"
	outputFile   = "file"
)

// Config is the Plugin-owned, operator-visible logging policy. Zero values
// select the documented defaults, so a hand-edited file only needs the keys it
// changes. Path is interpreted relative to this Plugin's state directory when
// it is not absolute.
type Config struct {
	// Level is the minimum emitted level: debug, info, warn or error.
	Level string `toml:"level"`
	// Format is the record rendering: text or json.
	Format string `toml:"format"`
	// Output selects the destination: stderr or file.
	Output string `toml:"output"`
	// Path is the file destination used when Output is file.
	Path string `toml:"path"`
	// MaxRecordBytes bounds one attribute value and one complete record. A
	// record that does not fit drops its remaining attributes and reports how
	// many were dropped.
	MaxRecordBytes int `toml:"max_record_bytes"`
	// MaxSizeBytes is the size at which the file destination rotates.
	MaxSizeBytes int64 `toml:"max_size_bytes"`
	// MaxBackups is the number of rotated files retained, named
	// <path>.1 (newest) through <path>.N.
	MaxBackups int `toml:"max_backups"`
}

// normalizeConfig validates cfg and replaces zero values with defaults. The
// result is complete: every field has an effective value. Negative sizes and
// unknown enumerations are rejected rather than clamped.
func normalizeConfig(cfg Config) (Config, error) {
	level := strings.ToLower(strings.TrimSpace(cfg.Level))
	if level == "" {
		level = defaultLevel
	}
	if _, err := parseLevel(level); err != nil {
		return Config{}, err
	}
	format := strings.ToLower(strings.TrimSpace(cfg.Format))
	if format == "" {
		format = defaultFormat
	}
	if format != formatText && format != formatJSON {
		return Config{}, fmt.Errorf("format %q: %w", cfg.Format, ErrInvalidConfig)
	}
	output := strings.ToLower(strings.TrimSpace(cfg.Output))
	if output == "" {
		output = defaultOutput
	}
	if output != outputStderr && output != outputFile {
		return Config{}, fmt.Errorf("output %q: %w", cfg.Output, ErrInvalidConfig)
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = defaultPath
	}
	if cfg.MaxRecordBytes < 0 {
		return Config{}, fmt.Errorf("max_record_bytes must not be negative: %w", ErrInvalidConfig)
	}
	maxRecordBytes := cfg.MaxRecordBytes
	if maxRecordBytes == 0 {
		maxRecordBytes = defaultMaxRecordBytes
	}
	if cfg.MaxSizeBytes < 0 {
		return Config{}, fmt.Errorf("max_size_bytes must not be negative: %w", ErrInvalidConfig)
	}
	maxSizeBytes := cfg.MaxSizeBytes
	if maxSizeBytes == 0 {
		maxSizeBytes = defaultMaxSizeBytes
	}
	if cfg.MaxBackups < 0 {
		return Config{}, fmt.Errorf("max_backups must not be negative: %w", ErrInvalidConfig)
	}
	maxBackups := cfg.MaxBackups
	if maxBackups == 0 {
		maxBackups = defaultMaxBackups
	}
	return Config{
		Level:          level,
		Format:         format,
		Output:         output,
		Path:           path,
		MaxRecordBytes: maxRecordBytes,
		MaxSizeBytes:   maxSizeBytes,
		MaxBackups:     maxBackups,
	}, nil
}

// loadConfig reads the Plugin-owned configuration. A missing file is the
// normal Unconfigured state and yields the zero Config so defaults apply; any
// other read or decode failure is reported instead of being discarded.
func loadConfig(scope string) (Config, error) {
	var config Config
	if scope == "" {
		return config, nil
	}
	data, err := os.ReadFile(filepath.Join(scope, configFileName))
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

// saveConfig atomically writes the Plugin-owned configuration into its state
// scope, creating the scope when needed.
func saveConfig(scope string, config Config) error {
	if scope == "" {
		return fmt.Errorf("state scope is required: %w", ErrInvalidConfig)
	}
	if err := os.MkdirAll(scope, 0o700); err != nil {
		return fmt.Errorf("create state scope: %w", err)
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode plugin config: %w", err)
	}
	temporary, err := os.CreateTemp(scope, ".config-")
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
	return os.Rename(temporaryPath, filepath.Join(scope, configFileName))
}
