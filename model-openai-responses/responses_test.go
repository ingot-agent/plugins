package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	openairesponses "github.com/ingot-agent/plugins/model-openai-responses"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

type clientFunc func(context.Context, *http.Request) (*http.Response, error)

func (f clientFunc) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return f(ctx, request)
}

type assetResolver struct {
	data map[string][]byte
}

func (r assetResolver) Stat(_ context.Context, reference asset.Reference) (asset.Info, error) {
	value, ok := r.data[reference.ID]
	if !ok {
		return asset.Info{}, errors.New("asset not found")
	}
	return asset.Info{Size: uint64(len(value))}, nil
}

func (r assetResolver) Open(_ context.Context, reference asset.Reference) (io.ReadCloser, error) {
	value, ok := r.data[reference.ID]
	if !ok {
		return nil, errors.New("asset not found")
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}

func dependencies(client httpx.Client) openairesponses.Dependencies {
	return openairesponses.Dependencies{HTTP: client, Assets: assetResolver{data: map[string][]byte{}}}
}

func TestCompleteMapsResponsesRequestAndResponse(t *testing.T) {
	var captured *http.Request
	var requestBody []byte
	client := clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		captured = request.Clone(context.Background())
		requestBody, _ = io.ReadAll(request.Body)
		return httpResponse(http.StatusOK, completedResponse("actual-model", "hello", `{"path":"."}`)), nil
	})
	headers := map[string]string{"X-Tenant": "one"}
	provider := newProvider(t, openairesponses.ProviderConfig{
		Name: "primary", BaseURL: "https://example.test/v1/", APIKey: "secret",
		Organization: "org", Project: "project", Models: []string{"requested-model"},
		DefaultHeaders: headers,
	}, client, assetResolver{data: map[string][]byte{}})
	headers["X-Tenant"] = "mutated"

	temperature := 0.25
	maxTokens := 128
	result, err := provider.Complete(context.Background(), model.Request{
		Model: "requested-model",
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: content.FromText("system")},
			{Role: model.RoleUser, Content: content.Content{
				content.Text("question"),
				content.Inline(content.KindImage, "image/png", "image.png", []byte{0, 1, 2}),
			}},
			{Role: model.RoleAssistant, Content: content.FromText("checking"), ToolCalls: []tool.Call{{
				ID: "call-previous", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`),
			}}},
			{Role: model.RoleTool, Content: content.FromText("contents"), ToolCallID: "call-previous"},
		},
		Tools: []tool.Definition{{
			Name: "read_file", Description: "Read one file.", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "primary" || result.Model != "actual-model" || result.FinishReason != "tool_calls" || !result.Usage.Reported || result.Usage.TotalTokens != 7 {
		t.Fatalf("result = %#v", result)
	}
	if text, ok := content.TextOnly(result.Message.Content); !ok || text != "hello" {
		t.Fatalf("content = %#v", result.Message.Content)
	}
	if len(result.Message.ToolCalls) != 1 || result.Message.ToolCalls[0].ID != "call-new" || result.Message.ToolCalls[0].Name != "list_files" || string(result.Message.ToolCalls[0].Arguments) != `{"path":"."}` {
		t.Fatalf("tool calls = %#v", result.Message.ToolCalls)
	}

	if captured.URL.String() != "https://example.test/v1/responses" {
		t.Fatalf("url = %s", captured.URL)
	}
	if captured.Header.Get("Authorization") != "Bearer secret" || captured.Header.Get("OpenAI-Organization") != "org" || captured.Header.Get("OpenAI-Project") != "project" || captured.Header.Get("X-Tenant") != "one" {
		t.Fatalf("headers = %v", captured.Header)
	}
	var payload struct {
		Model           string  `json:"model"`
		Instructions    string  `json:"instructions"`
		Stream          bool    `json:"stream"`
		Store           bool    `json:"store"`
		Temperature     float64 `json:"temperature"`
		MaxOutputTokens int     `json:"max_output_tokens"`
		Input           []struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
			Output    string          `json:"output"`
		} `json:"input"`
		Tools []struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "requested-model" || payload.Instructions != "system" || payload.Stream || payload.Store || payload.Temperature != 0.25 || payload.MaxOutputTokens != 128 {
		t.Fatalf("payload = %s", requestBody)
	}
	wantTypes := []string{"message", "message", "function_call", "function_call_output"}
	gotTypes := make([]string, len(payload.Input))
	for i := range payload.Input {
		gotTypes[i] = payload.Input[i].Type
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("input types = %v, want %v; payload = %s", gotTypes, wantTypes, requestBody)
	}
	if payload.Input[1].Role != "assistant" || payload.Input[2].CallID != "call-previous" || payload.Input[2].Name != "read_file" || payload.Input[2].Arguments != `{"path":"README.md"}` || payload.Input[3].Output != "contents" {
		t.Fatalf("conversation input = %s", requestBody)
	}
	var userContent []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	}
	if err := json.Unmarshal(payload.Input[0].Content, &userContent); err != nil {
		t.Fatal(err)
	}
	wantImage := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{0, 1, 2})
	if len(userContent) != 2 || userContent[0].Type != "input_text" || userContent[0].Text != "question" || userContent[1].Type != "input_image" || userContent[1].ImageURL != wantImage {
		t.Fatalf("user content = %#v", userContent)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Type != "function" || payload.Tools[0].Name != "read_file" || string(payload.Tools[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("tools = %#v", payload.Tools)
	}
	if bytes.Contains(requestBody, []byte(`"messages"`)) || bytes.Contains(requestBody, []byte(`"max_tokens"`)) {
		t.Fatalf("request contains Chat Completions fields: %s", requestBody)
	}
}

func TestCompleteOnlyPromotesLeadingSystemMessage(t *testing.T) {
	var requestBody []byte
	provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		requestBody, _ = io.ReadAll(request.Body)
		return httpResponse(http.StatusOK, `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[]}`), nil
	}), assetResolver{data: map[string][]byte{}})

	_, err := provider.Complete(context.Background(), model.Request{
		Model: "m",
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: content.Content{content.Text("global "), content.Text("instructions")}},
			{Role: model.RoleUser, Content: content.FromText("question")},
			{Role: model.RoleSystem, Content: content.FromText("historical system message")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Instructions string `json:"instructions"`
		Input        []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Instructions != "global instructions" {
		t.Fatalf("instructions = %q; payload = %s", payload.Instructions, requestBody)
	}
	if len(payload.Input) != 2 || payload.Input[0].Role != "user" || payload.Input[1].Role != "system" || payload.Input[1].Content != "historical system message" {
		t.Fatalf("input = %#v; payload = %s", payload.Input, requestBody)
	}
}

func TestCompleteMapsIncompleteFailureAndMissingUsage(t *testing.T) {
	t.Run("incomplete", func(t *testing.T) {
		body := `{"id":"resp_1","object":"response","status":"incomplete","model":"m","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","status":"incomplete","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(body), assetResolver{data: map[string][]byte{}})
		result, err := provider.Complete(context.Background(), model.Request{Model: "m"})
		if err != nil || result.FinishReason != "length" {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})

	t.Run("failed and redacted", func(t *testing.T) {
		const secret = "super-secret"
		body := `{"id":"resp_1","object":"response","status":"failed","model":"m","error":{"code":"server_error","message":"rejected ` + secret + `"},"output":[]}`
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test", APIKey: secret}, staticClient(body), assetResolver{data: map[string][]byte{}})
		_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
		var responseErr *openairesponses.ProviderResponseError
		if !errors.As(err, &responseErr) || responseErr.Status != "failed" || strings.Contains(err.Error(), secret) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("missing usage is unreported", func(t *testing.T) {
		body := `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[]}`
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(body), assetResolver{data: map[string][]byte{}})
		result, err := provider.Complete(context.Background(), model.Request{Model: "m"})
		if err != nil || result.Usage != (model.Usage{}) || result.FinishReason != "stop" {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})

	t.Run("invalid usage", func(t *testing.T) {
		body := `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":3}}`
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(body), assetResolver{data: map[string][]byte{}})
		_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
		if !errors.Is(err, openairesponses.ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestCompleteRejectsUnrepresentableSDKFields(t *testing.T) {
	clientCalls := 0
	provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		clientCalls++
		return nil, errors.New("must not be called")
	}), assetResolver{data: map[string][]byte{}})
	tests := []struct {
		name    string
		request model.Request
		want    error
	}{
		{name: "stop", request: model.Request{Model: "m", Stop: []string{}}, want: openairesponses.ErrInvalidRequest},
		{name: "message name", request: model.Request{Model: "m", Messages: []model.Message{{Role: model.RoleUser, Name: "named", Content: content.FromText("x")}}}, want: openairesponses.ErrInvalidRequest},
		{name: "tool media", request: model.Request{Model: "m", Messages: []model.Message{{Role: model.RoleTool, ToolCallID: "call", Content: content.Content{content.Inline(content.KindImage, "image/png", "x", []byte("x"))}}}}, want: content.ErrUnsupportedContent},
		{name: "assistant image", request: model.Request{Model: "m", Messages: []model.Message{{Role: model.RoleAssistant, Content: content.Content{content.Inline(content.KindImage, "image/png", "x", []byte("x"))}}}}, want: content.ErrUnsupportedContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := provider.Complete(context.Background(), test.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if clientCalls != 0 {
		t.Fatalf("HTTP calls = %d", clientCalls)
	}
}

func TestStreamMapsTypedResponsesEvents(t *testing.T) {
	terminal := strings.Replace(completedResponse("m", "hello", `{"x":1}`), `"output":[`, `"output":[{"id":"rs_1","type":"reasoning","status":"completed","summary":[]},`, 1)
	sse := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"m","output":[]}}`,
		``,
		`event: response.reasoning_summary_part.added`,
		`data: {"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		``,
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"think"}`,
		``,
		`event: response.reasoning_summary_text.done`,
		`data: {"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"text":"think"}`,
		``,
		`event: response.reasoning_text.delta`,
		`data: {"type":"response.reasoning_text.delta","output_index":0,"content_index":1,"delta":"raw"}`,
		``,
		`event: response.reasoning_text.done`,
		`data: {"type":"response.reasoning_text.done","output_index":0,"content_index":1,"text":"raw"}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		``,
		`event: response.content_part.added`,
		`data: {"type":"response.content_part.added","output_index":1,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"hel"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"lo"}`,
		``,
		`event: response.output_text.done`,
		`data: {"type":"response.output_text.done","output_index":1,"content_index":0,"text":"hello"}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","status":"in_progress","call_id":"call-new","name":"list_files","arguments":""}}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"x\":"}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","output_index":2,"delta":"1}"}`,
		``,
		`event: response.function_call_arguments.done`,
		`data: {"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"x\":1}"}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","status":"completed","call_id":"call-new","name":"list_files","arguments":"{\"x\":1}"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":` + terminal + `}`,
		``,
		`data: [DONE]`,
		``,
	}, "\r\n")
	var requestBody []byte
	provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		requestBody, _ = io.ReadAll(request.Body)
		return httpResponse(http.StatusOK, sse), nil
	}), assetResolver{data: map[string][]byte{}}).(model.StreamingProvider)

	var events []model.StreamEvent
	result, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(event model.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(requestBody, []byte(`"stream":true`)) || !bytes.Contains(requestBody, []byte(`"store":false`)) {
		t.Fatalf("request = %s", requestBody)
	}
	wantKinds := []model.StreamEventKind{
		model.StreamPartStart, model.StreamPartDelta, model.StreamPartEnd,
		model.StreamPartStart, model.StreamPartDelta, model.StreamPartEnd,
		model.StreamPartStart, model.StreamPartDelta, model.StreamPartDelta, model.StreamPartEnd,
	}
	wantSemantics := []model.StreamSemantic{
		model.StreamSemanticReasoning, model.StreamSemanticReasoning, model.StreamSemanticReasoning,
		model.StreamSemanticReasoning, model.StreamSemanticReasoning, model.StreamSemanticReasoning,
		model.StreamSemanticContent, model.StreamSemanticContent, model.StreamSemanticContent, model.StreamSemanticContent,
	}
	gotKinds := make([]model.StreamEventKind, len(events))
	gotSemantics := make([]model.StreamSemantic, len(events))
	var deltas []string
	for i, event := range events {
		gotKinds[i] = event.Kind
		gotSemantics[i] = event.Semantic
		if event.Kind == model.StreamPartDelta {
			deltas = append(deltas, event.TextDelta)
		}
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) || !reflect.DeepEqual(gotSemantics, wantSemantics) || !reflect.DeepEqual(deltas, []string{"think", "raw", "hel", "lo"}) {
		t.Fatalf("events = %#v", events)
	}
	if text, ok := content.TextOnly(result.Message.Content); !ok || text != "hello" || result.FinishReason != "tool_calls" || len(result.Message.ToolCalls) != 1 || string(result.Message.ToolCalls[0].Arguments) != `{"x":1}` {
		t.Fatalf("result = %#v", result)
	}
}

func TestStreamRejectsMissingTerminalAndMismatchedFinal(t *testing.T) {
	t.Run("missing terminal", func(t *testing.T) {
		body := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"x\"}\n\n"
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(body), assetResolver{data: map[string][]byte{}}).(model.StreamingProvider)
		_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
		if !errors.Is(err, openairesponses.ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("mismatched final", func(t *testing.T) {
		body := strings.Join([]string{
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"x"}`,
			``,
			`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"x"}`,
			``,
			`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"m","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"y"}]}]}}`,
			``,
		}, "\n")
		provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, staticClient(body), assetResolver{data: map[string][]byte{}}).(model.StreamingProvider)
		_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
		if !errors.Is(err, openairesponses.ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestStreamPreservesHandlerErrorAndAppliesLimit(t *testing.T) {
	handlerErr := errors.New("handler stopped")
	body := &trackingBody{Reader: strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"x\"}\n\n")}
	provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	}), assetResolver{data: map[string][]byte{}}).(model.StreamingProvider)
	_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(event model.StreamEvent) error {
		if event.Kind == model.StreamPartStart {
			return handlerErr
		}
		return nil
	})
	if !errors.Is(err, handlerErr) || !body.isClosed() {
		t.Fatalf("error = %v, closed = %v", err, body.isClosed())
	}

	provider = newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test", MaxResponseBytes: 8}, staticClient(strings.Repeat("x", 9)), assetResolver{data: map[string][]byte{}}).(model.StreamingProvider)
	_, err = provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
	if !errors.Is(err, openairesponses.ErrResponseLimit) {
		t.Fatalf("limit error = %v", err)
	}
}

func TestConfigAndHTTPErrorBoundaries(t *testing.T) {
	_, _, err := openairesponses.New(context.Background(), withState(t, openairesponses.Config{Providers: []openairesponses.ProviderConfig{{
		Name: "p", BaseURL: "https://example.test", DefaultHeaders: map[string]string{"authorization": "bad"},
	}}}, dependencies(staticClient(`{}`))))
	if !errors.Is(err, openairesponses.ErrInvalidConfig) {
		t.Fatalf("owned header error = %v", err)
	}

	client := clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		result := httpResponse(http.StatusTooManyRequests, "abcdef")
		result.Header.Set("X-Request-Id", "request-1")
		return result, nil
	})
	provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test", MaxErrorBodyBytes: 4}, client, assetResolver{data: map[string][]byte{}})
	_, err = provider.Complete(context.Background(), model.Request{Model: "m"})
	var httpErr *openairesponses.ProviderHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests || httpErr.RequestID != "request-1" || httpErr.Body != "abcd" || !httpErr.Truncated {
		t.Fatalf("HTTP error = %#v, raw error = %v", httpErr, err)
	}
}

func TestProviderSupportsConcurrentCompleteAndStream(t *testing.T) {
	client := clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.Header.Get("Accept") == "text/event-stream" {
			terminal := `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[]}`
			return httpResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":"+terminal+"}\n\n"), nil
		}
		return httpResponse(http.StatusOK, `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[]}`), nil
	})
	provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, client, assetResolver{data: map[string][]byte{}})
	streaming := provider.(model.StreamingProvider)
	errorsFound := make(chan error, 20)
	var wait sync.WaitGroup
	for i := 0; i < 10; i++ {
		wait.Add(2)
		go func() {
			defer wait.Done()
			_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
			errorsFound <- err
		}()
		go func() {
			defer wait.Done()
			_, err := streaming.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamEvent) error { return nil })
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func completedResponse(modelName, text, arguments string) string {
	return `{"id":"resp_1","object":"response","status":"completed","model":"` + modelName + `","output":[` +
		`{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]},` +
		`{"id":"fc_1","type":"function_call","status":"completed","call_id":"call-new","name":"list_files","arguments":"` + strings.ReplaceAll(arguments, `"`, `\"`) + `"}` +
		`],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`
}

func staticClient(body string) httpx.Client {
	return clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return httpResponse(http.StatusOK, body), nil
	})
}

func newProvider(t *testing.T, cfg openairesponses.ProviderConfig, client httpx.Client, assets asset.Resolver) model.Provider {
	t.Helper()
	exports, _, err := openairesponses.New(context.Background(), withState(t, openairesponses.Config{Providers: []openairesponses.ProviderConfig{cfg}}, openairesponses.Dependencies{HTTP: client, Assets: assets}))
	if err != nil {
		t.Fatal(err)
	}
	return exports.Providers[0].Value
}

type trackingBody struct {
	io.Reader
	mu     sync.Mutex
	closed bool
}

func (b *trackingBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

func (b *trackingBody) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
