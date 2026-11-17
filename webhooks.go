package billionsend

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// WebhookTolerance is how far a webhook-timestamp may be from now by default.
const WebhookTolerance = 300 * time.Second

const (
	webhookSecretPrefix   = "whsec_"
	webhookSecretMinBytes = 24
	webhookSecretMaxBytes = 64
)

// WebhookEvent is a verified webhook delivery's body.
type WebhookEvent struct {
	Type      string         `json:"type"`
	CreatedAt string         `json:"created_at"`
	Data      map[string]any `json:"data"`
}

// VerifyOptions tunes VerifyWebhook.
type VerifyOptions struct {
	// Tolerance is how far the timestamp may be from now (default 300 s).
	Tolerance time.Duration
	// Now is the time to check the timestamp against (default: now).
	Now time.Time
}

var digits = regexp.MustCompile(`^\d+$`)

// VerifyWebhook verifies a webhook delivery and returns its parsed body.
//
// The signature is HMAC-SHA256 over "<webhook-id>.<webhook-timestamp>.<body>"
// with the endpoint's whsec_ secret, sent as "v1,<base64>" in
// webhook-signature; during a secret rotation the header carries one signature
// per secret, space-separated, and any of them matching is enough. The
// timestamp (Unix seconds) must be within the tolerance of now, so a captured
// delivery cannot be replayed later.
//
// Pass the body exactly as it arrived (the raw bytes, before any JSON
// parsing): re-serialised JSON does not verify. The error is a
// *WebhookVerificationError when a header is missing, the timestamp is out of
// tolerance, no signature matches, the secret is malformed, or the body is
// not JSON.
func VerifyWebhook(payload []byte, header http.Header, secret string, opts ...VerifyOptions) (*WebhookEvent, error) {
	var o VerifyOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Tolerance == 0 {
		o.Tolerance = WebhookTolerance
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	id, timestamp, signature := header.Get("webhook-id"), header.Get("webhook-timestamp"), header.Get("webhook-signature")
	if id == "" || timestamp == "" || signature == "" {
		return nil, webhookError("missing the webhook-id, webhook-timestamp or webhook-signature header")
	}
	if !digits.MatchString(timestamp) {
		return nil, webhookError("invalid webhook-timestamp header")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, webhookError("invalid webhook-timestamp header")
	}
	now := o.Now.Unix()
	tolerance := int64(o.Tolerance / time.Second)
	if now-seconds > tolerance {
		return nil, webhookError("the webhook timestamp is too old")
	}
	if seconds-now > tolerance {
		return nil, webhookError("the webhook timestamp is too new")
	}
	key, err := webhookKey(secret)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(payload)
	expected := mac.Sum(nil)
	matched := false
	for _, candidate := range strings.Split(signature, " ") {
		version, encoded, ok := strings.Cut(candidate, ",")
		if !ok || version != "v1" {
			continue
		}
		given, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && hmac.Equal(given, expected) {
			matched = true
		}
	}
	if !matched {
		return nil, webhookError("no webhook signature matches")
	}
	var event WebhookEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, webhookError("the webhook body is not JSON")
	}
	return &event, nil
}

// webhookKey is the key of a whsec_ secret: canonical base64 of 24 to 64
// bytes, as the dashboard and the API hand it out.
func webhookKey(secret string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(secret, webhookSecretPrefix)
	if ok {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && len(key) >= webhookSecretMinBytes && len(key) <= webhookSecretMaxBytes &&
			base64.StdEncoding.EncodeToString(key) == encoded {
			return key, nil
		}
	}
	return nil, webhookError("the webhook secret is not a whsec_ secret")
}
