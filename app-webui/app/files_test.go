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
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
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
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeFileSelectionKeepsNonImagesAtOriginalPaths(t *testing.T) {
	a := testApplication(t)
	root := t.TempDir()
	paths := []string{
		localTestFile(t, root, "report.pdf", "%PDF-1.4"),
		localTestFile(t, root, "sheet.xlsx", "sheet"),
		localTestFile(t, root, "recording.mp3", "audio"),
		localTestFile(t, root, "clip.mp4", "video"),
		localTestFile(t, root, "picture.svg", "<svg></svg>"),
	}
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
		t.Fatalf("picker=%#v asset writes=%d", picker, assets.calls)
	}
	for i, file := range result.Files {
		if file.Path != paths[i] || file.Kind != "file" || file.AssetID != "" || file.Name != filepath.Base(paths[i]) {
			t.Fatalf("selected file=%#v", file)
		}
	}
	images, notice, err := a.prepareAttachments(context.Background(), result.Files)
	if err != nil || len(images) != 0 || notice == nil || assets.calls != 0 {
		t.Fatalf("images=%#v notice=%#v err=%v", images, notice, err)
	}
	for _, path := range paths {
		if !strings.Contains(notice.Text, strings.ReplaceAll(path, `\`, `\\`)) {
			t.Fatalf("file address missing: %s", notice.Text)
		}
	}
	if !strings.HasSuffix(notice.Text, "\u4ee5\u4e0a\u662f\u7d27\u968f\u5176\u540e\u7684\u7528\u6237\u8f93\u5165\u6240\u4e0a\u4f20\u7684\u6587\u4ef6\u3002") {
		t.Fatalf("association text missing: %s", notice.Text)
	}
}

func TestLocalFileOnlyAndMixedTurnsAppendNotices(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "file-only"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			a := testApplication(t)
			root := t.TempDir()
			pdf := localTestFile(t, root, "report.pdf", "%PDF-1.4")
			files := []appbackend.Attachment{{Kind: "file", Path: pdf}}
			input := ""
			if mixed {
				files = append(files, appbackend.Attachment{Kind: "image", Path: localTestFile(t, root, "photo.png", "image")})
				input = "compare these"
			}
			assets := &uploadStore{}
			a.assets = assets
			created, err := a.sessions.Create(context.Background(), "files", workspace.Binding{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			type submission struct {
				turn    agent.Turn
				entries []session.Entry
				err     error
			}
			received := make(chan submission, 1)
			store := a.sessions.(*defaultSessionController).store
			runtime := &testAgent{run: func(ctx context.Context, turn agent.Turn) (agent.Execution, error) {
				entries, err := store.Load(ctx, turn.SessionID)
				received <- submission{turn: turn, entries: entries, err: err}
				return agent.Execution{}, err
			}}
			a.turns.agent, err = newAgentController(ingotabi.Some[agent.Runtime](runtime), ingotabi.None[agent.StreamingRuntime](), runtime)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(createTurnRequest{SessionID: created.ID, Input: input, Attachments: files})
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/turns", strings.NewReader(string(body))))
			var response struct {
				ID string `json:"id"`
			}
			if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.ID == "" {
				t.Fatalf("turn response=%d %s", w.Code, w.Body.String())
			}
			select {
			case submitted := <-received:
				turn := submitted.turn
				if turn.Input != input {
					t.Fatalf("turn=%#v", turn)
				}
				entries := submitted.entries
				if submitted.err != nil || len(entries) != 1 || entries[0].Kind != testPluginInputKind {
					t.Fatalf("file notice was not persisted before Agent.Run: %#v err=%v", entries, submitted.err)
				}
				var notice agent.PluginInput
				if err := json.Unmarshal(entries[0].Payload, &notice); err != nil || notice.Plugin != "app.backend" || !strings.Contains(notice.Text, "report.pdf") {
					t.Fatalf("file notice=%#v err=%v", notice, err)
				}
				wantImages := 0
				if mixed {
					wantImages = 1
				}
				if len(turn.Attachments) != wantImages || assets.calls != wantImages {
					t.Fatalf("non-image leaked to model: %#v writes=%d", turn, assets.calls)
				}
				if mixed && turn.Attachments[0].Kind != content.KindImage {
					t.Fatal("image no longer uses media input")
				}
			case <-time.After(time.Second):
				t.Fatal("turn did not reach runtime")
			}
		})
	}
}

func TestFileNoticeAppendFailurePreventsTurnStart(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "not-committed"
		if committed {
			name = "committed-with-error"
		}
		t.Run(name, func(t *testing.T) {
			a := testApplication(t)
			root := t.TempDir()
			path := localTestFile(t, root, "file.txt", "data")
			created, err := a.sessions.Create(context.Background(), "files", workspace.Binding{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			store := a.sessions.(*defaultSessionController).store
			calls := 0
			a.pluginInputs = pluginInputWriterFunc(func(ctx context.Context, id session.ID, input agent.PluginInput) error {
				calls++
				if committed {
					if err := (testPluginInputs{store: store}).Append(ctx, id, input); err != nil {
						return err
					}
				}
				return errors.New("commit status unknown")
			})
			body, _ := json.Marshal(createTurnRequest{SessionID: created.ID, Attachments: []appbackend.Attachment{{Kind: "file", Path: path}}})
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/turns", strings.NewReader(string(body))))
			if w.Code != http.StatusInternalServerError || a.turns.nextID.Load() != 0 || calls != 1 {
				t.Fatalf("failed notice started a turn or retried: status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
			entries, err := store.Load(context.Background(), session.ID(created.ID))
			want := 0
			if committed {
				want = 1
			}
			if err != nil || len(entries) != want {
				t.Fatalf("durable progress changed after append failure: %#v err=%v", entries, err)
			}
		})
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

func TestLocalFilesRejectMissingRelativeDirectoriesOversizeAndLegacyMedia(t *testing.T) {
	a := testApplication(t)
	root := t.TempDir()
	for _, path := range []string{"relative.pdf", root, filepath.Join(root, "missing.pdf")} {
		if _, _, err := a.prepareAttachments(context.Background(), []appbackend.Attachment{{Kind: "file", Path: path}}); !errors.Is(err, errInvalidLocalFile) {
			t.Fatalf("path=%q err=%v", path, err)
		}
	}
	a.config.MaxAssetBytes = 1
	large := localTestFile(t, root, "large.txt", "xx")
	if _, _, err := a.prepareAttachments(context.Background(), []appbackend.Attachment{{Kind: "file", Path: large}}); !errors.Is(err, errLocalFileTooLarge) {
		t.Fatalf("oversize error=%v", err)
	}
	for _, kind := range []string{"file", "audio", "video"} {
		if _, _, err := a.prepareAttachments(context.Background(), []appbackend.Attachment{{Kind: kind, AssetID: "legacy"}}); !errors.Is(err, errInvalidLocalFile) {
			t.Fatalf("legacy media=%s err=%v", kind, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := a.prepareAttachments(ctx, []appbackend.Attachment{{Kind: "file", Path: large}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestMissingFileNoticeWriterPreventsTurnStart(t *testing.T) {
	a := testApplication(t)
	root := t.TempDir()
	path := localTestFile(t, root, "file.txt", "data")
	created, err := a.sessions.Create(context.Background(), "files", workspace.Binding{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	a.pluginInputs = nil
	a.filePicker = &stubFilePicker{}
	stateResponse := httptest.NewRecorder()
	a.routes().ServeHTTP(stateResponse, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var state appbackend.StateSnapshot
	if stateResponse.Code != http.StatusOK || json.Unmarshal(stateResponse.Body.Bytes(), &state) != nil || state.Files.Available {
		t.Fatalf("missing writer exposed file selection: %d %s", stateResponse.Code, stateResponse.Body.String())
	}
	body, _ := json.Marshal(createTurnRequest{SessionID: created.ID, Attachments: []appbackend.Attachment{{Kind: "file", Path: path}}})
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/turns", strings.NewReader(string(body))))
	if w.Code != http.StatusNotImplemented || a.turns.nextID.Load() != 0 {
		t.Fatalf("missing writer started a turn: status=%d body=%s", w.Code, w.Body.String())
	}
	entries, err := a.sessions.(*defaultSessionController).store.Load(context.Background(), session.ID(created.ID))
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing writer wrote history: %#v err=%v", entries, err)
	}
}
