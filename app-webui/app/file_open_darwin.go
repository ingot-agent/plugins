//go:build darwin

package appcomponent

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

func openNativeTextFile(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "/usr/bin/open", "-t", "--", path).Run(); err != nil {
		return fmt.Errorf("open the default text application: %w", err)
	}
	return nil
}
