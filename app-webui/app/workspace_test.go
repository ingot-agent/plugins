package appcomponent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareDefaultWorkspaceCreatesCanonicalDirectory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	root, err := prepareDefaultWorkspace(state)
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
