package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kauavnogueira/hookbox/internal/model"
)

func TestJSONStore(t *testing.T) {
	tempDir := t.TempDir()
	storeFile := filepath.Join(tempDir, "test_webhooks.json")

	store, err := NewJSONStore(storeFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Initial count should be 0
	count, err := store.Count()
	if err != nil {
		t.Fatalf("unexpected error getting count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected count 0, got %d", count)
	}

	// Check NextID
	nextID, err := store.NextID()
	if err != nil {
		t.Fatalf("failed to get next ID: %v", err)
	}
	if nextID != "req_01" {
		t.Fatalf("expected req_01, got %s", nextID)
	}

	// Save first webhook
	wh1 := &model.Webhook{
		Timestamp: time.Now(),
		Method:    "POST",
		Path:      "/webhook/stripe",
		Body:      `{"event":"charge.succeeded"}`,
		BodyBytes: 29,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
	}
	if err := store.Save(wh1); err != nil {
		t.Fatalf("failed to save webhook: %v", err)
	}
	if wh1.ID != "req_01" {
		t.Fatalf("expected assigned ID to be req_01, got %s", wh1.ID)
	}

	// Save second webhook
	wh2 := &model.Webhook{
		Timestamp: time.Now(),
		Method:    "GET",
		Path:      "/webhook/ping",
	}
	if err := store.Save(wh2); err != nil {
		t.Fatalf("failed to save second webhook: %v", err)
	}
	if wh2.ID != "req_02" {
		t.Fatalf("expected assigned ID to be req_02, got %s", wh2.ID)
	}

	// GetByID
	found, err := store.GetByID("req_01")
	if err != nil {
		t.Fatalf("failed to get req_01: %v", err)
	}
	if found.Path != "/webhook/stripe" {
		t.Fatalf("expected path /webhook/stripe, got %s", found.Path)
	}

	// Get non-existent
	_, err = store.GetByID("req_999")
	if err == nil {
		t.Fatalf("expected error for non-existent ID, got nil")
	}

	// GetAll
	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("failed to get all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 items, got %d", len(all))
	}

	// Persistence check: reopen store with new instance
	store2, err := NewJSONStore(storeFile)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	all2, err := store2.GetAll()
	if err != nil {
		t.Fatalf("failed to get all from reopened store: %v", err)
	}
	if len(all2) != 2 {
		t.Fatalf("expected 2 items in reopened store, got %d", len(all2))
	}

	nextID2, _ := store2.NextID()
	if nextID2 != "req_03" {
		t.Fatalf("expected next ID req_03, got %s", nextID2)
	}

	_ = os.Remove(storeFile)
}
