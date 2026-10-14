//go:build !linux && !darwin && !windows

package appcomponent

import (
	"context"
	"errors"
)

func openNativeTextFile(context.Context, string) error {
	return errors.New("opening a local text application is not supported on this platform")
}
