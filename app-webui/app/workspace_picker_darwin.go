//go:build darwin

package appcomponent

import (
	"context"
	"encoding/json"
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

const filePickerJavaScript = `ObjC.import('AppKit');
function run(argv) {
  const panel = $.NSOpenPanel.openPanel;
  panel.title = 'Select Files';
  panel.canChooseFiles = true;
  panel.canChooseDirectories = false;
  panel.allowsMultipleSelection = true;
  panel.directoryURL = $.NSURL.fileURLWithPath(argv[0]);
  $.NSApplication.sharedApplication.activateIgnoringOtherApps(true);
  if (panel.runModal !== 1) return '[]';
  const urls = panel.URLs;
  const paths = [];
  for (let i = 0; i < urls.count; i++) paths.push(ObjC.unwrap(urls.objectAtIndex(i).path));
  return JSON.stringify(paths);
}`

func selectNativeFiles(ctx context.Context, initialPath string) ([]string, bool, error) {
	command := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", filePickerJavaScript, initialPath)
	output, err := command.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, false, fmt.Errorf("run macOS file picker: %w%s", err, commandStderr(err))
	}
	var paths []string
	if err := json.Unmarshal(output, &paths); err != nil {
		return nil, false, fmt.Errorf("decode macOS file selection: %w", err)
	}
	return paths, len(paths) == 0, nil
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
