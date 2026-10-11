package appcomponent

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/session"
)

const maxFilePreviewBytes = 1 << 20
const maxFilePreviewLines = 20000

type fileRequest struct {
	Path      string `json:"path"`
	SessionID string `json:"sessionId,omitempty"`
	BasePath  string `json:"basePath,omitempty"`
}

type filePreview struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Text string `json:"text"`
	Size int64  `json:"size"`
}

type fileOpener interface {
	Open(context.Context, string) error
}

// File paths refer to the Runtime host, just like the native file picker.
// Relative references in a document use that document's directory; references
// in conversation output use the originating Session's Workspace.
func (a *application) resolveFile(ctx context.Context, input fileRequest) (string, error) {
	path := input.Path
	if path == "" || !utf8.ValidString(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", fmt.Errorf("file path is invalid: %w", errInvalidLocalFile)
	}
	if strings.HasPrefix(path, "//") || strings.HasPrefix(path, `\\`) || strings.Contains(path, "://") ||
		(runtime.GOOS != "windows" && len(path) > 1 && path[1] == ':') {
		return "", fmt.Errorf("file path must belong to this Runtime host: %w", errInvalidLocalFile)
	}
	root := a.defaultWorkspace
	if input.SessionID != "" {
		item, err := a.sessions.Get(ctx, session.ID(input.SessionID))
		if err != nil {
			return "", err
		}
		root = item.Workspace
	}
	if !filepath.IsAbs(path) {
		if input.BasePath != "" {
			if !filepath.IsAbs(input.BasePath) {
				return "", fmt.Errorf("document base path must be absolute: %w", errInvalidLocalFile)
			}
			root = filepath.Dir(input.BasePath)
		}
		if root == "" {
			return "", fmt.Errorf("this conversation has no workspace: %w", errInvalidLocalFile)
		}
		path = filepath.Join(root, path)
	}
	return filepath.EvalSymlinks(filepath.Clean(path))
}

func decodeFileRequest(w http.ResponseWriter, r *http.Request) (fileRequest, bool) {
	var input fileRequest
	// These endpoints read host files or launch a host application. Only accept
	// JSON from our own UI, not form submissions from another website.
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "json_required", "A JSON request is required.")
		return input, false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		value, err := url.Parse(origin)
		if err != nil || value.Host != r.Host || (value.Scheme != "http" && value.Scheme != "https") {
			writeAPIError(w, http.StatusForbidden, "invalid_origin", "File requests must come from this application.")
			return input, false
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeAPIError(w, http.StatusForbidden, "invalid_origin", "File requests must come from this application.")
		return input, false
	}
	if err := decodeJSON(w, r, &input); err != nil {
		return input, false
	}
	return input, true
}

func openRegularFile(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errInvalidLocalFile
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errInvalidLocalFile
	}
	return file, info, nil
}

func (a *application) handlePreviewFile(w http.ResponseWriter, r *http.Request) {
	noCache(w)
	input, ok := decodeFileRequest(w, r)
	if !ok {
		return
	}
	path, err := a.resolveFile(r.Context(), input)
	if err != nil {
		writeFileError(w, err)
		return
	}
	file, info, err := openRegularFile(path)
	if err != nil {
		writeFileError(w, err)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFilePreviewBytes+1))
	if err != nil {
		writeFileError(w, err)
		return
	}
	if len(data) > maxFilePreviewBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "preview_too_large", "This file exceeds the 1 MiB preview limit. Open it in a text application.")
		return
	}
	text, ok := decodePreviewText(data)
	if !ok {
		writeAPIError(w, http.StatusUnsupportedMediaType, "preview_not_text", "This file is binary or uses an unsupported text encoding. UTF-8 and UTF-16 with a BOM are supported.")
		return
	}
	if strings.Count(text, "\n") >= maxFilePreviewLines {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "preview_too_large", "This file exceeds the 20,000-line preview limit. Open it in a text application.")
		return
	}
	writeJSON(w, http.StatusOK, filePreview{Path: path, Name: filepath.Base(path), Text: text, Size: info.Size()})
}

func decodePreviewText(data []byte) (string, bool) {
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		if len(data)%2 != 0 {
			return "", false
		}
		var order binary.ByteOrder = binary.BigEndian
		if data[0] == 0xff {
			order = binary.LittleEndian
		}
		units := make([]uint16, (len(data)-2)/2)
		for i := range units {
			units[i] = order.Uint16(data[2+i*2:])
		}
		data = []byte(string(utf16.Decode(units)))
	} else {
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	}
	if !utf8.Valid(data) {
		return "", false
	}
	for _, b := range data {
		if b < 32 && b != '\t' && b != '\n' && b != '\r' && b != '\f' {
			return "", false
		}
	}
	return strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n"), true
}

func (a *application) handleOpenFile(w http.ResponseWriter, r *http.Request) {
	input, ok := decodeFileRequest(w, r)
	if !ok {
		return
	}
	path, err := a.resolveFile(r.Context(), input)
	if err != nil {
		writeFileError(w, err)
		return
	}
	file, _, err := openRegularFile(path)
	if err != nil {
		writeFileError(w, err)
		return
	}
	file.Close()
	if a.fileOpener == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "file_open_unavailable", "No local text application is available.")
		return
	}
	if err := a.fileOpener.Open(r.Context(), path); err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "file_open_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeFileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeAPIError(w, http.StatusNotFound, "file_not_found", "This file no longer exists or the path is incorrect.")
	case errors.Is(err, os.ErrPermission):
		writeAPIError(w, http.StatusForbidden, "file_permission_denied", "This file cannot be read with the Runtime's permissions.")
	case errors.Is(err, errInvalidLocalFile):
		writeAPIError(w, http.StatusBadRequest, "invalid_local_file", err.Error())
	default:
		writeError(w, err)
	}
}
