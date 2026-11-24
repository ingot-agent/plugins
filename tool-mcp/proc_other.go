//go:build !unix && !windows

package toolmcp

import "os/exec"

func configureProcess(*exec.Cmd) {}

func killProcessGroup(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}
