package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kauavnogueira/hookbox/internal/model"
	"github.com/kauavnogueira/hookbox/internal/replay"
	"github.com/kauavnogueira/hookbox/internal/storage"
	"github.com/kauavnogueira/hookbox/internal/ui"
)

// ServerOptions defines configuration options for the receiver server.
type ServerOptions struct {
	Port         int
	Store        storage.Store
	ForwardURL   string
	MaxBodyBytes int64
}

// Server handles listening for incoming webhooks, SSE broadcasting, and Web UI.
type Server struct {
	port         int
	store        storage.Store
	forwardURL   string
	maxBodyBytes int64
	replaySender *replay.Sender
	server       *http.Server

	subMu       sync.RWMutex
	subscribers map[chan *model.Webhook]struct{}
}

// NewServer creates a new webhook receiver server with default options.
func NewServer(port int, store storage.Store) *Server {
	return NewServerWithOptions(ServerOptions{
		Port:  port,
		Store: store,
	})
}

// NewServerWithOptions creates a webhook receiver server with custom configuration.
func NewServerWithOptions(opts ServerOptions) *Server {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 10 << 20 // 10 MB default limit
	}

	s := &Server{
		port:         opts.Port,
		store:        opts.Store,
		forwardURL:   opts.ForwardURL,
		maxBodyBytes: opts.MaxBodyBytes,
		replaySender: replay.NewSender(30 * time.Second),
		subscribers:  make(map[chan *model.Webhook]struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", s.handleWebhook)
	mux.HandleFunc("/webhook/", s.handleWebhook)
	mux.HandleFunc("/api/webhooks", s.handleAPIWebhooks)
	mux.HandleFunc("/api/webhooks/", s.handleAPIWebhooks)
	mux.HandleFunc("/api/events", s.handleAPIEvents)
	mux.HandleFunc("/api/replay", s.handleAPIReplay)
	mux.HandleFunc("/ui", s.handleUI)
	mux.HandleFunc("/ui/", s.handleUI)
	mux.HandleFunc("/", s.handleRoot)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", opts.Port),
		Handler: mux,
	}

	return s
}

// Start runs the HTTP receiver server.
func (s *Server) Start() error {
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) broadcast(wh *model.Webhook) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()

	for ch := range s.subscribers {
		select {
		case ch <- wh:
		default:
		}
	}
}

func (s *Server) subscribe() chan *model.Webhook {
	ch := make(chan *model.Webhook, 50)
	s.subMu.Lock()
	s.subscribers[ch] = struct{}{}
	s.subMu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan *model.Webhook) {
	s.subMu.Lock()
	delete(s.subscribers, ch)
	close(ch)
	s.subMu.Unlock()
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Accept /webhook or /webhook/*
	isWebhookPath := r.URL.Path == "/webhook" || strings.HasPrefix(r.URL.Path, "/webhook/")
	if !isWebhookPath {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "Not Found",
			"message": "Hookbox is listening for webhooks on /webhook/* (e.g. /webhook/test, /webhook/stripe)",
		})
		return
	}

	// Guard against Out of Memory (OOM) by capping max body size
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read body or body too large: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	bodyStr := string(bodyBytes)
	now := time.Now()

	wh := &model.Webhook{
		Timestamp:  now,
		Method:     r.Method,
		Path:       r.URL.Path,
		Query:      r.URL.RawQuery,
		Headers:    model.CloneHeaders(r.Header),
		Body:       bodyStr,
		BodyBytes:  len(bodyBytes),
		Host:       r.Host,
		RemoteAddr: r.RemoteAddr,
	}

	if err := s.store.Save(wh); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save webhook: %v", err), http.StatusInternalServerError)
		return
	}

	// Notify real-time Web UI subscribers via SSE
	s.broadcast(wh)

	// Log to console
	fmt.Printf("[%s] %s | %-6s %s | %d bytes\n",
		wh.ID,
		now.Format("15:04:05"),
		wh.Method,
		wh.Path,
		wh.BodyBytes,
	)

	// Transparent forward mode (Tee proxy) if configured
	if s.forwardURL != "" {
		go func(item *model.Webhook) {
			opts := replay.ReplayOptions{
				PreservePath:  true,
				PreserveQuery: true,
			}
			fwdRes, fwdErr := s.replaySender.SendWithOptions(item, s.forwardURL, opts)
			if fwdErr != nil {
				fmt.Printf("[%s] ⚠️  Falha no encaminhamento para %s: %v\n", item.ID, s.forwardURL, fwdErr)
			} else {
				fmt.Printf("[%s] ➔ Encaminhado para %s | Status: %s (%v)\n",
					item.ID,
					fwdRes.TargetURL,
					fwdRes.StatusText,
					fwdRes.Duration.Round(time.Millisecond),
				)
			}
		}(wh)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "success",
		"id":          wh.ID,
		"method":      wh.Method,
		"path":        wh.Path,
		"received_at": wh.Timestamp.Format(time.RFC3339),
		"size_bytes":  wh.BodyBytes,
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		s.handleUI(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	htmlData, err := ui.GetIndexHTML()
	if err != nil {
		http.Error(w, "UI not available", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(htmlData)
}

func (s *Server) handleAPIWebhooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		// Optional query params
		limitStr := r.URL.Query().Get("limit")
		method := r.URL.Query().Get("method")
		path := r.URL.Query().Get("path")
		id := r.URL.Query().Get("id")

		if id != "" {
			item, err := s.store.GetByID(id)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(item)
			return
		}

		limit := 0
		if limitStr != "" {
			limit, _ = strconv.Atoi(limitStr)
		}

		filter := storage.ListFilter{
			Limit:  limit,
			Method: method,
			Path:   path,
		}

		list, err := s.store.GetFiltered(filter)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		// Reverse to show newest first in JSON API
		reversed := make([]*model.Webhook, len(list))
		for i, wh := range list {
			reversed[len(list)-1-i] = wh
		}
		_ = json.NewEncoder(w).Encode(reversed)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id != "" {
			if err := s.store.Delete(id); err != nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": id})
			return
		}

		if err := s.store.Clear(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	notify := r.Context().Done()

	for {
		select {
		case <-notify:
			return
		case wh, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(wh)
			if err == nil {
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

type replayRequest struct {
	ID            string            `json:"id"`
	To            string            `json:"to"`
	StripeSecret  string            `json:"stripe_secret"`
	GitHubSecret  string            `json:"github_secret"`
	HMACSecret    string            `json:"hmac_secret"`
	HMACHeader    string            `json:"hmac_header"`
	PreservePath  bool              `json:"preserve_path"`
	PreserveQuery bool              `json:"preserve_query"`
	CustomBody    string            `json:"custom_body"`
	CustomHeaders map[string]string `json:"custom_headers"`
}

func (s *Server) handleAPIReplay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req replayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid json request body"})
		return
	}

	if req.ID == "" || req.To == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "id and to parameters are required"})
		return
	}

	wh, err := s.store.GetByID(req.ID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("webhook %s not found", req.ID)})
		return
	}

	opts := replay.ReplayOptions{
		StripeSecret:  req.StripeSecret,
		GitHubSecret:  req.GitHubSecret,
		HMACSecret:    req.HMACSecret,
		HMACHeader:    req.HMACHeader,
		PreservePath:  req.PreservePath,
		PreserveQuery: req.PreserveQuery,
		CustomBody:    req.CustomBody,
		CustomHeaders: req.CustomHeaders,
	}

	res, err := s.replaySender.SendWithOptions(wh, req.To, opts)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        "success",
		"target_url":    res.TargetURL,
		"status_code":   res.StatusCode,
		"status_text":   res.StatusText,
		"duration_ms":   res.Duration.Milliseconds(),
		"response_body": res.ResponseBody,
	})
}
