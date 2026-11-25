package toolmcp

import (
	"context"
	"encoding/json"
	"github.com/ingot-agent/sdk/workspace"
	"strings"
	"sync"
	"testing"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/tool"
)

// recordingConsumer captures observation details emitted during a call.
type recordingConsumer struct {
	mu      sync.Mutex
	details []observation.Detail
}

func (c *recordingConsumer) Emit(_ context.Context, detail observation.Detail) {
	c.mu.Lock()
	c.details = append(c.details, detail)
	c.mu.Unlock()
}

func (c *recordingConsumer) progress() []tool.Progress {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result []tool.Progress
	for _, detail := range c.details {
		if value, ok := detail.(observation.ToolProgress); ok {
			result = append(result, value.Progress)
		}
	}
	return result
}

func TestProgressNotificationsBecomeToolProgress(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	consumer := &recordingConsumer{}
	exports, cleanup, err := New(context.Background(), Dependencies{
		State:       testScope(dir),
		Observation: ingotabi.Some[observation.Consumer](consumer),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanupNow(cleanup)
	runDiscovery(t, exports)
	cleanupNow(cleanup)

	exports, cleanup, err = New(context.Background(), Dependencies{
		State:       testScope(dir),
		Observation: ingotabi.Some[observation.Consumer](consumer),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanupNow(cleanup)

	var target tool.Tool
	for _, candidate := range exports.Tools {
		if candidate.Definition().Name == "mcp_helper_progress" {
			target = candidate
		}
	}
	if target == nil {
		t.Fatalf("tools = %v", toolNames(exports.Tools))
	}

	result, err := target.Invoke(context.Background(), tool.Invocation{
		Call: tool.Call{ID: "call-42", Name: "mcp_helper_progress", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if text, ok := content.TextOnly(result.Content); !ok || text != "done" {
		t.Fatalf("result = %q", text)
	}

	// MCP progress notifications are independent of the response, so a
	// notification may be delivered just after the call returns.
	var progress []tool.Progress
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		progress = consumer.progress()
		if len(progress) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(progress) == 0 {
		t.Fatal("no progress notification was forwarded")
	}
	last := progress[len(progress)-1]
	if last.Channel != "helper" {
		t.Fatalf("channel = %q, want the server name", last.Channel)
	}
	text, ok := content.TextOnly(last.Content)
	if !ok || !strings.Contains(text, "halfway") || !strings.Contains(text, "1/2") {
		t.Fatalf("progress text = %q", text)
	}
}

func TestProgressWithoutMessageIsIgnored(t *testing.T) {
	consumer := &recordingConsumer{}
	config, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(t.TempDir(), ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), consumer, config)
	instance.emitProgress(context.Background(), "demo", nil)
	if len(consumer.details) != 0 {
		t.Fatalf("details = %#v", consumer.details)
	}
}

func TestMediaKind(t *testing.T) {
	cases := map[string]content.Kind{
		"image/png":       content.KindImage,
		"audio/wav":       content.KindAudio,
		"video/mp4":       content.KindVideo,
		"application/pdf": content.KindFile,
		"":                content.KindFile,
	}
	for mimeType, want := range cases {
		if got := mediaKind(mimeType); got != want {
			t.Fatalf("mediaKind(%q) = %d, want %d", mimeType, got, want)
		}
	}
}

func TestRingBufferBoundsAndPreservesRecentBytes(t *testing.T) {
	buffer := newRingBuffer(16)
	if _, err := buffer.Write([]byte("0123456789")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := buffer.String(); got != "0123456789" {
		t.Fatalf("buffer = %q", got)
	}
	if _, err := buffer.Write([]byte("abcdefghij")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := buffer.String(); got != "456789abcdefghij" {
		t.Fatalf("buffer = %q", got)
	}
}

func TestRingBufferReplacesInvalidUTF8(t *testing.T) {
	buffer := newRingBuffer(16)
	if _, err := buffer.Write([]byte{'a', 0xff, 'b'}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := buffer.String(); !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "b") {
		t.Fatalf("buffer = %q", got)
	}
}
