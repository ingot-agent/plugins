package openaicompat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	openaicompat "github.com/ingot-agent/plugins/model-openai-compatible"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

func TestToolImagesPreserveCallGroupsAndHistory(t *testing.T) {
	for _, mode := range []string{"complete", "stream"} {
		for _, history := range []string{"last round", "earlier round"} {
			t.Run(mode+"/"+history, func(t *testing.T) {
				var body []byte
				client := clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
					body, _ = io.ReadAll(request.Body)
					if mode == "stream" {
						return response(http.StatusOK, "data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"seen\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
					}
					return response(http.StatusOK, `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"seen"},"finish_reason":"stop"}]}`), nil
				})
				resolver := &observedResolver{data: map[string][]byte{"asset-1": []byte("asset")}}
				exports, _, err := openaicompat.New(context.Background(), withState(t, openaicompat.Config{Providers: []openaicompat.ProviderConfig{{
					Name: "p", BaseURL: "https://example.test", MaxAssetBytes: 32,
				}}}, openaicompat.Dependencies{HTTP: client, Assets: resolver}))
				if err != nil {
					t.Fatal(err)
				}
				request := model.Request{Model: "m", Messages: []model.Message{
					{Role: model.RoleAssistant, ToolCalls: []tool.Call{
						{ID: "call-1", Name: "render_image", Arguments: json.RawMessage(`{}`)},
						{ID: "call-2", Name: "render_image", Arguments: json.RawMessage(`{}`)},
						{ID: "call-3", Name: "read_file", Arguments: json.RawMessage(`{}`)},
					}},
					{Role: model.RoleTool, ToolCallID: "call-1", Content: content.Content{
						content.Text("before"),
						content.Inline(content.KindImage, "image/png", "inline.png", []byte{0, 1, 2}),
						content.Text("between"),
						content.URI(content.KindImage, "image/jpeg", "remote.jpg", "https://cdn.example/image.jpg"),
						content.AssetPart(content.KindImage, "image/webp", "stored.webp", asset.Reference{ID: "asset-1"}),
						content.Text("after"),
					}},
					{Role: model.RoleTool, ToolCallID: "call-2", Content: content.Content{
						content.URI(content.KindImage, "image/png", "second.png", "https://cdn.example/second.png"),
					}},
					{Role: model.RoleTool, ToolCallID: "call-3", Content: content.FromText("text result")},
				}}
				if history == "earlier round" {
					request.Messages = append(request.Messages,
						model.Message{Role: model.RoleAssistant, Content: content.FromText("previous answer")},
						model.Message{Role: model.RoleUser, Content: content.FromText("follow up")},
					)
				}
				before, _ := json.Marshal(request)
				provider := providerEntries(t, exports)[0]
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
					Stream   bool `json:"stream"`
					Messages []struct {
						Role       string          `json:"role"`
						ToolCallID string          `json:"tool_call_id"`
						Content    json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Stream != (mode == "stream") || len(wire.Messages) != len(request.Messages)+2 {
					t.Fatalf("unexpected request: %s", body)
				}
				if wire.Messages[0].Role != "assistant" {
					t.Fatalf("tool calls moved: %s", body)
				}
				for i, callID := range []string{"call-1", "call-2", "call-3"} {
					message := wire.Messages[i+1]
					var text string
					if message.Role != "tool" || message.ToolCallID != callID || json.Unmarshal(message.Content, &text) != nil {
						t.Fatalf("tool result group interrupted: %s", body)
					}
					if i == 0 {
						// Captions and image markers retain the original content order.
						last := -1
						for _, fragment := range []string{"before", "Image 1", "between", "Image 2", "Image 3", "after"} {
							next := strings.Index(text, fragment)
							if next <= last {
								t.Fatalf("missing/reordered %q in %q", fragment, text)
							}
							last = next
						}
					} else if i == 2 && text != "text result" {
						t.Fatalf("text-only output changed: %q", text)
					}
				}
				for i, urls := range [][]string{
					{"data:image/png;base64,AAEC", "https://cdn.example/image.jpg", "data:image/webp;base64,YXNzZXQ="},
					{"https://cdn.example/second.png"},
				} {
					message := wire.Messages[i+4]
					var parts []struct {
						Type     string `json:"type"`
						Text     string `json:"text"`
						ImageURL struct {
							URL string `json:"url"`
						} `json:"image_url"`
					}
					if message.Role != "user" || message.ToolCallID != "" || json.Unmarshal(message.Content, &parts) != nil || len(parts) != 2*len(urls) {
						t.Fatalf("invalid image attachment: %s", body)
					}
					callID := request.Messages[i+1].ToolCallID
					for j, url := range urls {
						if parts[2*j].Type != "text" || !strings.Contains(parts[2*j].Text, callID) ||
							parts[2*j+1].Type != "image_url" || parts[2*j+1].ImageURL.URL != url {
							t.Fatalf("image lost or associated with wrong call: %s", message.Content)
						}
					}
				}
				if history == "earlier round" && (wire.Messages[6].Role != "assistant" || string(wire.Messages[6].Content) != `"previous answer"` ||
					wire.Messages[7].Role != "user" || string(wire.Messages[7].Content) != `"follow up"`) {
					t.Fatalf("history reordered: %s", body)
				}
				if resolver.stats != 1 || resolver.opens != 1 || resolver.closes != 1 {
					t.Fatalf("asset calls stat=%d open=%d close=%d", resolver.stats, resolver.opens, resolver.closes)
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
				resolver := &observedResolver{data: map[string][]byte{"large": []byte("four")}}
				exports, _, err := openaicompat.New(context.Background(), withState(t, openaicompat.Config{Providers: []openaicompat.ProviderConfig{{
					Name: "p", BaseURL: "https://example.test", MaxAssetBytes: 3,
				}}}, openaicompat.Dependencies{Assets: resolver, HTTP: clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
					t.Error("invalid tool output reached HTTP client")
					return nil, errors.New("must not dispatch")
				})}))
				if err != nil {
					t.Fatal(err)
				}
				request := model.Request{Model: "m", Messages: []model.Message{
					{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call", Name: "render_image", Arguments: json.RawMessage(`{}`)}}},
					{Role: model.RoleTool, ToolCallID: "call", Content: content.Content{content.Text("caption"), tt.part}},
				}}
				provider := providerEntries(t, exports)[0]
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
				if errors.As(err, &unsupported) && (unsupported.MessageIndex != 1 || unsupported.PartIndex != 1) {
					t.Fatalf("wrong source position: %#v", unsupported)
				}
				if resolver.opens != 0 {
					t.Fatal("invalid asset opened")
				}
			})
		}
	}
}
