package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Status values for a canvas session.
const (
	StatusPending = "pending" // created, waiting for a human to draw
	StatusDone    = "done"    // human hit Send; result available
)

// Session is one canvas request created by an agent and fulfilled by a human.
type Session struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedBy string    `json:"created_by,omitempty"` // agent label
	// Result fields, populated on Send:
	Text     string `json:"text,omitempty"`      // optional human note
	HasImage bool   `json:"has_image"`           // PNG export present
	HasSnap  bool   `json:"has_snapshot"`        // tldraw snapshot present
}

type Store struct {
	mu  sync.RWMutex
	dir string
	m   map[string]*Session
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, m: map[string]*Session{}}
	// Load existing sessions from disk.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "sess_") {
			continue
		}
		var sess Session
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		if json.Unmarshal(b, &sess) == nil {
			s.m[sess.ID] = &sess
		}
	}
	slog.Info("store loaded", "sessions", len(s.m))
	return s, nil
}

func (s *Store) path(id string, parts ...string) string {
	return filepath.Join(append([]string{s.dir, id}, parts...)...)
}

func (s *Store) save(sess *Session) error {
	if err := os.MkdirAll(s.path(sess.ID), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(sess, "", "  ")
	return os.WriteFile(s.path(sess.ID, "meta.json"), b, 0o644)
}

func (s *Store) Create(prompt, by string) (*Session, error) {
	now := time.Now().UTC()
	sess := &Session{
		ID:        "sess_" + randID(),
		Prompt:    prompt,
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
		CreatedBy: by,
	}
	s.mu.Lock()
	s.m[sess.ID] = sess
	s.mu.Unlock()
	return sess, s.save(sess)
}

func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.m[id]
	return sess, ok
}

func (s *Store) List() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0, len(s.m))
	for _, v := range s.m {
		out = append(out, v)
	}
	return out
}

var errNotFound = errors.New("session not found")

// Submit records a human's drawing result and flips status to done.
func (s *Store) Submit(id, text string, png, snap []byte) error {
	s.mu.Lock()
	sess, ok := s.m[id]
	s.mu.Unlock()
	if !ok {
		return errNotFound
	}
	if len(png) > 0 {
		if err := os.WriteFile(s.path(id, "image.png"), png, 0o644); err != nil {
			return err
		}
		sess.HasImage = true
	}
	if len(snap) > 0 {
		if err := os.WriteFile(s.path(id, "snapshot.json"), snap, 0o644); err != nil {
			return err
		}
		sess.HasSnap = true
	}
	sess.Text = text
	sess.Status = StatusDone
	sess.UpdatedAt = time.Now().UTC()
	return s.save(sess)
}

func randID() string {
	b := make([]byte, 9)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---- HTTP ----

type Server struct {
	store     *Store
	staticDir string
}

func userEmail(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-ExeDev-Email"))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// POST /api/canvas  {prompt, by?} -> {id, url, status}
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string `json:"prompt"`
		By     string `json:"by"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.Prompt) == "" {
		req.Prompt = "Draw something for the agent."
	}
	sess, err := s.store.Create(req.Prompt, req.By)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"id":     sess.ID,
		"url":    "/c/" + sess.ID,
		"status": sess.Status,
	})
}

// GET /api/canvas/{id} -> session meta (for polling)
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 200, sess)
}

// POST /api/canvas/{id}/submit  {text, image(dataURL/base64 png), snapshot(json)}
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Text     string          `json:"text"`
		Image    string          `json:"image"` // data URL or raw base64 PNG
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var png []byte
	if req.Image != "" {
		data := req.Image
		if i := strings.Index(data, ","); strings.HasPrefix(data, "data:") && i >= 0 {
			data = data[i+1:]
		}
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad image: " + err.Error()})
			return
		}
		png = b
	}
	if err := s.store.Submit(id, req.Text, png, []byte(req.Snapshot)); err != nil {
		code := 500
		if errors.Is(err, errNotFound) {
			code = 404
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": StatusDone})
}

// GET /api/canvas/{id}/image.png
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.store.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(s.store.path(id, "image.png"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/png")
	io.Copy(w, f)
}

// GET /api/canvas/{id}/snapshot.json
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.store.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(s.store.path(id, "snapshot.json"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// GET /api/canvas  -> list (debug/dashboard)
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.store.List())
}

// GET /c/{id} -> canvas page
func (s *Server) handleCanvasPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.store.Get(r.PathValue("id")); !ok {
		http.Error(w, "canvas not found", 404)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.staticDir, "canvas.html"))
}

func main() {
	addr := flag.String("listen", ":8000", "listen address")
	dataDir := flag.String("data", "data", "data directory")
	flag.Parse()

	store, err := NewStore(*dataDir)
	if err != nil {
		slog.Error("store init", "err", err)
		os.Exit(1)
	}
	wd, _ := os.Getwd()
	s := &Server{store: store, staticDir: filepath.Join(wd, "static")}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/canvas", s.handleCreate)
	mux.HandleFunc("GET /api/canvas", s.handleList)
	mux.HandleFunc("GET /api/canvas/{id}", s.handleGet)
	mux.HandleFunc("POST /api/canvas/{id}/submit", s.handleSubmit)
	mux.HandleFunc("GET /api/canvas/{id}/image.png", s.handleImage)
	mux.HandleFunc("GET /api/canvas/{id}/snapshot.json", s.handleSnapshot)
	mux.HandleFunc("GET /c/{id}", s.handleCanvasPage)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(s.staticDir, "index.html"))
	})

	slog.Info("listening", "addr", *addr)
	if err := http.ListenAndServe(*addr, logging(mux)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("req", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).String())
	})
}

var _ = fmt.Sprintf
