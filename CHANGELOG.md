# Changelog

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
