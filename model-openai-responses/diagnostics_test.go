package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openairesponses "github.com/ingot-agent/plugins/model-openai-responses"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
)

func captureDiagnostics(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

func diagnosticRecords(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(buffer.Bytes()))
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestDiagnosticsProviderFailureAndRedaction(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			logs := captureDiagnostics(t)
			body := ": private-comment\n\nevent: keepalive\n\n" +
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\"}}\n\n" +
				"data: {\"type\":\"keepalive\"}\n\n"
			if partial {
				body += "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"private-output\"}\n\n"
			}
			body += "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"m\",\"status\":\"failed\",\"error\":{\"code\":\"upstream_http2_stream_error\",\"message\":\"private-prompt api-secret header-secret\"}}}\n\n"
			provider := newProvider(t, openairesponses.ProviderConfig{
				Name: "p", BaseURL: "https://example.test/private-path", APIKey: "api-secret",
				DefaultHeaders: map[string]string{"X-Credential": "header-secret"},
			}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
				response := httpResponse(http.StatusOK, body)
				response.Proto = "HTTP/2.0"
				response.Header.Set("X-Request-Id", "req-api-secret-header-secret")
				response.Header.Set("Set-Cookie", "private-cookie")
				return response, nil
			}), assetResolver{})
			ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: "s", TurnID: "turn", RoundIndex: 2})
			_, err := provider.Stream(ctx, model.Request{Model: "m", Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("private-prompt")}}}, func(model.StreamEvent) error { return nil })
			var upstream *openairesponses.ProviderResponseError
			if !errors.As(err, &upstream) || upstream.Code != "upstream_http2_stream_error" {
				t.Fatalf("failure changed: %v", err)
			}
			for _, secret := range []string{"api-secret", "header-secret", "private-prompt", "private-output", "private-cookie", "private-comment", "private-path"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("diagnostics exposed %q", secret)
				}
			}
			records := diagnosticRecords(t, logs)
			if len(records) != 3 {
				t.Fatalf("expected start, headers, failure; got %d", len(records))
			}
			failure := records[2]
			for key, want := range map[string]any{
				"level": "WARN", "failure_origin": "provider_response", "provider_code": "upstream_http2_stream_error",
				"http_status": float64(200), "http_protocol": "HTTP/2.0", "request_id": "req-[REDACTED]-[REDACTED]",
				"response_id": "resp_test", "last_event": "response.failed", "keepalives": float64(2), "sse_comments": float64(1),
				"session_id": "s", "turn_id": "turn", "round": float64(2), "adapter_retryable": true, "stream_reset_required": partial,
			} {
				if failure[key] != want {
					t.Errorf("%s = %v, want %v", key, failure[key], want)
				}
			}
			if failure["response_bytes"] != float64(len(body)) || failure["request_bytes"].(float64) <= 0 {
				t.Fatal("missing byte counts")
			}
			if (failure["delivered_events"].(float64) > 0) != partial {
				t.Fatal("incorrect output progress")
			}
			for _, record := range records {
				if record["call_id"] == "" || record["call_id"] != failure["call_id"] {
					t.Fatal("lost attempt correlation")
				}
			}
		})
	}
}

func TestDiagnosticsHTTP2ReuseAndStreamReset(t *testing.T) {
	logs := captureDiagnostics(t)
	var calls atomic.Int32
	consumed := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("request protocol = %s", r.Proto)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "request-h2")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"m\",\"output\":[]}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"private-output\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-consumed:
		case <-r.Context().Done():
			return
		}
		// net/http sends RST_STREAM(INTERNAL_ERROR) without terminating the
		// connection. This exercises a real local HTTP/2 transport read error.
		panic(http.ErrAbortHandler)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()
	provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: server.URL}, clientFunc(func(ctx context.Context, request *http.Request) (*http.Response, error) {
		return client.Do(request.WithContext(ctx))
	}), assetResolver{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := provider.Stream(ctx, model.Request{Model: "m"}, func(model.StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	_, err := provider.Stream(ctx, model.Request{Model: "m"}, func(model.StreamEvent) error { once.Do(func() { close(consumed) }); return nil })
	if err == nil {
		t.Fatal("expected HTTP/2 reset")
	}
	records := diagnosticRecords(t, logs)
	if len(records) != 6 {
		t.Fatalf("expected two attempts with three records each, got %d", len(records))
	}
	first, failure := records[2], records[5]
	for key, want := range map[string]any{
		"failure_origin": "local_transport_read", "http_protocol": "HTTP/2.0", "tls_alpn": "h2",
		"connection_reused": true, "http2_stream_error": true, "http2_error_code": "INTERNAL_ERROR", "stream_reset_required": true, "adapter_retryable": true,
	} {
		if failure[key] != want {
			t.Errorf("%s = %v, want %v (error: %v)", key, failure[key], want, err)
		}
	}
	if first["connection_reused"] != false || first["tls_handshake_failed"] != false {
		t.Fatal("missing initial TLS negotiation")
	}
	if first["call_id"] == failure["call_id"] {
		t.Fatal("attempt identifiers are not unique")
	}
	if strings.Contains(logs.String(), "private-output") {
		t.Fatal("output leaked")
	}
}

func TestDiagnosticsDoNotLogHTTPBodiesOrConsumerErrors(t *testing.T) {
	for _, consumer := range []bool{false, true} {
		t.Run(fmt.Sprintf("consumer=%v", consumer), func(t *testing.T) {
			logs := captureDiagnostics(t)
			provider := newProvider(t, openairesponses.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
				if consumer {
					return httpResponse(200, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"private-output\"}\n\n"), nil
				}
				return httpResponse(502, "private-provider-body"), nil
			}), assetResolver{})
			consumerErr := errors.New("private-consumer-error")
			_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamEvent) error { return consumerErr })
			if consumer && !errors.Is(err, consumerErr) {
				t.Fatalf("consumer error changed: %v", err)
			}
			for _, private := range []string{"private-output", "private-provider-body", "private-consumer-error"} {
				if strings.Contains(logs.String(), private) {
					t.Fatalf("diagnostics exposed %q", private)
				}
			}
			records := diagnosticRecords(t, logs)
			failure := records[len(records)-1]
			want := "http_status"
			if consumer {
				want = "consumer"
			}
			if failure["failure_origin"] != want {
				t.Fatalf("failure_origin = %v, want %s", failure["failure_origin"], want)
			}
		})
	}
}
