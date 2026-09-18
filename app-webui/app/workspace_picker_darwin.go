//go:build darwin

package appcomponent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const workspaceAppleScript = `on run argv
set initialFolder to POSIX file (item 1 of argv)
set selectedFolder to choose folder with prompt "Select Workspace Directory" default location initialFolder
return POSIX path of selectedFolder
end run`

func selectNativeWorkspace(ctx context.Context, initialPath string) (string, bool, error) {
	command := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", workspaceAppleScript, initialPath)
	output, err := command.Output()
	if err == nil {
		path := strings.TrimSpace(string(output))
		return path, path == "", nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		message := string(exitError.Stderr)
		if strings.Contains(message, "User canceled") || strings.Contains(message, "-128") {
			return "", true, nil
		}
	}
	return "", false, fmt.Errorf("run macOS workspace picker: %w%s", err, commandStderr(err))
}

func commandStderr(err error) string {
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return ""
	}
	message := strings.TrimSpace(string(exitError.Stderr))
	if message == "" {
		return ""
	}
	return ": " + message
}
