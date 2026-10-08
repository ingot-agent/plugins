//go:build !darwin && !linux && !windows

package appcomponent

import (
	"context"
	"fmt"
	"runtime"
)

func selectNativeWorkspace(context.Context, string) (string, bool, error) {
	return "", false, fmt.Errorf("%w on %s", errWorkspacePickerUnavailable, runtime.GOOS)
}

func selectNativeFiles(context.Context, string) ([]string, bool, error) {
	return nil, false, fmt.Errorf("%w on %s", errFilePickerUnavailable, runtime.GOOS)
}
