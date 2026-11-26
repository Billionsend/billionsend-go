package billionsend

import (
	"crypto/rand"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"time"
)

// MaxRetryAfter is the longest retry-after the client waits for; a longer one
// returns the error instead.
const MaxRetryAfter = 60 * time.Second

const (
	baseDelay  = 500 * time.Millisecond
	maxBackoff = 8 * time.Second
)

// safeMethods may be repeated without an Idempotency-Key.
var safeMethods = map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true, "PUT": true, "DELETE": true}

// retryDelay is how long to wait before retry number attempt+1: the server's
// retry-after when it sent one, otherwise an exponential backoff with jitter
// (500 ms doubling, capped at 8 s, less up to a quarter). false means "do not
// retry": the answer for a retry-after longer than MaxRetryAfter.
func retryDelay(attempt int, retryAfter string, now time.Time, random func() float64) (time.Duration, bool) {
	if d, ok := parseRetryAfter(retryAfter, now); ok {
		if d > MaxRetryAfter {
			return 0, false
		}
		return d, true
	}
	backoff := baseDelay << attempt
	if backoff > maxBackoff || backoff <= 0 {
		backoff = maxBackoff
	}
	return time.Duration(float64(backoff) * (1 - random()*0.25)), true
}

// retryable says whether a failure is one a repeat could mend: a 429, a 5xx, a
// timeout or a network failure; and, for a request that carries an
// Idempotency-Key, the 409 concurrent_idempotent_requests the API answers
// while the first attempt with that key is still being processed.
func retryable(err error, keyed bool) bool {
	var conn *APIConnectionError
	if errors.As(err, &conn) {
		return true
	}
	var timeout *APITimeoutError
	if errors.As(err, &timeout) {
		return true
	}
	var e *Error
	if !errors.As(err, &e) || e.StatusCode == 0 {
		return false
	}
	if e.StatusCode == 429 || e.StatusCode >= 500 {
		return true
	}
	return keyed && e.StatusCode == 409 && e.Name == "concurrent_idempotent_requests"
}

// NewIdempotencyKey returns a new random key, a version 4 UUID.
func NewIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		for i := range b {
			b[i] = byte(mathrand.IntN(256))
		}
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
