# Changelog

## 0.2.0 (2026-11-26)

The OpenAPI snapshot follows the document the API now publishes (186 operations, up from 139), and every one has a method.

### New operations

- `Automations` (`List`, `Get`, `Create`, `Update`, `Remove`, `Validate`, `Duplicate`, `Simulate`, `Stop`, `Metrics`, `Runs.List`, `Runs.Get`, `Runs.Cancel`) and `Events` (`List`, `Get`, `Create`, `Update`, `Remove`, `Send`). `Events.Send` always carries an `Idempotency-Key`.
- `Contacts.Imports` (`List`, `Get`, `Create`), `Contacts.Segments.List`, `Domains.Claims` (`Create`, `Get`, `Verify`), `Emails.Share`, `Emails.Unshare`, `Emails.Attachments` (`List`, `Get`), `Broadcasts.Recipients`, `Broadcasts.ClickedLinks`, `Logs` (`List`, `Get`), `OAuthGrants` (`List`, `Revoke`), `Webhooks.Events` (`List`, `Get`, `Replay`, `Attempts.List`), `Webhooks.RotateSigningSecret`, `APIKeys.Update` and `Inboxes.Agent.Update`. Every new cursor list has its iterator (`ListAll`, `RecipientsAll`, `ClickedLinksAll`).
- `Brand` (`Get`, `Update`, `Logo.Upload`, `Logo.Delete`) and `Domains.UnsubscribePage.Logo` (`Upload`, `Delete`).
- Uploads take a `File` (bytes, with an optional name and content type) in `UploadLogoForm` or `CreateContactImportForm`; `ColumnMap`, `Segments` and `Topics` of an import are typed and JSON-encoded by the client. A logo upload is not repeated after a 5xx, a timeout or a dropped connection; a 429 is still retried.

### Changed

- Removed: `Webhooks.Rotate` (use `Webhooks.RotateSigningSecret`) and `Unsubscribes.Delete` (a recipient's opt-out is permanent).
- A field of an update (PATCH) that `null` clears or resets is a `*Nullable[T]`: `Value(v)` sets it, `Null[T]()` sends `null`, nil leaves it out. This covers names of a contact, a domain's tracking and unsubscribe subdomains and its unsubscribe page, brand, template, topic, segment, broadcast, webhook, inbox, agent, draft and safety settings.
- `SendEmailRequest.From` and `.Subject` are optional (`*string`), for a send that names a template; `SendEmailRequest.Template` is a `*SendTemplate` (it was `any`). `CreateBroadcastRequest.From` and `.Subject` are optional too, and it takes a `Template`.
- `Usage.Get` answers the new report (`Emails`, `Contacts`, `Domains`, ...).
- Suppressions block an address or a whole domain: `Domain` / `Domains` and `Note` on create, `Kind` on list; an entry has `Kind`, `Domain`, `Removable` and `Note`, and its `Email` is a `*string` (nil on a domain entry, so `CreateSuppressionRequest.Email` is optional too).
- Domains carry `SendTransactionalToUnsubscribed`, `SendingLimits` (on `Get`) and the unsubscribe page's `SupportURL`, `PoweredBy` and `PoweredByEffective`; broadcasts take `DormantCheck` and `IncludeDormant`; `Deliverability.Get` answers `Domains`. A template's `From`, `ReplyTo` and `Variables` are typed.
- A restricted API key refused by an operation is a `*PermissionDeniedError` named `restricted_api_key`.

### Client behaviour

- A `429` is retried for every request, a `POST` or `PATCH` without an `Idempotency-Key` included.
- An event stream drops a connection that delivers nothing for `WithIdleTimeout` (default 45 s, `0` turns it off) and reconnects; it also resumes from the position the server opens it at, so a connection closed before its first event loses nothing.
- A path parameter of `.` or `..` is refused before any request. A header value HTTP cannot carry fails the call with an error named `invalid_header`; nothing is sent or retried.
- The API key is read without the whitespace around it; a blank one falls back to `BILLIONSEND_API_KEY`. `VerifyWebhook` accepts a secret and header values with surrounding whitespace.
- An empty list in a query parameter is left out; a call whose context is already canceled makes no request.
- `Inboxes.Threads.Emails.ListAll` continues past the first page (it ended with "no id" before).

## 0.1.3 (2026-11-17)

- A reconnect the API refuses with a `Retry-After` (a 429, a 5xx) waits that long instead of the reconnect delay; one longer than `MaxRetryAfter` (60 s) is yielded at once. When the reconnects run out, the stream yields the last one's error as its own type (`*RateLimitError`, `*APIError`, ...) when the API answered, and an `*APIConnectionError` ("gave up after N reconnects") only when it could not be reached.
- `APIError` carries the server's `Retry-After` too: `RetryAfter`, 0 without one.

## 0.1.2 (2026-11-17)

- An inbox event stream counts only the reconnects that fail in a row toward `WithMaxReconnects`: each reconnect that opens the stream resets the count. A quiet inbox, whose stream the server closes every ten minutes or so with only keep-alive comments in between, no longer gives up after an hour.

## 0.1.1 (2026-11-17)

- The `...All` iterators end with an `*Error` named `application_error` on an answer that is not a list page (no `data` array) or when the item to continue from has no `id`, instead of ending quietly: a truncated walk no longer passes for the whole list.

## 0.1.0 (2026-11-17)

- First version of the BillionSend SDK for Go: every operation of the BillionSend API (139), grouped into services and named after each operation's `operationId` (`emails.receiving.attachments.list` is `client.Emails.Receiving.Attachments.List`), with request and response types generated from the OpenAPI document. `emails.metrics`, which also has sub-operations, is `client.Emails.Metrics.Get`.
- Errors typed by status: `ValidationError`, `AuthenticationError`, `PermissionDeniedError`, `NotFoundError`, `ConflictError`, `RateLimitError` (with `RetryAfter`), `APIError`, `APIConnectionError` and `APITimeoutError`; each unwraps to `*Error` with `StatusCode`, `Name`, `Message` and `RequestID` (the answer's `x-request-id`).
- The sending operations always carry an `Idempotency-Key`: `WithIdempotencyKey`'s, or a new UUID per call that its retries reuse.
- Retries for 429, 5xx, timeouts, network failures and `409 concurrent_idempotent_requests`, on requests safe to repeat; `Retry-After` honoured up to 60 seconds, exponential backoff with jitter otherwise (500 ms doubling, at most 8 s); per-attempt timeouts.
- List query parameters go comma-separated in one key, as the document declares them.
- `ListAll` / `SearchAll` iterators (`iter.Seq2`) over cursor-paginated lists, forward or backward.
- Inbox event streams (server-sent events) yielding `*InboxStreamEvent` and `*StreamGapEvent`, resuming with `Last-Event-ID` after a dropped connection; a gap handed out once per position; one inbox's stream ends after its `inbox.deleted` or `inbox.expired`.
- `VerifyWebhook` checks a webhook delivery's signature and timestamp and returns the parsed event.
- `BILLIONSEND_API_KEY` and `BILLIONSEND_BASE_URL`; no dependencies beyond the standard library; Go 1.23 or later.
