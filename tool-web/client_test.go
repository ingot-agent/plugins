package toolweb

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRetryDelayHonorsRetryAfterAndCapsIt(t *testing.T) {
	client := &deepSeekClient{backoff: time.Millisecond}
	header := make(http.Header)
	header.Set("Retry-After", "3")
	if got := client.retryDelay(1, header); got != 3*time.Second {
		t.Fatalf("delay = %v", got)
	}
	header.Set("Retry-After", "9999")
	if got := client.retryDelay(1, header); got != maxRetryAfter {
		t.Fatalf("capped delay = %v", got)
	}
	if got := client.retryDelay(1, nil); got != time.Millisecond {
		t.Fatalf("backoff delay = %v", got)
	}
	if got := client.retryDelay(2, nil); got != 2*time.Millisecond {
		t.Fatalf("second backoff delay = %v", got)
	}
}

func TestReadLimited(t *testing.T) {
	body, err := readLimited(strings.NewReader("abc"), 3)
	if err != nil || string(body) != "abc" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if _, err := readLimited(strings.NewReader("abcd"), 3); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeAPIError(t *testing.T) {
	apiErr := decodeAPIError(http.StatusBadRequest, []byte(`{"error":{"message":"bad\nrequest","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`))
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_request_error" || apiErr.Message != "bad request" {
		t.Fatalf("api error = %#v", apiErr)
	}
	if message := apiErr.userMessage(searchToolName); !strings.Contains(message, "bad request") {
		t.Fatalf("user message = %q", message)
	}
	fallback := decodeAPIError(http.StatusBadGateway, []byte("plain gateway failure"))
	if fallback.Message != "plain gateway failure" || fallback.Code != "" {
		t.Fatalf("fallback = %#v", fallback)
	}
	empty := decodeAPIError(http.StatusInternalServerError, nil)
	if empty.Message != http.StatusText(http.StatusInternalServerError) {
		t.Fatalf("empty = %#v", empty)
	}
}

func TestAPIErrorMessages(t *testing.T) {
	tests := []struct {
		status   int
		contains string
	}{
		{status: http.StatusBadRequest, contains: "rejected"},
		{status: http.StatusUnauthorized, contains: "API key"},
		{status: http.StatusPaymentRequired, contains: "insufficient balance"},
		{status: http.StatusForbidden, contains: "security policy"},
		{status: http.StatusNotFound, contains: "could not retrieve usable page content"},
		{status: http.StatusRequestTimeout, contains: "timed out"},
		{status: http.StatusTooManyRequests, contains: "rate limit"},
		{status: http.StatusInternalServerError, contains: "service error"},
	}
	for _, test := range tests {
		apiErr := &apiError{Status: test.status, Message: "detail"}
		if message := apiErr.userMessage(fetchToolName); !strings.Contains(message, test.contains) {
			t.Fatalf("status %d message = %q, want %q", test.status, message, test.contains)
		}
	}
	if message := (&apiError{Status: http.StatusNotFound, Message: "detail"}).userMessage(searchToolName); !strings.Contains(message, "check base_url") {
		t.Fatalf("search 404 message = %q", message)
	}
}
