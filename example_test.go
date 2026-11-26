package billionsend_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/billionsend/billionsend-go"
)

func Example() {
	client := billionsend.NewClient() // BILLIONSEND_API_KEY
	sent, err := client.Emails.Send(context.Background(), &billionsend.SendEmailRequest{
		From:    billionsend.String("Acme <hello@acme.dev>"),
		To:      []string{"ana@example.com"},
		Subject: billionsend.String("Welcome to Acme"),
		HTML:    billionsend.String("<p>Glad you are here.</p>"),
	}, billionsend.WithIdempotencyKey("welcome/42"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(sent.ID)
}

// An agent's receive loop: open the inbox's event stream and answer each new
// message in its thread. The stream reconnects on its own and resumes after
// the last event; keep LastEventID to resume after a restart too.
func Example_agentLoop() {
	ctx := context.Background()
	client := billionsend.NewClient()
	stream, err := client.Inboxes.Events.StreamInbox(ctx, "support@acme.dev", &billionsend.InboxesEventsStreamInboxParams{
		Types: billionsend.String("inbox.email.received"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()
	for item, err := range stream.Events() {
		if err != nil {
			log.Fatal(err)
		}
		switch ev := item.(type) {
		case *billionsend.StreamGapEvent:
			// Events may be missing: read the threads list again.
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
}

func Example_listAll() {
	client := billionsend.NewClient()
	for email, err := range client.Emails.ListAll(context.Background(), &billionsend.EmailsListParams{Limit: billionsend.Int64(100)}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(email.ID)
	}
}

func Example_errors() {
	client := billionsend.NewClient(billionsend.WithMaxRetries(4), billionsend.WithTimeout(30*time.Second))
	_, err := client.Emails.Get(context.Background(), "4ef9a417-02e9-4d39-ad75-9611e0fcc33c")
	var notFound *billionsend.NotFoundError
	var limited *billionsend.RateLimitError
	var apiErr *billionsend.Error
	switch {
	case errors.As(err, &notFound):
		fmt.Println("no such email")
	case errors.As(err, &limited):
		fmt.Println("retry after", limited.RetryAfter)
	case errors.As(err, &apiErr):
		fmt.Println(apiErr.StatusCode, apiErr.Name, apiErr.Message, apiErr.RequestID)
	}
}

func ExampleVerifyWebhook() {
	http.HandleFunc("/webhooks/billionsend", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		event, err := billionsend.VerifyWebhook(body, r.Header, os.Getenv("BILLIONSEND_WEBHOOK_SECRET"))
		if err != nil {
			http.Error(w, "bad signature", http.StatusBadRequest)
			return
		}
		fmt.Println(event.Type)
		w.WriteHeader(http.StatusNoContent)
	})
}

// Import contacts from a CSV, and clear a domain's own tracking host.
func Example_uploadAndClear() {
	ctx := context.Background()
	client := billionsend.NewClient()
	csv, err := os.ReadFile("contacts.csv")
	if err != nil {
		log.Fatal(err)
	}
	queued, err := client.Contacts.Imports.Create(ctx, &billionsend.CreateContactImportForm{
		File:      billionsend.File{Content: csv},
		ColumnMap: &billionsend.ContactImportColumnMap{Email: billionsend.String("Email Address")},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(queued.ID)
	_, err = client.Domains.Update(ctx, "d91cd9bd-1176-453e-8fc1-35364d380206", &billionsend.UpdateDomainRequest{
		TrackingSubdomain: billionsend.Null[string](),
	})
	if err != nil {
		log.Fatal(err)
	}
}
