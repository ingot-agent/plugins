package toolweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ingot-agent/sdk/httpx"
)

const (
	defaultMaxAttempts  = 3
	defaultRetryBackoff = 500 * time.Millisecond
	maxRetryDelay       = 10 * time.Second
	maxRetryAfter       = 10 * time.Second
	maxErrorBodyBytes   = 64 << 10
	userAgent           = "ingot-tool-web/0.1"
)

var (
	errTransport        = errors.New("deepseek request failed")
	errResponseTooLarge = errors.New("deepseek response too large")
)

// deepSeekClient is immutable except for the test-only backoff field set
// before use. All tool calls share it safely.
type deepSeekClient struct {
	http    httpx.Client
	backoff time.Duration
}

type searchRequest struct {
	Query       string `json:"query"`
	Count       int    `json:"count"`
	OriQuestion string `json:"ori_question,omitempty"`
	NeedContent bool   `json:"need_content"`
}

type searchResponse struct {
	Results []searchResult `json:"results"`
}

type searchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    *string `json:"site_name"`
	Date        *string `json:"date"`
	Authority   int     `json:"authority"`
}

type fetchResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// apiError is a non-2xx response from the DeepSeek API.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	code := e.Code
	if code == "" {
		code = "error"
	}
	return fmt.Sprintf("deepseek HTTP %d %s: %s", e.Status, code, e.Message)
}

func (e *apiError) userMessage(operation string) string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.Status)
	}
	switch e.Status {
	case http.StatusBadRequest:
		return "DeepSeek rejected the request: " + message
	case http.StatusUnauthorized:
		return "DeepSeek API key is missing or invalid; set DEEPSEEK_API_KEY or run the tool-web config operation"
	case http.StatusPaymentRequired:
		return "DeepSeek account has insufficient balance"
	case http.StatusForbidden:
		return "DeepSeek rejected the request by security policy: " + message
	case http.StatusNotFound:
		if operation == fetchToolName {
			return "DeepSeek could not retrieve usable page content: " + message
		}
		return "DeepSeek endpoint was not found; check base_url"
	case http.StatusRequestTimeout:
		return "DeepSeek timed out while reading the request"
	case http.StatusRequestEntityTooLarge:
		return "request body was too large for DeepSeek"
	case http.StatusTooManyRequests:
		return "DeepSeek rate limit exceeded; retry later"
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Sprintf("DeepSeek service error (HTTP %d): %s", e.Status, message)
	default:
		return fmt.Sprintf("DeepSeek returned HTTP %d: %s", e.Status, message)
	}
}

func (c *deepSeekClient) search(ctx context.Context, configuration effectiveConfig, request searchRequest) ([]searchResult, error) {
	var response searchResponse
	if err := c.post(ctx, configuration, configuration.searchURL, request, &response); err != nil {
		return nil, err
	}
	return response.Results, nil
}

func (c *deepSeekClient) fetch(ctx context.Context, configuration effectiveConfig, target string) (fetchResponse, error) {
	var response fetchResponse
	if err := c.post(ctx, configuration, configuration.fetchURL, struct {
		URL string `json:"url"`
	}{URL: target}, &response); err != nil {
		return fetchResponse{}, err
	}
	return response, nil
}

func (c *deepSeekClient) post(ctx context.Context, configuration effectiveConfig, endpoint string, payload, output any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode DeepSeek request: %w", err)
	}
	for attempt := 1; attempt <= defaultMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create DeepSeek request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+configuration.apiKey)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", userAgent)

		response, err := c.http.Do(ctx, request)
		if err == nil && response == nil {
			err = errors.New("nil HTTP response")
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if attempt == defaultMaxAttempts {
				return fmt.Errorf("%w: %w", errTransport, err)
			}
			if err := sleepContext(ctx, c.retryDelay(attempt, nil)); err != nil {
				return err
			}
			continue
		}
		if response.Body == nil {
			if attempt == defaultMaxAttempts {
				return fmt.Errorf("%w: nil response body", errTransport)
			}
			if err := sleepContext(ctx, c.retryDelay(attempt, nil)); err != nil {
				return err
			}
			continue
		}
		responseBody, readErr := readLimited(response.Body, configuration.maxResponseBytes)
		_ = response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if err := json.Unmarshal(responseBody, output); err != nil {
				return fmt.Errorf("decode DeepSeek response: %w", err)
			}
			return nil
		}
		apiResponseError := decodeAPIError(response.StatusCode, responseBody)
		if !retryableStatus(response.StatusCode) || attempt == defaultMaxAttempts {
			return apiResponseError
		}
		if err := sleepContext(ctx, c.retryDelay(attempt, response.Header)); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: retry loop exhausted", errTransport)
}

func (c *deepSeekClient) retryDelay(attempt int, header http.Header) time.Duration {
	if header != nil {
		if delay := retryAfter(header.Get("Retry-After")); delay > 0 {
			if delay > maxRetryAfter {
				delay = maxRetryAfter
			}
			return delay
		}
	}
	base := c.backoff
	if base <= 0 {
		base = defaultRetryBackoff
	}
	delay := base << (attempt - 1)
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return delay
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func retryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if instant, err := http.ParseTime(value); err == nil {
		if delay := time.Until(instant); delay > 0 {
			return delay
		}
	}
	return 0
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTransport, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: limit is %d bytes", errResponseTooLarge, limit)
	}
	return data, nil
}

func decodeAPIError(status int, body []byte) *apiError {
	message := ""
	code := ""
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		message = envelope.Error.Message
		code = envelope.Error.Code
		if code == "" {
			code = envelope.Error.Type
		}
	}
	if message == "" {
		message = truncateUTF8(string(body), maxErrorBodyBytes, "...")
	}
	message = cleanInline(message)
	if message == "" {
		message = http.StatusText(status)
	}
	return &apiError{Status: status, Code: truncateUTF8(code, 128, "..."), Message: message}
}
