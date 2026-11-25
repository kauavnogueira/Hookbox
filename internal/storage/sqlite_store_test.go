package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kauavnogueira/hookbox/internal/model"
)

func TestSQLiteStore(t *testing.T) {
	tempDir := t.TempDir()
	dbFile := filepath.Join(tempDir, "test_webhooks.db")

	store, err := NewSQLiteStore(dbFile)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	// Initial count
	count, err := store.Count()
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// NextID initial
	nextID, err := store.NextID()
	if err != nil {
		t.Fatalf("failed to get next id: %v", err)
	}
	if nextID != "req_01" {
		t.Errorf("expected req_01, got %s", nextID)
	}

	// Save first
	now := time.Now().Truncate(time.Millisecond)
	wh1 := &model.Webhook{
		Timestamp:  now,
		Method:     "POST",
		Path:       "/webhook/orders",
		Query:      "status=pending",
		Headers:    map[string][]string{"X-Test": {"val1", "val2"}},
		Body:       `{"order_id": 123}`,
		BodyBytes:  17,
		Host:       "localhost:8080",
		RemoteAddr: "127.0.0.1:54321",
	}
	if err := store.Save(wh1); err != nil {
		t.Fatalf("failed to save webhook: %v", err)
	}
	if wh1.ID != "req_01" {
		t.Errorf("expected ID req_01, got %s", wh1.ID)
	}

	// Save second
	wh2 := &model.Webhook{
		Timestamp: now.Add(time.Second),
		Method:    "GET",
		Path:      "/webhook/ping",
	}
	if err := store.Save(wh2); err != nil {
		t.Fatalf("failed to save second webhook: %v", err)
	}

	// Filter test
	filteredPost, err := store.GetFiltered(ListFilter{Method: "POST"})
	if err != nil {
		t.Fatalf("failed filter: %v", err)
	}
	if len(filteredPost) != 1 || filteredPost[0].ID != "req_01" {
		t.Errorf("expected 1 POST item req_01, got %v", filteredPost)
	}

	filteredPath, err := store.GetFiltered(ListFilter{Path: "ping"})
	if err != nil {
		t.Fatalf("failed path filter: %v", err)
	}
	if len(filteredPath) != 1 || filteredPath[0].ID != "req_02" {
		t.Errorf("expected 1 ping item req_02, got %v", filteredPath)
	}

	// Delete test
	if err := store.Delete("req_01"); err != nil {
		t.Fatalf("failed delete: %v", err)
	}
	if _, err := store.GetByID("req_01"); err == nil {
		t.Errorf("expected error getting deleted req_01")
	}

	// Clear test
	if err := store.Clear(); err != nil {
		t.Fatalf("failed clear: %v", err)
	}
	countAfterClear, _ := store.Count()
	if countAfterClear != 0 {
		t.Errorf("expected 0 count after clear, got %d", countAfterClear)
	}
}

func TestStoreFactory(t *testing.T) {
	tempDir := t.TempDir()

	// JSON
	jsonPath := filepath.Join(tempDir, "data.json")
	s1, err := NewStore(jsonPath)
	if err != nil {
		t.Fatalf("failed NewStore with json: %v", err)
	}
	if _, ok := s1.(*JSONStore); !ok {
		t.Errorf("expected JSONStore, got %T", s1)
	}

	// SQLite
	dbPath := filepath.Join(tempDir, "data.db")
	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed NewStore with db: %v", err)
	}
	defer s2.Close()
	if _, ok := s2.(*SQLiteStore); !ok {
		t.Errorf("expected SQLiteStore, got %T", s2)
	}
}
