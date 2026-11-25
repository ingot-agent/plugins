package appcomponent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ingot-agent/sdk/workspace"
)

func TestPrepareDefaultWorkspaceCreatesCanonicalDirectory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	root, err := prepareDefaultWorkspace(state, "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(state, defaultWorkspaceDirectory)
	if root != want {
		t.Fatalf("default workspace = %q, want %q", root, want)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("default workspace info = %#v, %v", info, err)
	}
}

func TestCanonicalWorkspaceRootResolvesSymlinksAndRejectsFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err == nil {
		got, err := canonicalWorkspaceRoot(link)
		if err != nil || got != target {
			t.Fatalf("canonical symlink = %q, %v", got, err)
		}
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalWorkspaceRoot(file); err == nil {
		t.Fatal("file was accepted as a workspace")
	}
}

func TestPrepareDefaultWorkspaceUsesConfiguredDirectory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	configured := filepath.Join(t.TempDir(), "agent", "work")
	root, err := prepareDefaultWorkspace(state, configured)
	if err != nil {
		t.Fatal(err)
	}
	if root != configured {
		t.Fatalf("default workspace = %q, want %q", root, configured)
	}
	info, err := os.Stat(configured)
	if err != nil || !info.IsDir() {
		t.Fatalf("configured workspace info = %#v, %v", info, err)
	}
}

func TestPrepareDefaultWorkspaceRejectsRelativeDirectory(t *testing.T) {
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	working := t.TempDir()
	if err := os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if _, err := prepareDefaultWorkspace(t.TempDir(), "relative/workspace"); !errors.Is(err, workspace.ErrInvalidBinding) {
		t.Fatalf("relative workspace error = %v, want ErrInvalidBinding", err)
	}
	// A rejected path must not create a directory relative to the process.
	entries, err := os.ReadDir(working)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("relative workspace left %d entries behind", len(entries))
	}
}

func TestWorkspaceStateAppliesImmediately(t *testing.T) {
	settings := &workspaceState{}
	if got := settings.get(); got != "" {
		t.Fatalf("zero value = %q, want empty", got)
	}
	settings.set("/first")
	if got := settings.get(); got != "/first" {
		t.Fatalf("first = %q", got)
	}
	settings.set("/second")
	if got := settings.get(); got != "/second" {
		t.Fatalf("second = %q", got)
	}
	// A missing holder is a configuration bug, not a panic on the request path.
	var absent *workspaceState
	if got := absent.get(); got != "" {
		t.Fatalf("nil getter = %q", got)
	}
	absent.set("/ignored")
}
