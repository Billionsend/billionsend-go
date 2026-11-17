package billionsend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a test server that answers from a script and keeps every request.
type recorder struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	answer   func(n int, r *http.Request, w http.ResponseWriter)
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	rec.requests = append(rec.requests, r)
	rec.bodies = append(rec.bodies, string(body))
	n := len(rec.requests)
	rec.mu.Unlock()
	rec.answer(n, r, w)
}

func jsonAnswer(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// testClient returns a client on a recorder, with no real waiting between retries.
func testClient(t *testing.T, answer func(n int, r *http.Request, w http.ResponseWriter), opts ...Option) (*Client, *recorder, *[]time.Duration) {
	t.Helper()
	rec := &recorder{answer: answer}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	c := NewClient(append([]Option{WithAPIKey("bs_test"), WithBaseURL(srv.URL)}, opts...)...)
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return ctx.Err() == nil
	}
	c.random = func() float64 { return 0 }
	return c, rec, &waits
}

var sendBody = &SendEmailRequest{From: "Acme <hello@acme.dev>", To: []string{"ana@example.com"}, Subject: "Hi", HTML: String("<p>Hi</p>")}

func TestReadsTheKeyAndBaseURLFromTheEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bs_env" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "billionsend-go/") {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		jsonAnswer(w, 200, `{"object":"list","data":[],"has_more":false}`)
	}))
	defer srv.Close()
	t.Setenv("BILLIONSEND_API_KEY", "bs_env")
	t.Setenv("BILLIONSEND_BASE_URL", srv.URL+"/")
	if _, err := NewClient().Domains.List(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRefusesToCallWithoutAKey(t *testing.T) {
	t.Setenv("BILLIONSEND_API_KEY", "")
	_, err := NewClient(WithBaseURL("http://127.0.0.1:1")).Emails.Get(context.Background(), "e1")
	var auth *AuthenticationError
	if !errors.As(err, &auth) || auth.Name != "missing_api_key" || auth.StatusCode != 0 {
		t.Fatalf("got %v", err)
	}
}

func TestMapsEachStatusToItsErrorType(t *testing.T) {
	cases := []struct {
		status int
		check  func(error) bool
	}{
		{400, func(e error) bool { var x *ValidationError; return errors.As(e, &x) }},
		{422, func(e error) bool { var x *ValidationError; return errors.As(e, &x) }},
		{401, func(e error) bool { var x *AuthenticationError; return errors.As(e, &x) }},
		{403, func(e error) bool { var x *PermissionDeniedError; return errors.As(e, &x) }},
		{404, func(e error) bool { var x *NotFoundError; return errors.As(e, &x) }},
		{409, func(e error) bool { var x *ConflictError; return errors.As(e, &x) }},
		{429, func(e error) bool { var x *RateLimitError; return errors.As(e, &x) && x.RetryAfter == 120*time.Second }},
		{503, func(e error) bool { var x *APIError; return errors.As(e, &x) }},
		{418, func(e error) bool { var x *Error; return errors.As(e, &x) && x.StatusCode == 418 }},
	}
	for _, tc := range cases {
		c, _, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
			w.Header().Set("x-request-id", "req_1")
			w.Header().Set("Retry-After", "120")
			jsonAnswer(w, tc.status, `{"statusCode":0,"name":"some_error","message":"Nope"}`)
		}, WithMaxRetries(0))
		_, err := c.Emails.Get(context.Background(), "e1")
		if !tc.check(err) {
			t.Errorf("%d: got %T %v", tc.status, err, err)
		}
		base, ok := asError(err)
		if !ok || base.StatusCode != tc.status || base.Name != "some_error" || base.Message != "Nope" || base.RequestID != "req_1" {
			t.Errorf("%d: base %+v", tc.status, base)
		}
	}
}

func TestKeepsTheStatusTextWhenTheBodyIsNotAnError(t *testing.T) {
	c, _, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("bad gateway"))
	}, WithMaxRetries(0))
	_, err := c.Emails.Get(context.Background(), "e1")
	var api *APIError
	if !errors.As(err, &api) || api.Name != "application_error" || api.Message != "bad gateway" {
		t.Fatalf("got %+v", err)
	}
}

func TestRetriesASafeRequestWithBackoff(t *testing.T) {
	c, rec, waits := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n < 3 {
			jsonAnswer(w, 500, `{"name":"internal_error","message":"boom"}`)
			return
		}
		jsonAnswer(w, 200, `{"id":"e1"}`)
	})
	got, err := c.Emails.Get(context.Background(), "e1")
	if err != nil || got.ID != "e1" {
		t.Fatalf("got %v %v", got, err)
	}
	if len(rec.requests) != 3 {
		t.Fatalf("%d requests", len(rec.requests))
	}
	if want := []time.Duration{500 * time.Millisecond, time.Second}; !equalDurations(*waits, want) {
		t.Fatalf("waits %v", *waits)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBacksOffNoLongerThanEightSeconds(t *testing.T) {
	for attempt, want := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second} {
		got, ok := retryDelay(attempt, "", time.Now(), func() float64 { return 0 })
		if !ok || got != want {
			t.Errorf("attempt %d: %v", attempt, got)
		}
	}
	low, _ := retryDelay(0, "", time.Now(), func() float64 { return 1 })
	if low != 375*time.Millisecond {
		t.Errorf("jitter takes at most a quarter: %v", low)
	}
}

func TestHonoursRetryAfterAndGivesUpPastSixtySeconds(t *testing.T) {
	c, rec, waits := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("Retry-After", "3")
			jsonAnswer(w, 429, `{"name":"rate_limit_exceeded","message":"slow down"}`)
			return
		}
		w.Header().Set("Retry-After", "61")
		jsonAnswer(w, 429, `{"name":"rate_limit_exceeded","message":"slow down"}`)
	})
	_, err := c.Emails.Get(context.Background(), "e1")
	var limited *RateLimitError
	if !errors.As(err, &limited) || limited.RetryAfter != 61*time.Second {
		t.Fatalf("got %v", err)
	}
	if len(rec.requests) != 2 || !equalDurations(*waits, []time.Duration{3 * time.Second}) {
		t.Fatalf("%d requests, waits %v", len(rec.requests), *waits)
	}
}

func TestNeverRetriesAPostWithoutAKey(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 503, `{"name":"unavailable","message":"later"}`)
	})
	_, err := c.Domains.Create(context.Background(), &CreateDomainRequest{Name: "acme.dev"})
	if err == nil || len(rec.requests) != 1 {
		t.Fatalf("%d requests, %v", len(rec.requests), err)
	}
}

func TestASendCarriesOneKeyAcrossItsRetries(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch n {
		case 1:
			jsonAnswer(w, 409, `{"name":"concurrent_idempotent_requests","message":"in progress"}`)
		case 2:
			jsonAnswer(w, 500, `{"name":"internal_error","message":"boom"}`)
		default:
			jsonAnswer(w, 200, `{"id":"e1"}`)
		}
	})
	got, err := c.Emails.Send(context.Background(), sendBody)
	if err != nil || got.ID != "e1" || len(rec.requests) != 3 {
		t.Fatalf("got %v %v after %d requests", got, err, len(rec.requests))
	}
	key := rec.requests[0].Header.Get("Idempotency-Key")
	if !uuidV4.MatchString(key) {
		t.Fatalf("key %q", key)
	}
	for _, r := range rec.requests {
		if r.Header.Get("Idempotency-Key") != key {
			t.Fatal("a retry changed the key")
		}
	}
	if !strings.Contains(rec.bodies[0], `"subject":"Hi"`) || rec.requests[0].Header.Get("Content-Type") != "application/json" {
		t.Fatalf("body %s", rec.bodies[0])
	}
	// The next call is another send: another key.
	_, _ = c.Emails.Send(context.Background(), sendBody)
	if rec.requests[3].Header.Get("Idempotency-Key") == key {
		t.Fatal("two calls shared a key")
	}
}

func TestUsesTheCallersKeyAndStopsOnAnOrdinaryConflict(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 409, `{"name":"invalid_idempotent_request","message":"another payload"}`)
	})
	_, err := c.Emails.Send(context.Background(), sendBody, WithIdempotencyKey("welcome/42"))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || len(rec.requests) != 1 || rec.requests[0].Header.Get("Idempotency-Key") != "welcome/42" {
		t.Fatalf("got %v after %d requests", err, len(rec.requests))
	}
}

func TestTimesOutAnAttemptAndRetriesIt(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			time.Sleep(200 * time.Millisecond)
		}
		jsonAnswer(w, 200, `{"id":"e1"}`)
	}, WithTimeout(50*time.Millisecond))
	got, err := c.Emails.Get(context.Background(), "e1")
	if err != nil || got.ID != "e1" || len(rec.requests) != 2 {
		t.Fatalf("got %v %v after %d", got, err, len(rec.requests))
	}
	_, err = c.Emails.Get(context.Background(), "e1", WithRequestMaxRetries(0), WithRequestTimeout(10*time.Millisecond))
	_ = err
}

func TestReportsATimeoutAsAPITimeoutError(t *testing.T) {
	c, _, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		time.Sleep(200 * time.Millisecond)
		jsonAnswer(w, 200, `{}`)
	}, WithTimeout(30*time.Millisecond), WithMaxRetries(0))
	_, err := c.Emails.Get(context.Background(), "e1")
	var timeout *APITimeoutError
	var conn *APIConnectionError
	if !errors.As(err, &timeout) || !errors.As(err, &conn) {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestReportsANetworkFailureAsAPIConnectionError(t *testing.T) {
	c := NewClient(WithAPIKey("bs_test"), WithBaseURL("http://127.0.0.1:1"), WithMaxRetries(0))
	_, err := c.Emails.Get(context.Background(), "e1")
	var conn *APIConnectionError
	if !errors.As(err, &conn) || conn.StatusCode != 0 {
		t.Fatalf("got %v", err)
	}
}

func TestEncodesPathsQueriesAndHeaders(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{"object":"list","data":[],"has_more":false}`)
	})
	_, err := c.Inboxes.Threads.Search(context.Background(), "support@example.com", &InboxesThreadsSearchParams{
		Limit: Int64(5), Read: Bool(false), Query: String("invoice paid"),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := rec.requests[0]
	if r.URL.EscapedPath() != "/inboxes/support@example.com/threads/search" {
		t.Errorf("path %s", r.URL.EscapedPath())
	}
	q := r.URL.Query()
	if q.Get("limit") != "5" || q.Get("read") != "false" || q.Get("query") != "invoice paid" || q.Has("after") {
		t.Errorf("query %v", q)
	}
	if _, err := c.Emails.Get(context.Background(), ""); err == nil {
		t.Error("an empty path parameter went out")
	}
	_, _ = c.Emails.Get(context.Background(), "a/b")
	if got := rec.requests[len(rec.requests)-1].URL.EscapedPath(); got != "/emails/a%2Fb" {
		t.Errorf("escaped path %s", got)
	}
	_, _ = c.Emails.Batch(context.Background(), BatchEmailRequest{*sendBody}, &EmailsBatchParams{XBatchValidation: String("permissive")})
	last := rec.requests[len(rec.requests)-1]
	if last.Header.Get("X-Batch-Validation") != "permissive" || !uuidV4.MatchString(last.Header.Get("Idempotency-Key")) {
		t.Errorf("headers %v", last.Header)
	}
}

func TestAnOperationWithoutAuthSendsNoKey(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 201, `{}`)
	})
	if _, err := c.Agent.Signup(context.Background(), &AgentSignupRequest{}); err != nil {
		t.Fatal(err)
	}
	if rec.requests[0].Header.Get("Authorization") != "" {
		t.Fatal("sent a key")
	}
}

func TestHandsBackTheResponseHeaders(t *testing.T) {
	c, _, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		w.Header().Set("x-request-id", "req_9")
		jsonAnswer(w, 200, `{"id":"e1"}`)
	})
	var h http.Header
	if _, err := c.Emails.Get(context.Background(), "e1", WithResponseHeaders(&h)); err != nil || h.Get("x-request-id") != "req_9" {
		t.Fatalf("headers %v, %v", h, err)
	}
}

func TestNewIdempotencyKeyIsAVersion4UUID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		k := NewIdempotencyKey()
		if !uuidV4.MatchString(k) || seen[k] {
			t.Fatalf("key %q", k)
		}
		seen[k] = true
	}
}
