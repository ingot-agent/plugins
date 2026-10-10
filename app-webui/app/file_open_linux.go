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
	"time"
)

func openNativeTextFile(ctx context.Context, path string) error {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("a desktop session is required to open a local text application")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Use the text/plain association even for scripts and HTML. Opening by the
	// target extension could execute a script or navigate away to a browser.
	output, err := exec.CommandContext(ctx, "xdg-mime", "query", "default", "text/plain").Output()
	if err != nil {
		return fmt.Errorf("find the default text application: %w", err)
	}
	name := strings.TrimSpace(string(output))
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".desktop") {
		return errors.New("no default text application is configured for text/plain")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dataHome = filepath.Join(userHome, ".local", "share")
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for _, directory := range append([]string{dataHome}, filepath.SplitList(dataDirs)...) {
		entry := filepath.Join(directory, "applications", name)
		if info, err := os.Stat(entry); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := exec.CommandContext(ctx, "gio", "launch", entry, path).Run(); err != nil {
			return fmt.Errorf("open the default text application: %w", err)
		}
		return nil
	}
	return errors.New("the default text application's desktop entry could not be found")
}
