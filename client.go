package billionsend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the API the client talks to unless told otherwise.
const DefaultBaseURL = "https://api.billionsend.com"

const (
	// DefaultTimeout bounds each attempt; event streams are timed only until they connect.
	DefaultTimeout = 60 * time.Second
	// DefaultMaxRetries is how many times a failed request safe to repeat is retried.
	DefaultMaxRetries = 2
)

// Client is the BillionSend API client. It is safe for concurrent use; make
// one and share it.
type Client struct {
	services
	apiKey     string
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
	maxRetries int
	headers    http.Header
	now        func() time.Time
	random     func() float64
	sleep      func(ctx context.Context, d time.Duration) bool
}

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sets the API key (bs_...). The default is BILLIONSEND_API_KEY.
func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }

// WithBaseURL sets the API's base URL. The default is BILLIONSEND_BASE_URL,
// then https://api.billionsend.com.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient sets the HTTP client the requests go through.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// WithTimeout sets the per-attempt timeout (default 60 s).
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithMaxRetries sets the retries for 429, 5xx, timeouts and network failures
// (default 2). Only requests safe to repeat are retried: GET, PUT and DELETE,
// and the sending operations, which always carry an Idempotency-Key.
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// WithHeader adds a header sent with every request.
func WithHeader(name, value string) Option {
	return func(c *Client) { c.headers.Add(name, value) }
}

// NewClient makes a client. Without WithAPIKey it reads BILLIONSEND_API_KEY;
// without WithBaseURL, BILLIONSEND_BASE_URL.
func NewClient(opts ...Option) *Client {
	c := &Client{
		apiKey:     os.Getenv("BILLIONSEND_API_KEY"),
		baseURL:    strings.TrimRight(os.Getenv("BILLIONSEND_BASE_URL"), "/"),
		httpClient: http.DefaultClient,
		timeout:    DefaultTimeout,
		maxRetries: DefaultMaxRetries,
		headers:    http.Header{},
		now:        time.Now,
		random:     mathrand.Float64,
		sleep:      sleepContext,
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.maxRetries < 0 {
		c.maxRetries = 0
	}
	c.initServices()
	return c
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// RequestOption configures one call.
type RequestOption func(*requestConfig)

type requestConfig struct {
	idempotencyKey  string
	headers         http.Header
	timeout         time.Duration
	maxRetries      *int
	responseHeaders *http.Header
	lastEventID     string
	noReconnect     bool
	maxReconnects   *int
	reconnectDelay  time.Duration
}

// WithIdempotencyKey sets the Idempotency-Key of a sending operation (1 to
// 256 characters): a repeat with the same key and payload answers the first
// result instead of sending again. Without one the client makes a new key for
// each call (a UUID) that the call's retries reuse; pass your own to make the
// send safe across calls and processes too, such as "welcome/" + userID.
func WithIdempotencyKey(key string) RequestOption {
	return func(r *requestConfig) { r.idempotencyKey = key }
}

// WithRequestHeader adds a header to this request.
func WithRequestHeader(name, value string) RequestOption {
	return func(r *requestConfig) { r.headers.Add(name, value) }
}

// WithRequestTimeout overrides the client's per-attempt timeout for this request.
func WithRequestTimeout(d time.Duration) RequestOption {
	return func(r *requestConfig) { r.timeout = d }
}

// WithRequestMaxRetries overrides the client's retries for this request.
func WithRequestMaxRetries(n int) RequestOption {
	return func(r *requestConfig) { r.maxRetries = &n }
}

// WithResponseHeaders stores the answer's headers (of the last attempt) in h.
func WithResponseHeaders(h *http.Header) RequestOption {
	return func(r *requestConfig) { r.responseHeaders = h }
}

// call describes one operation, as the generated methods fill it in.
type call struct {
	operation  string
	method     string
	path       string
	pathParams []string // name, value, name, value, ...
	body       any
	params     any
	idempotent bool
	noAuth     bool
}

func (c *Client) config(opts []RequestOption) *requestConfig {
	r := &requestConfig{headers: http.Header{}, timeout: c.timeout}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// fillPath fills {name} placeholders, refusing an empty value.
func fillPath(template string, params []string) (string, error) {
	out := template
	for i := 0; i+1 < len(params); i += 2 {
		if params[i+1] == "" {
			return "", applicationError("missing path parameter %q", params[i])
		}
		out = strings.Replace(out, "{"+params[i]+"}", url.PathEscape(params[i+1]), 1)
	}
	return out, nil
}

// encodeParams splits a params struct into its query string and headers
// (struct tags `query:"name"` and `header:"name"`). List values go
// comma-separated in one key, as the API document declares them.
func encodeParams(params any) (url.Values, http.Header) {
	q, h := url.Values{}, http.Header{}
	v := reflect.ValueOf(params)
	if !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return q, h
	}
	v = reflect.Indirect(v)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		text, ok := paramText(f)
		if !ok {
			continue
		}
		if name := t.Field(i).Tag.Get("query"); name != "" {
			q.Set(name, text)
		} else if name := t.Field(i).Tag.Get("header"); name != "" {
			h.Set(name, text)
		}
	}
	return q, h
}

func paramText(f reflect.Value) (string, bool) {
	switch f.Kind() {
	case reflect.Pointer, reflect.Interface:
		if f.IsNil() {
			return "", false
		}
		return paramText(f.Elem())
	case reflect.Slice:
		if f.IsNil() {
			return "", false
		}
		parts := make([]string, 0, f.Len())
		for i := 0; i < f.Len(); i++ {
			if s, ok := paramText(f.Index(i)); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ","), true
	case reflect.String:
		return f.String(), true
	case reflect.Bool:
		return strconv.FormatBool(f.Bool()), true
	case reflect.Int, reflect.Int64, reflect.Int32:
		return strconv.FormatInt(f.Int(), 10), true
	case reflect.Float64, reflect.Float32:
		return strconv.FormatFloat(f.Float(), 'f', -1, 64), true
	}
	return fmt.Sprint(f.Interface()), true
}

// prepared is a request ready to send, again and again on retries.
type prepared struct {
	method, url string
	header      http.Header
	body        []byte
	keyed       bool
	retryable   bool
	maxRetries  int
	timeout     time.Duration
}

func (c *Client) prepare(spec *call, r *requestConfig, stream bool) (*prepared, error) {
	if !spec.noAuth && c.apiKey == "" {
		return nil, &AuthenticationError{errorBase{
			Name:    "missing_api_key",
			Message: "no API key: pass WithAPIKey to NewClient or set BILLIONSEND_API_KEY",
		}}
	}
	path, err := fillPath(spec.path, spec.pathParams)
	if err != nil {
		return nil, err
	}
	query, paramHeaders := encodeParams(spec.params)
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	h := http.Header{}
	if stream {
		h.Set("Accept", "text/event-stream")
	} else {
		h.Set("Accept", "application/json")
	}
	h.Set("User-Agent", "billionsend-go/"+Version)
	for _, src := range []http.Header{c.headers, r.headers, paramHeaders} {
		for k, vs := range src {
			h[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
		}
	}
	p := &prepared{method: spec.method, url: u, header: h, timeout: r.timeout}
	if spec.body != nil && !isNilPointer(spec.body) {
		p.body, err = json.Marshal(spec.body)
		if err != nil {
			return nil, applicationError("encoding the request body: %v", err)
		}
		h.Set("Content-Type", "application/json")
	}
	switch {
	case r.idempotencyKey != "":
		h.Set("Idempotency-Key", r.idempotencyKey)
	case spec.idempotent && h.Get("Idempotency-Key") == "":
		h.Set("Idempotency-Key", NewIdempotencyKey())
	}
	if spec.noAuth {
		h.Del("Authorization")
	} else {
		h.Set("Authorization", "Bearer "+c.apiKey)
	}
	p.keyed = h.Get("Idempotency-Key") != ""
	p.retryable = safeMethods[spec.method] || p.keyed
	p.maxRetries = c.maxRetries
	if r.maxRetries != nil {
		p.maxRetries = *r.maxRetries
	}
	return p, nil
}

func isNilPointer(v any) bool {
	rv := reflect.ValueOf(v)
	return (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Slice) && rv.IsNil()
}

// do calls one operation and decodes its JSON answer into out (nil: drop it).
func (c *Client) do(ctx context.Context, spec *call, out any, opts []RequestOption) error {
	r := c.config(opts)
	p, err := c.prepare(spec, r, false)
	if err != nil {
		return err
	}
	resp, body, err := c.send(ctx, p, false)
	if r.responseHeaders != nil && resp != nil {
		*r.responseHeaders = resp.Header
	}
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{
			StatusCode: resp.StatusCode,
			Name:       "application_error",
			Message:    "the response body is not valid JSON: " + err.Error(),
			RequestID:  resp.Header.Get("x-request-id"),
		}
	}
	return nil
}

// send runs the attempts of one request. A stream's answer comes back open
// (its body unread); every other answer is read whole.
func (c *Client) send(ctx context.Context, p *prepared, stream bool) (*http.Response, []byte, error) {
	for attempt := 0; ; attempt++ {
		resp, body, retryAfter, err := c.attempt(ctx, p, stream)
		if err == nil {
			return resp, body, nil
		}
		if ctx.Err() != nil || !p.retryable || attempt >= p.maxRetries || !retryable(err, p.keyed) {
			return resp, body, err
		}
		delay, ok := retryDelay(attempt, retryAfter, c.now(), c.random)
		if !ok {
			return resp, body, err
		}
		if !c.sleep(ctx, delay) {
			return resp, body, applicationError("request canceled: %v", ctx.Err())
		}
	}
}

func (c *Client) attempt(ctx context.Context, p *prepared, stream bool) (*http.Response, []byte, string, error) {
	actx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(p.timeout, cancel)
	timedOut := func() bool { return !timer.Stop() && ctx.Err() == nil }
	var reader io.Reader
	if p.body != nil {
		reader = bytes.NewReader(p.body)
	}
	req, err := http.NewRequestWithContext(actx, p.method, p.url, reader)
	if err != nil {
		cancel()
		return nil, nil, "", applicationError("building the request: %v", err)
	}
	req.Header = p.header.Clone()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		expired := timedOut()
		cancel()
		switch {
		case ctx.Err() != nil:
			return nil, nil, "", applicationError("request canceled: %v", ctx.Err())
		case expired:
			return nil, nil, "", timeoutError(fmt.Sprintf("request timed out after %s", p.timeout))
		}
		return nil, nil, "", connectionError("network error: " + err.Error())
	}
	if stream && resp.StatusCode < 300 {
		timer.Stop()
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			_ = resp.Body.Close()
			cancel()
			return resp, nil, "", &Error{
				StatusCode: resp.StatusCode,
				Name:       "application_error",
				Message:    "expected an event stream, got " + resp.Header.Get("Content-Type"),
				RequestID:  resp.Header.Get("x-request-id"),
			}
		}
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil, "", nil
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	expired := timedOut()
	cancel()
	if readErr != nil {
		if expired {
			return resp, nil, "", timeoutError(fmt.Sprintf("request timed out after %s", p.timeout))
		}
		return resp, nil, "", connectionError("reading the response: " + readErr.Error())
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, body, "", nil
	}
	return resp, body, resp.Header.Get("Retry-After"),
		errorFromResponse(resp.StatusCode, resp.Status, body, resp.Header)
}

type cancelOnClose struct {
	io.ReadCloser
	cancel func()
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// String returns a pointer to s, for optional fields.
func String(s string) *string { return &s }

// Int64 returns a pointer to n, for optional fields.
func Int64(n int64) *int64 { return &n }

// Float64 returns a pointer to f, for optional fields.
func Float64(f float64) *float64 { return &f }

// Bool returns a pointer to b, for optional fields.
func Bool(b bool) *bool { return &b }

// asError is errors.As for the client's own types, for tests and callers alike.
func asError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
