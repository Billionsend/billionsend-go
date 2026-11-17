// Command sync-openapi refreshes openapi/api.openapi.json, the contract this
// SDK is generated from and tested against.
//
//	go run ./internal/cmd/sync-openapi                  # fetch the published document
//	go run ./internal/cmd/sync-openapi -from <path>     # copy a local document instead
//
// Then run go run ./internal/cmd/generate and go test ./...: whatever fails
// is what the SDK has to follow.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const defaultURL = "https://billionsend.com/docs/api.openapi.json"

func main() {
	from := flag.String("from", "", "a local OpenAPI document to copy")
	url := flag.String("url", defaultURL, "the published OpenAPI document")
	flag.Parse()
	raw, err := load(*from, *url)
	if err != nil {
		fail(err)
	}
	var doc struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.OpenAPI == "" || len(doc.Paths) == 0 {
		fail(fmt.Errorf("not an OpenAPI document: %v", err))
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		fail(err)
	}
	out.WriteByte('\n')
	if err := os.WriteFile("openapi/api.openapi.json", out.Bytes(), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("openapi/api.openapi.json: OpenAPI %s, %d paths\n", doc.OpenAPI, len(doc.Paths))
}

func load(from, url string) ([]byte, error) {
	if from != "" {
		return os.ReadFile(from)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s answered %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
