package replay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// SignStripe generates a valid Stripe-Signature header using HMAC-SHA256.
// If timestamp <= 0, the current Unix timestamp is used.
// Format: t=<timestamp>,v1=<signature>
func SignStripe(body string, secret string, timestamp int64) (string, int64) {
	if timestamp <= 0 {
		timestamp = time.Now().Unix()
	}

	payloadToSign := fmt.Sprintf("%d.%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadToSign))
	sigHex := hex.EncodeToString(mac.Sum(nil))

	headerVal := fmt.Sprintf("t=%d,v1=%s", timestamp, sigHex)
	return headerVal, timestamp
}

// SignGitHub generates a valid X-Hub-Signature-256 header using HMAC-SHA256.
// Format: sha256=<signature>
func SignGitHub(body string, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sigHex := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("sha256=%s", sigHex)
}

// SignHMACSHA256 computes raw hex-encoded HMAC-SHA256 of the payload.
func SignHMACSHA256(body string, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}
