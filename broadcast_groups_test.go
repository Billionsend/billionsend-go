package billionsend

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBroadcastGroupsCreateSendsTheGroupAndReadsItsBroadcasts(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 201, `{"id":"g1","broadcast_ids":["b1","b2"],"finishes_at":null}`)
	})
	got, err := c.BroadcastGroups.Create(context.Background(), &CreateBroadcastGroupRequest{
		Name:          "Launch",
		DomainIDs:     []string{"d1", "d2"},
		FromLocalPart: "news",
		Subject:       "Hello",
		HTML:          String("<p>Hi</p>"),
		Send:          Bool(true),
	})
	if err != nil || got.ID != "g1" || strings.Join(got.BroadcastIDs, ",") != "b1,b2" || got.FinishesAt != nil {
		t.Fatalf("%+v %v", got, err)
	}
	r := rec.requests[0]
	if r.Method != "POST" || r.URL.Path != "/broadcast-groups" {
		t.Fatalf("%s %s", r.Method, r.URL.Path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rec.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["from_local_part"] != "news" || body["send"] != true || body["html"] != "<p>Hi</p>" || len(body["domain_ids"].([]any)) != 2 {
		t.Fatalf("body %s", rec.bodies[0])
	}
	if _, has := body["text"]; has {
		t.Fatal("an unset field must be left out")
	}
}

func TestBroadcastGroupsGetDecodesTheGroupAndEachDomainsBroadcast(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{"object":"broadcast_group","id":"g1","name":"Launch","status":"sending","domain_ids":["d1"],"domain_weights":{"d1":1},"from_local_part":"news","from_name":null,"subject":"Hi","html":null,"text":"t","reply_to":[],"preview_text":null,"topic_id":null,"segment_id":"s1","dormant_check":true,"include_dormant":false,"reassign_domain_ids":[],"other_brand_count":null,"scheduled_at":null,"finishes_at":"2026-11-27T10:00:00Z","sent_count":4,"total_count":10,"created_at":"x","updated_at":"y","broadcasts":[{"id":"b1","domain_id":"d1","from":"News <news@a.example>","status":"sending","sent_at":null,"sent_count":4,"total_count":10,"finishes_at":null,"failure_reason":null}]}`)
	})
	g, err := c.BroadcastGroups.Get(context.Background(), "g1")
	if err != nil || g.Status != "sending" || g.DomainWeights["d1"] != 1 || g.SegmentID == nil || *g.SegmentID != "s1" || g.TotalCount == nil || *g.TotalCount != 10 {
		t.Fatalf("%+v %v", g, err)
	}
	if len(g.Broadcasts) != 1 || g.Broadcasts[0].DomainID == nil || *g.Broadcasts[0].DomainID != "d1" || g.Broadcasts[0].SentCount != 4 {
		t.Fatalf("broadcasts %+v", g.Broadcasts)
	}
	if rec.requests[0].Method != "GET" || rec.requests[0].URL.Path != "/broadcast-groups/g1" {
		t.Fatalf("%s %s", rec.requests[0].Method, rec.requests[0].URL.Path)
	}
}

func TestBroadcastGroupsListAllWalksPages(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if r.URL.Query().Get("after") == "" {
			jsonAnswer(w, 200, `{"object":"list","has_more":true,"data":[{"id":"g2","name":"B","broadcasts":[]}]}`)
			return
		}
		jsonAnswer(w, 200, `{"object":"list","has_more":false,"data":[{"id":"g1","name":"A","broadcasts":[]}]}`)
	})
	var ids []string
	for g, err := range c.BroadcastGroups.ListAll(context.Background(), &BroadcastGroupsListParams{Limit: Int64(1)}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, g.ID)
	}
	if strings.Join(ids, ",") != "g2,g1" || rec.requests[1].URL.Path != "/broadcast-groups" || rec.requests[1].URL.Query().Get("after") != "g2" {
		t.Fatalf("%v %v", ids, rec.requests[1].URL)
	}
}

func TestBroadcastGroupsUpdateClearsOrKeepsAField(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{"id":"g1"}`)
	})
	got, err := c.BroadcastGroups.Update(context.Background(), "g1", &UpdateBroadcastGroupRequest{
		Subject:   String("New"),
		FromName:  Null[string](),
		SegmentID: Value("s2"),
	})
	if err != nil || got.ID != "g1" {
		t.Fatalf("%+v %v", got, err)
	}
	if rec.requests[0].Method != "PATCH" || rec.requests[0].URL.Path != "/broadcast-groups/g1" {
		t.Fatalf("%s %s", rec.requests[0].Method, rec.requests[0].URL.Path)
	}
	if rec.bodies[0] != `{"from_name":null,"segment_id":"s2","subject":"New"}` {
		t.Fatalf("body %s", rec.bodies[0])
	}
}

func TestBroadcastGroupsSendCancelAndRemove(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch n {
		case 1:
			jsonAnswer(w, 200, `{"id":"g1","broadcast_ids":["b1"],"finishes_at":"2026-11-27T10:00:00Z"}`)
		case 2:
			jsonAnswer(w, 200, `{"object":"broadcast_group","id":"g1","canceled_remaining":7}`)
		default:
			jsonAnswer(w, 200, `{"object":"broadcast_group","id":"g1","deleted":true}`)
		}
	})
	ctx := context.Background()
	sent, err := c.BroadcastGroups.Send(ctx, "g1", &SendBroadcastRequest{ScheduledAt: String("in 1 hour")})
	if err != nil || sent.FinishesAt == nil || len(sent.BroadcastIDs) != 1 {
		t.Fatalf("%+v %v", sent, err)
	}
	canceled, err := c.BroadcastGroups.Cancel(ctx, "g1")
	if err != nil || canceled.CanceledRemaining != 7 {
		t.Fatalf("%+v %v", canceled, err)
	}
	removed, err := c.BroadcastGroups.Remove(ctx, "g1")
	if err != nil || !removed.Deleted {
		t.Fatalf("%+v %v", removed, err)
	}
	want := []string{"POST /broadcast-groups/g1/send", "POST /broadcast-groups/g1/cancel", "DELETE /broadcast-groups/g1"}
	for i, r := range rec.requests {
		if r.Method+" "+r.URL.Path != want[i] {
			t.Fatalf("request %d: %s %s, want %s", i, r.Method, r.URL.Path, want[i])
		}
	}
	if rec.bodies[0] != `{"scheduled_at":"in 1 hour"}` {
		t.Fatalf("send body %s", rec.bodies[0])
	}
}

func TestAutomationStopTakesAnOptionalCancelRuns(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{}`)
	})
	ctx := context.Background()
	if _, err := c.Automations.Stop(ctx, "a1", &StopAutomationRequest{CancelRuns: Bool(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Automations.Stop(ctx, "a1", nil); err != nil {
		t.Fatal(err)
	}
	if rec.bodies[0] != `{"cancel_runs":true}` || rec.bodies[1] != "" {
		t.Fatalf("bodies %q", rec.bodies)
	}
}

func TestTopicListsTakeCursorParameters(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{"object":"list","has_more":false,"data":[]}`)
	})
	ctx := context.Background()
	if _, err := c.Topics.List(ctx, &TopicsListParams{Limit: Int64(5), After: String("t1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Contacts.Topics.List(ctx, "c1", &ContactsTopicsListParams{Before: String("t9")}); err != nil {
		t.Fatal(err)
	}
	if q := rec.requests[0].URL.Query(); rec.requests[0].URL.Path != "/topics" || q.Get("limit") != "5" || q.Get("after") != "t1" {
		t.Fatalf("%s", rec.requests[0].URL)
	}
	if q := rec.requests[1].URL.Query(); rec.requests[1].URL.Path != "/contacts/c1/topics" || q.Get("before") != "t9" {
		t.Fatalf("%s", rec.requests[1].URL)
	}
}
