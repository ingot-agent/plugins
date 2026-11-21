package toolweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/tool"
)

func TestWebSearchUsesDeepSeekContractAndFormatsResults(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{
		status: http.StatusOK,
		body: `{"results":[
			{"rerank_score":2.4,"title":"DeepSeek V4 released","url":"https://example.com/v4","snippet":"short","summary":"A longer model-oriented summary.","content":"# V4\n\nFull page body.","site_name":"Example News","site_icon":null,"date":"2026-06-01T00:00:00+00:00","authority":3},
			{"rerank_score":1.2,"title":"Second","url":"https://example.com/second","snippet":"fallback snippet","summary":"","content":"","site_name":null,"site_icon":null,"date":null,"authority":-1}
		]}`,
	}}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key", BaseURL: "https://api.deepseek.com"}, "")

	result, err := invokeTool(t, exports, searchToolName, `{"query":"DeepSeek \"V4\"","count":2,"ori_question":"What is new in V4?","need_content":true}`)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	for _, expected := range []string{
		`Search results for "DeepSeek \"V4\"": 2`,
		"DeepSeek V4 released",
		"URL: https://example.com/v4",
		"Site: Example News",
		"Published: 2026-06-01T00:00:00+00:00",
		"Authority: 3",
		"Relevance: 2.40",
		"A longer model-oriented summary.",
		"Full page body.",
		"fallback snippet",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("result missing %q:\n%s", expected, text)
		}
	}

	request := client.request(0)
	if request.method != http.MethodPost || request.url != "https://api.deepseek.com/v1/tools/search" {
		t.Fatalf("request = %#v", request)
	}
	if request.authorization != "Bearer secret-key" || request.contentType != "application/json" {
		t.Fatalf("headers = %#v", request)
	}
	body := decodeJSONBody(t, request.body)
	if body["query"] != `DeepSeek "V4"` || body["count"] != float64(2) || body["ori_question"] != "What is new in V4?" || body["need_content"] != true {
		t.Fatalf("body = %#v", body)
	}
}

func TestWebSearchOmitsContentUnlessRequested(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{
		status: http.StatusOK,
		body:   `{"results":[{"rerank_score":1,"title":"T","url":"https://example.com","snippet":"S","summary":"M","content":"SECRET PAGE BODY","site_name":null,"date":null,"authority":0}]}`,
	}}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	result, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, result); strings.Contains(text, "SECRET PAGE BODY") || strings.Contains(text, "Content:") {
		t.Fatalf("unexpected content:\n%s", text)
	}
	body := decodeJSONBody(t, client.request(0).body)
	if body["count"] != float64(10) || body["need_content"] != false {
		t.Fatalf("defaults body = %#v", body)
	}
}

func TestWebSearchEmptyResults(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{status: http.StatusOK, body: `{"results":[]}`}}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	result, err := invokeTool(t, exports, searchToolName, `{"query":"nothing"}`)
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, result); text != `No search results for "nothing".` {
		t.Fatalf("result = %q", text)
	}
}

func TestWebFetchUsesDeepSeekContract(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{
		status: http.StatusOK,
		body:   `{"title":"Example article","content":"# Example\n\nArticle body."}`,
	}}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	result, err := invokeTool(t, exports, fetchToolName, `{"url":"https://example.com/article?x=1"}`)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	for _, expected := range []string{"Title: Example article", "URL: https://example.com/article?x=1", "# Example", "Article body."} {
		if !strings.Contains(text, expected) {
			t.Fatalf("result missing %q:\n%s", expected, text)
		}
	}
	request := client.request(0)
	if request.url != "https://api.deepseek.com/v1/tools/fetch" {
		t.Fatalf("url = %q", request.url)
	}
	body := decodeJSONBody(t, request.body)
	if body["url"] != "https://example.com/article?x=1" || len(body) != 1 {
		t.Fatalf("body = %#v", body)
	}
}

func TestNotConfiguredIsABusinessResult(t *testing.T) {
	client := &fakeHTTP{}
	exports := newTestExports(t, client, Config{}, "")
	for _, name := range []string{searchToolName, fetchToolName} {
		arguments := `{"query":"q"}`
		if name == fetchToolName {
			arguments = `{"url":"https://example.com"}`
		}
		result, err := invokeTool(t, exports, name, arguments)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := resultText(t, result)
		if !strings.Contains(text, "not configured") || !strings.Contains(text, apiKeyEnv) || !strings.Contains(text, "tool-web config") {
			t.Fatalf("%s result = %q", name, text)
		}
	}
	if client.requestCount() != 0 {
		t.Fatalf("unexpected HTTP calls: %d", client.requestCount())
	}
}

func TestEnvironmentKeyIsUsedWhenConfigKeyIsEmpty(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{status: http.StatusOK, body: `{"results":[]}`}}}
	exports := newTestExports(t, client, Config{}, "environment-key")
	if _, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	if got := client.request(0).authorization; got != "Bearer environment-key" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestConfigKeyTakesPrecedenceOverEnvironment(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{status: http.StatusOK, body: `{"results":[]}`}}}
	exports := newTestExports(t, client, Config{APIKey: "config-key"}, "environment-key")
	if _, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	if got := client.request(0).authorization; got != "Bearer config-key" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestUnauthorizedIsNotRetried(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{
		status: http.StatusUnauthorized,
		body:   `{"error":{"message":"Authentication Fails","type":"authentication_error","param":null,"code":"invalid_api_key"}}`,
	}}}
	exports := newTestExports(t, client, Config{APIKey: "bad-key"}, "")
	result, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "API key is missing or invalid") || !strings.Contains(text, apiKeyEnv) {
		t.Fatalf("result = %q", text)
	}
	if client.requestCount() != 1 {
		t.Fatalf("requests = %d, want 1", client.requestCount())
	}
}

func TestRateLimitIsRetried(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{
		{status: http.StatusTooManyRequests, body: `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit"}}`},
		{status: http.StatusOK, body: `{"results":[]}`},
	}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	search := toolByName(t, exports, searchToolName).(*searchTool)
	search.client.backoff = time.Millisecond
	result, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, result), "No search results") {
		t.Fatalf("result = %q", resultText(t, result))
	}
	if client.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2", client.requestCount())
	}
}

func TestServerErrorIsRetriedThenReported(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{
		{status: http.StatusInternalServerError, body: `{"error":{"message":"boom","type":"server_error","code":"server_error"}}`},
		{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway","type":"server_error","code":"server_error"}}`},
		{status: http.StatusServiceUnavailable, body: `{"error":{"message":"unavailable","type":"server_error","code":"server_error"}}`},
	}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	search := toolByName(t, exports, searchToolName).(*searchTool)
	search.client.backoff = time.Millisecond
	result, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "DeepSeek service error (HTTP 503)") {
		t.Fatalf("result = %q", text)
	}
	if client.requestCount() != defaultMaxAttempts {
		t.Fatalf("requests = %d, want %d", client.requestCount(), defaultMaxAttempts)
	}
}

func TestBadRequestIsNotRetriedAndIncludesMessage(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{
		status: http.StatusBadRequest,
		body:   `{"error":{"message":"'count' must be between 1 and 50","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`,
	}}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	result, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "DeepSeek rejected the request") || !strings.Contains(text, "'count' must be between 1 and 50") {
		t.Fatalf("result = %q", text)
	}
	if client.requestCount() != 1 {
		t.Fatalf("requests = %d, want 1", client.requestCount())
	}
}

func TestFetchPolicyAndMissingPageErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		contains string
	}{
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":{"message":"blocked by policy","type":"security_error","code":"forbidden"}}`, contains: "security policy"},
		{name: "not found", status: http.StatusNotFound, body: `{"error":{"message":"no extractable content","type":"fetch_error","code":"not_found"}}`, contains: "could not retrieve usable page content"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeHTTP{responses: []fakeHTTPResponse{{status: test.status, body: test.body}}}
			exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
			result, err := invokeTool(t, exports, fetchToolName, `{"url":"https://example.com"}`)
			if err != nil {
				t.Fatal(err)
			}
			if text := resultText(t, result); !strings.Contains(text, test.contains) {
				t.Fatalf("result = %q", text)
			}
		})
	}
}

func TestOversizedResponseIsABusinessResult(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{
		status: http.StatusOK,
		body:   strings.Repeat("x", minMaxResponseBytes+1),
	}}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key", MaxResponseBytes: minMaxResponseBytes}, "")
	result, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, result); !strings.Contains(text, "response-size limit") || !strings.Contains(text, "reduce count") {
		t.Fatalf("result = %q", text)
	}
}

func TestTransportErrorIsRetriedThenReported(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{
		{err: errors.New("connection reset")},
		{err: errors.New("connection reset")},
		{err: errors.New("connection reset")},
	}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	search := toolByName(t, exports, searchToolName).(*searchTool)
	search.client.backoff = time.Millisecond
	result, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, result); !strings.Contains(text, "could not reach DeepSeek") || !strings.Contains(text, "connection reset") {
		t.Fatalf("result = %q", text)
	}
	if client.requestCount() != defaultMaxAttempts {
		t.Fatalf("requests = %d, want %d", client.requestCount(), defaultMaxAttempts)
	}
}

func TestMalformedSuccessResponseIsAnInternalError(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{status: http.StatusOK, body: `not json`}}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	if _, err := invokeTool(t, exports, searchToolName, `{"query":"q"}`); err == nil || !strings.Contains(err.Error(), "decode DeepSeek response") {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchEmptyContentIsABusinessResult(t *testing.T) {
	client := &fakeHTTP{responses: []fakeHTTPResponse{{status: http.StatusOK, body: `{"title":"","content":"   "}`}}}
	exports := newTestExports(t, client, Config{APIKey: "secret-key"}, "")
	result, err := invokeTool(t, exports, fetchToolName, `{"url":"https://example.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, result); !strings.Contains(text, "no extractable page content") {
		t.Fatalf("result = %q", text)
	}
}

func TestToolCallNameMismatch(t *testing.T) {
	exports := newTestExports(t, &fakeHTTP{}, Config{APIKey: "secret-key"}, "")
	_, err := toolByName(t, exports, searchToolName).Invoke(context.Background(), tool.Invocation{Call: tool.Call{Name: fetchToolName, Arguments: []byte(`{"query":"q"}`)}})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("error = %v", err)
	}
}

func TestNilContextIsRejected(t *testing.T) {
	exports := newTestExports(t, &fakeHTTP{}, Config{APIKey: "secret-key"}, "")
	_, err := toolByName(t, exports, searchToolName).Invoke(nil, tool.Invocation{Call: tool.Call{Arguments: []byte(`{"query":"q"}`)}})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestSearchArgumentValidation(t *testing.T) {
	exports := newTestExports(t, &fakeHTTP{}, Config{APIKey: "secret-key"}, "")
	invalid := []string{
		`{}`,
		`{"query":""}`,
		`{"query":"   "}`,
		`{"query":"ok","count":0}`,
		`{"query":"ok","count":51}`,
		`{"query":"ok","extra":true}`,
		`{"query":"ok"} {}`,
		`{"query":"` + strings.Repeat("x", maxSearchQueryRunes+1) + `"}`,
	}
	for _, arguments := range invalid {
		t.Run(fmt.Sprintf("%.40q", arguments), func(t *testing.T) {
			_, err := invokeTool(t, exports, searchToolName, arguments)
			if !errors.Is(err, ErrInvalidArguments) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidArguments)
			}
		})
	}
	success := newTestExports(t, &fakeHTTP{responses: []fakeHTTPResponse{{status: http.StatusOK, body: `{"results":[]}`}}}, Config{APIKey: "secret-key"}, "")
	if _, err := invokeTool(t, success, searchToolName, `{"query":"ok","ori_question":"   "}`); err != nil {
		t.Fatalf("blank ori_question should be treated as omitted: %v", err)
	}
}

func TestFetchArgumentValidation(t *testing.T) {
	exports := newTestExports(t, &fakeHTTP{}, Config{APIKey: "secret-key"}, "")
	invalid := []string{
		`{}`,
		`{"url":""}`,
		`{"url":"ftp://example.com/file"}`,
		`{"url":"https://user:password@example.com"}`,
		`{"url":"https://example.com/a b"}`,
		`{"url":"https://"}`,
		`{"url":"https://example.com","extra":true}`,
		`{"url":"https://example.com"} {}`,
		`{"url":"https://example.com/` + strings.Repeat("x", maxFetchURLLength) + `"}`,
	}
	for _, arguments := range invalid {
		t.Run(fmt.Sprintf("%.40q", arguments), func(t *testing.T) {
			_, err := invokeTool(t, exports, fetchToolName, arguments)
			if !errors.Is(err, ErrInvalidArguments) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidArguments)
			}
		})
	}
}
