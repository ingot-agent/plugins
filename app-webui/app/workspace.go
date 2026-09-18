package appcomponent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/workspace"
)

const defaultWorkspaceDirectory = "workspace"

var (
	errWorkspacePickerBusy        = errors.New("workspace picker is already open")
	errWorkspacePickerUnavailable = errors.New("native workspace picker is unavailable")
	errWorkspacePickerFailed      = errors.New("native workspace picker failed")
)

type workspacePicker interface {
	Select(context.Context, string) (path string, canceled bool, err error)
}

type nativeWorkspacePicker struct{}

func (nativeWorkspacePicker) Select(ctx context.Context, initialPath string) (string, bool, error) {
	return selectNativeWorkspace(ctx, initialPath)
}

func prepareDefaultWorkspace(stateDirectory string) (string, error) {
	if strings.TrimSpace(stateDirectory) == "" {
		return "", fmt.Errorf("plugin state directory is required: %w", appbackend.ErrInvalidConfig)
	}
	root, err := filepath.Abs(filepath.Join(stateDirectory, defaultWorkspaceDirectory))
	if err != nil {
		return "", fmt.Errorf("resolve default workspace: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create default workspace %q: %w", root, err)
	}
	return canonicalWorkspaceRoot(root)
}

func canonicalWorkspaceRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("workspace must be an absolute directory path: %w", workspace.ErrInvalidBinding)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve workspace %q: %w: %w", root, err, workspace.ErrInvalidBinding)
	}
	absolute, err := filepath.Abs(real)
	if err != nil {
		return "", fmt.Errorf("resolve workspace %q: %w: %w", root, err, workspace.ErrInvalidBinding)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect workspace %q: %w: %w", absolute, err, workspace.ErrInvalidBinding)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %q is not a directory: %w", absolute, workspace.ErrInvalidBinding)
	}
	return filepath.Clean(absolute), nil
}

func (a *application) workspaceBinding(root string) (workspace.Binding, error) {
	if strings.TrimSpace(root) == "" {
		root = a.defaultWorkspace
	}
	canonical, err := canonicalWorkspaceRoot(root)
	if err != nil {
		return workspace.Binding{}, err
	}
	return workspace.Binding{Root: canonical}, nil
}

func (a *application) ensureWorkspace(ctx context.Context, id session.ID) (appbackend.Session, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	item, err := a.sessions.Get(ctx, id)
	if err != nil || item.Workspace != "" {
		return item, err
	}
	item, err = a.sessions.AssignWorkspace(ctx, id, workspace.Binding{Root: a.defaultWorkspace})
	if errors.Is(err, workspace.ErrAlreadyAssigned) {
		return a.sessions.Get(ctx, id)
	}
	if err == nil {
		_ = a.backend.Events().Publish(appbackend.Event{Type: "session.workspace_assigned", Data: item})
	}
	return item, err
}

func (a *application) handleSelectWorkspace(w http.ResponseWriter, r *http.Request) {
	var request struct {
		InitialPath string `json:"initialPath"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	initial := request.InitialPath
	if strings.TrimSpace(initial) == "" {
		initial = a.defaultWorkspace
	}
	canonicalInitial, err := canonicalWorkspaceRoot(initial)
	if err != nil {
		writeError(w, err)
		return
	}
	if !a.workspacePickerMu.TryLock() {
		writeError(w, errWorkspacePickerBusy)
		return
	}
	defer a.workspacePickerMu.Unlock()
	path, canceled, err := a.workspacePicker.Select(r.Context(), canonicalInitial)
	if err != nil {
		if !errors.Is(err, errWorkspacePickerUnavailable) &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("%w: %v", errWorkspacePickerFailed, err)
		}
		writeError(w, err)
		return
	}
	if canceled {
		writeJSON(w, http.StatusOK, appbackend.WorkspaceSelection{})
		return
	}
	path, err = canonicalWorkspaceRoot(path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, appbackend.WorkspaceSelection{Path: &path})
}
