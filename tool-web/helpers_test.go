package toolweb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/tool"
)

type testScope string

func (s testScope) Dir() string { return string(s) }

type fakeHTTPResponse struct {
	status int
	header http.Header
	body   string
	err    error
}

type recordedRequest struct {
	method        string
	url           string
	authorization string
	contentType   string
	body          string
}

type fakeHTTP struct {
	mu        sync.Mutex
	responses []fakeHTTPResponse
	requests  []recordedRequest
}

func (f *fakeHTTP) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, recordedRequest{
		method:        request.Method,
		url:           request.URL.String(),
		authorization: request.Header.Get("Authorization"),
		contentType:   request.Header.Get("Content-Type"),
		body:          string(body),
	})
	var response fakeHTTPResponse
	if index < len(f.responses) {
		response = f.responses[index]
	} else {
		response = fakeHTTPResponse{status: http.StatusInternalServerError, body: "unexpected request"}
	}
	f.mu.Unlock()
	if response.err != nil {
		return nil, response.err
	}
	header := response.header
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: response.status,
		Status:     http.StatusText(response.status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(response.body)),
		Request:    request,
	}, nil
}

func (f *fakeHTTP) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeHTTP) request(index int) recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[index]
}

type fakeInteraction struct {
	mu       sync.Mutex
	requests []interaction.Request
	answers  map[string]interaction.Response
	err      error
	before   func(interaction.Request)
}

func (f *fakeInteraction) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	before := f.before
	err := f.err
	answers := f.answers
	f.mu.Unlock()
	if before != nil {
		before(request)
	}
	if err != nil {
		return interaction.Response{}, err
	}
	return answers[request.Name], nil
}

func (*fakeInteraction) Emit(context.Context, interaction.Event) error { return nil }
func (*fakeInteraction) Set(context.Context, interaction.State) error  { return nil }
func (*fakeInteraction) Clear(context.Context, string) error           { return nil }

func (f *fakeInteraction) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func newTestExports(t *testing.T, client httpx.Client, config Config, environmentKey string) Exports {
	t.Helper()
	dir := t.TempDir()
	if config != (Config{}) {
		if err := saveConfig(dir, config); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(apiKeyEnv, environmentKey)
	exports, cleanup, err := New(context.Background(), Dependencies{HTTP: client, State: testScope(dir)})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(func() { _ = cleanup(context.Background()) })
	}
	return exports
}

func toolByName(t *testing.T, exports Exports, name string) tool.Tool {
	t.Helper()
	for _, candidate := range exports.Tools {
		if candidate.Definition().Name == name {
			return candidate
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func invokeTool(t *testing.T, exports Exports, name, arguments string) (tool.Result, error) {
	t.Helper()
	return toolByName(t, exports, name).Invoke(context.Background(), tool.Invocation{
		Call: tool.Call{Name: name, Arguments: []byte(arguments)},
	})
}

func resultText(t *testing.T, result tool.Result) string {
	t.Helper()
	text, ok := content.TextOnly(result.Content)
	if !ok {
		t.Fatalf("result is not text-only: %#v", result.Content)
	}
	return text
}

func decodeJSONBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("decode request body %q: %v", body, err)
	}
	return value
}

func setupOperationFor(t *testing.T, exports Exports) operation.Operation {
	t.Helper()
	if len(exports.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(exports.Operations))
	}
	return exports.Operations[0]
}
