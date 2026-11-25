package replay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestSignStripe(t *testing.T) {
	body := `{"id":"evt_test_123","object":"event"}`
	secret := "whsec_test_secret_key"
	fixedTimestamp := int64(1700000000)

	sigHeader, ts := SignStripe(body, secret, fixedTimestamp)
	if ts != fixedTimestamp {
		t.Errorf("expected timestamp %d, got %d", fixedTimestamp, ts)
	}

	// Verify header format: t=1700000000,v1=...
	parts := strings.Split(sigHeader, ",")
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts in Stripe-Signature, got %v", parts)
	}
	if parts[0] != "t=1700000000" {
		t.Errorf("expected t=1700000000, got %s", parts[0])
	}
	if !strings.HasPrefix(parts[1], "v1=") {
		t.Errorf("expected v1= prefix, got %s", parts[1])
	}

	// Manually compute expected signature
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.%s", fixedTimestamp, body)))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	actualSig := strings.TrimPrefix(parts[1], "v1=")
	if actualSig != expectedSig {
		t.Errorf("expected signature %s, got %s", expectedSig, actualSig)
	}
}

func TestSignGitHub(t *testing.T) {
	body := `{"action":"opened","issue":{"number":1}}`
	secret := "github_webhook_secret"

	sigHeader := SignGitHub(body, secret)
	if !strings.HasPrefix(sigHeader, "sha256=") {
		t.Fatalf("expected sha256= prefix, got %s", sigHeader)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	actualSig := strings.TrimPrefix(sigHeader, "sha256=")
	if actualSig != expectedSig {
		t.Errorf("expected signature %s, got %s", expectedSig, actualSig)
	}
}

func TestSignHMACSHA256(t *testing.T) {
	body := "hello world"
	secret := "mysecret"

	sig := SignHMACSHA256(body, secret)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	expected := hex.EncodeToString(mac.Sum(nil))

	if sig != expected {
		t.Errorf("expected %s, got %s", expected, sig)
	}
}

func TestSignStripeAutoTimestamp(t *testing.T) {
	body := "payload"
	secret := "sec"

	sigHeader, ts := SignStripe(body, secret, 0)
	if ts <= 0 {
		t.Errorf("expected valid timestamp > 0, got %d", ts)
	}

	expectedPrefix := "t=" + strconv.FormatInt(ts, 10)
	if !strings.HasPrefix(sigHeader, expectedPrefix) {
		t.Errorf("expected header to start with %s, got %s", expectedPrefix, sigHeader)
	}
}
