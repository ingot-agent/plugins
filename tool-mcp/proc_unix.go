//go:build unix

package toolmcp

import (
	"os/exec"
	"syscall"
)

// configureProcess places the MCP server in its own process group so that the
// whole tree can be terminated, not only the direct child.
func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup terminates the server and every process it spawned.
// Package-launched servers are often wrappers such as npx or uvx that leave
// grandchildren behind when only the leader is signalled.
func killProcessGroup(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	// Signal only a live group: after the leader is reaped, the process group
	// identifier can be recycled, so a blind kill could hit an unrelated group.
	if err := syscall.Kill(-command.Process.Pid, 0); err != nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
