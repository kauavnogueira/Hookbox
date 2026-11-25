package replay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kauavnogueira/hookbox/internal/model"
)

func TestReplaySender(t *testing.T) {
	var receivedMethod string
	var receivedBody string
	var receivedHeaderVal string
	var receivedReplayHeader string

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		receivedHeaderVal = r.Header.Get("X-Custom-Auth")
		receivedReplayHeader = r.Header.Get("X-Hookbox-Replay")

		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"received": true}`))
	}))
	defer targetServer.Close()

	sender := NewSender(5 * time.Second)

	wh := &model.Webhook{
		ID:        "req_01",
		Timestamp: time.Now(),
		Method:    http.MethodPost,
		Path:      "/webhook/events",
		Body:      `{"event":"user.signup"}`,
		BodyBytes: 23,
		Headers: map[string][]string{
			"Content-Type":  {"application/json"},
			"X-Custom-Auth": {"secret_token_abc"},
		},
	}

	res, err := sender.Send(wh, targetServer.URL)
	if err != nil {
		t.Fatalf("sender.Send failed: %v", err)
	}

	if res.StatusCode != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", res.StatusCode)
	}
	if res.ResponseBody != `{"received": true}` {
		t.Errorf("expected response body {\"received\": true}, got %s", res.ResponseBody)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("expected POST method, got %s", receivedMethod)
	}
	if receivedBody != wh.Body {
		t.Errorf("expected body %s, got %s", wh.Body, receivedBody)
	}
	if receivedHeaderVal != "secret_token_abc" {
		t.Errorf("expected header secret_token_abc, got %s", receivedHeaderVal)
	}
	if receivedReplayHeader != "true" {
		t.Errorf("expected X-Hookbox-Replay header to be true, got %s", receivedReplayHeader)
	}
}

func TestReplayWithOptions_ReSigningAndRouting(t *testing.T) {
	var (
		receivedStripeSig string
		receivedGitHubSig string
		receivedCustomSig string
		receivedPath      string
		receivedQuery     string
	)

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedQuery = r.URL.RawQuery
		receivedStripeSig = r.Header.Get("Stripe-Signature")
		receivedGitHubSig = r.Header.Get("X-Hub-Signature-256")
		receivedCustomSig = r.Header.Get("X-My-HMAC")

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer targetServer.Close()

	sender := NewSender(5 * time.Second)

	wh := &model.Webhook{
		ID:        "req_42",
		Timestamp: time.Now().Add(-1 * time.Hour), // 1 hour ago
		Method:    http.MethodPost,
		Path:      "/webhook/stripe/connect",
		Query:     "source=checkout&env=sandbox",
		Body:      `{"event":"charge.captured","amount":2500}`,
		Headers: map[string][]string{
			"Stripe-Signature": {"t=1600000000,v1=old_expired_signature"},
		},
	}

	opts := ReplayOptions{
		StripeSecret:  "whsec_new_stripe_secret",
		GitHubSecret:  "gh_secret_test",
		HMACSecret:    "custom_secret_key",
		HMACHeader:    "X-My-HMAC",
		PreservePath:  true,
		PreserveQuery: true,
	}

	res, err := sender.SendWithOptions(wh, targetServer.URL, opts)
	if err != nil {
		t.Fatalf("SendWithOptions failed: %v", err)
	}

	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}

	// Verify routing: path and query preserved
	if receivedPath != "/webhook/stripe/connect" {
		t.Errorf("expected path /webhook/stripe/connect, got %s", receivedPath)
	}
	if receivedQuery != "source=checkout&env=sandbox" {
		t.Errorf("expected query source=checkout&env=sandbox, got %s", receivedQuery)
	}

	// Verify Stripe signature was re-signed and NOT the old expired one
	if receivedStripeSig == "t=1600000000,v1=old_expired_signature" {
		t.Errorf("Stripe-Signature was not re-signed")
	}
	if !strings.Contains(receivedStripeSig, "t=") || !strings.Contains(receivedStripeSig, "v1=") {
		t.Errorf("invalid Stripe-Signature format: %s", receivedStripeSig)
	}

	// Verify GitHub signature
	if !strings.HasPrefix(receivedGitHubSig, "sha256=") {
		t.Errorf("invalid GitHub signature: %s", receivedGitHubSig)
	}

	// Verify custom HMAC
	if receivedCustomSig == "" {
		t.Errorf("expected custom HMAC header to be set")
	}
}

func TestReplayWithOptions_CustomBodyAndHeaders(t *testing.T) {
	var receivedBody string
	var receivedAuth string
	var receivedCustomHeader string

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		receivedAuth = r.Header.Get("Authorization")
		receivedCustomHeader = r.Header.Get("X-Custom-Env")

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"modified": true}`))
	}))
	defer targetServer.Close()

	sender := NewSender(5 * time.Second)

	wh := &model.Webhook{
		ID:     "req_01",
		Method: "POST",
		Body:   `{"original": "data"}`,
		Headers: map[string][]string{
			"Authorization": {"Bearer old_token"},
		},
	}

	opts := ReplayOptions{
		CustomBody: `{"modified": "new_data"}`,
		CustomHeaders: map[string]string{
			"Authorization": "Bearer new_override_token",
			"X-Custom-Env":  "staging",
		},
	}

	res, err := sender.SendWithOptions(wh, targetServer.URL, opts)
	if err != nil {
		t.Fatalf("SendWithOptions failed: %v", err)
	}

	if res.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", res.StatusCode)
	}
	if receivedBody != `{"modified": "new_data"}` {
		t.Errorf("expected modified body, got %s", receivedBody)
	}
	if receivedAuth != "Bearer new_override_token" {
		t.Errorf("expected new override token, got %s", receivedAuth)
	}
	if receivedCustomHeader != "staging" {
		t.Errorf("expected staging env header, got %s", receivedCustomHeader)
	}
}

func TestResolveTargetURL(t *testing.T) {
	wh := &model.Webhook{
		Path:  "/webhook/orders",
		Query: "filter=active",
	}

	// No options
	u1, err := ResolveTargetURL("http://localhost:3000", wh, ReplayOptions{})
	if err != nil || u1 != "http://localhost:3000" {
		t.Errorf("expected http://localhost:3000, got %s (err: %v)", u1, err)
	}

	// Preserve path
	u2, err := ResolveTargetURL("http://localhost:3000", wh, ReplayOptions{PreservePath: true})
	if err != nil || u2 != "http://localhost:3000/webhook/orders" {
		t.Errorf("expected http://localhost:3000/webhook/orders, got %s (err: %v)", u2, err)
	}

	// Preserve path with existing subpath
	u3, err := ResolveTargetURL("http://localhost:3000/api", wh, ReplayOptions{PreservePath: true})
	if err != nil || u3 != "http://localhost:3000/api/webhook/orders" {
		t.Errorf("expected http://localhost:3000/api/webhook/orders, got %s (err: %v)", u3, err)
	}

	// Preserve path & query
	u4, err := ResolveTargetURL("http://localhost:3000/api?mode=test", wh, ReplayOptions{PreservePath: true, PreserveQuery: true})
	if err != nil || u4 != "http://localhost:3000/api/webhook/orders?mode=test&filter=active" {
		t.Errorf("expected merged query, got %s (err: %v)", u4, err)
	}
}
