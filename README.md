# BillionSend Go SDK

The official Go client for the [BillionSend](https://billionsend.com) email API: send email, run inboxes for people and agents, stream inbox events, verify webhooks, and manage domains, contacts and broadcasts.

- Every API operation (186), typed from the BillionSend OpenAPI document: sending, domains, contacts and imports, broadcasts, templates, automations and custom events, webhooks, inboxes, logs and brand settings.
- Errors typed by status, all unwrapping to `*billionsend.Error` with the request id.
- Safe sends: every send carries an `Idempotency-Key`, so retries after a timeout never send twice.
- Iterators for every list and for live inbox events, with automatic resume.
- The standard library only. Go 1.23 or later.

Documentation: [billionsend.com/docs](https://billionsend.com/docs) · SDK guide: [billionsend.com/docs/sdks/go](https://billionsend.com/docs/sdks/go) · Source: [github.com/billionsend/billionsend-go](https://github.com/billionsend/billionsend-go)

## Install

```bash
go get github.com/billionsend/billionsend-go
```

## Quick start

Create an API key on the dashboard (it starts with `bs_`) and pass it to the client, or set `BILLIONSEND_API_KEY`:

```go
import "github.com/billionsend/billionsend-go"

client := billionsend.NewClient() // reads BILLIONSEND_API_KEY
// or: billionsend.NewClient(billionsend.WithAPIKey("bs_..."))

sent, err := client.Emails.Send(ctx, &billionsend.SendEmailRequest{
	From:    billionsend.String("Acme <hello@acme.dev>"),
	To:      []string{"ana@example.com"},
	Subject: billionsend.String("Welcome to Acme"),
	HTML:    billionsend.String("<p>Glad you are here.</p>"),
})
if err != nil {
	return err
}
fmt.Println(sent.ID)
```

`BILLIONSEND_BASE_URL` (or `WithBaseURL`) points the client at another API host. `WithTimeout` (per attempt, default 60 s), `WithMaxRetries`, `WithHTTPClient` and `WithHeader` set the rest. A key restricted to one inbox is what an agent should hold; see [API keys](https://billionsend.com/docs/api-keys).

## How the methods are named

Every operation is a method, named after its `operationId`: the dotted path is the service chain, each segment in Go's capitals. `emails.send` is `client.Emails.Send`, `emails.receiving.attachments.list` is `client.Emails.Receiving.Attachments.List`, `inboxes.threads.labels.set` is `client.Inboxes.Threads.Labels.Set`. The one operation that also has sub-operations, `emails.metrics`, is `client.Emails.Metrics.Get` beside `client.Emails.Metrics.Links`, `Countries` and `Clients`.

A method takes the context, then the path parameters in order, then the request body (`*XxxRequest`, generated from the document), then the query and header parameters (`*XxxParams`), then request options. Optional fields are pointers: `billionsend.String`, `Int64`, `Float64` and `Bool` make them. A field that accepts more than one shape (an address or a list of them, say) is typed `any`.

## Clearing a field

In an update, leaving a field out keeps its value, and for some fields `null` clears or resets it. Those are `*billionsend.Nullable[T]`: `billionsend.Value(v)` sets, `billionsend.Null[T]()` sends `null` (`Null[any]()` for a field typed `any`).

```go
client.Domains.Update(ctx, domainID, &billionsend.UpdateDomainRequest{
	TrackingSubdomain: billionsend.Null[string](), // back to the shared tracking host
})
```

## Uploads

Logos and contact imports are uploaded as `multipart/form-data` from bytes. A logo (PNG, JPEG or WebP, at most 2 MiB) is named and typed by its bytes unless `Name` and `ContentType` say otherwise; a CSV (at most 20 MiB) is sent as `contacts.csv` unless named. `ColumnMap`, `Segments` and `Topics` are typed values the client JSON-encodes.

```go
csv, err := os.ReadFile("contacts.csv")
if err != nil {
	return err
}
queued, err := client.Contacts.Imports.Create(ctx, &billionsend.CreateContactImportForm{
	File:      billionsend.File{Content: csv},
	ColumnMap: &billionsend.ContactImportColumnMap{Email: billionsend.String("Email Address")},
})
// then poll client.Contacts.Imports.Get(ctx, queued.ID)
```

`client.Brand.Logo.Upload` and `client.Domains.UnsubscribePage.Logo.Upload` take a `*billionsend.UploadLogoForm`.

## Receive mail in an agent loop

Open an inbox's event stream and act on each new message. The stream reconnects on its own with `Last-Event-ID`, replaying what happened while it was away; keep `stream.LastEventID()` and pass it to `WithLastEventID` to resume after a restart too. A `*StreamGapEvent` means the stream resumed from a position older than the server keeps (24 hours): read the lists again.

```go
stream, err := client.Inboxes.Events.StreamInbox(ctx, "support@acme.dev", &billionsend.InboxesEventsStreamInboxParams{
	Types: billionsend.String("inbox.email.received"),
})
if err != nil {
	return err
}
defer stream.Close()
for item, err := range stream.Events() {
	if err != nil {
		return err // the reconnects ran out, or the key no longer works
	}
	switch ev := item.(type) {
	case *billionsend.StreamGapEvent:
		// events may be missing: read the threads list again
	case *billionsend.InboxStreamEvent:
		d := ev.Data.Data
		_, err := client.Inboxes.Threads.Emails.Reply(ctx, ev.Data.InboxID, d.ThreadID, d.EmailID, &billionsend.InboxReplyRequest{
			Text: billionsend.String("Thanks, we are on it."),
		})
		if err != nil {
			log.Print(err)
		}
	}
}
```

One inbox's stream ends after that inbox's `inbox.deleted` or `inbox.expired`. `client.Inboxes.Events.Stream` follows every inbox the key can read. `WithoutReconnect`, `WithMaxReconnects` (reconnects that may fail in a row, default 5; one that opens the stream resets the count) and `WithReconnectDelay` (default 1 s; the server's `retry` overrides it) tune the reconnects, and `WithIdleTimeout` (default 45 s, `0` turns it off) drops a connection that delivers nothing, not even the server's keep-alive, and reconnects. A reconnect the API refuses with a `Retry-After` waits that long (longer than 60 seconds: that error is yielded at once); when the reconnects run out the stream yields the API's answer as its own type, or an `*APIConnectionError` when the API could not be reached.

## Lists

Every cursor-paginated list has an `...All` iterator that fetches pages as you go: forward from `After`, or backward when `Before` is set.

```go
for email, err := range client.Emails.ListAll(ctx, &billionsend.EmailsListParams{Limit: billionsend.Int64(100)}) {
	if err != nil {
		return err
	}
	fmt.Println(email.ID)
}
```

## Errors

A failed call returns one of `*ValidationError` (400, 422), `*AuthenticationError` (401, or no key configured: `Name` is `missing_api_key` and no request is made), `*PermissionDeniedError` (403), `*NotFoundError` (404), `*ConflictError` (409), `*RateLimitError` (429, with `RetryAfter`), `*APIError` (5xx, with `RetryAfter` when the server sent one), `*APIConnectionError` (no answer) or `*APITimeoutError` (the attempt timed out); any other status, and a failure the client makes up (an empty, `.` or `..` path parameter, an answer that is not JSON), is a plain `*billionsend.Error` named `application_error`. A header value HTTP cannot carry (non-ASCII or a control character, say in an `Idempotency-Key`) is a plain `*billionsend.Error` named `invalid_header`, and nothing is sent. `VerifyWebhook` fails with `*WebhookVerificationError`. Each unwraps to `*billionsend.Error`, whose `StatusCode`, `Name` (the API's error name, such as `validation_error`), `Message` and `RequestID` (the answer's `x-request-id`, the request on the dashboard's Logs page) say what happened.

```go
_, err := client.Emails.Get(ctx, id)
var notFound *billionsend.NotFoundError
var apiErr *billionsend.Error
switch {
case errors.As(err, &notFound):
	// no such email
case errors.As(err, &apiErr):
	log.Printf("%d %s: %s (request %s)", apiErr.StatusCode, apiErr.Name, apiErr.Message, apiErr.RequestID)
}
```

## Retries and idempotency

A `429` is retried for every request, since the API answers it before it writes anything (`WithMaxRetries`, default 2). 5xx, timeouts and network failures are retried only for requests safe to repeat: `GET`, `PUT` and `DELETE` (the two logo uploads excepted), and the nine operations that always carry an `Idempotency-Key` (the sending operations and `events.send`). Such an operation's retries reuse one key, so a retry after a timeout never sends twice; a `409 concurrent_idempotent_requests` (the first attempt still in progress) is retried too. The wait is the server's `Retry-After` when it sends one (longer than 60 seconds: the error is returned instead), otherwise an exponential backoff with jitter from 500 ms, at most 8 seconds. Each attempt is bounded by `WithTimeout` (default 60 s).

Pass your own key to make a send safe across calls and processes too:

```go
client.Emails.Send(ctx, req, billionsend.WithIdempotencyKey("welcome/"+userID))
```

`WithRequestTimeout`, `WithRequestMaxRetries`, `WithRequestHeader` and `WithResponseHeaders` work per call.

## Verify webhooks

Pass the raw body, the request headers and the endpoint's `whsec_` secret. The signature (HMAC-SHA256, any of the signatures a secret rotation sends) and the timestamp (within 5 minutes) are checked before the event is parsed:

```go
http.HandleFunc("/webhooks/billionsend", func(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	event, err := billionsend.VerifyWebhook(body, r.Header, os.Getenv("BILLIONSEND_WEBHOOK_SECRET"))
	if err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	log.Print(event.Type)
	w.WriteHeader(http.StatusNoContent)
})
```

## Development

The types and methods are generated from `openapi/api.openapi.json`, a snapshot of the published API document:

```bash
go run ./internal/cmd/sync-openapi      # refresh the snapshot from https://billionsend.com/docs/api.openapi.json (or -from <path>)
go run ./internal/cmd/generate          # regenerate types_gen.go, services_gen.go and coverage_gen_test.go
go test -race ./...
```

The multipart forms of the uploads are written by hand in `upload.go`, and a test holds their fields to the snapshot. The coverage test calls every operation of the snapshot through the SDK against a server that knows only the snapshot's operations: a new operation without a method, or a method that reaches anything else, fails it.

## Releasing

A Go module needs no registry: the version tag is the release.

1. Set `Version` in `version.go`, add the version to `CHANGELOG.md`, and commit.
2. Tag the commit `v<Version>` (for example `v0.2.0`) and push the tag.
3. Run the `release` workflow (Actions → release → Run workflow) on that tag. It refuses a tag that does not name `version.go`'s version, runs the checks, creates the GitHub release and asks `proxy.golang.org` for the version, so `go get github.com/billionsend/billionsend-go@v<Version>` finds it at once.

## License

MIT
