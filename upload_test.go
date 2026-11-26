package billionsend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

type formPart struct {
	filename, contentType, value string
}

// parseForm reads the multipart body of the n-th recorded request.
func parseForm(t *testing.T, rec *recorder, n int) map[string]formPart {
	t.Helper()
	rec.mu.Lock()
	header, body := rec.requests[n].Header.Get("Content-Type"), rec.bodies[n]
	rec.mu.Unlock()
	kind, params, err := mime.ParseMediaType(header)
	if err != nil || kind != "multipart/form-data" {
		t.Fatalf("content type %q: %v", header, err)
	}
	r := multipart.NewReader(strings.NewReader(body), params["boundary"])
	parts := map[string]formPart{}
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatal(err)
		}
		value, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		parts[p.FormName()] = formPart{p.FileName(), p.Header.Get("Content-Type"), string(value)}
	}
}

func TestUploadsALogoNamedAndTypedByItsBytes(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{"object":"logo","logo_url":"https://cdn.example/logo.png"}`)
	})
	ctx := context.Background()
	cases := []struct {
		file              File
		filename, content string
	}{
		{File{Content: []byte("\x89PNG\r\n\x1a\nrest")}, "logo.png", "image/png"},
		{File{Content: []byte("\xff\xd8\xffrest")}, "logo.jpg", "image/jpeg"},
		{File{Content: []byte("RIFF0000WEBPrest")}, "logo.webp", "image/webp"},
		{File{Content: []byte("<svg/>")}, "logo.png", "application/octet-stream"},
		{File{Content: []byte("\xff\xd8\xff"), Name: `my "brand".jpeg`, ContentType: "image/x-own"}, `my "brand".jpeg`, "image/x-own"},
	}
	for i, tc := range cases {
		out, err := c.Brand.Logo.Upload(ctx, &UploadLogoForm{File: tc.file})
		if err != nil {
			t.Fatal(err)
		}
		if out.LogoURL != "https://cdn.example/logo.png" {
			t.Fatalf("answer %+v", out)
		}
		got := parseForm(t, rec, i)
		want := formPart{tc.filename, tc.content, string(tc.file.Content)}
		if len(got) != 1 || got["file"] != want {
			t.Fatalf("case %d: parts %q", i, got)
		}
	}
	if _, err := c.Domains.UnsubscribePage.Logo.Upload(ctx, "d1", &UploadLogoForm{File: cases[0].file}); err != nil {
		t.Fatal(err)
	}
	last := len(cases)
	if rec.requests[last].Method != "PUT" || rec.requests[last].URL.Path != "/domains/d1/unsubscribe-page/logo" {
		t.Fatalf("request %s %s", rec.requests[last].Method, rec.requests[last].URL.Path)
	}
	if parseForm(t, rec, last)["file"].filename != "logo.png" {
		t.Fatal("the domain logo was not sent")
	}
}

func TestUploadsAContactImportWithItsFieldsJSONEncoded(t *testing.T) {
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		jsonAnswer(w, 200, `{"object":"contact_import","id":"i1"}`)
	}, WithHeader("Content-Type", "application/json"))
	ctx := context.Background()
	csv := []byte("Email,Plan\nana@example.com,pro\n")
	out, err := c.Contacts.Imports.Create(ctx, &CreateContactImportForm{
		File: File{Content: csv},
		ColumnMap: &ContactImportColumnMap{
			Email:      String("Email"),
			Properties: map[string]ContactImportPropertyColumn{"plan": {Column: "Plan", Type: String("string")}},
		},
		OnConflict: String("skip"),
		Segments:   []ContactImportSegment{{ID: "s1"}},
		Topics:     []ContactImportTopic{{ID: "t1", Subscription: "opt_in"}},
	})
	if err != nil || out.ID != "i1" {
		t.Fatalf("%+v %v", out, err)
	}
	got := parseForm(t, rec, 0)
	want := map[string]string{
		"file":        string(csv),
		"column_map":  `{"email":"Email","properties":{"plan":{"column":"Plan","type":"string"}}}`,
		"on_conflict": "skip",
		"segments":    `[{"id":"s1"}]`,
		"topics":      `[{"id":"t1","subscription":"opt_in"}]`,
	}
	for name, value := range want {
		if got[name].value != value {
			t.Errorf("%s: %q", name, got[name].value)
		}
	}
	if got["file"].filename != "contacts.csv" || got["file"].contentType != "text/csv" {
		t.Errorf("file part %+v", got["file"])
	}
	if _, err := c.Contacts.Imports.Create(ctx, &CreateContactImportForm{File: File{Content: csv, Name: "people.csv"}}); err != nil {
		t.Fatal(err)
	}
	if got := parseForm(t, rec, 1); len(got) != 1 || got["file"].filename != "people.csv" {
		t.Fatalf("only the file was to be sent: %v", got)
	}
}

// The hand-written forms send exactly the fields the snapshot's schemas name.
func TestTheFormsHaveTheSnapshotsFields(t *testing.T) {
	raw, err := os.ReadFile("openapi/api.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) { jsonAnswer(w, 200, `{}`) })
	ctx := context.Background()
	if _, err := c.Brand.Logo.Upload(ctx, &UploadLogoForm{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Contacts.Imports.Create(ctx, &CreateContactImportForm{
		ColumnMap: &ContactImportColumnMap{}, OnConflict: String("upsert"),
		Segments: []ContactImportSegment{}, Topics: []ContactImportTopic{},
	}); err != nil {
		t.Fatal(err)
	}
	for n, schema := range []string{"UploadLogoForm", "CreateContactImportForm"} {
		var sent, declared []string
		for name := range parseForm(t, rec, n) {
			sent = append(sent, name)
		}
		for name := range doc.Components.Schemas[schema].Properties {
			declared = append(declared, name)
		}
		sort.Strings(sent)
		sort.Strings(declared)
		if strings.Join(sent, ",") != strings.Join(declared, ",") {
			t.Errorf("%s: sends %v, the snapshot declares %v", schema, sent, declared)
		}
	}
}

func TestDoesNotRepeatAnUploadAfterAFailureButDoesAfterA429(t *testing.T) {
	form := &UploadLogoForm{File: File{Content: []byte("\x89PNG\r\n\x1a\n")}}
	ctx := context.Background()

	c, rec, _ := testClient(t, func(n int, r *http.Request, w http.ResponseWriter) { jsonAnswer(w, 503, `{}`) })
	var failed *APIError
	if _, err := c.Brand.Logo.Upload(ctx, form); !errors.As(err, &failed) || rec.count() != 1 {
		t.Fatalf("after a 5xx: %v, %d requests", err, rec.count())
	}

	c, rec, _ = testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		panic(http.ErrAbortHandler) // the connection drops without an answer
	})
	var dropped *APIConnectionError
	if _, err := c.Domains.UnsubscribePage.Logo.Upload(ctx, "d1", form); !errors.As(err, &dropped) || rec.count() != 1 {
		t.Fatalf("after a dropped connection: %v, %d requests", err, rec.count())
	}

	release := make(chan struct{})
	c, rec, _ = testClient(t, func(n int, r *http.Request, w http.ResponseWriter) { <-release })
	t.Cleanup(func() { close(release) }) // runs before the server closes
	var late *APITimeoutError
	if _, err := c.Brand.Logo.Upload(ctx, form, WithRequestTimeout(50*time.Millisecond)); !errors.As(err, &late) {
		t.Fatalf("after a timeout: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("after a timeout: %d requests", rec.count())
	}

	c, rec, _ = testClient(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			jsonAnswer(w, 429, `{"name":"rate_limit_exceeded","message":"slow down"}`)
			return
		}
		jsonAnswer(w, 200, `{}`)
	})
	if _, err := c.Brand.Logo.Upload(ctx, form); err != nil || rec.count() != 2 {
		t.Fatalf("after a 429: %v, %d requests", err, rec.count())
	}
	if rec.bodies[0] != rec.bodies[1] || rec.bodies[0] == "" {
		t.Fatal("the retry did not send the same upload")
	}
}
