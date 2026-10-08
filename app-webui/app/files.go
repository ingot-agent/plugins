package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
)

var (
	errFilePickerUnavailable = errors.New("native file picker is unavailable")
	errFilePickerFailed      = errors.New("native file picker failed")
	errInvalidLocalFile      = errors.New("invalid local file")
	errLocalFileTooLarge     = errors.New("local file exceeds max_asset_bytes")
)

type filePicker interface {
	Select(context.Context, string) (paths []string, canceled bool, err error)
}

type nativeFilePicker struct{}

func (nativeFilePicker) Select(ctx context.Context, initialPath string) ([]string, bool, error) {
	return selectNativeFiles(ctx, initialPath)
}

func (a *application) handleSelectFiles(w http.ResponseWriter, r *http.Request) {
	var request struct {
		InitialPath string `json:"initialPath"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if a.filePicker == nil {
		writeError(w, errFilePickerUnavailable)
		return
	}
	initial := request.InitialPath
	if initial == "" {
		initial = a.defaultWorkspace
	}
	initial, err := canonicalWorkspaceRoot(initial)
	if err != nil {
		writeError(w, err)
		return
	}
	if !a.workspacePickerMu.TryLock() {
		writeError(w, errWorkspacePickerBusy)
		return
	}
	defer a.workspacePickerMu.Unlock()
	paths, canceled, err := a.filePicker.Select(r.Context(), initial)
	if err != nil {
		if !errors.Is(err, errFilePickerUnavailable) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("%w: %v", errFilePickerFailed, err)
		}
		writeError(w, err)
		return
	}
	files := make([]appbackend.Attachment, 0, len(paths))
	if !canceled {
		for _, path := range paths {
			file, err := a.inspectLocalFile(path)
			if err == nil && file.Kind == "image" && a.assets != nil {
				err = a.importLocalImage(r.Context(), &file)
			}
			if err != nil {
				writeError(w, err)
				return
			}
			files = append(files, file)
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Files []appbackend.Attachment `json:"files"`
	}{Files: files})
}

func (a *application) inspectLocalFile(path string) (appbackend.Attachment, error) {
	if !utf8.ValidString(path) || !filepath.IsAbs(path) {
		return appbackend.Attachment{}, fmt.Errorf("file requires a real absolute path: %w", errInvalidLocalFile)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return appbackend.Attachment{}, fmt.Errorf("resolve file %q: %w: %w", path, errInvalidLocalFile, err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return appbackend.Attachment{}, fmt.Errorf("resolve file %q: %w: %w", path, errInvalidLocalFile, err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return appbackend.Attachment{}, fmt.Errorf("path %q must identify an existing regular file: %w", path, errInvalidLocalFile)
	}
	if info.Size() > a.config.MaxAssetBytes {
		return appbackend.Attachment{}, fmt.Errorf("file %q: %w", path, errLocalFileTooLarge)
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(real)))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	kind := "file"
	if supportedImage(mimeType) {
		kind = "image"
	}
	return appbackend.Attachment{Kind: kind, MIMEType: mimeType, Name: filepath.Base(real), Path: real, Size: uint64(info.Size())}, nil
}

func supportedImage(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func (a *application) importLocalImage(ctx context.Context, file *appbackend.Attachment) error {
	reader, err := os.Open(file.Path)
	if err != nil {
		return fmt.Errorf("open image %q: %w", file.Path, err)
	}
	defer reader.Close()
	reference, info, err := a.assets.Put(ctx, asset.PutRequest{Body: reader, Size: file.Size})
	if err != nil {
		return err
	}
	if reference.ID == "" || info.Size != file.Size {
		return errors.New("asset store returned an invalid image reference or size")
	}
	file.AssetID = reference.ID
	return nil
}

func (a *application) prepareAttachments(ctx context.Context, requested []appbackend.Attachment) ([]content.Attachment, *agent.PluginInput, error) {
	images := make([]content.Attachment, 0, len(requested))
	files := make([]appbackend.Attachment, 0, len(requested))
	for _, item := range requested {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if item.Path != "" {
			file, err := a.inspectLocalFile(item.Path)
			if err != nil {
				return nil, nil, err
			}
			file.AssetID = item.AssetID
			if file.Kind == "image" && file.AssetID == "" && a.assets != nil {
				if err := a.importLocalImage(ctx, &file); err != nil {
					return nil, nil, err
				}
			}
			files = append(files, file)
			item = file
		} else if item.Kind != "image" || !supportedImage(item.MIMEType) {
			return nil, nil, fmt.Errorf("non-image attachments require a host-local path: %w", errInvalidLocalFile)
		}
		if item.Kind == "image" && item.AssetID != "" {
			images = append(images, content.Attachment{Kind: content.KindImage, Media: content.Media{MIMEType: item.MIMEType, Name: item.Name,
				Source: content.Source{Kind: content.SourceAsset, Asset: asset.Reference{ID: item.AssetID}}}})
		} else if item.Path == "" {
			return nil, nil, fmt.Errorf("image attachment requires an asset: %w", content.ErrInvalidContent)
		}
	}
	if err := content.ValidateAttachments(images); err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return images, nil, nil
	}
	// The body describes all selected files; only supported images are sent as
	// model media. Non-image files remain at their original host-local paths.
	for i := range files {
		files[i].AssetID = ""
	}
	encoded, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	plugin := agent.PluginInput{Plugin: "app.backend", Text: string(encoded) + "\n\n\u4ee5\u4e0a\u662f\u7d27\u968f\u5176\u540e\u7684\u7528\u6237\u8f93\u5165\u6240\u4e0a\u4f20\u7684\u6587\u4ef6\u3002"}
	return images, &plugin, nil
}
