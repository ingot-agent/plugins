//go:build windows

package appcomponent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func openNativeTextFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Notepad opens text regardless of the extension; a shell "open" action
	// could execute .bat/.ps1 files instead of displaying their contents.
	command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe"), path)
	if err := command.Start(); err != nil {
		return fmt.Errorf("open Notepad: %w", err)
	}
	go func() { _ = command.Wait() }()
	return nil
}
