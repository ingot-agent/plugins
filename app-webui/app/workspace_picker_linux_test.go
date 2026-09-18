//go:build linux

package appcomponent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxWorkspacePickerFallsBackOnlyWhenZenityIsMissing(t *testing.T) {
	t.Setenv("DISPLAY", ":1")
	t.Setenv("WAYLAND_DISPLAY", "")
	selected := t.TempDir()

	fallbackBin := t.TempDir()
	writePickerScript(t, fallbackBin, "kdialog", "printf '%s\\n' \"$PICKER_SELECTED\"")
	t.Setenv("PICKER_SELECTED", selected)
	t.Setenv("PATH", fallbackBin)
	path, canceled, err := selectNativeWorkspace(context.Background(), selected)
	if err != nil || canceled || path != selected {
		t.Fatalf("kdialog fallback = %q, %v, %v", path, canceled, err)
	}

	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "kdialog-called")
	writePickerScript(t, bin, "zenity", "printf 'zenity failed' >&2\nexit 2")
	writePickerScript(t, bin, "kdialog", "printf called > \"$PICKER_MARKER\"\nprintf '%s\\n' \"$PICKER_SELECTED\"")
	t.Setenv("PICKER_MARKER", marker)
	t.Setenv("PATH", bin)
	_, _, err = selectNativeWorkspace(context.Background(), selected)
	if err == nil || !strings.Contains(err.Error(), "zenity failed") {
		t.Fatalf("zenity failure = %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("kdialog ran after zenity failure: %v", statErr)
	}
}

func TestLinuxWorkspacePickerCancellationAndMissingDesktop(t *testing.T) {
	selected := t.TempDir()
	bin := t.TempDir()
	writePickerScript(t, bin, "zenity", "exit 1")
	t.Setenv("PATH", bin)
	t.Setenv("DISPLAY", ":1")
	_, canceled, err := selectNativeWorkspace(context.Background(), selected)
	if err != nil || !canceled {
		t.Fatalf("cancel = %v, %v", canceled, err)
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if _, _, err := selectNativeWorkspace(context.Background(), selected); err == nil || !strings.Contains(err.Error(), "DISPLAY") {
		t.Fatalf("missing desktop = %v", err)
	}
}

func writePickerScript(t *testing.T, directory, name, body string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}
