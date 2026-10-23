// Package toolimage provides view_image for local image files.
package toolimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
	_ "golang.org/x/image/webp"
)

const (
	toolName     = "view_image"
	maxFileBytes = 20 * 1024 * 1024
	maxPixels    = 40 * 1000 * 1000
)

var (
	// ErrInvalidDependencies indicates missing construction dependencies.
	ErrInvalidDependencies = errors.New("invalid tool.image dependencies")
	// ErrInvalidArguments indicates malformed view_image arguments. This is
	// distinct from tool.ErrInvalidArguments, which is reserved for pre-dispatch rejection.
	ErrInvalidArguments = errors.New("invalid view_image arguments")
	errImageLimit       = errors.New("image exceeds byte limit")
)

// Dependencies supplies session-relative path resolution and durable images.
type Dependencies struct {
	Workspace workspace.Resolver
	Assets    asset.Store
}

// Exports contains the model-visible local image tool.
type Exports struct {
	Tools []tool.Tool
}

type imageTool struct {
	workspace workspace.Resolver
	assets    asset.Store
	slots     chan struct{}
}

// New creates a concurrency-safe tool. Limits are fixed; no configuration,
// plugin-owned state files or background workers are created.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.Workspace) || isNil(deps.Assets) {
		return Exports{}, nil, ErrInvalidDependencies
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	return Exports{Tools: []tool.Tool{&imageTool{
		workspace: deps.Workspace, assets: deps.Assets, slots: make(chan struct{}, 2),
	}}}, nil, nil
}

func (*imageTool) Definition() tool.Definition {
	return tool.Definition{
		Name: toolName,
		Description: "View a local PNG, JPEG, WebP or GIF image. " +
			"path may be absolute or relative to the current session workspace. " +
			"Returns the image itself with its dimensions; GIFs show the first frame. " +
			"Maximum file size is 20 MiB and maximum image size is 40 million pixels. " +
			"Use a local file path, not a URL. Example: {\"path\":\"screenshots/page.png\"}.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"description":"Absolute local file path or path relative to the session workspace."}}}`),
	}
}

func (t *imageTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, errors.New("view_image: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if invocation.Call.Name != "" && invocation.Call.Name != toolName {
		return tool.Result{}, fmt.Errorf("call name %q: %w", invocation.Call.Name, ErrInvalidArguments)
	}
	path, err := decodePath(invocation.Call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	if strings.Contains(path, "://") {
		return businessResult(ctx, "use a local file path, not a URL")
	}
	target := filepath.Clean(path)
	if !filepath.IsAbs(target) {
		// A Windows drive-relative or rooted-without-drive path depends on
		// process state rather than the session workspace; require an explicit drive.
		if filepath.VolumeName(target) != "" || os.IsPathSeparator(target[0]) {
			return businessResult(ctx, "use a fully absolute path or a workspace-relative path")
		}
		binding, err := t.workspace.Resolve(ctx, invocation.Scope)
		if err != nil {
			return tool.Result{}, fmt.Errorf("view_image resolve workspace: %w", err)
		}
		if !filepath.IsAbs(binding.Root) {
			return tool.Result{}, fmt.Errorf("view_image workspace root must be absolute: %w", workspace.ErrInvalidBinding)
		}
		target = filepath.Join(binding.Root, target)
	}
	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	// Reject directories and special files before opening (e.g. a FIFO could
	// otherwise block). Inspect the opened handle again before reading.
	info, err := os.Stat(target)
	if err != nil {
		return fileError(ctx, path, err)
	}
	if !info.Mode().IsRegular() {
		return businessResult(ctx, fmt.Sprintf("path is not a regular file: %s", path))
	}
	if info.Size() > maxFileBytes {
		return businessResult(ctx, "file exceeds the 20 MiB limit")
	}
	file, err := os.Open(target)
	if err != nil {
		return fileError(ctx, path, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return fileError(ctx, path, err)
	}
	if !info.Mode().IsRegular() {
		return businessResult(ctx, "path is not a regular file")
	}
	if info.Size() > maxFileBytes {
		return businessResult(ctx, "file exceeds the 20 MiB limit")
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxFileBytes+1))
	if err != nil {
		return fileError(ctx, path, err)
	}
	if len(data) > maxFileBytes {
		return businessResult(ctx, "file exceeds the 20 MiB limit")
	}
	config, format, err := image.DecodeConfig(contextReader{ctx, bytes.NewReader(data)})
	if err != nil {
		return businessResult(ctx, "invalid or unsupported image; expected PNG, JPEG, WebP or GIF")
	}
	mimeType := imageMIME(format)
	if mimeType == "" {
		return businessResult(ctx, "unsupported image format; expected PNG, JPEG, WebP or GIF")
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width) > maxPixels/int64(config.Height) {
		return businessResult(ctx, "image dimensions are invalid or exceed 40 million pixels")
	}
	// Decode from the same bounded snapshot that will be stored. A valid
	// header alone does not establish that the image body is readable.
	decoded, _, err := image.Decode(contextReader{ctx, bytes.NewReader(data)})
	if err != nil {
		return businessResult(ctx, "image data is corrupt or unsupported")
	}
	// GIF frame bounds can be smaller than its logical screen. Report the
	// dimensions of the extracted frame that is actually returned.
	if format == "gif" {
		config.Width, config.Height = decoded.Bounds().Dx(), decoded.Bounds().Dy()
	}
	name := filepath.Base(target)
	summary := fmt.Sprintf("image: %s\nsize: %d x %d", path, config.Width, config.Height)
	if format == "gif" {
		// Model image inputs need a still image. Decode only the first GIF
		// frame, avoiding allocations for every frame of an animation.
		var output boundedBuffer
		if err := png.Encode(contextWriter{ctx, &output}, decoded); err != nil {
			if errors.Is(err, errImageLimit) {
				return businessResult(ctx, "GIF first frame exceeds the 20 MiB output limit")
			}
			return tool.Result{}, fmt.Errorf("view_image encode GIF first frame: %w", err)
		}
		data, mimeType = output.Bytes(), "image/png"
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".png"
		summary += "\nframe: first GIF frame"
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	reference, stored, err := t.assets.Put(ctx, asset.PutRequest{Body: bytes.NewReader(data), Size: uint64(len(data))})
	if err != nil {
		return tool.Result{}, fmt.Errorf("view_image store image: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if reference.ID == "" || stored.Size != uint64(len(data)) {
		return tool.Result{}, errors.New("view_image: asset store returned an invalid reference or size")
	}
	return tool.Result{Content: content.Content{
		content.Text(summary),
		content.AssetPart(content.KindImage, mimeType, name, reference),
	}}, nil
}

func decodePath(raw json.RawMessage) (string, error) {
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("arguments must be UTF-8: %w", ErrInvalidArguments)
	}
	var args struct {
		Path string `json:"path"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", fmt.Errorf("decode arguments: %w: %w", ErrInvalidArguments, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", fmt.Errorf("arguments must contain one JSON object: %w", ErrInvalidArguments)
	}
	if strings.TrimSpace(args.Path) == "" || strings.ContainsRune(args.Path, 0) {
		return "", fmt.Errorf("path must be a non-empty local file path: %w", ErrInvalidArguments)
	}
	return args.Path, nil
}

func imageMIME(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return ""
	}
}

func businessResult(ctx context.Context, message string) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: content.FromText("view_image error: " + message)}, nil
}

func fileError(ctx context.Context, path string, err error) (tool.Result, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return tool.Result{}, ctxErr
	}
	if errors.Is(err, os.ErrNotExist) {
		return businessResult(ctx, fmt.Sprintf("file not found: %s", path))
	}
	if errors.Is(err, os.ErrPermission) {
		return businessResult(ctx, fmt.Sprintf("permission denied: %s", path))
	}
	return tool.Result{}, fmt.Errorf("view_image read %q: %w", path, err)
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

type contextWriter struct {
	ctx context.Context
	io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.Writer.Write(p)
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > maxFileBytes-b.Len() {
		return 0, errImageLimit
	}
	return b.Buffer.Write(p)
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

var _ tool.Tool = (*imageTool)(nil)
