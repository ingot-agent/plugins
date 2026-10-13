package openaicompat

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"syscall"
	"time"
)

// RetryDelay reports whether a non-success HTTP response is transient. The
// provider may already have executed and billed the request.
func (e *ProviderHTTPError) RetryDelay() (time.Duration, bool) {
	if e == nil {
		return 0, false
	}
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return e.RetryAfter, true
	default:
		return 0, false
	}
}

// Only failures dispatching the model HTTP request qualify here. Local asset
// reads, invalid inputs and stream/response decoding never enter this path.
func (e *providerRequestError) RetryDelay() (time.Duration, bool) {
	if e == nil || errors.Is(e.cause, context.Canceled) || errors.Is(e.cause, context.DeadlineExceeded) {
		return 0, false
	}
	return transientNetworkError(e.cause)
}

// providerReadError only wraps an I/O read failure, never a decoding or
// validation failure. A retrying stream consumer must retract failed progress.
type providerReadError struct{ cause error }

func (e *providerReadError) Error() string { return e.cause.Error() }
func (e *providerReadError) Unwrap() error { return e.cause }
func (e *providerReadError) RetryDelay() (time.Duration, bool) {
	return transientNetworkError(e.cause)
}

func wrapReadError(err error) error {
	if err == nil {
		return nil
	}
	return &providerReadError{cause: err}
}

func transientNetworkError(err error) (time.Duration, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	var network net.Error
	if errors.As(err, &network) && (network.Timeout() || network.Temporary()) {
		return 0, true
	}
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.EPIPE):
		return 0, true
	}
	return 0, transientHTTP2Error(err, 0)
}

// The standard library keeps its HTTP/2 error types private. Inspect only the
// numeric protocol code on known transport types; never classify by error text.
func transientHTTP2Error(err error, depth int) bool {
	if err == nil || depth >= 16 {
		return false
	}
	value := reflect.ValueOf(err)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.Struct {
		kind := value.Type()
		stream := (kind.PkgPath() == "net/http" && kind.Name() == "http2StreamError") || (kind.PkgPath() == "golang.org/x/net/http2" && kind.Name() == "StreamError")
		goaway := (kind.PkgPath() == "net/http" && kind.Name() == "http2GoAwayError") || (kind.PkgPath() == "golang.org/x/net/http2" && kind.Name() == "GoAwayError")
		if stream || goaway {
			field := "Code"
			if goaway {
				field = "ErrCode"
			}
			code := value.FieldByName(field)
			if code.IsValid() && code.CanUint() {
				switch code.Uint() {
				case 0:
					return goaway // Graceful connection retirement.
				case 2, 7, 8, 11:
					return true // INTERNAL_ERROR, REFUSED_STREAM, CANCEL, ENHANCE_YOUR_CALM.
				}
			}
			return false
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if transientHTTP2Error(cause, depth+1) {
				return true
			}
		}
		return false
	}
	return transientHTTP2Error(errors.Unwrap(err), depth+1)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 300 {
			return 300 * time.Second
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := date.Sub(now)
		if delay > 300*time.Second {
			return 300 * time.Second
		}
		if delay > 0 {
			return delay
		}
	}
	return 0
}
