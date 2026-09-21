//go:build windows

package appcomponent

import (
	"context"
	"testing"
)

func TestWaitForWindowsWorkspacePickerReturnsWhenDialogFinishes(t *testing.T) {
	showDone := make(chan struct{})
	close(showDone)
	closed := false
	waitForWindowsWorkspacePicker(context.Background(), showDone, func() { closed = true })
	if closed {
		t.Fatal("finished dialog was closed again")
	}
}

func TestWaitForWindowsWorkspacePickerClosesDialogAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := false
	waitForWindowsWorkspacePicker(ctx, make(chan struct{}), func() { closed = true })
	if !closed {
		t.Fatal("canceled picker was not closed")
	}
}
