package openairesponses

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
)

// One diagnostic belongs to one provider attempt, including its entire body.
// Tracing is observational only: no request headers or retry policy are changed.
type requestDiagnostic struct {
	provider                                           *provider
	logger                                             logging.Logger
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
	inputTokens, cachedInputTokens                     *int

	// net/http trace callbacks can run concurrently, even after Do returns.
	mu         sync.Mutex
	traceAttrs map[string]logging.Value
}

func (p *provider) startDiagnostic(ctx context.Context, request model.Request, stream bool) (*requestDiagnostic, context.Context) {
	d := &requestDiagnostic{provider: p, started: time.Now(), stage: "encode", stream: stream, traceAttrs: make(map[string]logging.Value)}
	endpoint, _ := url.Parse(p.endpoint)
	d.logger = p.logger.With(
		logging.String("component", "model.openai-responses"),
		logging.String("call_id", rand.Text()),
		logging.String("provider", d.clean(p.name)),
		logging.String("model", d.clean(request.Model)),
		logging.String("reasoning_effort", d.clean(string(request.ReasoningEffort))),
		logging.String("endpoint_host", d.clean(endpoint.Host)),
		logging.Bool("stream", stream),
	)
	// Correlation is added by the logging implementation from ctx.
	attrs := []logging.Attr{
		logging.Int("messages", int64(len(request.Messages))),
		logging.Int("tools", int64(len(request.Tools))),
		logging.String("go_version", runtime.Version()),
		logging.Bool("cache_session_key_present", sessionCacheKey(ctx) != ""),
	}
	if request.MaxTokens != nil {
		attrs = append(attrs, logging.Int("max_output_tokens", int64(*request.MaxTokens)))
	}
	if deadline, ok := ctx.Deadline(); ok {
		attrs = append(attrs, logging.Int("deadline_remaining_ms", time.Until(deadline).Milliseconds()))
	}
	d.logger.Info(ctx, "model provider request started", attrs...)
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { d.traceInt("dns_started_after_ms", time.Since(d.started).Milliseconds()) },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			d.traceInt("dns_done_after_ms", time.Since(d.started).Milliseconds())
			d.traceBool("dns_failed", info.Err != nil)
		},
		ConnectStart: func(_, _ string) { d.traceInt("connect_started_after_ms", time.Since(d.started).Milliseconds()) },
		ConnectDone: func(_, _ string, err error) {
			d.traceBool("connect_failed", err != nil)
			if err == nil {
				d.traceInt("connected_after_ms", time.Since(d.started).Milliseconds())
			}
		},
		TLSHandshakeStart: func() { d.traceInt("tls_started_after_ms", time.Since(d.started).Milliseconds()) },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			d.traceInt("tls_done_after_ms", time.Since(d.started).Milliseconds())
			d.traceBool("tls_handshake_failed", err != nil)
			if err == nil {
				d.traceText("tls_alpn", d.clean(state.NegotiatedProtocol))
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			d.traceBool("connection_reused", info.Reused)
			d.traceBool("connection_was_idle", info.WasIdle)
			d.traceInt("connection_idle_ms", info.IdleTime.Milliseconds())
			if conn, ok := info.Conn.(*tls.Conn); ok {
				d.traceText("tls_alpn", d.clean(conn.ConnectionState().NegotiatedProtocol))
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			d.traceInt("request_written_after_ms", time.Since(d.started).Milliseconds())
			d.traceBool("request_write_failed", info.Err != nil)
		},
		GotFirstResponseByte: func() { d.traceInt("first_byte_after_ms", time.Since(d.started).Milliseconds()) },
	}
	return d, httptrace.WithClientTrace(ctx, trace)
}

// Trace attributes are produced by net/http callbacks with three concrete
// value shapes, so each shape has a typed setter instead of an any bucket.
func (d *requestDiagnostic) traceInt(key string, value int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.traceAttrs[key] = logging.IntValue(value)
}

func (d *requestDiagnostic) traceBool(key string, value bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.traceAttrs[key] = logging.BoolValue(value)
}

func (d *requestDiagnostic) traceText(key string, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.traceAttrs[key] = logging.StringValue(value)
}

func (d *requestDiagnostic) receivedHeaders(ctx context.Context, response *http.Response) {
	d.status, d.protocol = response.StatusCode, d.clean(response.Proto)
	d.requestID = d.clean(response.Header.Get("X-Request-Id"))
	if d.requestID == "" {
		d.requestID = d.clean(response.Header.Get("Request-Id"))
	}
	d.cfRay = d.clean(response.Header.Get("Cf-Ray"))
	d.logger.Info(ctx, "model provider response headers",
		logging.Int("http_status", int64(d.status)),
		logging.String("http_protocol", d.protocol),
		logging.String("request_id", d.requestID),
		logging.String("cf_ray", d.cfRay),
		logging.Int("headers_after_ms", time.Since(d.started).Milliseconds()),
	)
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
	if response != nil && (kind == "response.completed" || kind == "response.incomplete") {
		d.recordUsage(response.Usage)
	}
}

func (d *requestDiagnostic) recordUsage(usage *responseUsage) {
	if usage == nil {
		return
	}
	d.inputTokens = usage.InputTokens
	if usage.InputTokensDetails != nil {
		d.cachedInputTokens = usage.InputTokensDetails.CachedTokens
	}
}

func (d *requestDiagnostic) finish(ctx context.Context, err error) {
	now := time.Now()
	attrs := []logging.Attr{
		logging.String("stage", d.stage),
		logging.Int("elapsed_ms", now.Sub(d.started).Milliseconds()),
		logging.Int("http_status", int64(d.status)),
		logging.String("http_protocol", d.protocol),
		logging.String("request_id", d.requestID),
		logging.String("response_id", d.responseID),
		logging.String("cf_ray", d.cfRay),
		logging.Int("request_bytes", int64(d.requestBytes)),
		logging.Int("response_bytes", int64(d.bytes)),
		logging.Int("sse_events", int64(d.events)),
		logging.Int("sse_comments", int64(d.comments)),
		logging.Int("keepalives", int64(d.keepalives)),
		logging.Int("delivered_events", int64(d.delivered)),
		logging.String("last_event", d.lastEvent),
		logging.Bool("consumer_failed", d.consumerFailed),
	}
	if err == nil && d.inputTokens != nil {
		attrs = append(attrs, logging.Int("input_tokens", int64(*d.inputTokens)))
		if d.cachedInputTokens != nil {
			attrs = append(attrs, logging.Int("cached_input_tokens", int64(*d.cachedInputTokens)))
		}
	}
	if !d.firstEvent.IsZero() {
		attrs = append(attrs, logging.Int("first_event_after_ms", d.firstEvent.Sub(d.started).Milliseconds()))
	}
	if !d.lastEventAt.IsZero() {
		attrs = append(attrs, logging.Int("last_event_age_ms", now.Sub(d.lastEventAt).Milliseconds()))
	}
	if !d.previousEventAt.IsZero() {
		attrs = append(attrs, logging.Int("last_event_gap_ms", d.lastEventAt.Sub(d.previousEventAt).Milliseconds()))
	}
	if !d.lastRead.IsZero() {
		attrs = append(attrs, logging.Int("last_read_age_ms", now.Sub(d.lastRead).Milliseconds()))
	}
	attrs = append(attrs, d.traceAttributes()...)
	if ctx.Err() != nil {
		attrs = append(attrs, logging.String("context_error", ctx.Err().Error()))
	}
	if err == nil {
		d.logger.Info(ctx, "model provider request completed", attrs...)
		return
	}
	attrs = append(attrs,
		logging.String("error_type", fmt.Sprintf("%T", err)),
		logging.String("failure_origin", d.failureOrigin(ctx, err)),
	)
	var hint interface{ RetryDelay() (time.Duration, bool) }
	retryable := false
	if !d.consumerFailed && errors.As(err, &hint) {
		_, retryable = hint.RetryDelay()
	}
	attrs = append(attrs,
		logging.Bool("adapter_retryable", retryable),
		logging.Bool("stream_reset_required", d.stream && d.delivered > 0),
	)
	if !d.consumerFailed {
		var responseErr *ProviderResponseError
		if errors.As(err, &responseErr) {
			attrs = append(attrs,
				logging.String("provider_status", d.clean(responseErr.Status)),
				logging.String("provider_code", d.clean(responseErr.Code)),
			)
		}
		attrs = append(attrs, networkErrorAttrs(err)...)
	}
	d.logger.Warn(ctx, "model provider request failed", attrs...)
}

// traceAttributes copies the transport timeline in a deterministic key order so
// two records for the same attempt are directly comparable.
func (d *requestDiagnostic) traceAttributes() []logging.Attr {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]string, 0, len(d.traceAttrs))
	for key := range d.traceAttrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attrs := make([]logging.Attr, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, logging.Attr{Key: key, Value: d.traceAttrs[key]})
	}
	return attrs
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
func networkErrorAttrs(err error) []logging.Attr {
	var attrs []logging.Attr
	var network net.Error
	if errors.As(err, &network) {
		attrs = append(attrs, logging.Bool("network_timeout", network.Timeout()))
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		attrs = append(attrs,
			logging.Int("network_errno", int64(errno)),
			logging.String("network_error", errno.Error()),
		)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		attrs = append(attrs, logging.String("network_error", "unexpected EOF"))
	} else if errors.Is(err, io.EOF) {
		attrs = append(attrs, logging.String("network_error", "EOF"))
	}
	var types []string
	for cause, depth := err, 0; cause != nil && depth < 16; cause, depth = errors.Unwrap(cause), depth+1 {
		kind := fmt.Sprintf("%T", cause)
		types = append(types, kind)
		// Extract only numeric IDs and protocol codes from known Go transport
		// errors. Never log a wrapped cause or GOAWAY's arbitrary debug data.
		if kind == "http.http2StreamError" || kind == "http2.StreamError" {
			attrs = append(attrs, logging.Bool("http2_stream_error", true))
			if match := http2StreamDiagnostic.FindStringSubmatch(cause.Error()); len(match) == 3 {
				attrs = append(attrs,
					logging.String("http2_stream_id", match[1]),
					logging.String("http2_error_code", match[2]),
				)
			}
		}
		if kind == "http.http2GoAwayError" || kind == "http2.GoAwayError" {
			attrs = append(attrs, logging.Bool("http2_goaway", true))
			if match := http2GoAwayDiagnostic.FindStringSubmatch(cause.Error()); len(match) == 3 {
				attrs = append(attrs,
					logging.String("http2_last_stream_id", match[1]),
					logging.String("http2_error_code", match[2]),
				)
			}
		}
	}
	return append(attrs, logging.String("error_chain_types", strings.Join(types, " -> ")))
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
