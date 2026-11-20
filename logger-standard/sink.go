package loggerstandard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/logging"
)

// sink owns the implementation policy that the logging contract deliberately
// leaves open: the effective level, record rendering, destination and file
// retention. A sink is safe for concurrent use.
type sink struct {
	dir string

	level atomic.Int32

	mu        sync.Mutex
	config    Config
	out       io.Writer
	file      *os.File
	path      string
	size      int64
	lastError error
	now       func() time.Time
}

// newSink validates cfg and opens the configured destination.
func newSink(dir string, cfg Config) (*sink, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	s := &sink{dir: dir, config: normalized, out: os.Stderr, now: time.Now}
	s.level.Store(int32(levelOf(normalized.Level)))
	if err := s.openLocked(normalized, true); err != nil {
		return nil, err
	}
	return s, nil
}

// Enabled reports whether level is at or above the current minimum.
func (s *sink) Enabled(level logging.Level) bool {
	return int8(level) >= int8(s.level.Load())
}

// Apply switches the live policy. The new destination is opened before the
// current one is closed, so a failed switch keeps the previous sink usable and
// returns the error without changing observable behavior.
func (s *sink) Apply(cfg Config) error {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(normalized, false); err != nil {
		return err
	}
	s.config = normalized
	s.level.Store(int32(levelOf(normalized.Level)))
	return nil
}

// openLocked prepares the destination described by cfg. When reopen is false
// and the requested destination is identical, the current handle is retained.
func (s *sink) openLocked(cfg Config, reopen bool) error {
	if cfg.Output == outputStderr {
		if s.file != nil {
			_ = s.file.Close()
			s.file = nil
		}
		s.out, s.path, s.size = os.Stderr, "", 0
		return nil
	}
	absolute := cfg.Path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(s.dir, cfg.Path)
	}
	absolute = filepath.Clean(absolute)
	if !reopen && s.file != nil && s.path == absolute {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	if s.file != nil {
		_ = s.file.Close()
	}
	s.file, s.out, s.path, s.size = file, file, absolute, info.Size()
	return nil
}

// Close releases the file destination. It is idempotent and does not close the
// process-wide standard error stream.
func (s *sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out = io.Discard
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// write renders and appends one record. Level filtering is evaluated again
// under the write lock so a concurrent policy change cannot admit a record that
// was already disabled when the caller checked Enabled.
func (s *sink) write(level logging.Level, message string, attrs []logging.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if int8(level) < int8(s.level.Load()) {
		return
	}
	config := s.config
	message = truncateText(message, config.MaxRecordBytes)
	record := s.encode(level, message, attrs, config)
	s.rotateLocked(config, int64(len(record)))
	if _, err := s.out.Write(record); err != nil {
		s.lastError = err
		return
	}
	if s.file != nil {
		s.size += int64(len(record))
	}
}

// rotateLocked renames the current file when appending incoming bytes would
// exceed the configured size, then reopens an empty file. Rotated files are
// named <path>.1 (newest) through <path>.N; older files are removed.
func (s *sink) rotateLocked(config Config, incoming int64) {
	if s.file == nil || config.MaxSizeBytes <= 0 {
		return
	}
	if s.size == 0 || s.size+incoming <= config.MaxSizeBytes {
		return
	}
	_ = s.file.Close()
	s.file = nil
	s.out = os.Stderr
	base, backups := s.path, config.MaxBackups
	if backups < 1 {
		_ = os.Remove(base)
	} else {
		_ = os.Remove(fmt.Sprintf("%s.%d", base, backups))
		for index := backups - 1; index >= 1; index-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", base, index), fmt.Sprintf("%s.%d", base, index+1))
		}
		if err := os.Rename(base, base+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.lastError = err
		}
	}
	if err := s.openLocked(config, true); err != nil {
		s.lastError = err
	}
}

// encode renders one complete record including its trailing newline.
func (s *sink) encode(level logging.Level, message string, attrs []logging.Attr, config Config) []byte {
	attrs = mergeAttributes(attrs)
	if config.Format == formatJSON {
		return s.encodeJSON(level, message, attrs, config.MaxRecordBytes)
	}
	return s.encodeText(level, message, attrs, config.MaxRecordBytes)
}

func (s *sink) encodeText(level logging.Level, message string, attrs []logging.Attr, budget int) []byte {
	var builder strings.Builder
	builder.WriteString("time=")
	builder.WriteString(s.now().Format(time.RFC3339Nano))
	builder.WriteString(" level=")
	builder.WriteString(strings.ToUpper(level.String()))
	builder.WriteString(" msg=")
	builder.WriteString(quoteText(message))
	remaining := budget - builder.Len()
	dropped := 0
	for _, attr := range attrs {
		var rendered strings.Builder
		writeTextAttr(&rendered, "", attr, budget)
		if rendered.Len()+1 > remaining {
			dropped++
			continue
		}
		builder.WriteByte(' ')
		builder.WriteString(rendered.String())
		remaining -= rendered.Len() + 1
	}
	writeTextDropped(&builder, dropped)
	builder.WriteByte('\n')
	return []byte(builder.String())
}

func (s *sink) encodeJSON(level logging.Level, message string, attrs []logging.Attr, budget int) []byte {
	var builder strings.Builder
	builder.WriteString(`{"time":`)
	builder.WriteString(jsonString(s.now().Format(time.RFC3339Nano)))
	builder.WriteString(`,"level":`)
	builder.WriteString(jsonString(strings.ToUpper(level.String())))
	builder.WriteString(`,"msg":`)
	builder.WriteString(jsonString(message))
	remaining := budget - builder.Len()
	dropped := 0
	for _, attr := range attrs {
		var rendered strings.Builder
		rendered.WriteByte(',')
		rendered.WriteString(jsonString(sanitizeKey(attr.Key)))
		rendered.WriteByte(':')
		writeJSONValue(&rendered, attr.Value, budget)
		if rendered.Len()+1 > remaining {
			dropped++
			continue
		}
		builder.WriteString(rendered.String())
		remaining -= rendered.Len()
	}
	if dropped > 0 {
		builder.WriteString(`,"truncated_attrs":`)
		builder.WriteString(strconv.Itoa(dropped))
	}
	builder.WriteString("}\n")
	return []byte(builder.String())
}

func writeTextAttr(builder *strings.Builder, prefix string, attr logging.Attr, budget int) {
	key := sanitizeKey(attr.Key)
	if key == "" {
		return
	}
	key = prefix + key
	if attr.Value.Kind == logging.KindGroup {
		for _, entry := range attr.Value.Group {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			writeTextAttr(builder, key+".", entry, budget)
		}
		return
	}
	builder.WriteString(key)
	builder.WriteByte('=')
	builder.WriteString(textValue(attr.Value, budget))
}

func writeTextDropped(builder *strings.Builder, dropped int) {
	if dropped <= 0 {
		return
	}
	builder.WriteString(" truncated_attrs=")
	builder.WriteString(strconv.Itoa(dropped))
}

func textValue(value logging.Value, budget int) string {
	switch value.Kind {
	case logging.KindString:
		return quoteText(truncateText(value.String, budget))
	case logging.KindInt64:
		return strconv.FormatInt(value.Int64, 10)
	case logging.KindUint64:
		return strconv.FormatUint(value.Uint64, 10)
	case logging.KindFloat64:
		return strconv.FormatFloat(value.Float64, 'g', -1, 64)
	case logging.KindBool:
		return strconv.FormatBool(value.Boolean)
	case logging.KindDuration:
		return value.Duration.String()
	case logging.KindTime:
		return value.Time.Format(time.RFC3339Nano)
	case logging.KindError:
		if value.Error == nil {
			return `""`
		}
		return quoteText(truncateText(value.Error.Error(), budget))
	default:
		return `""`
	}
}

func writeJSONValue(builder *strings.Builder, value logging.Value, budget int) {
	switch value.Kind {
	case logging.KindString:
		builder.WriteString(jsonString(truncateText(value.String, budget)))
	case logging.KindInt64:
		builder.WriteString(strconv.FormatInt(value.Int64, 10))
	case logging.KindUint64:
		builder.WriteString(strconv.FormatUint(value.Uint64, 10))
	case logging.KindFloat64:
		builder.WriteString(strconv.FormatFloat(value.Float64, 'g', -1, 64))
	case logging.KindBool:
		builder.WriteString(strconv.FormatBool(value.Boolean))
	case logging.KindDuration:
		builder.WriteString(jsonString(value.Duration.String()))
	case logging.KindTime:
		builder.WriteString(jsonString(value.Time.Format(time.RFC3339Nano)))
	case logging.KindError:
		if value.Error == nil {
			builder.WriteString(`""`)
			return
		}
		builder.WriteString(jsonString(truncateText(value.Error.Error(), budget)))
	case logging.KindGroup:
		builder.WriteByte('{')
		written := 0
		for _, attr := range value.Group {
			key := sanitizeKey(attr.Key)
			if key == "" {
				continue
			}
			if written > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(jsonString(key))
			builder.WriteByte(':')
			writeJSONValue(builder, attr.Value, budget)
			written++
		}
		builder.WriteByte('}')
	default:
		builder.WriteString(`""`)
	}
}

// jsonString encodes one JSON string. Control characters use the JSON escapes
// that strconv.Quote does not produce, so the result is valid JSON for every
// UTF-8 input.
func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// mergeAttributes drops attributes with an empty key and keeps the last value
// for a repeated key, preserving the position of that final occurrence.
func mergeAttributes(attrs []logging.Attr) []logging.Attr {
	seen := make(map[string]struct{}, len(attrs))
	merged := make([]logging.Attr, 0, len(attrs))
	for index := len(attrs) - 1; index >= 0; index-- {
		attr := attrs[index]
		if attr.Key == "" {
			continue
		}
		if _, ok := seen[attr.Key]; ok {
			continue
		}
		seen[attr.Key] = struct{}{}
		merged = append(merged, attr)
	}
	for left, right := 0, len(merged)-1; left < right; left, right = left+1, right-1 {
		merged[left], merged[right] = merged[right], merged[left]
	}
	return merged
}

// truncateText bounds one text value and always returns valid UTF-8. The
// marker reports how many bytes were removed so a reader can tell a truncated
// payload from a short one.
func truncateText(text string, budget int) string {
	if budget <= 0 || len(text) <= budget {
		return text
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	removed := len(text) - cut
	return text[:cut] + fmt.Sprintf("...(truncated %d bytes)", removed)
}

// quoteText renders text as a bare token when it is unambiguous and as a
// quoted Go string otherwise.
func quoteText(text string) string {
	if text == "" {
		return `""`
	}
	for _, char := range text {
		if char <= ' ' || char == '"' || char == '=' || char == utf8.RuneError {
			return strconv.Quote(text)
		}
	}
	return text
}

// sanitizeKey keeps the key machine-readable: characters outside the common
// identifier set become underscores instead of breaking the record syntax.
func sanitizeKey(key string) string {
	var builder strings.Builder
	builder.Grow(len(key))
	for _, char := range key {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '.', char == '-':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}
