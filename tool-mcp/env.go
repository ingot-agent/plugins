package toolmcp

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// defaultEnvAllowlist is the minimum process environment forwarded to a stdio
// MCP server. Forwarding the whole environment would hand every secret of the
// host process to a third-party executable, so anything else must be listed
// explicitly in the server's inherit_env.
var defaultEnvAllowlist = []string{
	"PATH",
	"HOME",
	"TMPDIR",
	"TMP",
	"TEMP",
	"LANG",
	"USER",
	"LOGNAME",
	"SHELL",
	"TERM",
	"SystemRoot",
	"SystemDrive",
	"COMSPEC",
	"PATHEXT",
	"WINDIR",
	"APPDATA",
	"LOCALAPPDATA",
	"USERPROFILE",
}

func allowedByDefault(name string) bool {
	for _, allowed := range defaultEnvAllowlist {
		if name == allowed {
			return true
		}
	}
	return strings.HasPrefix(name, "LC_")
}

// buildEnvironment assembles the child process environment: the allowlisted
// host variables, the explicitly inherited variables, and the configured
// values in that precedence order.
func buildEnvironment(server ServerConfig) ([]string, error) {
	values := make(map[string]string)

	inherited := make(map[string]bool, len(server.InheritEnv))
	for _, name := range server.InheritEnv {
		inherited[name] = true
	}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if allowedByDefault(name) || inherited[name] {
			values[name] = value
		}
	}

	for name, raw := range server.Env {
		value, err := expandVariables(raw)
		if err != nil {
			return nil, fmt.Errorf("server %q: env %s: %w", server.Name, name, err)
		}
		values[name] = value
	}

	result := make([]string, 0, len(values))
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	sort.Strings(result)
	return result, nil
}

// expandVariables substitutes ${NAME} references from the current process
// environment in a single non-recursive pass.
func expandVariables(value string) (string, error) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	var builder strings.Builder
	remainder := value
	for {
		index := strings.Index(remainder, "${")
		if index < 0 {
			builder.WriteString(remainder)
			return builder.String(), nil
		}
		builder.WriteString(remainder[:index])
		remainder = remainder[index+2:]
		end := strings.IndexByte(remainder, '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated ${ reference: %w", ErrInvalidConfig)
		}
		name := remainder[:end]
		if !envNamePattern.MatchString(name) {
			return "", fmt.Errorf("invalid ${%s} reference: %w", name, ErrInvalidConfig)
		}
		replacement, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("environment variable %s is not set: %w", name, ErrInvalidConfig)
		}
		builder.WriteString(replacement)
		remainder = remainder[end+1:]
	}
}

// expandServer applies ${NAME} expansion to every configured value that is
// passed to the server or its transport.
func expandServer(server ServerConfig) (ServerConfig, error) {
	expanded := server
	expanded.Args = append([]string(nil), server.Args...)
	for index := range expanded.Args {
		value, err := expandVariables(expanded.Args[index])
		if err != nil {
			return ServerConfig{}, fmt.Errorf("server %q: args[%d]: %w", server.Name, index, err)
		}
		expanded.Args[index] = value
	}
	if expanded.URL != "" {
		value, err := expandVariables(expanded.URL)
		if err != nil {
			return ServerConfig{}, fmt.Errorf("server %q: url: %w", server.Name, err)
		}
		expanded.URL = value
	}
	if expanded.Cwd != "" {
		value, err := expandVariables(expanded.Cwd)
		if err != nil {
			return ServerConfig{}, fmt.Errorf("server %q: cwd: %w", server.Name, err)
		}
		expanded.Cwd = value
	}
	return expanded, nil
}

// redactSecrets removes configured secret values from server-controlled text
// such as child stderr, which often echoes credentials. Values shorter than
// redactionMinimumLength are skipped so that redaction cannot mangle unrelated
// text.
func redactSecrets(server ServerConfig, text string) string {
	if text == "" {
		return ""
	}
	secrets := make([]string, 0, len(server.Env)+len(server.Headers))
	for _, raw := range server.Env {
		if value, err := expandVariables(raw); err == nil {
			secrets = append(secrets, value)
		}
	}
	for _, raw := range server.Headers {
		if value, err := expandVariables(raw); err == nil {
			secrets = append(secrets, value)
		}
	}
	result := text
	for _, secret := range secrets {
		if len(secret) < redactionMinimumLength {
			continue
		}
		result = strings.ReplaceAll(result, secret, "[redacted]")
	}
	return result
}
