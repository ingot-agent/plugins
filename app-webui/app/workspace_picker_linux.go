//go:build linux

package appcomponent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func selectNativeWorkspace(ctx context.Context, initialPath string) (string, bool, error) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return "", false, fmt.Errorf("%w: DISPLAY or WAYLAND_DISPLAY is required", errWorkspacePickerUnavailable)
	}
	initial := filepath.Clean(initialPath)
	if !strings.HasSuffix(initial, string(filepath.Separator)) {
		initial += string(filepath.Separator)
	}
	path, canceled, err := runLinuxWorkspacePicker(ctx, "zenity",
		"--file-selection", "--directory", "--title=Select Workspace Directory", "--filename="+initial)
	if err == nil || canceled {
		return path, canceled, err
	}
	if !errors.Is(err, exec.ErrNotFound) {
		return "", false, err
	}
	path, canceled, err = runLinuxWorkspacePicker(ctx, "kdialog",
		"--getexistingdirectory", initialPath, "--title", "Select Workspace Directory")
	if err == nil || canceled {
		return path, canceled, err
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", false, fmt.Errorf("%w: install zenity or kdialog", errWorkspacePickerUnavailable)
	}
	return "", false, err
}

func runLinuxWorkspacePicker(ctx context.Context, command string, arguments ...string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, command, arguments...)
	output, err := cmd.Output()
	if err == nil {
		path := strings.TrimSpace(string(output))
		return path, path == "", nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return "", true, nil
	}
	return "", false, fmt.Errorf("run %s workspace picker: %w%s", filepath.Base(command), err, commandStderr(err))
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
