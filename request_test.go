package billionsend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestNullableFieldsAreLeftOutSentAsNullOrSet(t *testing.T) {
	cases := []struct {
		body any
		want string
	}{
		{&UpdateDomainRequest{}, `{}`},
		{&UpdateDomainRequest{TrackingSubdomain: Value("links"), UnsubscribeSubdomain: Null[string]()}, `{"tracking_subdomain":"links","unsubscribe_subdomain":null}`},
		{&UpdateDomainRequest{UnsubscribePage: &UpdateDomainRequestUnsubscribePage{PoweredBy: Value(false), SupportURL: Null[string]()}}, `{"unsubscribe_page":{"powered_by":false,"support_url":null}}`},
		{&UpdateBrandRequest{Font: Null[string]()}, `{"font":null}`},
		{&UpdateWebhookRequest{InboxIDs: Value([]string{})}, `{"inbox_ids":[]}`},
		{&UpdateWebhookRequest{InboxIDs: Null[[]string]()}, `{"inbox_ids":null}`},
		{&UpdateTemplateRequest{ReplyTo: Null[any]()}, `{"reply_to":null}`},
		{&UpdateEventRequest{}, `{"schema":null}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.body)
		if err != nil || string(got) != tc.want {
			t.Errorf("%T: %s %v, want %s", tc.body, got, err, tc.want)
		}
	}
	var read UpdateDomainRequest
	if err := json.Unmarshal([]byte(`{"tracking_subdomain":"links"}`), &read); err != nil {
		t.Fatal(err)
	}
	if v, ok := read.TrackingSubdomain.Get(); !ok || v != "links" || read.UnsubscribeSubdomain != nil {
		t.Fatalf("read %+v", read)
	}
	if v, ok := Null[int64]().Get(); ok || v != 0 {
		t.Fatal("null has no value")
	}
}

func TestDecodesTheNewAnswerShapes(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch n {
		case 1:
			jsonAnswer(w, 200, `{"object":"list","has_more":false,"data":[{"id":"s1","email":null,"kind":"domain","domain":"bad.example","removable":true,"origin":"manual","source_id":null,"note":"abuse","created_at":"x"}]}`)
		case 2:
			jsonAnswer(w, 200, `{"id":"d1","sending_limits":{"mode":"warming"}}`)
		default:
			jsonAnswer(w, 403, `{"statusCode":403,"name":"restricted_api_key","message":"no"}`)
		}
	})
	ctx := context.Background()
	list, err := c.Suppressions.List(ctx, &SuppressionsListParams{Kind: String("domain")})
	if err != nil {
		t.Fatal(err)
	}
	entry := list.Data[0]
	if entry.Email != nil || entry.Domain == nil || *entry.Domain != "bad.example" || entry.Kind != "domain" || !entry.Removable || entry.Note == nil {
		t.Fatalf("entry %+v", entry)
	}
	if rec.requests[0].URL.Query().Get("kind") != "domain" {
		t.Fatal("kind was not sent")
	}
	domain, err := c.Domains.Get(ctx, "d1")
	if err != nil || domain.SendingLimits == nil || domain.SendingLimits.Mode != "warming" {
		t.Fatalf("%+v %v", domain, err)
	}
	var denied *PermissionDeniedError
	if _, err := c.Brand.Get(ctx); !errors.As(err, &denied) || denied.Name != "restricted_api_key" {
		t.Fatalf("got %v", err)
	}
}

func TestRetriesA429OnAnyRequestButNoOtherFailureOfAnUnkeyedPost(t *testing.T) {
	c, rec, waits := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch n {
		case 1:
			w.Header().Set("Retry-After", "2")
			jsonAnswer(w, 429, `{}`)
		case 2:
			jsonAnswer(w, 200, `{}`)
		default:
			jsonAnswer(w, 500, `{}`)
		}
	})
	ctx := context.Background()
	if _, err := c.Automations.Duplicate(ctx, "a1"); err != nil || len(rec.requests) != 2 || (*waits)[0].Seconds() != 2 {
		t.Fatalf("%v, %d requests, waits %v", err, len(rec.requests), *waits)
	}
	if _, err := c.Automations.Duplicate(ctx, "a1"); err == nil || len(rec.requests) != 3 {
		t.Fatalf("a 500 was repeated: %v, %d requests", err, len(rec.requests))
	}
}

func TestSendingAnEventKeepsOneKeyAcrossItsRetries(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			jsonAnswer(w, 503, `{}`)
			return
		}
		jsonAnswer(w, 202, `{}`)
	})
	if _, err := c.Events.Send(context.Background(), &SendEventRequest{Event: "signed.up", Email: String("ana@example.com")}); err != nil {
		t.Fatal(err)
	}
	first := rec.requests[0].Header.Get("Idempotency-Key")
	if len(rec.requests) != 2 || first == "" || rec.requests[1].Header.Get("Idempotency-Key") != first {
		t.Fatalf("%d requests, key %q", len(rec.requests), first)
	}
}

func TestSendsNothingForADotSegmentABadHeaderOrACanceledCall(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) { jsonAnswer(w, 200, `{}`) })
	ctx := context.Background()
	for _, id := range []string{".", ".."} {
		if _, err := c.Emails.Get(ctx, id); err == nil || !strings.Contains(err.Error(), "are not ids") {
			t.Errorf("%q: %v", id, err)
		}
	}
	for _, opt := range []RequestOption{
		WithIdempotencyKey("welcome/订单-1"),
		WithIdempotencyKey("secret\n"),
		WithRequestHeader("bad name", "secret"),
	} {
		_, err := c.Emails.Send(ctx, sendBody, opt)
		e, ok := asError(err)
		if !ok || e.Name != "invalid_header" || strings.Contains(e.Message, "secret") || strings.Contains(e.Message, "订单") {
			t.Errorf("got %v", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Emails.Get(canceled, "e1"); err == nil {
		t.Error("a canceled call succeeded")
	}
	if len(rec.requests) != 0 {
		t.Fatalf("%d requests were sent", len(rec.requests))
	}
}

func TestTrimsTheKeyAndTakesTheEnvironmentsForABlankOne(t *testing.T) {
	t.Setenv("BILLIONSEND_API_KEY", " bs_env\n")
	for given, want := range map[string]string{"bs_file\n": "Bearer bs_file", "  \n": "Bearer bs_env"} {
		c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) { jsonAnswer(w, 200, `{}`) }, WithAPIKey(given))
		if _, err := c.Emails.Get(context.Background(), "e1"); err != nil {
			t.Fatal(err)
		}
		if got := rec.requests[0].Header.Get("Authorization"); got != want {
			t.Errorf("%q: %q", given, got)
		}
	}
}

func TestLeavesAnEmptyListParameterOut(t *testing.T) {
	q, _ := encodeParams(&EmailsMetricsGetParams{DomainID: []string{}})
	if len(q) != 0 {
		t.Fatalf("query %v", q)
	}
}
