package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/workspace"
)

type stubFilePicker struct {
	paths    []string
	canceled bool
	err      error
	initial  string
	calls    int
}

func (p *stubFilePicker) Select(_ context.Context, initial string) ([]string, bool, error) {
	p.initial = initial
	p.calls++
	return append([]string(nil), p.paths...), p.canceled, p.err
}
func localTestFile(t *testing.T, root, name, text string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeFileSelectionKeepsNonImagesAtOriginalPaths(t *testing.T) {
	a := testApplication(t)
	root := t.TempDir()
	paths := []string{localTestFile(t, root, "report.pdf", "%PDF-1.4"), localTestFile(t, root, "sheet.xlsx", "sheet"), localTestFile(t, root, "recording.mp3", "audio"), localTestFile(t, root, "clip.mp4", "video"), localTestFile(t, root, "picture.svg", "<svg></svg>")}
	picker := &stubFilePicker{paths: paths}
	a.filePicker = picker
	assets := &uploadStore{}
	a.assets = assets
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/files/select", strings.NewReader(`{}`)))
	var result struct {
		Files []appbackend.Attachment `json:"files"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Files) != len(paths) {
		t.Fatalf("selection=%d %s", w.Code, w.Body.String())
	}
	if picker.initial != a.defaultWorkspace || assets.calls != 0 {
		t.Fatalf("picker=%+v uploads=%d", picker, assets.calls)
	}
	input, err := attachmentInput("files", result.Files)
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range input.Attachments {
		if file.Path != paths[i] || file.Media != nil || file.Name != filepath.Base(paths[i]) {
			t.Fatalf("file=%+v", file)
		}
	}
}

func TestFileAndImageInputPersistAdjacentStandardUserMessages(t *testing.T) {
	a := testApplication(t)
	root := t.TempDir()
	path := localTestFile(t, root, "notes.md", "notes")
	created, err := a.sessions.Create(context.Background(), "attachments", workspace.Binding{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	request := createTurnRequest{SessionID: created.ID, Input: "inspect", Attachments: []appbackend.Attachment{{Kind: "file", Path: path, Name: "notes.md", MIMEType: "text/markdown", Size: 5}, {Kind: "image", AssetID: "image", Name: "image.png", MIMEType: "image/png"}}}
	body, _ := json.Marshal(request)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/turns", strings.NewReader(string(body))))
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit=%d %s", w.Code, w.Body.String())
	}
	var snapshot appbackend.TurnSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	waitTurnStatus(t, a.turns, snapshot.ID, "succeeded")
	messages, err := a.agent.History(context.Background(), session.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Role != "user" || messages[1].Role != "user" || !strings.Contains(messages[0].Content[0].Text, "notes.md") {
		t.Fatalf("messages=%+v", messages)
	}
	if len(messages[1].Content) != 2 || messages[1].Content[0].Text != "inspect" || messages[1].Content[1].Kind != content.KindImage {
		t.Fatalf("user=%+v", messages[1])
	}
	entries, err := a.sessions.(*defaultSessionController).store.Load(context.Background(), session.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Kind != "agent.message" {
			t.Fatalf("separate plugin upload route remains: %s", entry.Kind)
		}
	}
}

func TestFileValidationAndAvailabilityWithoutPluginWriter(t *testing.T) {
	a := testApplication(t)
	a.filePicker = &stubFilePicker{}
	state := httptest.NewRecorder()
	a.routes().ServeHTTP(state, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var snapshot appbackend.StateSnapshot
	if json.Unmarshal(state.Body.Bytes(), &snapshot) != nil || !snapshot.Files.Available {
		t.Fatalf("file selection unavailable: %s", state.Body.String())
	}
	for _, path := range []string{"relative.txt", t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		if _, err := a.inspectLocalFile(path); !errors.Is(err, errInvalidLocalFile) {
			t.Fatalf("path=%s err=%v", path, err)
		}
	}
	for _, attachment := range []appbackend.Attachment{{Kind: "file"}, {Kind: "audio", AssetID: "legacy"}, {Kind: "image"}} {
		if _, err := attachmentInput("", []appbackend.Attachment{attachment}); !errors.Is(err, agent.ErrInvalidMessage) {
			t.Fatal(err)
		}
	}
}
func TestNativeFileSelectionCancellationFailuresAndSharedDialogGate(t *testing.T) {
	a := testApplication(t)
	picker := &stubFilePicker{canceled: true}
	a.filePicker = picker
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/files/select", strings.NewReader(`{}`)))
		return w
	}
	if w := request(); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"files":[]`) {
		t.Fatalf("cancellation=%d %s", w.Code, w.Body.String())
	}
	a.workspacePickerMu.Lock()
	w := request()
	a.workspacePickerMu.Unlock()
	if w.Code != http.StatusConflict || picker.calls != 1 {
		t.Fatalf("dialog gate=%d calls=%d", w.Code, picker.calls)
	}
	picker.err = errors.New("dialog failed")
	if w := request(); w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "file_picker_failed") {
		t.Fatalf("picker failure=%d %s", w.Code, w.Body.String())
	}
	picker.err = errFilePickerUnavailable
	if w := request(); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("picker unavailable=%d", w.Code)
	}
}
