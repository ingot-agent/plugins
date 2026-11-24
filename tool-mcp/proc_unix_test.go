//go:build unix

package toolmcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestFailedConnectReapsTheWholeProcessGroup covers the orphan case: a stdio
// server launched through a wrapper leaves grandchildren behind, so a failed
// handshake must terminate the process group rather than only the direct child.
func TestFailedConnectReapsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	script := filepath.Join(dir, "wrapper.sh")
	// The wrapper starts a long-lived grandchild, records its pid, then waits
	// without ever speaking MCP, so the handshake is guaranteed to time out.
	body := "#!/bin/sh\nsleep 120 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}

	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name:                   "wrapper",
		Transport:              transportStdio,
		Command:                script,
		ConnectTimeoutSeconds:  1,
		ShutdownTimeoutSeconds: 1,
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}

	instance := newPlatform(t.TempDir(), ingotabiNone(), discardLogger(), nil, config)
	defer instance.closeAll()

	start := time.Now()
	if _, err := instance.acquire(context.Background(), "wrapper"); err == nil {
		t.Fatal("expected the handshake to fail")
	}
	// The caller must learn about the failure within the configured connect
	// budget, not after the SDK's post-mortem shutdown escalation.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("reporting the failure took %s, want about the connect timeout", elapsed)
	}

	grandchild := waitForPIDFile(t, pidFile)
	defer func() {
		if processExists(grandchild) {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
		}
	}()
	if processExists(grandchild) {
		_ = syscall.Kill(-grandchild, syscall.SIGKILL)
	}
	waitForProcessExit(t, grandchild)
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the wrapper never wrote %s", path)
	return 0
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d survived process-group termination", pid)
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// TestConfigureProcessSetsProcessGroup asserts the platform hook actually
// isolates the server, which is the precondition for group termination.
func TestConfigureProcessSetsProcessGroup(t *testing.T) {
	command := exec.Command("true")
	configureProcess(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v", command.SysProcAttr)
	}
}

// TestCleanupTerminatesChildrenImmediately covers shutdown: Cleanup must end the
// child process tree itself instead of waiting for in-flight leases to be
// released, because the Runtime process is about to exit.
func TestCleanupTerminatesChildrenImmediately(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "server.sh")
	body := "#!/bin/sh\necho $$ > " + pidFile + "\nsleep 120 &\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "server", Transport: transportStdio, Command: script,
		ConnectTimeoutSeconds: 1, ShutdownTimeoutSeconds: 1,
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(t.TempDir(), ingotabiNone(), discardLogger(), nil, config)

	// Force a live child without completing the handshake.
	go func() { _, _ = instance.acquire(context.Background(), "server") }()
	pid := waitForPIDFile(t, pidFile)

	start := time.Now()
	instance.closeAll()
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("cleanup took %s", elapsed)
	}
	waitForProcessExit(t, pid)
}
