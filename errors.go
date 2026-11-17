package billionsend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Error is what every failure of the client unwraps to (errors.As with
// **Error). Name is the API's error name ("validation_error",
// "invalid_api_key", ...), "application_error" for a failure that never got
// an API answer. StatusCode is 0 when there was no HTTP response at all.
// RequestID is the answer's x-request-id (the request on the dashboard's
// Logs page), empty without one.
type Error struct {
	StatusCode int    `json:"statusCode"`
	Name       string `json:"name"`
	Message    string `json:"message"`
	RequestID  string `json:"requestId,omitempty"`
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, "billionsend: %d %s: %s", e.StatusCode, e.Name, e.Message)
	} else {
		fmt.Fprintf(&b, "billionsend: %s: %s", e.Name, e.Message)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request %s)", e.RequestID)
	}
	return b.String()
}

// errorBase embeds Error in the typed errors: an embedded field takes its
// type's name, and one named Error would hide the Error method.
type errorBase = Error

// ValidationError is a 400 or 422: the request was refused as invalid.
type ValidationError struct{ errorBase }

// AuthenticationError is a 401, or no API key configured (Name
// "missing_api_key", StatusCode 0): the key is missing, malformed or revoked.
type AuthenticationError struct{ errorBase }

// PermissionDeniedError is a 403: the key, its scope, the role or the plan
// does not allow this.
type PermissionDeniedError struct{ errorBase }

// NotFoundError is a 404: no such resource or endpoint.
type NotFoundError struct{ errorBase }

// ConflictError is a 409: the request conflicts with the current state, or
// with an earlier use of its idempotency key.
type ConflictError struct{ errorBase }

// RateLimitError is a 429: a rate limit, a sending quota or a stream cap.
// RetryAfter is the server's hint (0 without one).
type RateLimitError struct {
	errorBase
	RetryAfter time.Duration
}

// APIError is a 5xx: the API failed to answer the request. RetryAfter is the
// server's hint (0 without one).
type APIError struct {
	errorBase
	RetryAfter time.Duration
}

// APIConnectionError is a request that never got an answer: DNS, connection,
// TLS or protocol failure. StatusCode is 0.
type APIConnectionError struct{ errorBase }

// APITimeoutError is a request that timed out before the answer arrived.
type APITimeoutError struct{ APIConnectionError }

// WebhookVerificationError is a webhook whose signature, timestamp or body did
// not verify (VerifyWebhook).
type WebhookVerificationError struct{ errorBase }

func (e *ValidationError) Unwrap() error          { return &e.errorBase }
func (e *AuthenticationError) Unwrap() error      { return &e.errorBase }
func (e *PermissionDeniedError) Unwrap() error    { return &e.errorBase }
func (e *NotFoundError) Unwrap() error            { return &e.errorBase }
func (e *ConflictError) Unwrap() error            { return &e.errorBase }
func (e *RateLimitError) Unwrap() error           { return &e.errorBase }
func (e *APIError) Unwrap() error                 { return &e.errorBase }
func (e *APIConnectionError) Unwrap() error       { return &e.errorBase }
func (e *APITimeoutError) Unwrap() error          { return &e.APIConnectionError }
func (e *WebhookVerificationError) Unwrap() error { return &e.errorBase }

func applicationError(format string, args ...any) *Error {
	return &Error{Name: "application_error", Message: fmt.Sprintf(format, args...)}
}

func connectionError(message string) *APIConnectionError {
	return &APIConnectionError{Error{Name: "application_error", Message: message}}
}

func timeoutError(message string) *APITimeoutError {
	return &APITimeoutError{*connectionError(message)}
}

func webhookError(message string) *WebhookVerificationError {
	return &WebhookVerificationError{Error{Name: "webhook_verification_error", Message: message}}
}

// parseRetryAfter reads a retry-after value (seconds or an HTTP date); false without one.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds < 0 {
			seconds = 0
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	if at, err := http.ParseTime(value); err == nil {
		d := at.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// errorFromResponse maps a non-2xx answer to its error type, keeping the API's
// own ErrorResponse (statusCode, name, message) when it sent one.
func errorFromResponse(status int, statusText string, body []byte, header http.Header) error {
	base := Error{StatusCode: status, Name: "application_error", RequestID: header.Get("x-request-id")}
	var parsed struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		base.Message = parsed.Message
		if parsed.Name != "" {
			base.Name = parsed.Name
		}
	} else {
		text := strings.TrimSpace(string(body))
		if len(text) > 500 {
			text = text[:500]
		}
		if text == "" {
			text = statusText
		}
		if text == "" {
			text = http.StatusText(status)
		}
		base.Message = text
	}
	switch {
	case status == 400 || status == 422:
		return &ValidationError{base}
	case status == 401:
		return &AuthenticationError{base}
	case status == 403:
		return &PermissionDeniedError{base}
	case status == 404:
		return &NotFoundError{base}
	case status == 409:
		return &ConflictError{base}
	case status == 429:
		after, _ := parseRetryAfter(header.Get("retry-after"), time.Now())
		return &RateLimitError{errorBase: base, RetryAfter: after}
	case status >= 500:
		after, _ := parseRetryAfter(header.Get("retry-after"), time.Now())
		return &APIError{errorBase: base, RetryAfter: after}
	}
	return &base
}
