package billionsend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StreamGapEventName is the event name of a StreamGapEvent.
const StreamGapEventName = "stream.gap"

// InboxEventData is the webhook payload's data: metadata only (never a body,
// code or link). The ids say what to read next with the REST methods; the
// rest (thread, email, draft, ...) rides along in Extra as the matching
// webhook event has it.
type InboxEventData struct {
	// Who caused it: "api", "dashboard" or "system" (empty on email.received).
	Source   string `json:"source,omitempty"`
	InboxID  string `json:"inbox_id,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	EmailID  string `json:"email_id,omitempty"`
	DraftID  string `json:"draft_id,omitempty"`
	// Every field of data, the ones above included.
	Extra map[string]any `json:"-"`
}

// UnmarshalJSON keeps every field in Extra besides the named ones.
func (d *InboxEventData) UnmarshalJSON(b []byte) error {
	type plain InboxEventData
	if err := json.Unmarshal(b, (*plain)(d)); err != nil {
		return err
	}
	return json.Unmarshal(b, &d.Extra)
}

// InboxEventPayload is the JSON body of one inbox event.
type InboxEventPayload struct {
	Type      string         `json:"type"`
	CreatedAt string         `json:"created_at"`
	InboxID   string         `json:"inbox_id"`
	Data      InboxEventData `json:"data"`
}

// InboxStreamItem is what an inbox EventStream yields: an *InboxStreamEvent
// or a *StreamGapEvent. Tell them apart with a type switch.
type InboxStreamItem interface{ isInboxStreamItem() }

// InboxStreamEvent is one inbox event. ID is its stream position: keep the
// last one to resume with WithLastEventID.
type InboxStreamEvent struct {
	ID    string
	Event string
	Data  InboxEventPayload
	// Raw is the event's data as the server sent it.
	Raw json.RawMessage
}

// StreamGapEvent says the stream resumed from a position older than the
// server keeps (24 hours): events may be missing, so read the REST lists
// again. It does not move the stream position. One stream hands it out once
// per position.
type StreamGapEvent struct {
	Reason string
}

func (*InboxStreamEvent) isInboxStreamItem() {}
func (*StreamGapEvent) isInboxStreamItem()   {}

// WithLastEventID resumes a stream after this event id (sent as Last-Event-ID).
func WithLastEventID(id string) RequestOption {
	return func(r *requestConfig) { r.lastEventID = id }
}

// WithoutReconnect makes a stream end when its connection drops, instead of
// reconnecting with Last-Event-ID.
func WithoutReconnect() RequestOption { return func(r *requestConfig) { r.noReconnect = true } }

// WithMaxReconnects sets how many reconnects in a row may fail before a
// stream gives up (default 5). A reconnect that opens the stream resets the
// count, so the server's scheduled closes never use it up.
func WithMaxReconnects(n int) RequestOption { return func(r *requestConfig) { r.maxReconnects = &n } }

// WithReconnectDelay sets the wait before reconnecting (default 1 s; a server
// retry field overrides it).
func WithReconnectDelay(d time.Duration) RequestOption {
	return func(r *requestConfig) { r.reconnectDelay = d }
}

// EventStream is a live inbox event stream. Range over Events; leaving the
// loop, or calling Close, closes the connection. When the connection drops
// (the server also closes every stream about every ten minutes) it reconnects
// with Last-Event-ID and the events in between are replayed first, unless
// WithoutReconnect was given. After the reconnect limit it yields an
// *APIConnectionError; an error a retry cannot fix (a revoked key) is yielded
// as its own type.
type EventStream struct {
	client  *Client
	ctx     context.Context
	cancel  context.CancelFunc
	prep    *prepared
	cfg     *requestConfig
	endsOn  map[string]bool
	mu      sync.Mutex
	body    io.ReadCloser
	lastID  string
	delay   time.Duration
	started bool
}

func (c *Client) stream(ctx context.Context, spec *call, endsOn []string, opts []RequestOption) (*EventStream, error) {
	r := c.config(opts)
	p, err := c.prepare(spec, r, true)
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &EventStream{
		client: c, ctx: sctx, cancel: cancel, prep: p, cfg: r,
		endsOn: map[string]bool{}, lastID: r.lastEventID, delay: time.Second,
	}
	if r.reconnectDelay > 0 {
		s.delay = r.reconnectDelay
	}
	for _, e := range endsOn {
		s.endsOn[e] = true
	}
	if err := s.connect(); err != nil {
		cancel()
		return nil, err
	}
	return s, nil
}

func (s *EventStream) connect() error {
	p := *s.prep
	p.header = s.prep.header.Clone()
	if s.lastID != "" {
		p.header.Set("Last-Event-ID", s.lastID)
	}
	resp, _, err := s.client.send(s.ctx, &p, true)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.body = resp.Body
	s.mu.Unlock()
	return nil
}

// LastEventID is the id of the last event received: pass it to
// WithLastEventID to resume later.
func (s *EventStream) LastEventID() string { return s.lastID }

// Close closes the stream; a running loop ends without an error.
func (s *EventStream) Close() {
	s.cancel()
	s.mu.Lock()
	if s.body != nil {
		_ = s.body.Close()
		s.body = nil
	}
	s.mu.Unlock()
}

// serverEvent is one parsed server-sent event.
type serverEvent struct {
	id    string
	event string
	data  string
}

// parseEvents reads a text/event-stream body following the WHATWG rules:
// fields per line, an event per blank line, comments and unknown fields
// skipped, retry setting the reconnect delay, an unterminated event dropped.
func parseEvents(body io.Reader, lastID string, onRetry func(time.Duration), yield func(serverEvent) bool) error {
	r := bufio.NewReader(body)
	var data []string
	event := ""
	id := lastID
	first := true
	for {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if first {
			line = strings.TrimPrefix(line, "\uFEFF")
			first = false
		}
		if line == "" {
			if len(data) > 0 {
				name := event
				if name == "" {
					name = "message"
				}
				if !yield(serverEvent{id: id, event: name, data: strings.Join(data, "\n")}) {
					return nil
				}
			}
			data, event = nil, ""
			if err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			event = value
		case "id":
			if !strings.Contains(value, "\x00") {
				id = value
			}
		case "retry":
			if ms, convErr := strconv.Atoi(value); convErr == nil && ms >= 0 {
				onRetry(time.Duration(ms) * time.Millisecond)
			}
		}
		if err != nil {
			return err
		}
	}
}

// Events yields the stream's items until it ends, fails or is closed. It can
// be ranged over once.
func (s *EventStream) Events() iter.Seq2[InboxStreamItem, error] {
	return func(yield func(InboxStreamItem, error) bool) {
		defer s.Close()
		if s.started {
			yield(nil, applicationError("an EventStream can be ranged over only once"))
			return
		}
		s.started = true
		reconnect := !s.cfg.noReconnect
		maxReconnects := 5
		if s.cfg.maxReconnects != nil {
			maxReconnects = *s.cfg.maxReconnects
		}
		failures := 0
		// The position a gap was last handed out for: the server repeats the
		// gap on every reconnect from that position until a newer event moves it.
		gapAt, gapGiven := "", false
		for s.ctx.Err() == nil {
			s.mu.Lock()
			body := s.body
			s.mu.Unlock()
			if body != nil {
				stop, ended := false, false
				_ = parseEvents(body, s.lastID, func(d time.Duration) { s.delay = d }, func(ev serverEvent) bool {
					if ev.event == StreamGapEventName {
						if gapGiven && gapAt == s.lastID {
							return true
						}
						gapAt, gapGiven = s.lastID, true
						var gap struct {
							Reason string `json:"reason"`
						}
						_ = json.Unmarshal([]byte(ev.data), &gap)
						if !yield(&StreamGapEvent{Reason: gap.Reason}, nil) {
							stop = true
							return false
						}
						return true
					}
					s.lastID = ev.id
					item := &InboxStreamEvent{ID: ev.id, Event: ev.event, Raw: json.RawMessage(ev.data)}
					_ = json.Unmarshal([]byte(ev.data), &item.Data)
					if !yield(item, nil) {
						stop = true
						return false
					}
					if s.endsOn[ev.event] {
						ended = true
						return false
					}
					return true
				})
				s.mu.Lock()
				if s.body != nil {
					_ = s.body.Close()
					s.body = nil
				}
				s.mu.Unlock()
				if stop || ended {
					return
				}
			}
			if s.ctx.Err() != nil || !reconnect {
				return
			}
			failures++
			if failures > maxReconnects {
				yield(nil, connectionError(fmt.Sprintf("the event stream disconnected; gave up after %d reconnects", maxReconnects)))
				return
			}
			if !s.client.sleep(s.ctx, s.delay) {
				return
			}
			if err := s.connect(); err != nil {
				if s.ctx.Err() != nil {
					return
				}
				if !retryable(err, false) {
					yield(nil, err)
					return
				}
				continue
			}
			// Open again: only reconnects that fail in a row count, not the
			// server closing a quiet stream every ten minutes or so.
			failures = 0
		}
	}
}
