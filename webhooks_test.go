package billionsend

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The known-answer vector the API's own signing test pins.
const (
	vectorSecret    = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	vectorID        = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	vectorTimestamp = "1614265330"
	vectorPayload   = `{"test": 2432232314}`
	vectorSignature = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
)

var vectorAt = time.Unix(1614265330, 0)

func webhookHeaders(signature, timestamp string) http.Header {
	h := http.Header{}
	h.Set("webhook-id", vectorID)
	h.Set("webhook-timestamp", timestamp)
	h.Set("webhook-signature", signature)
	return h
}

func sign(secret, id, timestamp, payload string) string {
	key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "." + payload))
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func refused(t *testing.T, err error, want string) {
	t.Helper()
	var v *WebhookVerificationError
	var base *Error
	if !errors.As(err, &v) || !errors.As(err, &base) || base.Name != "webhook_verification_error" || !strings.Contains(v.Message, want) {
		t.Fatalf("want %q, got %v", want, err)
	}
}

func TestVerifiesThePublishedVector(t *testing.T) {
	event, err := VerifyWebhook([]byte(vectorPayload), webhookHeaders(vectorSignature, vectorTimestamp), vectorSecret, VerifyOptions{Now: vectorAt})
	if err != nil || event == nil {
		t.Fatal(err)
	}
	if sign(vectorSecret, vectorID, vectorTimestamp, vectorPayload) != vectorSignature {
		t.Fatal("the independent signer disagrees with the vector")
	}
}

func TestAcceptsAnySignatureOfARotationAndRefusesOthers(t *testing.T) {
	both := "v1,AAAA " + vectorSignature
	if _, err := VerifyWebhook([]byte(vectorPayload), webhookHeaders(both, vectorTimestamp), vectorSecret, VerifyOptions{Now: vectorAt}); err != nil {
		t.Fatal(err)
	}
	v2 := strings.Replace(vectorSignature, "v1,", "v2,", 1)
	_, err := VerifyWebhook([]byte(vectorPayload), webhookHeaders(v2, vectorTimestamp), vectorSecret, VerifyOptions{Now: vectorAt})
	refused(t, err, "no webhook signature matches")
	_, err = VerifyWebhook([]byte(vectorPayload+" "), webhookHeaders(vectorSignature, vectorTimestamp), vectorSecret, VerifyOptions{Now: vectorAt})
	refused(t, err, "no webhook signature matches")
}

func TestRefusesOldNewAndMalformedTimestamps(t *testing.T) {
	h := webhookHeaders(vectorSignature, vectorTimestamp)
	_, err := VerifyWebhook([]byte(vectorPayload), h, vectorSecret, VerifyOptions{Now: vectorAt.Add(301 * time.Second)})
	refused(t, err, "too old")
	_, err = VerifyWebhook([]byte(vectorPayload), h, vectorSecret, VerifyOptions{Now: vectorAt.Add(-301 * time.Second)})
	refused(t, err, "too new")
	_, err = VerifyWebhook([]byte(vectorPayload), webhookHeaders(vectorSignature, "16x"), vectorSecret, VerifyOptions{Now: vectorAt})
	refused(t, err, "invalid webhook-timestamp")
	if _, err := VerifyWebhook([]byte(vectorPayload), h, vectorSecret, VerifyOptions{Now: vectorAt.Add(600 * time.Second), Tolerance: 700 * time.Second}); err != nil {
		t.Fatal(err)
	}
}

func TestRefusesMissingHeadersBadSecretsAndNonJSON(t *testing.T) {
	_, err := VerifyWebhook([]byte(vectorPayload), http.Header{}, vectorSecret)
	refused(t, err, "missing")
	_, err = VerifyWebhook([]byte(vectorPayload), webhookHeaders(vectorSignature, vectorTimestamp), "secret", VerifyOptions{Now: vectorAt})
	refused(t, err, "not a whsec_ secret")
	short := "whsec_" + base64.StdEncoding.EncodeToString([]byte("too short"))
	_, err = VerifyWebhook([]byte(vectorPayload), webhookHeaders(vectorSignature, vectorTimestamp), short, VerifyOptions{Now: vectorAt})
	refused(t, err, "not a whsec_ secret")
	body := "not json"
	_, err = VerifyWebhook([]byte(body), webhookHeaders(sign(vectorSecret, vectorID, vectorTimestamp, body), vectorTimestamp), vectorSecret, VerifyOptions{Now: vectorAt})
	refused(t, err, "not JSON")
}

func TestAcceptsASecretAndHeadersWithSurroundingWhitespace(t *testing.T) {
	h := webhookHeaders(vectorSignature+" ", " "+vectorTimestamp)
	if _, err := VerifyWebhook([]byte(vectorPayload), h, vectorSecret+"\n", VerifyOptions{Now: vectorAt}); err != nil {
		t.Fatal(err)
	}
}
