package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/kauavnogueira/hookbox/internal/model"
)

var idRegex = regexp.MustCompile(`^req_(\d+)$`)

// ListFilter provides optional criteria for querying stored webhooks.
type ListFilter struct {
	Limit  int
	Method string
	Path   string
}

// Store defines operations for saving and reading webhooks.
type Store interface {
	Save(wh *model.Webhook) error
	GetAll() ([]*model.Webhook, error)
	GetFiltered(filter ListFilter) ([]*model.Webhook, error)
	GetByID(id string) (*model.Webhook, error)
	NextID() (string, error)
	Count() (int, error)
	Delete(id string) error
	Clear() error
	Close() error
}

// JSONStore implements Store backed by a JSON file on disk.
type JSONStore struct {
	mu       sync.RWMutex
	filePath string
}

// NewJSONStore creates or opens a JSONStore at the given file path.
func NewJSONStore(filePath string) (*JSONStore, error) {
	if filePath == "" {
		filePath = "webhooks.json"
	}

	dir := filepath.Dir(filePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for store: %w", err)
		}
	}

	store := &JSONStore{filePath: filePath}

	// Initialize file if not present
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if err := store.writeAll([]*model.Webhook{}); err != nil {
			return nil, fmt.Errorf("failed to initialize store: %w", err)
		}
	}

	return store, nil
}

// Close is a no-op for JSONStore to satisfy Store interface.
func (s *JSONStore) Close() error {
	return nil
}

// readAll reads all webhooks from the file. Caller must hold at least read lock.
func (s *JSONStore) readAll() ([]*model.Webhook, error) {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*model.Webhook{}, nil
		}
		return nil, fmt.Errorf("failed to read store file %s: %w", s.filePath, err)
	}

	if len(data) == 0 {
		return []*model.Webhook{}, nil
	}

	var list []*model.Webhook
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse json store %s: %w", s.filePath, err)
	}

	return list, nil
}

// writeAll writes webhooks atomically to disk using a temp file. Caller must hold write lock.
func (s *JSONStore) writeAll(list []*model.Webhook) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal webhooks: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", s.filePath, os.Getpid())
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp store file: %w", err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to replace store file: %w", err)
	}

	return nil
}

// NextID generates the next short identifier, e.g. req_01, req_02...
func (s *JSONStore) NextID() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list, err := s.readAll()
	if err != nil {
		return "", err
	}

	maxNum := 0
	for _, wh := range list {
		matches := idRegex.FindStringSubmatch(wh.ID)
		if len(matches) == 2 {
			if n, err := strconv.Atoi(matches[1]); err == nil && n > maxNum {
				maxNum = n
			}
		}
	}

	return fmt.Sprintf("req_%02d", maxNum+1), nil
}

// Save saves a webhook record to the JSON store.
func (s *JSONStore) Save(wh *model.Webhook) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.readAll()
	if err != nil {
		return err
	}

	if wh.ID == "" {
		maxNum := 0
		for _, item := range list {
			matches := idRegex.FindStringSubmatch(item.ID)
			if len(matches) == 2 {
				if n, err := strconv.Atoi(matches[1]); err == nil && n > maxNum {
					maxNum = n
				}
			}
		}
		wh.ID = fmt.Sprintf("req_%02d", maxNum+1)
	}

	list = append(list, wh)
	return s.writeAll(list)
}

// GetAll returns all saved webhooks.
func (s *JSONStore) GetAll() ([]*model.Webhook, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.readAll()
}

// GetFiltered returns webhooks filtered by method, path substring, and/or limit.
func (s *JSONStore) GetFiltered(filter ListFilter) ([]*model.Webhook, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all, err := s.readAll()
	if err != nil {
		return nil, err
	}

	var res []*model.Webhook
	targetMethod := strings.ToUpper(strings.TrimSpace(filter.Method))
	targetPath := strings.ToLower(strings.TrimSpace(filter.Path))

	for _, wh := range all {
		if targetMethod != "" && strings.ToUpper(wh.Method) != targetMethod {
			continue
		}
		if targetPath != "" && !strings.Contains(strings.ToLower(wh.Path), targetPath) {
			continue
		}
		res = append(res, wh)
		if filter.Limit > 0 && len(res) >= filter.Limit {
			break
		}
	}

	if res == nil {
		res = []*model.Webhook{}
	}
	return res, nil
}

// GetByID finds a webhook by its ID.
func (s *JSONStore) GetByID(id string) (*model.Webhook, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list, err := s.readAll()
	if err != nil {
		return nil, err
	}

	for _, wh := range list {
		if wh.ID == id {
			return wh, nil
		}
	}

	return nil, fmt.Errorf("webhook with ID %q not found", id)
}

// Count returns the number of saved webhooks.
func (s *JSONStore) Count() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list, err := s.readAll()
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// Delete removes a specific webhook by ID.
func (s *JSONStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.readAll()
	if err != nil {
		return err
	}

	found := false
	filtered := make([]*model.Webhook, 0, len(list))
	for _, wh := range list {
		if wh.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, wh)
	}

	if !found {
		return fmt.Errorf("webhook with ID %q not found", id)
	}

	return s.writeAll(filtered)
}

// Clear removes all stored webhooks.
func (s *JSONStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.writeAll([]*model.Webhook{})
}
