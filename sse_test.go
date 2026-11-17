package billionsend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func streamAnswer(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(body))
}

func event(id, name, inbox string) string {
	return fmt.Sprintf("id: %s\nevent: %s\ndata: {\"type\":%q,\"created_at\":\"2026-11-17T00:00:00Z\",\"inbox_id\":%q,\"data\":{\"thread_id\":\"t1\",\"thread\":{\"subject\":\"Hi\"}}}\n\n", id, name, name, inbox)
}

func TestStreamsTypedEventsAndResumesAfterADrop(t *testing.T) {
	c, rec, waits := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch n {
		case 1:
			streamAnswer(w, ": keep-alive\n\nretry: 250\n"+event("1", "inbox.email.received", "i1")+event("2", "inbox.thread.created", "i1"))
		case 2:
			streamAnswer(w, "event: stream.gap\ndata: {\"reason\":\"position expired\"}\n\n"+event("3", "email.received", "i1")+"data: unterminated")
		default:
			jsonAnswer(w, 503, `{"name":"unavailable","message":"later"}`)
		}
	}, WithMaxRetries(0))
	s, err := c.Inboxes.Events.Stream(context.Background(), &InboxesEventsStreamParams{Types: String("inbox.email.received")}, WithLastEventID("0"), WithMaxReconnects(1))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var final error
	for item, err := range s.Events() {
		if err != nil {
			final = err
			break
		}
		switch ev := item.(type) {
		case *InboxStreamEvent:
			got = append(got, ev.ID+":"+ev.Event+":"+ev.Data.Data.ThreadID+":"+fmt.Sprint(ev.Data.Data.Extra["thread"] != nil))
		case *StreamGapEvent:
			got = append(got, "gap:"+ev.Reason)
		}
	}
	want := "[1:inbox.email.received:t1:true 2:inbox.thread.created:t1:true gap:position expired 3:email.received:t1:true]"
	if fmt.Sprint(got) != want {
		t.Fatalf("got %v", got)
	}
	var conn *APIConnectionError
	if !errors.As(final, &conn) {
		t.Fatalf("final %v", final)
	}
	if rec.requests[0].Header.Get("Last-Event-ID") != "0" || rec.requests[1].Header.Get("Last-Event-ID") != "2" || rec.requests[2].Header.Get("Last-Event-ID") != "3" {
		t.Fatal("did not resume from the last event")
	}
	if rec.requests[0].URL.Query().Get("types") != "inbox.email.received" || rec.requests[0].Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("first request %v", rec.requests[0].URL)
	}
	if len(*waits) == 0 || (*waits)[0] != 250*time.Millisecond {
		t.Fatalf("the server's retry was not used: %v", *waits)
	}
	if s.LastEventID() != "3" {
		t.Fatalf("last id %q", s.LastEventID())
	}
}

func TestHandsOutAGapOncePerPosition(t *testing.T) {
	gap := "event: stream.gap\ndata: {\"reason\":\"old\"}\n\n"
	c, _, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n <= 2 {
			streamAnswer(w, gap)
			return
		}
		streamAnswer(w, gap+event("9", "inbox.updated", "i1"))
	})
	s, err := c.Inboxes.Events.Stream(context.Background(), nil, WithMaxReconnects(3))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for item, err := range s.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if ev, ok := item.(*InboxStreamEvent); ok {
			got = append(got, ev.ID)
			s.Close()
			continue
		}
		got = append(got, "gap")
	}
	if fmt.Sprint(got) != "[gap 9]" {
		t.Fatalf("got %v", got)
	}
}

func TestOneInboxsStreamEndsWithItsDeletion(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		streamAnswer(w, event("1", "inbox.email.received", "i1")+event("2", "inbox.deleted", "i1")+event("3", "inbox.updated", "i1"))
	})
	s, err := c.Inboxes.Events.StreamInbox(context.Background(), "support@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for item, err := range s.Events() {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, item.(*InboxStreamEvent).Event)
	}
	if fmt.Sprint(got) != "[inbox.email.received inbox.deleted]" || len(rec.requests) != 1 {
		t.Fatalf("got %v after %d requests", got, len(rec.requests))
	}
}

func TestStopsOnAnErrorARetryCannotFix(t *testing.T) {
	c, _, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			streamAnswer(w, event("1", "inbox.updated", "i1"))
			return
		}
		jsonAnswer(w, 401, `{"name":"invalid_api_key","message":"revoked"}`)
	})
	s, err := c.Inboxes.Events.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var final error
	for _, err := range s.Events() {
		if err != nil {
			final = err
		}
	}
	var auth *AuthenticationError
	if !errors.As(final, &auth) {
		t.Fatalf("got %v", final)
	}
}

func TestRefusesAnAnswerThatIsNotAStream(t *testing.T) {
	c, _, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{}`)
	})
	_, err := c.Inboxes.Events.Stream(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "expected an event stream") {
		t.Fatalf("got %v", err)
	}
}

func TestWithoutReconnectEndsWhenTheConnectionDrops(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		streamAnswer(w, event("1", "inbox.updated", "i1"))
	})
	s, err := c.Inboxes.Events.Stream(context.Background(), nil, WithoutReconnect())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, err := range s.Events() {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 1 || len(rec.requests) != 1 {
		t.Fatalf("%d events, %d requests", n, len(rec.requests))
	}
}
