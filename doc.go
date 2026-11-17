// Package billionsend is the Go client for the BillionSend email API: send
// email, run inboxes for people and agents, stream inbox events, verify
// webhooks, and manage domains, contacts and broadcasts.
//
// Every API operation is a method, grouped by its operationId's dotted path:
// emails.send is client.Emails.Send, emails.receiving.attachments.list is
// client.Emails.Receiving.Attachments.List. Request and response types are
// generated from the BillionSend OpenAPI document (openapi/api.openapi.json).
//
//	client := billionsend.NewClient() // reads BILLIONSEND_API_KEY
//	sent, err := client.Emails.Send(ctx, &billionsend.SendEmailRequest{
//		From:    "Acme <hello@acme.dev>",
//		To:      []string{"ana@example.com"},
//		Subject: "Welcome",
//		HTML:    billionsend.String("<p>Glad you are here.</p>"),
//	})
//
// Errors are typed by status (ValidationError, AuthenticationError,
// PermissionDeniedError, NotFoundError, ConflictError, RateLimitError,
// APIError, APIConnectionError, APITimeoutError); every one unwraps to *Error,
// which carries StatusCode, Name, Message and RequestID.
package billionsend
