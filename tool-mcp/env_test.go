package toolmcp

import (
	"strings"
	"testing"
)

func TestExpandVariables(t *testing.T) {
	t.Setenv("TOOLMCP_TEST_TOKEN", "secret")
	value, err := expandVariables("Bearer ${TOOLMCP_TEST_TOKEN}")
	if err != nil {
		t.Fatalf("expandVariables: %v", err)
	}
	if value != "Bearer secret" {
		t.Fatalf("value = %q", value)
	}
	if _, err := expandVariables("${TOOLMCP_TEST_MISSING}"); err == nil {
		t.Fatal("expected an error for a missing variable")
	}
	if _, err := expandVariables("${UNCLOSED"); err == nil {
		t.Fatal("expected an error for an unterminated reference")
	}
}

func TestExpandVariablesIsNotRecursive(t *testing.T) {
	t.Setenv("TOOLMCP_TEST_A", "${TOOLMCP_TEST_B}")
	t.Setenv("TOOLMCP_TEST_B", "nested")
	value, err := expandVariables("${TOOLMCP_TEST_A}")
	if err != nil {
		t.Fatalf("expandVariables: %v", err)
	}
	if value != "${TOOLMCP_TEST_B}" {
		t.Fatalf("value = %q, want a single non-recursive pass", value)
	}
}

func TestBuildEnvironmentFiltersHostSecrets(t *testing.T) {
	t.Setenv("TOOLMCP_TEST_SECRET", "must-not-leak")
	t.Setenv("TOOLMCP_ALLOWED_EXTRA", "explicit")
	server := ServerConfig{
		Name:       "demo",
		Env:        map[string]string{"CONFIGURED": "${TOOLMCP_ALLOWED_EXTRA}"},
		InheritEnv: []string{"TOOLMCP_ALLOWED_EXTRA"},
	}
	environment, err := buildEnvironment(server)
	if err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "TOOLMCP_TEST_SECRET") {
		t.Fatal("an unlisted host variable leaked into the child environment")
	}
	if !strings.Contains(joined, "TOOLMCP_ALLOWED_EXTRA=explicit") {
		t.Fatalf("inherited variable missing: %v", environment)
	}
	if !strings.Contains(joined, "CONFIGURED=explicit") {
		t.Fatalf("configured expansion missing: %v", environment)
	}
}

func TestExpandServerExpandsArgsURLAndCwd(t *testing.T) {
	t.Setenv("TOOLMCP_TEST_DIR", "/tmp/workspace")
	t.Setenv("TOOLMCP_TEST_URL", "https://example.test/mcp")
	server, err := expandServer(ServerConfig{
		Name: "demo",
		Args: []string{"--dir", "${TOOLMCP_TEST_DIR}"},
		Cwd:  "${TOOLMCP_TEST_DIR}",
		URL:  "${TOOLMCP_TEST_URL}",
	})
	if err != nil {
		t.Fatalf("expandServer: %v", err)
	}
	if server.Args[1] != "/tmp/workspace" || server.Cwd != "/tmp/workspace" || server.URL != "https://example.test/mcp" {
		t.Fatalf("expansion failed: %#v", server)
	}
}
