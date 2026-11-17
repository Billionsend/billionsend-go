package billionsend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// snapshotOperation is one operation of openapi/api.openapi.json.
type snapshotOperation struct {
	id, method, template string
	pattern              *regexp.Regexp
	status               int
	stream               bool
	idempotent           bool
	noAuth               bool
}

func loadSnapshot(t *testing.T) []snapshotOperation {
	t.Helper()
	raw, err := os.ReadFile("openapi/api.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Parameters  []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
			Responses map[string]struct {
				Content map[string]any `json:"content"`
			} `json:"responses"`
			Security *[]any `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var ops []snapshotOperation
	for path, methods := range doc.Paths {
		for method, o := range methods {
			if o.OperationID == "" {
				continue
			}
			re := "^" + regexp.MustCompile(`\\\{[^}]+\\\}`).ReplaceAllString(regexp.QuoteMeta(path), `[^/]+`) + "$"
			op := snapshotOperation{
				id: o.OperationID, method: strings.ToUpper(method), template: path,
				pattern: regexp.MustCompile(re), noAuth: o.Security != nil && len(*o.Security) == 0,
			}
			for code, r := range o.Responses {
				if strings.HasPrefix(code, "2") && (op.status == 0 || code < fmt.Sprint(op.status)) {
					fmt.Sscan(code, &op.status)
					_, op.stream = r.Content["text/event-stream"]
				}
			}
			for _, p := range o.Parameters {
				if p.In == "header" && strings.EqualFold(p.Name, "Idempotency-Key") {
					op.idempotent = true
				}
			}
			ops = append(ops, op)
		}
	}
	// A literal path wins over a templated one (/emails/metrics before /emails/{id}).
	sort.Slice(ops, func(i, j int) bool {
		return strings.Count(ops[i].template, "{") < strings.Count(ops[j].template, "{")
	})
	return ops
}

func match(ops []snapshotOperation, method, path string) *snapshotOperation {
	for i := range ops {
		if ops[i].method == method && ops[i].pattern.MatchString(path) {
			return &ops[i]
		}
	}
	return nil
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Every operation of the snapshot is reached by exactly one generated call,
// every call reaches the operation it is named after, and nothing the
// snapshot does not have is ever requested.
func TestEveryOperationIsCoveredAndNothingElse(t *testing.T) {
	ops := loadSnapshot(t)
	var mu sync.Mutex
	hits := map[string][]string{}
	var unknown []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := match(ops, r.Method, r.URL.Path)
		if op == nil {
			mu.Lock()
			unknown = append(unknown, r.Method+" "+r.URL.Path)
			mu.Unlock()
			w.WriteHeader(599)
			return
		}
		problem := ""
		switch {
		case op.noAuth && r.Header.Get("Authorization") != "":
			problem = "sent a key to an operation that takes none"
		case !op.noAuth && r.Header.Get("Authorization") != "Bearer bs_test":
			problem = "no bearer key"
		case op.idempotent && !uuidV4.MatchString(r.Header.Get("Idempotency-Key")):
			problem = "no generated Idempotency-Key"
		case !op.idempotent && r.Header.Get("Idempotency-Key") != "":
			problem = "an Idempotency-Key on an operation that takes none"
		}
		mu.Lock()
		hits[op.id] = append(hits[op.id], problem)
		mu.Unlock()
		if op.stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(op.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(op.status)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient(WithAPIKey("bs_test"), WithBaseURL(srv.URL), WithMaxRetries(0))

	snapshot := map[string]bool{}
	for _, op := range ops {
		snapshot[op.id] = true
	}
	for id := range snapshot {
		if _, ok := coverageCalls[id]; !ok {
			t.Errorf("no SDK call for %s", id)
		}
	}
	for id, callOp := range coverageCalls {
		if !snapshot[id] {
			t.Errorf("an SDK call for %s, which the snapshot does not have", id)
			continue
		}
		mu.Lock()
		before := len(hits[id])
		mu.Unlock()
		if err := callOp(context.Background(), c); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		mu.Lock()
		got := hits[id][before:]
		mu.Unlock()
		if len(got) != 1 {
			t.Errorf("%s reached its operation %d times", id, len(got))
		}
		for _, problem := range got {
			if problem != "" {
				t.Errorf("%s: %s", id, problem)
			}
		}
	}
	if len(unknown) > 0 {
		t.Errorf("requests the snapshot does not have: %v", unknown)
	}
	if len(snapshot) != 139 {
		t.Logf("the snapshot holds %d operations", len(snapshot))
	}
	total := 0
	for _, h := range hits {
		total += len(h)
	}
	if total != len(coverageCalls) {
		t.Errorf("%d requests for %d calls", total, len(coverageCalls))
	}
}
