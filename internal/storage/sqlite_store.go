package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kauavnogueira/hookbox/internal/model"
	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store using SQLite with WAL mode for fast, safe concurrent access.
type SQLiteStore struct {
	db       *sql.DB
	filePath string
}

// NewSQLiteStore creates or opens a SQLite database at the given file path.
func NewSQLiteStore(filePath string) (*SQLiteStore, error) {
	if filePath == "" {
		filePath = "webhooks.db"
	}

	dir := filepath.Dir(filePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for sqlite store: %w", err)
		}
	}

	// Enable WAL mode, busy timeout for multi-process concurrency
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filePath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	createTableQuery := `
	CREATE TABLE IF NOT EXISTS webhooks (
		id TEXT PRIMARY KEY,
		timestamp TEXT NOT NULL,
		method TEXT NOT NULL,
		path TEXT NOT NULL,
		query TEXT,
		headers TEXT,
		body TEXT,
		body_bytes INTEGER NOT NULL,
		host TEXT,
		remote_addr TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_webhooks_timestamp ON webhooks(timestamp);
	`
	if _, err := db.Exec(createTableQuery); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize sqlite schema: %w", err)
	}

	return &SQLiteStore{
		db:       db,
		filePath: filePath,
	}, nil
}

// Close closes the underlying SQLite database connection.
func (s *SQLiteStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// NextID returns the next sequential ID, e.g. req_01, req_02...
func (s *SQLiteStore) NextID() (string, error) {
	rows, err := s.db.Query("SELECT id FROM webhooks WHERE id LIKE 'req_%'")
	if err != nil {
		return "", fmt.Errorf("failed to query webhook ids: %w", err)
	}
	defer rows.Close()

	maxNum := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			matches := idRegex.FindStringSubmatch(id)
			if len(matches) == 2 {
				if n, err := strconv.Atoi(matches[1]); err == nil && n > maxNum {
					maxNum = n
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("error iterating webhook ids: %w", err)
	}

	return fmt.Sprintf("req_%02d", maxNum+1), nil
}

// Save inserts a webhook record into SQLite.
func (s *SQLiteStore) Save(wh *model.Webhook) error {
	if wh.ID == "" {
		nextID, err := s.NextID()
		if err != nil {
			return err
		}
		wh.ID = nextID
	}

	headersJSON, err := json.Marshal(wh.Headers)
	if err != nil {
		return fmt.Errorf("failed to marshal headers: %w", err)
	}

	timeStr := wh.Timestamp.Format(time.RFC3339Nano)

	query := `
	INSERT INTO webhooks (id, timestamp, method, path, query, headers, body, body_bytes, host, remote_addr)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = s.db.Exec(query,
		wh.ID,
		timeStr,
		wh.Method,
		wh.Path,
		wh.Query,
		string(headersJSON),
		wh.Body,
		wh.BodyBytes,
		wh.Host,
		wh.RemoteAddr,
	)
	if err != nil {
		return fmt.Errorf("failed to insert webhook %s: %w", wh.ID, err)
	}

	return nil
}

// GetAll returns all saved webhooks ordered by timestamp ascending.
func (s *SQLiteStore) GetAll() ([]*model.Webhook, error) {
	return s.GetFiltered(ListFilter{})
}

// GetFiltered returns webhooks filtered by method, path, and/or limit.
func (s *SQLiteStore) GetFiltered(filter ListFilter) ([]*model.Webhook, error) {
	queryBuilder := strings.Builder{}
	queryBuilder.WriteString(`
	SELECT id, timestamp, method, path, query, headers, body, body_bytes, host, remote_addr
	FROM webhooks
	WHERE 1=1
	`)

	var args []any
	if filter.Method != "" {
		queryBuilder.WriteString(" AND UPPER(method) = ?")
		args = append(args, strings.ToUpper(strings.TrimSpace(filter.Method)))
	}
	if filter.Path != "" {
		queryBuilder.WriteString(" AND LOWER(path) LIKE ?")
		args = append(args, "%"+strings.ToLower(strings.TrimSpace(filter.Path))+"%")
	}

	queryBuilder.WriteString(" ORDER BY timestamp ASC")

	if filter.Limit > 0 {
		queryBuilder.WriteString(" LIMIT ?")
		args = append(args, filter.Limit)
	}

	rows, err := s.db.Query(queryBuilder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query webhooks: %w", err)
	}
	defer rows.Close()

	var list []*model.Webhook
	for rows.Next() {
		wh, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, wh)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating webhooks: %w", err)
	}

	if list == nil {
		list = []*model.Webhook{}
	}

	return list, nil
}

// GetByID returns a webhook by its unique ID.
func (s *SQLiteStore) GetByID(id string) (*model.Webhook, error) {
	query := `
	SELECT id, timestamp, method, path, query, headers, body, body_bytes, host, remote_addr
	FROM webhooks
	WHERE id = ?
	LIMIT 1
	`
	row := s.db.QueryRow(query, id)
	wh, err := scanWebhookRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("webhook with ID %q not found", id)
		}
		return nil, fmt.Errorf("failed to query webhook %s: %w", id, err)
	}

	return wh, nil
}

// Count returns total number of stored webhooks.
func (s *SQLiteStore) Count() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM webhooks").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count webhooks: %w", err)
	}
	return count, nil
}

// Delete removes a specific webhook by ID.
func (s *SQLiteStore) Delete(id string) error {
	res, err := s.db.Exec("DELETE FROM webhooks WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete webhook %s: %w", id, err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("webhook with ID %q not found", id)
	}
	return nil
}

// Clear removes all stored webhooks from SQLite.
func (s *SQLiteStore) Clear() error {
	_, err := s.db.Exec("DELETE FROM webhooks")
	if err != nil {
		return fmt.Errorf("failed to clear webhooks: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWebhook(scanner rowScanner) (*model.Webhook, error) {
	var (
		id          string
		timeStr     string
		method      string
		path        string
		query       sql.NullString
		headersJSON sql.NullString
		body        sql.NullString
		bodyBytes   int
		host        sql.NullString
		remoteAddr  sql.NullString
	)

	err := scanner.Scan(
		&id,
		&timeStr,
		&method,
		&path,
		&query,
		&headersJSON,
		&body,
		&bodyBytes,
		&host,
		&remoteAddr,
	)
	if err != nil {
		return nil, err
	}

	ts, err := time.Parse(time.RFC3339Nano, timeStr)
	if err != nil {
		// Fallback for standard RFC3339
		ts, _ = time.Parse(time.RFC3339, timeStr)
	}

	var headers map[string][]string
	if headersJSON.Valid && headersJSON.String != "" {
		_ = json.Unmarshal([]byte(headersJSON.String), &headers)
	}
	if headers == nil {
		headers = make(map[string][]string)
	}

	return &model.Webhook{
		ID:         id,
		Timestamp:  ts,
		Method:     method,
		Path:       path,
		Query:      query.String,
		Headers:    headers,
		Body:       body.String,
		BodyBytes:  bodyBytes,
		Host:       host.String,
		RemoteAddr: remoteAddr.String,
	}, nil
}

func scanWebhookRow(row *sql.Row) (*model.Webhook, error) {
	return scanWebhook(row)
}

// NewStore initializes a Store based on file extension (.json -> JSONStore, otherwise SQLiteStore).
func NewStore(filePath string) (Store, error) {
	if filePath == "" {
		filePath = "webhooks.db"
	}

	if strings.HasSuffix(strings.ToLower(filePath), ".json") {
		return NewJSONStore(filePath)
	}

	return NewSQLiteStore(filePath)
}
