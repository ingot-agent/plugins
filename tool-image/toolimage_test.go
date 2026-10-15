package toolimage_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	toolimage "github.com/ingot-agent/plugins/tool-image"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

type resolverFunc func(context.Context, execution.Scope) (workspace.Binding, error)

func (f resolverFunc) Resolve(ctx context.Context, scope execution.Scope) (workspace.Binding, error) {
	return f(ctx, scope)
}

type memoryStore struct {
	mu   sync.Mutex
	data map[string][]byte
	put  func(context.Context, asset.PutRequest) (asset.Reference, asset.Info, error)
}

func (s *memoryStore) Put(ctx context.Context, request asset.PutRequest) (asset.Reference, asset.Info, error) {
	if s.put != nil {
		return s.put(ctx, request)
	}
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return asset.Reference{}, asset.Info{}, err
	}
	if uint64(len(data)) != request.Size {
		return asset.Reference{}, asset.Info{}, errors.New("wrong size")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	id := fmt.Sprintf("image-%d", len(s.data))
	s.data[id] = data
	return asset.Reference{ID: id}, asset.Info{Size: uint64(len(data))}, nil
}

func (s *memoryStore) Stat(_ context.Context, ref asset.Reference) (asset.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.data[ref.ID]
	if !ok {
		return asset.Info{}, os.ErrNotExist
	}
	return asset.Info{Size: uint64(len(data))}, nil
}

func (s *memoryStore) Open(_ context.Context, ref asset.Reference) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.data[ref.ID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func newTool(t *testing.T, resolver workspace.Resolver, store asset.Store) tool.Tool {
	t.Helper()
	exports, _, err := toolimage.New(context.Background(), toolimage.Dependencies{Workspace: resolver, Assets: store})
	if err != nil {
		t.Fatal(err)
	}
	if len(exports.Tools) != 1 || exports.Tools[0].Definition().Name != "view_image" {
		t.Fatalf("unexpected tools: %#v", exports.Tools)
	}
	return exports.Tools[0]
}

func workspaceAt(root string) resolverFunc {
	return func(context.Context, execution.Scope) (workspace.Binding, error) {
		return workspace.Binding{Root: root}, nil
	}
}

func invoke(t *testing.T, implementation tool.Tool, path string) (tool.Result, error) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return implementation.Invoke(context.Background(), tool.Invocation{
		Scope: execution.Scope{SessionID: "session-1"},
		Call:  tool.Call{ID: "call-1", Name: "view_image", Arguments: args},
	})
}

func fixture(t *testing.T, format string) []byte {
	t.Helper()
	var encoded bytes.Buffer
	picture := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	picture.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var err error
	switch format {
	case "png":
		err = png.Encode(&encoded, picture)
	case "jpeg":
		err = jpeg.Encode(&encoded, picture, nil)
	case "gif":
		first := image.NewPaletted(picture.Bounds(), color.Palette{color.Black, color.White})
		second := image.NewPaletted(picture.Bounds(), first.Palette)
		first.SetColorIndex(0, 0, 1)
		err = gif.EncodeAll(&encoded, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{0, 0}})
	case "webp":
		// A one-pixel lossless WebP; no encoder or external fixture is needed.
		data, decodeErr := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4TBUAAAAvAAAAAAfQ//73v/+BiOh/AAA=")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return data
	default:
		t.Fatalf("unknown fixture format %s", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func writeFixture(t *testing.T, root, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestViewImageStoresActualImageContent(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "webp", "gif"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			original := fixture(t, format)
			path := writeFixture(t, root, "图片.data", original)
			store := &memoryStore{}
			implementation := newTool(t, workspaceAt(root), store)
			result, err := invoke(t, implementation, "图片.data")
			if err != nil {
				t.Fatal(err)
			}
			if err := content.Validate(result.Content); err != nil {
				t.Fatal(err)
			}
			if len(result.Content) != 2 || result.Content[0].Kind != content.KindText || result.Content[1].Kind != content.KindImage {
				t.Fatalf("expected text and actual image, got %#v", result)
			}
			media := result.Content[1].Media
			mime := "image/" + format
			if format == "gif" {
				mime = "image/png"
				if !strings.Contains(result.Content[0].Text, "first GIF frame") || media.Name != "图片.png" {
					t.Fatalf("missing GIF first-frame metadata: %#v", result)
				}
			} else if media.Name != "图片.data" {
				t.Fatalf("name = %q", media.Name)
			}
			if media.MIMEType != mime || media.Source.Kind != content.SourceAsset || len(media.Source.Data) != 0 || media.Source.URI != "" {
				t.Fatalf("media = %#v", media)
			}
			// Replacing the source must not change the image replayed later.
			writeFixture(t, root, filepath.Base(path), []byte("replaced"))
			reader, err := store.Open(context.Background(), media.Source.Asset)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			stored, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if format != "gif" && !bytes.Equal(stored, original) {
				t.Fatal("stored image differs from original snapshot")
			}
			decoded, _, err := image.Decode(bytes.NewReader(stored))
			if err != nil {
				t.Fatalf("stored image cannot be decoded: %v", err)
			}
			if format == "gif" && color.GrayModel.Convert(decoded.At(0, 0)).(color.Gray).Y != 255 {
				t.Fatal("GIF did not return the first frame")
			}
			bounds := decoded.Bounds()
			if !strings.Contains(result.Content[0].Text, fmt.Sprintf("%d x %d", bounds.Dx(), bounds.Dy())) {
				t.Fatalf("wrong dimensions: %s", result.Content[0].Text)
			}
		})
	}
}

func TestPathsUseSessionWorkspaceAndAllowAbsoluteFiles(t *testing.T) {
	root := t.TempDir()
	data := fixture(t, "png")
	writeFixture(t, root, "local.png", data)
	outside := writeFixture(t, t.TempDir(), "external.png", data)
	seen := 0
	implementation := newTool(t, resolverFunc(func(_ context.Context, scope execution.Scope) (workspace.Binding, error) {
		seen++
		if scope.SessionID != "session-1" {
			t.Fatalf("scope = %#v", scope)
		}
		return workspace.Binding{Root: root}, nil
	}), &memoryStore{})
	for _, path := range []string{"local.png", outside} {
		result, err := invoke(t, implementation, path)
		if err != nil || len(result.Content) != 2 {
			t.Fatalf("path=%q result=%#v error=%v", path, result, err)
		}
	}
	if seen != 1 {
		t.Fatalf("workspace resolutions = %d, want 1 for relative path only", seen)
	}
	implementation = newTool(t, resolverFunc(func(context.Context, execution.Scope) (workspace.Binding, error) {
		return workspace.Binding{}, workspace.ErrNotAssigned
	}), &memoryStore{})
	if _, err := invoke(t, implementation, "local.png"); !errors.Is(err, workspace.ErrNotAssigned) {
		t.Fatalf("unbound workspace error = %v", err)
	}
	if result, err := invoke(t, implementation, outside); err != nil || len(result.Content) != 2 {
		t.Fatalf("absolute path requires no workspace binding: result=%#v error=%v", result, err)
	}
}

func TestKnownFileFailuresReturnTextWithoutStoringAssets(t *testing.T) {
	root := t.TempDir()
	pngData := fixture(t, "png")
	writeFixture(t, root, "text.png", []byte("not an image"))
	writeFixture(t, root, "vector.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	writeFixture(t, root, "truncated.png", pngData[:33])
	largeHeader := bytes.Clone(pngData)
	binary.BigEndian.PutUint32(largeHeader[16:20], 40001)
	binary.BigEndian.PutUint32(largeHeader[20:24], 1000)
	binary.BigEndian.PutUint32(largeHeader[29:33], crc32.ChecksumIEEE(largeHeader[12:29]))
	writeFixture(t, root, "large-pixels.png", largeHeader)
	largeFile, err := os.Create(filepath.Join(root, "large-bytes.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := largeFile.Truncate(20*1024*1024 + 1); err != nil {
		largeFile.Close()
		t.Fatal(err)
	}
	largeFile.Close()
	store := &memoryStore{}
	implementation := newTool(t, workspaceAt(root), store)
	for _, tt := range []struct{ path, reason string }{
		{"missing.png", "file not found"}, {".", "not a regular file"},
		{"text.png", "invalid or unsupported"}, {"vector.svg", "invalid or unsupported"},
		{"truncated.png", "corrupt"}, {"large-pixels.png", "40 million pixels"},
		{"large-bytes.png", "20 MiB"}, {"https://example.test/image.png", "not a URL"},
		{"file:///tmp/image.png", "not a URL"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			result, err := invoke(t, implementation, tt.path)
			text, ok := content.TextOnly(result.Content)
			if err != nil || !ok || !strings.Contains(text, tt.reason) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
		})
	}
	if len(store.data) != 0 {
		t.Fatal("invalid images were stored")
	}
}

func TestArgumentAndDependencyErrors(t *testing.T) {
	root := t.TempDir()
	store := &memoryStore{}
	resolver := workspaceAt(root)
	implementation := newTool(t, resolver, store)
	for _, raw := range []string{"", `null`, `[]`, `{}`, `{"path":null}`, `{"path":1}`, `{"path":""}`,
		`{"path":"   "}`, `{"path":"a\u0000.png"}`, `{"path":"x","extra":true}`, `{"path":"x"}{}`, "{\"path\":\"\xff\"}"} {
		_, err := implementation.Invoke(context.Background(), tool.Invocation{Call: tool.Call{Name: "view_image", Arguments: json.RawMessage(raw)}})
		if !errors.Is(err, toolimage.ErrInvalidArguments) || errors.Is(err, tool.ErrInvalidArguments) {
			t.Fatalf("args=%q error=%v", raw, err)
		}
	}
	for _, deps := range []toolimage.Dependencies{
		{}, {Workspace: resolver}, {Assets: store},
		{Workspace: resolverFunc(nil), Assets: store}, {Workspace: resolver, Assets: (*memoryStore)(nil)},
	} {
		if _, _, err := toolimage.New(context.Background(), deps); !errors.Is(err, toolimage.ErrInvalidDependencies) {
			t.Fatalf("dependencies=%#v error=%v", deps, err)
		}
	}
	if _, _, err := toolimage.New(nil, toolimage.Dependencies{Workspace: resolver, Assets: store}); !errors.Is(err, toolimage.ErrInvalidDependencies) {
		t.Fatalf("nil context error=%v", err)
	}
}

func TestStoreFailuresAndCancellationRemainErrors(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "image.png", fixture(t, "png"))
	failure := errors.New("store failed")
	for _, tt := range []struct {
		name string
		put  func(context.Context, asset.PutRequest) (asset.Reference, asset.Info, error)
		want error
	}{
		{"failure", func(context.Context, asset.PutRequest) (asset.Reference, asset.Info, error) {
			return asset.Reference{}, asset.Info{}, failure
		}, failure},
		{"empty reference", func(_ context.Context, r asset.PutRequest) (asset.Reference, asset.Info, error) {
			return asset.Reference{}, asset.Info{Size: r.Size}, nil
		}, nil},
		{"wrong size", func(context.Context, asset.PutRequest) (asset.Reference, asset.Info, error) {
			return asset.Reference{ID: "wrong"}, asset.Info{}, nil
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			implementation := newTool(t, workspaceAt(root), &memoryStore{put: tt.put})
			result, err := invoke(t, implementation, path)
			if err == nil || len(result.Content) != 0 || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	implementation := newTool(t, workspaceAt(root), &memoryStore{put: func(context.Context, asset.PutRequest) (asset.Reference, asset.Info, error) {
		cancel()
		return asset.Reference{}, asset.Info{}, context.Canceled
	}})
	args, _ := json.Marshal(map[string]string{"path": path})
	invocation := tool.Invocation{Call: tool.Call{Name: "view_image", Arguments: args}}
	if _, err := implementation.Invoke(ctx, invocation); !errors.Is(err, context.Canceled) {
		t.Fatalf("store cancellation error=%v", err)
	}
	if _, err := implementation.Invoke(ctx, invocation); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled invocation error=%v", err)
	}
	if _, _, err := toolimage.New(ctx, toolimage.Dependencies{Workspace: workspaceAt(root), Assets: &memoryStore{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("construction cancellation error=%v", err)
	}
}

func TestConcurrentCallsAndCanceledWaiter(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "image.png", fixture(t, "png"))
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	implementation := newTool(t, workspaceAt(root), &memoryStore{put: func(ctx context.Context, request asset.PutRequest) (asset.Reference, asset.Info, error) {
		started <- struct{}{}
		select {
		case <-release:
			return asset.Reference{ID: "image"}, asset.Info{Size: request.Size}, nil
		case <-ctx.Done():
			return asset.Reference{}, asset.Info{}, ctx.Err()
		}
	}})
	args, _ := json.Marshal(map[string]string{"path": path})
	invocation := tool.Invocation{Call: tool.Call{Name: "view_image", Arguments: args}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := implementation.Invoke(ctx, invocation); results <- err }()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("parallel calls did not reach asset store")
		}
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if _, err := implementation.Invoke(waitCtx, invocation); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting invocation error=%v", err)
	}
	cancel()
	for range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("active invocation error=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("active invocation did not stop")
		}
	}
}
