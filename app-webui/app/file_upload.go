package appcomponent

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	appbackend "github.com/ingot-agent/plugins/app-webui"
)

// Clipboard files carry bytes, not a trustworthy path on the Runtime host.
// The Agent owns file notices, so all clipboard attachments retain durable paths
// in their original order. Supported images are also imported into Assets.
func (a *application) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !utf8.ValidString(name) || !filepath.IsLocal(name) || name == "." || strings.ContainsAny(name, "/\\\x00") {
		writeAPIError(w, http.StatusBadRequest, "invalid_file_name", "a file name without directory components is required")
		return
	}
	if r.ContentLength < 0 {
		writeAPIError(w, http.StatusLengthRequired, "file_size_required", "Content-Length is required")
		return
	}
	if r.ContentLength > a.config.MaxAssetBytes {
		writeError(w, errLocalFileTooLarge)
		return
	}
	if err := os.MkdirAll(a.uploadDirectory, 0o700); err != nil {
		writeError(w, err)
		return
	}
	directory, err := os.MkdirTemp(a.uploadDirectory, "file-")
	if err != nil {
		writeError(w, err)
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(directory)
		}
	}()
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		writeError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.config.MaxAssetBytes)
	written, err := io.Copy(file, r.Body)
	closeErr := file.Close()
	if err == nil && written != r.ContentLength {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = r.Context().Err()
	}
	if err != nil {
		writeUploadError(w, err)
		return
	}
	attachment, err := a.prepareLocalFile(r.Context(), appbackend.Attachment{Path: path})
	if err != nil {
		writeError(w, err)
		return
	}
	// Retain accepted files across sends, reloads and Runtime restarts because
	// conversation history and forks refer to their absolute paths.
	keep = true
	writeJSON(w, http.StatusCreated, attachment)
}

func writeUploadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, errLocalFileTooLarge)
	case errors.Is(err, io.ErrUnexpectedEOF):
		writeAPIError(w, http.StatusBadRequest, "file_size_mismatch", "file size does not match Content-Length")
	default:
		writeError(w, err)
	}
}
