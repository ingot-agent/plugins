package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	openairesponses "github.com/ingot-agent/plugins/model-openai-responses"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
)

const replayOutput = `[
 {"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-state-one"},
 {"type":"message","id":"msg_1","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"before","annotations":[]}]},
 {"type":"function_call","id":"fc_1","status":"completed","call_id":"call_1","name":"shell","arguments":"{ \"cmd\": \"a && b > out\" }"},
 {"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"opaque-state-two"},
 {"type":"message","id":"msg_2","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"after","annotations":[]}]}
]`
const replayResponse = `{"object":"response","status":"completed","model":"resolved-model","output":` + replayOutput + `}`

func replayStream() string {
	return strings.Join([]string{
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"before"}`,
		`data: {"type":"response.output_text.done","output_index":1,"content_index":0,"text":"before"}`,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{ \"cmd\": \"a && b > out\" }"}}`,
		`data: {"type":"response.output_text.delta","output_index":4,"content_index":0,"delta":"after"}`,
		`data: {"type":"response.output_text.done","output_index":4,"content_index":0,"text":"after"}`,
		// SSE data must occupy one line; encrypted state only arrives at completion.
		`data: {"type":"response.completed","response":` + strings.ReplaceAll(replayResponse, "\n", "") + `}`,
	}, "\n\n") + "\n\n"
}

type replayRequest struct {
	Input   []json.RawMessage `json:"input"`
	Include []string          `json:"include"`
	Store   bool              `json:"store"`
}

func TestHistoryReplayPreservesOutputAcrossRounds(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			var requests []replayRequest
			provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(_ context.Context, req *http.Request) (*http.Response, error) {
				var body replayRequest
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				requests = append(requests, body)
				response := replayResponse
				if stream {
					response = replayStream()
				}
				return httpResponse(200, response), nil
			}), assetResolver{})
			request := model.Request{Model: "alias-model", Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("run")}}}
			invoke := func() model.Response {
				var result model.Response
				var err error
				if stream {
					result, err = provider.Stream(context.Background(), request, func(event model.StreamEvent) error {
						if strings.Contains(event.TextDelta, "opaque-state") {
							t.Fatal("opaque replay rendered as text")
						}
						return nil
					})
				} else {
					result, err = provider.Complete(context.Background(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			first := invoke()
			if first.Message.Replay == nil || first.Message.Replay.Model != "alias-model" {
				t.Fatal("missing replay scope for requested model alias")
			}
			if text, _ := content.TextOnly(first.Message.Content); text != "beforeafter" {
				t.Fatalf("visible content = %q", text)
			}
			request.Messages = append(request.Messages, first.Message, model.Message{Role: model.RoleTool, ToolCallID: "call_1", Content: content.FromText("ok")})
			second := invoke()
			request.Messages = append(request.Messages, second.Message, model.Message{Role: model.RoleTool, ToolCallID: "call_1", Content: content.FromText("ok again")})
			invoke()
			var want []json.RawMessage
			if err := json.Unmarshal([]byte(replayOutput), &want); err != nil {
				t.Fatal(err)
			}
			for _, body := range requests {
				if body.Store || !reflect.DeepEqual(body.Include, []string{"reasoning.encrypted_content"}) {
					t.Fatal("stateless reasoning request options missing")
				}
			}
			if len(requests[1].Input) != len(want)+2 {
				t.Fatalf("input count = %d", len(requests[1].Input))
			}
			for i, item := range want {
				var a, b any
				json.Unmarshal(item, &a)
				json.Unmarshal(requests[1].Input[i+1], &b)
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("output item %d changed: %s", i, requests[1].Input[i+1])
				}
			}
			for i, item := range requests[1].Input {
				if !bytes.Equal(item, requests[2].Input[i]) {
					t.Fatalf("old input prefix changed at item %d", i)
				}
			}
		})
	}
}

func TestHistoryReplayRejectsMalformedAndIgnoresStaleOrForeignState(t *testing.T) {
	source := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(replayResponse), assetResolver{})
	response, err := source.Complete(context.Background(), model.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                      string
		mutate                    func(*model.Message)
		provider, endpoint, model string
		wantError                 bool
	}{
		{name: "edited text", mutate: func(m *model.Message) { m.Content = content.FromText("edited") }},
		{name: "filtered calls", mutate: func(m *model.Message) { m.ToolCalls = nil }},
		{name: "reformatted arguments", mutate: func(m *model.Message) { m.ToolCalls[0].Arguments = json.RawMessage(`{"cmd":"a && b > out"}`) }},
		{name: "provider", provider: "other"},
		{name: "endpoint", endpoint: "https://other.test"},
		{name: "model", model: "other"},
		{name: "unknown format", mutate: func(m *model.Message) { m.Replay.Format = "other.v1" }},
		{name: "malformed", mutate: func(m *model.Message) { m.Replay.Data = []byte(`{`) }, wantError: true},
		{name: "invalid output", mutate: func(m *model.Message) {
			m.Replay.Data = bytes.Replace(m.Replay.Data, []byte(`"type":"reasoning"`), []byte(`"type":"unknown"`), 1)
		}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := response.Message
			message.Content = content.Clone(message.Content)
			message.ToolCalls = append(message.ToolCalls[:0:0], message.ToolCalls...)
			message.Replay = message.Replay.Clone()
			if tc.mutate != nil {
				tc.mutate(&message)
			}
			name, endpoint, modelName := tc.provider, tc.endpoint, tc.model
			if name == "" {
				name = "p"
			}
			if endpoint == "" {
				endpoint = "https://example.test"
			}
			if modelName == "" {
				modelName = "m"
			}
			var captured []byte
			provider := newProvider(t, openairesponses.ProviderConfig{Name: name, BaseURL: endpoint}, clientFunc(func(_ context.Context, r *http.Request) (*http.Response, error) {
				captured, _ = io.ReadAll(r.Body)
				return httpResponse(200, replayResponse), nil
			}), assetResolver{})
			_, err := provider.Complete(context.Background(), model.Request{Model: modelName, Messages: []model.Message{message}})
			if tc.wantError {
				if !errors.Is(err, openairesponses.ErrInvalidRequest) || len(captured) != 0 {
					t.Fatalf("err=%v dispatched=%v", err, len(captured) != 0)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(captured, []byte("opaque-state")) || bytes.Contains(captured, []byte(`"phase"`)) {
				t.Fatal("stale or foreign replay was sent")
			}
			var body replayRequest
			if err := json.Unmarshal(captured, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Input) != 1+len(message.ToolCalls) {
				t.Fatal("stale replay overrode edited calls")
			}
			var first struct{ Content string }
			if err := json.Unmarshal(body.Input[0], &first); err != nil {
				t.Fatal(err)
			}
			if want, _ := content.TextOnly(message.Content); first.Content != want {
				t.Fatal("stale replay overrode edited text")
			}
		})
	}
}
