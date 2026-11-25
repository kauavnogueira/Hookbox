package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kauavnogueira/hookbox/internal/model"
	"github.com/kauavnogueira/hookbox/internal/storage"
)

func TestServerReceiver_Flow(t *testing.T) {
	tempDir := t.TempDir()
	storeFile := filepath.Join(tempDir, "webhooks.db")
	store, err := storage.NewStore(storeFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	srv := NewServer(8080, store)

	// Test invalid path
	reqInvalid := httptest.NewRequest(http.MethodGet, "/random-non-webhook-path", nil)
	wInvalid := httptest.NewRecorder()
	srv.handleWebhook(wInvalid, reqInvalid)
	// Webhook handler now accepts /webhook/*; other root paths are handled by handleRoot
	payload := `{"action":"payment.succeeded","amount":100}`
	reqValid := httptest.NewRequest(http.MethodPost, "/webhook/stripe", bytes.NewBufferString(payload))
	reqValid.Header.Set("Content-Type", "application/json")
	reqValid.Header.Set("X-Custom", "abc")

	wValid := httptest.NewRecorder()
	srv.handleWebhook(wValid, reqValid)

	if wValid.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", wValid.Code)
	}

	saved, err := store.GetByID("req_01")
	if err != nil {
		t.Fatalf("failed to retrieve saved webhook: %v", err)
	}
	if saved.Body != payload {
		t.Errorf("expected body %s, got %s", payload, saved.Body)
	}
}

func TestServerReceiver_MaxBytesLimit(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := storage.NewStore(filepath.Join(tempDir, "data.db"))
	defer store.Close()

	// Limit to 50 bytes
	srv := NewServerWithOptions(ServerOptions{
		Port:         8080,
		Store:        store,
		MaxBodyBytes: 50,
	})

	// 100 bytes payload
	largePayload := strings.Repeat("A", 100)
	req := httptest.NewRequest(http.MethodPost, "/webhook/test", strings.NewReader(largePayload))
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for oversized body, got %d", w.Code)
	}
}

func TestServerReceiver_ForwardMode(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := storage.NewStore(filepath.Join(tempDir, "data.db"))
	defer store.Close()

	var mu sync.Mutex
	var receivedByTarget bool
	var targetBody string

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		receivedByTarget = true
		b, _ := io.ReadAll(r.Body)
		targetBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()

	srv := NewServerWithOptions(ServerOptions{
		Port:       8080,
		Store:      store,
		ForwardURL: targetServer.URL,
	})

	payload := `{"test":"forward"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/events", strings.NewReader(payload))
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Allow goroutine to forward
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !receivedByTarget {
		t.Errorf("expected forward target to receive webhook")
	}
	if targetBody != payload {
		t.Errorf("expected target body %s, got %s", payload, targetBody)
	}
}

func TestServerReceiver_APIWebhooksAndReplay(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := storage.NewStore(filepath.Join(tempDir, "data.db"))
	defer store.Close()

	srv := NewServer(8080, store)

	// Save one item
	wh := &model.Webhook{
		ID:        "req_01",
		Timestamp: time.Now(),
		Method:    "POST",
		Path:      "/webhook/api-test",
		Body:      `{"hello":"world"}`,
		BodyBytes: 17,
	}
	_ = store.Save(wh)

	// Test GET /api/webhooks
	reqGet := httptest.NewRequest(http.MethodGet, "/api/webhooks", nil)
	wGet := httptest.NewRecorder()
	srv.handleAPIWebhooks(wGet, reqGet)

	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /api/webhooks, got %d", wGet.Code)
	}

	var list []*model.Webhook
	if err := json.Unmarshal(wGet.Body.Bytes(), &list); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}
	if len(list) != 1 || list[0].ID != "req_01" {
		t.Errorf("expected 1 item with id req_01, got %v", list)
	}

	// Test POST /api/replay
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"replayed": true}`))
	}))
	defer targetServer.Close()

	replayPayload := map[string]any{
		"id": "req_01",
		"to": targetServer.URL,
	}
	payloadBytes, _ := json.Marshal(replayPayload)
	reqReplay := httptest.NewRequest(http.MethodPost, "/api/replay", bytes.NewReader(payloadBytes))
	wReplay := httptest.NewRecorder()
	srv.handleAPIReplay(wReplay, reqReplay)

	if wReplay.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/replay, got %d (body: %s)", wReplay.Code, wReplay.Body.String())
	}

	// Test DELETE /api/webhooks
	reqDel := httptest.NewRequest(http.MethodDelete, "/api/webhooks", nil)
	wDel := httptest.NewRecorder()
	srv.handleAPIWebhooks(wDel, reqDel)

	if wDel.Code != http.StatusOK {
		t.Fatalf("expected 200 for DELETE, got %d", wDel.Code)
	}
	count, _ := store.Count()
	if count != 0 {
		t.Errorf("expected count 0 after delete, got %d", count)
	}
}

func TestServerReceiver_UI(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := storage.NewStore(filepath.Join(tempDir, "data.db"))
	defer store.Close()

	srv := NewServer(8080, store)

	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	w := httptest.NewRecorder()
	srv.handleUI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /ui, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "HOOKBOX") {
		t.Errorf("expected HTML to contain HOOKBOX")
	}
}
