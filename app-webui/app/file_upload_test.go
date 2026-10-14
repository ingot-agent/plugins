package appcomponent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appbackend "github.com/ingot-agent/plugins/app-webui"
)

func uploadFileRequest(name, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/files/upload?name="+url.QueryEscape(name), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/octet-stream")
	return request
}

func TestClipboardImagesUseAssetsAndAgentFileNotices(t *testing.T) {
	for _, name := range []string{"screenshot.png", "photo.JPG", "animation.gif", "image.webp"} {
		t.Run(name, func(t *testing.T) {
			a := testApplication(t)
			assets := &uploadStore{}
			a.assets = assets
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, uploadFileRequest(name, "image"))
			var file appbackend.Attachment
			if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &file) != nil || file.Kind != "image" || file.Name != name || file.AssetID != "asset-one" || file.Path == "" || file.Size != 5 {
				t.Fatalf("image upload=%d %s", w.Code, w.Body.String())
			}
			input, err := attachmentInput("", []appbackend.Attachment{file})
			if err != nil || len(input.Attachments) != 1 || input.Attachments[0].Media == nil || input.Attachments[0].Path != file.Path || assets.calls != 1 || assets.body != "image" {
				t.Fatalf("image input=%#v notice=%#v writes=%d err=%v", input, nil, assets.calls, err)
			}
			if saved, err := os.ReadFile(file.Path); err != nil || string(saved) != "image" {
				t.Fatalf("image file=%q err=%v", saved, err)
			}
		})
	}
}

func TestClipboardDocumentsHaveDurableDistinctPaths(t *testing.T) {
	a := testApplication(t)
	var previous string
	for _, body := range []string{"first report", "second report"} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, uploadFileRequest("报告.txt", body))
		var file appbackend.Attachment
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &file) != nil || file.Name != "报告.txt" || file.Kind != "file" || file.Path == "" || file.Path == previous || file.Size != uint64(len(body)) {
			t.Fatalf("document upload=%d %s", w.Code, w.Body.String())
		}
		if filepath.Dir(filepath.Dir(file.Path)) != a.uploadDirectory {
			t.Fatalf("file escaped upload directory: %s", file.Path)
		}
		data, err := os.ReadFile(file.Path)
		if err != nil || string(data) != body {
			t.Fatalf("saved file=%q err=%v", data, err)
		}
		input, err := attachmentInput("", []appbackend.Attachment{file})
		if err != nil || len(input.Attachments) != 1 || input.Attachments[0].Media != nil || input.Attachments[0].Path != file.Path {
			t.Fatalf("file notice=%#v images=%#v err=%v", nil, input, err)
		}
		previous = file.Path
	}
}

func TestClipboardImageWithoutAssetsUsesFileNotice(t *testing.T) {
	a := testApplication(t)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, uploadFileRequest("screenshot.png", "image"))
	var file appbackend.Attachment
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &file) != nil || file.Path == "" || file.AssetID != "" {
		t.Fatalf("image path upload=%d %s", w.Code, w.Body.String())
	}
	input, err := attachmentInput("", []appbackend.Attachment{file})
	if err != nil || len(input.Attachments) != 1 || input.Attachments[0].Media != nil || input.Attachments[0].Path == "" {
		t.Fatalf("image path notice=%#v images=%#v err=%v", nil, input, err)
	}
}

func TestClipboardNonImageUploadsUseAgentFileNotices(t *testing.T) {
	a := testApplication(t)
	assets := &uploadStore{}
	a.assets = assets
	for _, name := range []string{"report.pdf", "image.svg", "image.avif"} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, uploadFileRequest(name, "data"))
		if w.Code != http.StatusCreated || assets.calls != 0 {
			t.Fatalf("unsupported upload=%d %s writes=%d", w.Code, w.Body.String(), assets.calls)
		}
	}
	if files, err := os.ReadDir(a.uploadDirectory); err != nil || len(files) != 3 {
		t.Fatalf("uploaded files=%v err=%v", files, err)
	}
}

func TestClipboardUploadValidatesNamesSizesAndCleansFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		file     string
		body     string
		length   int64
		canceled bool
		status   int
	}{
		{name: "missing-name", body: "a", length: 1, status: http.StatusBadRequest},
		{name: "parent", file: "../file.txt", body: "a", length: 1, status: http.StatusBadRequest},
		{name: "windows-path", file: `..\file.txt`, body: "a", length: 1, status: http.StatusBadRequest},
		{name: "absolute-path", file: "/file.txt", body: "a", length: 1, status: http.StatusBadRequest},
		{name: "unknown-size", file: "file.txt", body: "a", length: -1, status: http.StatusLengthRequired},
		{name: "oversize", file: "file.txt", body: "large", length: 5, status: http.StatusRequestEntityTooLarge},
		{name: "overflow-body", file: "file.txt", body: "large", length: 1, status: http.StatusRequestEntityTooLarge},
		{name: "short-body", file: "file.txt", body: "a", length: 3, status: http.StatusBadRequest},
		{name: "long-body", file: "file.txt", body: "abc", length: 1, status: http.StatusBadRequest},
		{name: "canceled", file: "file.txt", body: "a", length: 1, canceled: true, status: http.StatusRequestTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := testApplication(t)
			a.config.MaxAssetBytes = 3
			r := uploadFileRequest(test.file, test.body)
			r.ContentLength = test.length
			if test.canceled {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("invalid upload=%d %s", w.Code, w.Body.String())
			}
			files, err := os.ReadDir(a.uploadDirectory)
			if err != nil && !os.IsNotExist(err) || len(files) != 0 {
				t.Fatalf("failed upload left files=%v err=%v", files, err)
			}
		})
	}
}
