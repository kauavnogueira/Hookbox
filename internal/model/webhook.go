package model

import (
	"net/http"
	"time"
)

// Webhook represents a captured HTTP request.
type Webhook struct {
	ID         string              `json:"id"`
	Timestamp  time.Time           `json:"timestamp"`
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Query      string              `json:"query,omitempty"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
	BodyBytes  int                 `json:"body_bytes"`
	Host       string              `json:"host,omitempty"`
	RemoteAddr string              `json:"remote_addr,omitempty"`
}

// CloneHeaders copies http.Header into a map[string][]string.
func CloneHeaders(h http.Header) map[string][]string {
	if h == nil {
		return make(map[string][]string)
	}
	res := make(map[string][]string, len(h))
	for k, v := range h {
		res[k] = append([]string(nil), v...)
	}
	return res
}
