package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	openairesponses "github.com/ingot-agent/plugins/model-openai-responses"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

func TestToolOutputsPreserveImagesAndText(t *testing.T) {
	for _, mode := range []string{"complete", "stream"} {
		for _, tt := range []struct {
			name    string
			content content.Content
			output  string
		}{
			{"empty", nil, `""`},
			{"text", content.Content{content.Text("one"), content.Text("two")}, `"onetwo"`},
			{"image only", content.Content{content.Inline(content.KindImage, "image/png", "x.png", []byte{0, 1, 2})},
				`[{"type":"input_image","image_url":"data:image/png;base64,AAEC"}]`},
			{"empty text with image", content.Content{content.Text(""), content.Inline(content.KindImage, "image/png", "x.png", []byte{0, 1, 2})},
				`[{"type":"input_text","text":""},{"type":"input_image","image_url":"data:image/png;base64,AAEC"}]`},
			{"mixed sources", content.Content{
				content.Text("before"),
				content.Inline(content.KindImage, "image/png", "inline.png", []byte{0, 1, 2}),
				content.Text("between"),
				content.URI(content.KindImage, "image/jpeg", "remote.jpg", "https://cdn.example/image.jpg"),
				content.AssetPart(content.KindImage, "image/webp", "stored.webp", asset.Reference{ID: "asset-1"}),
				content.Text("after"),
			}, `[{"type":"input_text","text":"before"},{"type":"input_image","image_url":"data:image/png;base64,AAEC"},{"type":"input_text","text":"between"},{"type":"input_image","image_url":"https://cdn.example/image.jpg"},{"type":"input_image","image_url":"data:image/webp;base64,YXNzZXQ="},{"type":"input_text","text":"after"}]`},
		} {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				var body []byte
				client := clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
					body, _ = io.ReadAll(request.Body)
					result := `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[]}`
					if mode == "stream" {
						result = "data: {\"type\":\"response.completed\",\"response\":" + result + "}\n\n"
					}
					return httpResponse(http.StatusOK, result), nil
				})
				provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, client,
					assetResolver{data: map[string][]byte{"asset-1": []byte("asset")}})
				request := model.Request{Model: "m", Messages: []model.Message{
					{Role: model.RoleSystem, Content: content.FromText("instructions")},
					{Role: model.RoleAssistant, ToolCalls: []tool.Call{
						{ID: "call-image", Name: "render_image", Arguments: json.RawMessage(`{}`)},
						{ID: "call-text", Name: "read_file", Arguments: json.RawMessage(`{}`)},
					}},
					{Role: model.RoleTool, ToolCallID: "call-image", Content: tt.content},
					{Role: model.RoleTool, ToolCallID: "call-text", Content: content.FromText("text result")},
				}}
				before, _ := json.Marshal(request)
				var err error
				if mode == "stream" {
					_, err = provider.Stream(context.Background(), request, func(model.StreamEvent) error { return nil })
				} else {
					_, err = provider.Complete(context.Background(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
				after, _ := json.Marshal(request)
				if !bytes.Equal(before, after) {
					t.Fatal("provider changed canonical history")
				}
				var wire struct {
					Stream bool `json:"stream"`
					Input  []struct {
						Type   string          `json:"type"`
						CallID string          `json:"call_id"`
						Output json.RawMessage `json:"output"`
					} `json:"input"`
				}
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Stream != (mode == "stream") || len(wire.Input) != 4 {
					t.Fatalf("unexpected request: %s", body)
				}
				for i, callID := range []string{"call-image", "call-text"} {
					if wire.Input[i].Type != "function_call" || wire.Input[i].CallID != callID ||
						wire.Input[i+2].Type != "function_call_output" || wire.Input[i+2].CallID != callID {
						t.Fatalf("tool call association lost: %s", body)
					}
				}
				if string(wire.Input[2].Output) != tt.output || string(wire.Input[3].Output) != `"text result"` {
					t.Fatalf("unexpected outputs: %s", body)
				}
			})
		}
	}
}

func TestToolImagesValidateBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"complete", "stream"} {
		for _, tt := range []struct {
			name string
			part content.Part
			want error
		}{
			{"local URI", content.URI(content.KindImage, "image/png", "x", "file:///tmp/x.png"), content.ErrUnsupportedContent},
			{"invalid MIME", content.Inline(content.KindImage, "text/plain", "x", []byte("x")), content.ErrUnsupportedContent},
			{"oversized inline", content.Inline(content.KindImage, "image/png", "x", []byte("four")), content.ErrUnsupportedContent},
			{"oversized asset", content.AssetPart(content.KindImage, "image/png", "x", asset.Reference{ID: "large"}), content.ErrUnsupportedContent},
			{"missing asset", content.AssetPart(content.KindImage, "image/png", "x", asset.Reference{ID: "missing"}), nil},
			{"audio", content.Inline(content.KindAudio, "audio/wav", "x", []byte("x")), content.ErrUnsupportedContent},
		} {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test", MaxAssetBytes: 3},
					clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
						t.Error("invalid tool output reached HTTP client")
						return nil, errors.New("must not dispatch")
					}), assetResolver{data: map[string][]byte{"large": []byte("four")}})
				request := model.Request{Model: "m", Messages: []model.Message{
					{Role: model.RoleSystem, Content: content.FromText("instructions")},
					{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call", Name: "render_image", Arguments: json.RawMessage(`{}`)}}},
					{Role: model.RoleTool, ToolCallID: "call", Content: content.Content{content.Text("caption"), tt.part}},
				}}
				var err error
				if mode == "stream" {
					_, err = provider.Stream(context.Background(), request, func(model.StreamEvent) error { return nil })
				} else {
					_, err = provider.Complete(context.Background(), request)
				}
				if tt.want == nil {
					if err == nil || !strings.Contains(err.Error(), "stat model input asset") {
						t.Fatalf("missing asset error = %v", err)
					}
				} else if !errors.Is(err, tt.want) {
					t.Fatalf("error = %v, want %v", err, tt.want)
				}
				var unsupported *content.UnsupportedError
				if errors.As(err, &unsupported) && (unsupported.MessageIndex != 2 || unsupported.PartIndex != 1) {
					t.Fatalf("wrong source position: %#v", unsupported)
				}
			})
		}
	}
}
