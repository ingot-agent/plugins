package openairesponses_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	openairesponses "github.com/ingot-agent/plugins/model-openai-responses"
	"github.com/ingot-agent/sdk/model"
)

func TestStreamIgnoresKeepalive(t *testing.T) {
	frames := []string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hel"}`,
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"lo"}`,
		`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"hello"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"m","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		`data: [DONE]`,
	}
	stream := func(t *testing.T, body string) (model.Response, []model.StreamEvent) {
		t.Helper()
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(body), assetResolver{})
		var events []model.StreamEvent
		result, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(event model.StreamEvent) error {
			events = append(events, event)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return result, events
	}
	wantResult, wantEvents := stream(t, strings.Join(frames, "\n\n")+"\n\n")
	for _, tt := range []struct {
		name  string
		frame string
	}{
		{"comment", ": keepalive"},
		{"event only", "event: keepalive"},
		{"empty data", "event: keepalive\ndata:"},
		{"named JSON", "event: keepalive\ndata: {}"},
		{"named typed JSON", "event: keepalive\ndata: {\"type\":\"keepalive\"}"},
		{"typed JSON", `data: {"type":"keepalive"}`},
		{"multiline JSON", "event: keepalive\ndata: {\ndata: \"type\":\"keepalive\"\ndata: }"},
		{"CRLF", "event: keepalive\r\ndata: {\"type\":\"keepalive\"}\r"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Cover heartbeats before, between, and after response events, including
			// after the terminal event and [DONE], and a final heartbeat at EOF.
			var body strings.Builder
			for _, frame := range frames {
				body.WriteString(tt.frame + "\n\n" + frame + "\n\n")
			}
			body.WriteString(tt.frame)
			result, events := stream(t, body.String())
			if !reflect.DeepEqual(result, wantResult) || !reflect.DeepEqual(events, wantEvents) {
				t.Fatalf("heartbeats changed response or events: result = %#v, events = %#v", result, events)
			}
		})
	}
}

func TestStreamKeepalivePreservesProtocolErrors(t *testing.T) {
	heartbeat := "data: {\"type\":\"keepalive\"}\n\n"
	terminal := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"m\",\"output\":[]}}\n\n"
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"missing terminal", heartbeat, "stream ended before a terminal response event"},
		{"premature done", heartbeat + "data: [DONE]\n\n", "[DONE] is only valid after a terminal response event"},
		{"malformed JSON", heartbeat + "data: {\n\n", "decode SSE data"},
		{"missing type", heartbeat + "data: {}\n\n", "SSE event name and payload type are missing or inconsistent"},
		{"mismatched name", "event: response.created\ndata: {\"type\":\"keepalive\"}\n\n", "SSE event name and payload type are missing or inconsistent"},
		{"mismatched heartbeat", "event: keepalive\ndata: {\"type\":\"response.created\"}\n\n", "SSE event name and payload type are missing or inconsistent"},
		{"unknown event", heartbeat + "data: {\"type\":\"unknown\"}\n\n", `unsupported Responses API stream event "unknown"`},
		{"event after terminal", terminal + heartbeat + "data: {\"type\":\"response.created\"}\n\n", `stream event "response.created" arrived after terminal response`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(tt.body), assetResolver{})
			_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(event model.StreamEvent) error {
				t.Fatalf("unexpected stream event: %#v", event)
				return nil
			})
			if !errors.Is(err, openairesponses.ErrProtocol) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want protocol error containing %q", err, tt.want)
			}
		})
	}
}
