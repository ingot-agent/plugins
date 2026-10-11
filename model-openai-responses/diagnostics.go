package openairesponses

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
)

// One diagnostic belongs to one provider attempt, including its entire body.
// Tracing is observational only: no request headers or retry policy are changed.
type requestDiagnostic struct {
	provider                                           *provider
	logger                                             *slog.Logger
	started                                            time.Time
	stage                                              string
	stream                                             bool
	status                                             int
	protocol                                           string
	requestID, responseID, cfRay                       string
	bytes, events, comments, keepalives, delivered     int
	lastEvent                                          string
	firstEvent, lastEventAt, previousEventAt, lastRead time.Time
	consumerFailed                                     bool
	requestBytes                                       int

	// net/http trace callbacks can run concurrently, even after Do returns.
	mu         sync.Mutex
	traceAttrs map[string]any
}

func (p *provider) startDiagnostic(ctx context.Context, request model.Request, stream bool) (*requestDiagnostic, context.Context) {
	d := &requestDiagnostic{provider: p, started: time.Now(), stage: "encode", stream: stream, traceAttrs: make(map[string]any)}
	endpoint, _ := url.Parse(p.endpoint)
	d.logger = slog.Default().With("component", "model.openai-responses", "call_id", rand.Text(),
		"provider", d.clean(p.name), "model", d.clean(request.Model), "reasoning_effort", d.clean(string(request.ReasoningEffort)),
		"endpoint_host", d.clean(endpoint.Host), "stream", stream)
	if correlation, ok := observation.CorrelationFromContext(ctx); ok {
		d.logger = d.logger.With("session_id", d.clean(string(correlation.SessionID)), "turn_id", d.clean(string(correlation.TurnID)), "round", correlation.RoundIndex)
	}
	attrs := []any{"messages", len(request.Messages), "tools", len(request.Tools), "go_version", runtime.Version()}
	if request.MaxTokens != nil {
		attrs = append(attrs, "max_output_tokens", *request.MaxTokens)
	}
	if deadline, ok := ctx.Deadline(); ok {
		attrs = append(attrs, "deadline_remaining_ms", time.Until(deadline).Milliseconds())
	}
	d.logger.InfoContext(ctx, "model provider request started", attrs...)
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { d.trace("dns_started_after_ms", time.Since(d.started).Milliseconds()) },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			d.trace("dns_done_after_ms", time.Since(d.started).Milliseconds())
			d.trace("dns_failed", info.Err != nil)
		},
		ConnectStart: func(_, _ string) { d.trace("connect_started_after_ms", time.Since(d.started).Milliseconds()) },
		ConnectDone: func(_, _ string, err error) {
			d.trace("connect_failed", err != nil)
			if err == nil {
				d.trace("connected_after_ms", time.Since(d.started).Milliseconds())
			}
		},
		TLSHandshakeStart: func() { d.trace("tls_started_after_ms", time.Since(d.started).Milliseconds()) },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			d.trace("tls_done_after_ms", time.Since(d.started).Milliseconds())
			d.trace("tls_handshake_failed", err != nil)
			if err == nil {
				d.trace("tls_alpn", d.clean(state.NegotiatedProtocol))
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			d.trace("connection_reused", info.Reused)
			d.trace("connection_was_idle", info.WasIdle)
			d.trace("connection_idle_ms", info.IdleTime.Milliseconds())
			if conn, ok := info.Conn.(*tls.Conn); ok {
				d.trace("tls_alpn", d.clean(conn.ConnectionState().NegotiatedProtocol))
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			d.trace("request_written_after_ms", time.Since(d.started).Milliseconds())
			d.trace("request_write_failed", info.Err != nil)
		},
		GotFirstResponseByte: func() { d.trace("first_byte_after_ms", time.Since(d.started).Milliseconds()) },
	}
	return d, httptrace.WithClientTrace(ctx, trace)
}

func (d *requestDiagnostic) trace(key string, value any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.traceAttrs[key] = value
}

func (d *requestDiagnostic) receivedHeaders(ctx context.Context, response *http.Response) {
	d.status, d.protocol = response.StatusCode, d.clean(response.Proto)
	d.requestID = d.clean(response.Header.Get("X-Request-Id"))
	if d.requestID == "" {
		d.requestID = d.clean(response.Header.Get("Request-Id"))
	}
	d.cfRay = d.clean(response.Header.Get("Cf-Ray"))
	d.logger.InfoContext(ctx, "model provider response headers", "http_status", d.status, "http_protocol", d.protocol,
		"request_id", d.requestID, "cf_ray", d.cfRay, "headers_after_ms", time.Since(d.started).Milliseconds())
	response.Body = &diagnosticBody{ReadCloser: response.Body, diagnostic: d}
}

type diagnosticBody struct {
	io.ReadCloser
	diagnostic *requestDiagnostic
}

func (b *diagnosticBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.diagnostic.bytes += n
		b.diagnostic.lastRead = time.Now()
	}
	return n, err
}

func (d *requestDiagnostic) event(kind string, response *responseObject) {
	now := time.Now()
	d.events++
	if d.firstEvent.IsZero() {
		d.firstEvent = now
	}
	d.previousEventAt, d.lastEventAt = d.lastEventAt, now
	d.lastEvent = d.clean(kind)
	if kind == "keepalive" {
		d.keepalives++
	}
	if response != nil && response.ID != "" {
		d.responseID = d.clean(response.ID)
	}
}

func (d *requestDiagnostic) finish(ctx context.Context, err error) {
	now := time.Now()
	attrs := []any{"stage", d.stage, "elapsed_ms", now.Sub(d.started).Milliseconds(), "http_status", d.status,
		"http_protocol", d.protocol, "request_id", d.requestID, "response_id", d.responseID, "cf_ray", d.cfRay,
		"request_bytes", d.requestBytes, "response_bytes", d.bytes, "sse_events", d.events, "sse_comments", d.comments, "keepalives", d.keepalives,
		"delivered_events", d.delivered, "last_event", d.lastEvent, "consumer_failed", d.consumerFailed}
	if !d.firstEvent.IsZero() {
		attrs = append(attrs, "first_event_after_ms", d.firstEvent.Sub(d.started).Milliseconds())
	}
	if !d.lastEventAt.IsZero() {
		attrs = append(attrs, "last_event_age_ms", now.Sub(d.lastEventAt).Milliseconds())
	}
	if !d.previousEventAt.IsZero() {
		attrs = append(attrs, "last_event_gap_ms", d.lastEventAt.Sub(d.previousEventAt).Milliseconds())
	}
	if !d.lastRead.IsZero() {
		attrs = append(attrs, "last_read_age_ms", now.Sub(d.lastRead).Milliseconds())
	}
	d.mu.Lock()
	for key, value := range d.traceAttrs {
		attrs = append(attrs, key, value)
	}
	d.mu.Unlock()
	if ctx.Err() != nil {
		attrs = append(attrs, "context_error", ctx.Err().Error())
	}
	if err == nil {
		d.logger.InfoContext(ctx, "model provider request completed", attrs...)
		return
	}
	attrs = append(attrs, "error_type", fmt.Sprintf("%T", err), "failure_origin", d.failureOrigin(ctx, err))
	var hint interface{ RetryDelay() (time.Duration, bool) }
	retryable := false
	if !d.consumerFailed && errors.As(err, &hint) {
		_, retryable = hint.RetryDelay()
	}
	attrs = append(attrs, "adapter_retryable", retryable, "stream_replay_blocked", d.stream && d.delivered > 0)
	if !d.consumerFailed {
		var responseErr *ProviderResponseError
		if errors.As(err, &responseErr) {
			attrs = append(attrs, "provider_status", d.clean(responseErr.Status), "provider_code", d.clean(responseErr.Code))
		}
		attrs = append(attrs, networkErrorAttrs(err)...)
	}
	d.logger.WarnContext(ctx, "model provider request failed", attrs...)
}

func (d *requestDiagnostic) failureOrigin(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "context"
	}
	if d.consumerFailed {
		return "consumer"
	}
	var responseErr *ProviderResponseError
	var requestErr *providerRequestError
	var readErr *providerReadError
	var httpErr *ProviderHTTPError
	switch {
	case errors.As(err, &responseErr):
		return "provider_response"
	case errors.As(err, &requestErr):
		return "local_transport_request"
	case errors.Is(err, ErrResponseLimit):
		return "response_limit"
	case errors.As(err, &readErr):
		return "local_transport_read"
	case errors.As(err, &httpErr):
		return "http_status"
	case errors.Is(err, ErrProtocol):
		return "adapter_protocol"
	default:
		return "request_preparation"
	}
}

var (
	http2StreamDiagnostic = regexp.MustCompile(`^stream error: stream ID ([0-9]+); ([A-Z_]+)(?:;|$)`)
	http2GoAwayDiagnostic = regexp.MustCompile(`LastStreamID=([0-9]+), ErrCode=([A-Z_]+),`)
)

// Arbitrary error strings can contain response bodies, user content or proxy
// credentials. Only known transport diagnostics and typed provider codes are logged.
func networkErrorAttrs(err error) []any {
	var attrs []any
	var network net.Error
	if errors.As(err, &network) {
		attrs = append(attrs, "network_timeout", network.Timeout())
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		attrs = append(attrs, "network_errno", int(errno), "network_error", errno.Error())
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		attrs = append(attrs, "network_error", "unexpected EOF")
	} else if errors.Is(err, io.EOF) {
		attrs = append(attrs, "network_error", "EOF")
	}
	var types []string
	for cause, depth := err, 0; cause != nil && depth < 16; cause, depth = errors.Unwrap(cause), depth+1 {
		kind := fmt.Sprintf("%T", cause)
		types = append(types, kind)
		// Extract only numeric IDs and protocol codes from known Go transport
		// errors. Never log a wrapped cause or GOAWAY's arbitrary debug data.
		if kind == "http.http2StreamError" || kind == "http2.StreamError" {
			attrs = append(attrs, "http2_stream_error", true)
			if match := http2StreamDiagnostic.FindStringSubmatch(cause.Error()); len(match) == 3 {
				attrs = append(attrs, "http2_stream_id", match[1], "http2_error_code", match[2])
			}
		}
		if kind == "http.http2GoAwayError" || kind == "http2.GoAwayError" {
			attrs = append(attrs, "http2_goaway", true)
			if match := http2GoAwayDiagnostic.FindStringSubmatch(cause.Error()); len(match) == 3 {
				attrs = append(attrs, "http2_last_stream_id", match[1], "http2_error_code", match[2])
			}
		}
	}
	return append(attrs, "error_chain_types", strings.Join(types, " -> "))
}

func (d *requestDiagnostic) clean(value string) string {
	for _, secret := range []string{d.provider.apiKey, d.provider.organization, d.provider.project} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	for _, values := range d.provider.headers {
		for _, secret := range values {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[REDACTED]")
			}
		}
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 256 {
		return string(runes[:256]) + "…"
	}
	return value
}
