package billionsend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestListAllWalksForwardPageByPage(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch r.URL.Query().Get("after") {
		case "":
			jsonAnswer(w, 200, `{"object":"list","has_more":true,"data":[{"id":"a"},{"id":"b"}]}`)
		case "b":
			jsonAnswer(w, 200, `{"object":"list","has_more":true,"data":[{"id":"c"}]}`)
		default:
			jsonAnswer(w, 200, `{"object":"list","has_more":false,"data":[{"id":"d"}]}`)
		}
	})
	var ids []string
	for item, err := range c.Emails.ListAll(context.Background(), &EmailsListParams{Limit: Int64(2)}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, item.ID)
	}
	if fmt.Sprint(ids) != "[a b c d]" || len(rec.requests) != 3 {
		t.Fatalf("ids %v after %d requests", ids, len(rec.requests))
	}
	if rec.requests[2].URL.Query().Get("after") != "c" || rec.requests[2].URL.Query().Get("limit") != "2" {
		t.Fatalf("third page %s", rec.requests[2].URL.RawQuery)
	}
}

func TestListAllWalksBackwardFromBefore(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if r.URL.Query().Get("before") == "x" {
			jsonAnswer(w, 200, `{"object":"list","has_more":true,"data":[{"id":"v"},{"id":"w"}]}`)
			return
		}
		jsonAnswer(w, 200, `{"object":"list","has_more":false,"data":[{"id":"u"}]}`)
	})
	var ids []string
	for item, err := range c.Emails.ListAll(context.Background(), &EmailsListParams{Before: String("x")}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, item.ID)
	}
	if fmt.Sprint(ids) != "[v w u]" || rec.requests[1].URL.Query().Get("before") != "v" || rec.requests[1].URL.Query().Has("after") {
		t.Fatalf("ids %v, second %s", ids, rec.requests[1].URL.RawQuery)
	}
}

func TestListAllEndsOnAFailedPageAndStopsWhenTheLoopDoes(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			jsonAnswer(w, 200, `{"object":"list","has_more":true,"data":[{"id":"a"},{"id":"b"}]}`)
			return
		}
		jsonAnswer(w, 403, `{"name":"restricted_api_key","message":"no"}`)
	}, WithMaxRetries(0))
	var seen []string
	var failed error
	for item, err := range c.Emails.ListAll(context.Background(), nil) {
		if err != nil {
			failed = err
			break
		}
		seen = append(seen, item.ID)
	}
	var denied *PermissionDeniedError
	if fmt.Sprint(seen) != "[a b]" || !errors.As(failed, &denied) {
		t.Fatalf("seen %v, %v", seen, failed)
	}
	for range c.Emails.ListAll(context.Background(), nil) {
		break
	}
	if len(rec.requests) != 3 {
		t.Fatalf("%d requests", len(rec.requests))
	}
}

func TestListAllRefusesAnAnswerThatIsNotAListPage(t *testing.T) {
	for _, answer := range []string{`{"object":"list","has_more":true}`, `{"data":null,"has_more":true}`, `{"data":{},"has_more":true}`, `[]`} {
		c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
			jsonAnswer(w, 200, answer)
		})
		var failed error
		for _, err := range c.Emails.ListAll(context.Background(), nil) {
			if err != nil {
				failed = err
			}
		}
		var e *Error
		if !errors.As(failed, &e) || e.Name != "application_error" || len(rec.requests) != 1 {
			t.Errorf("%s: got %v after %d requests", answer, failed, len(rec.requests))
		}
	}
}

func TestListAllRefusesAnItemToContinueFromWithoutAnID(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{"object":"list","has_more":true,"data":[{"id":"a"},{"subject":"no id"}]}`)
	})
	var seen []string
	var failed error
	for item, err := range c.Emails.ListAll(context.Background(), nil) {
		if err != nil {
			failed = err
			break
		}
		seen = append(seen, item.ID)
	}
	var e *Error
	if !errors.As(failed, &e) || e.Name != "application_error" || !strings.Contains(e.Message, "no id") {
		t.Fatalf("got %v", failed)
	}
	if fmt.Sprint(seen) != "[a ]" || len(rec.requests) != 1 {
		t.Fatalf("seen %v after %d requests", seen, len(rec.requests))
	}
}
