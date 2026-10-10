package appcomponent

import (
	"context"
	"fmt"
	"os/exec"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/plugins/app-webui/internal/editorcmd"
)

// Read the editor preference for each explicit open action so saving it takes
// effect immediately, independently of the server settings requiring restart.
type configuredFileOpener struct {
	scopeDir string
}

func (opener configuredFileOpener) Open(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	config, err := appbackend.LoadConfig(opener.scopeDir)
	if err != nil {
		return err
	}
	arguments, err := editorcmd.Expand(config.Backend.TextEditorCommand, path)
	if err != nil {
		return err
	}
	if len(arguments) == 0 {
		return openNativeTextFile(ctx, path)
	}
	// The editor outlives this HTTP request. Reap its process when it exits,
	// without tying its lifetime to the request's cancellation or waiting for
	// the user to close the editor before returning the HTTP response.
	command := exec.Command(arguments[0], arguments[1:]...)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start text editor %q: %w", arguments[0], err)
	}
	go func() { _ = command.Wait() }()
	return nil
}
