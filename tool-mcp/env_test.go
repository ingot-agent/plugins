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

// TestBuildEnvironmentForwardsNetworkInfrastructure covers the variables a
// server needs to reach the network at all. Withholding them produced a
// connection error that never mentioned the proxy, which is one of the hardest
// failures to diagnose.
func TestBuildEnvironmentForwardsNetworkInfrastructure(t *testing.T) {
	names := []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	}
	for _, name := range names {
		t.Setenv(name, "probe-"+name)
	}
	environment, err := buildEnvironment(ServerConfig{Name: "probe"})
	if err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	joined := strings.Join(environment, "\n")
	for _, name := range names {
		if !strings.Contains(joined, name+"=probe-"+name) {
			t.Fatalf("%s was not forwarded", name)
		}
	}
}

// TestBuildEnvironmentStillWithholdsCredentials guards the other direction: the
// wider allowlist must not become a blanket pass-through.
func TestBuildEnvironmentStillWithholdsCredentials(t *testing.T) {
	for _, name := range []string{"PROBE_TOKEN", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "NPM_CONFIG__AUTH"} {
		t.Setenv(name, "must-not-leak")
	}
	environment, err := buildEnvironment(ServerConfig{Name: "probe"})
	if err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	joined := strings.Join(environment, "\n")
	for _, name := range []string{"PROBE_TOKEN", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "NPM_CONFIG__AUTH"} {
		if strings.Contains(joined, name+"=") {
			t.Fatalf("%s leaked into the child environment", name)
		}
	}
}

// TestInheritEnvForwardsAsIs is the documented way to pass any other variable.
func TestInheritEnvForwardsAsIs(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_example")
	environment, err := buildEnvironment(ServerConfig{
		Name:       "probe",
		InheritEnv: []string{"GITHUB_TOKEN"},
	})
	if err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	if !strings.Contains(strings.Join(environment, "\n"), "GITHUB_TOKEN=ghp_example") {
		t.Fatalf("the inherited variable is missing: %v", environment)
	}
}

// TestEnvCanRenameAndCompose covers the second documented way: deriving a value
// from a host variable, including a rename or a prefix.
func TestEnvCanRenameAndCompose(t *testing.T) {
	t.Setenv("HOST_TOKEN", "abc123")
	environment, err := buildEnvironment(ServerConfig{
		Name: "probe",
		Env: map[string]string{
			"RENAMED": "123",
			"AUTH":    "Bearer ${HOST_TOKEN}",
		},
	})
	if err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "RENAMED=123") {
		t.Fatalf("literal value missing: %v", environment)
	}
	if !strings.Contains(joined, "AUTH=Bearer abc123") {
		t.Fatalf("composed value missing: %v", environment)
	}
	if strings.Contains(joined, "HOST_TOKEN=") {
		t.Fatal("a value used only as a source must not also be forwarded")
	}
}
