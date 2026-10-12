// Trailvault is a small, single-process, append-only agent action journal.
package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var web embed.FS

type Input struct {
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
	Details string `json:"details"`
}
type Event struct {
	Sequence int    `json:"sequence"`
	Time     string `json:"time"`
	Input
	Previous string `json:"previous"`
	Hash     string `json:"hash"`
}
type Store struct {
	mu     sync.Mutex
	path   string
	events []Event
	failed bool
}

func digest(e Event) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func verify(events []Event) error {
	previous := ""
	for i, e := range events {
		if e.Sequence != i+1 || e.Previous != previous || e.Hash != digest(e) {
			return fmt.Errorf("chain mismatch at sequence %d", i+1)
		}
		previous = e.Hash
	}
	return nil
}
func openStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			return nil, err
		}
		if last[0] != '\n' {
			return nil, errors.New("journal lacks terminating newline; inspect interrupted write")
		}
	}
	s := &Store{path: path, events: []Event{}}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var e Event
		if err := func() error {
			dec := json.NewDecoder(strings.NewReader(scanner.Text()))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&e); err != nil {
				return err
			}
			if err := dec.Decode(&struct{}{}); err != io.EOF {
				return errors.New("one JSON record required")
			}
			return nil
		}(); err != nil {
			return nil, fmt.Errorf("invalid journal: %w", err)
		}
		s.events = append(s.events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := verify(s.events); err != nil {
		return nil, err
	}
	return s, nil
}
func validate(in Input) error {
	if strings.TrimSpace(in.Actor) == "" || strings.TrimSpace(in.Action) == "" {
		return errors.New("actor and action are required")
	}
	if len(in.Actor) > 120 || len(in.Action) > 120 || len(in.Details) > 8192 {
		return errors.New("field too long")
	}
	switch in.Outcome {
	case "ok", "blocked", "failed":
	default:
		return errors.New("outcome must be ok, blocked, or failed")
	}
	return nil
}
func (s *Store) append(in Input) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return Event{}, errors.New("journal write previously failed; restart and inspect journal")
	}
	if err := validate(in); err != nil {
		return Event{}, err
	}
	previous := ""
	if len(s.events) > 0 {
		previous = s.events[len(s.events)-1].Hash
	}
	e := Event{Sequence: len(s.events) + 1, Time: time.Now().UTC().Format(time.RFC3339Nano), Input: in, Previous: previous}
	e.Hash = digest(e)
	b, _ := json.Marshal(e)
	b = append(b, '\n')
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return Event{}, err
	}
	n, err := f.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		s.failed = true
		return Event{}, err
	}
	s.events = append(s.events, e)
	return e, nil
}
func jsonOut(w http.ResponseWriter, status int, obj any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(obj)
}
func api(s *Store, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		path := "web" + r.URL.Path
		if r.URL.Path == "/" {
			path = "web/index.html"
		}
		b, err := web.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(path, ".js") {
			w.Header().Set("Content-Type", "application/javascript")
		}
		if strings.HasSuffix(path, ".css") {
			w.Header().Set("Content-Type", "text/css")
		}
		_, _ = w.Write(b)
	})
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			a, b := sha256.Sum256([]byte(supplied)), sha256.Sum256([]byte(token))
			if r.Header.Get("Authorization") != "Bearer "+supplied || subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
				jsonOut(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/api/events", auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			r.Body = http.MaxBytesReader(w, r.Body, 12<<10)
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			var in Input
			if err := dec.Decode(&in); err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					jsonOut(w, 413, map[string]string{"error": "body too large"})
				} else {
					jsonOut(w, 400, map[string]string{"error": "invalid JSON"})
				}
				return
			}
			if err := dec.Decode(&struct{}{}); err != io.EOF {
				jsonOut(w, 400, map[string]string{"error": "one JSON object required"})
				return
			}
			if err := validate(in); err != nil {
				jsonOut(w, 400, map[string]string{"error": err.Error()})
				return
			}
			event, err := s.append(in)
			if err != nil {
				jsonOut(w, 500, map[string]string{"error": "journal write failed"})
				return
			}
			jsonOut(w, 201, event)
		case "GET":
			after := 0
			if raw := r.URL.Query().Get("after"); raw != "" {
				n, err := strconv.Atoi(raw)
				if err != nil || n < 0 {
					jsonOut(w, 400, map[string]string{"error": "after must be a nonnegative integer"})
					return
				}
				after = n
			}
			actor := r.URL.Query().Get("actor")
			s.mu.Lock()
			rows := []Event{}
			for _, e := range s.events {
				if e.Sequence > after && (actor == "" || e.Actor == actor) {
					rows = append(rows, e)
					if len(rows) == 100 {
						break
					}
				}
			}
			s.mu.Unlock()
			jsonOut(w, 200, rows)
		default:
			w.Header().Set("Allow", "GET, POST")
			jsonOut(w, 405, map[string]string{"error": "method not allowed"})
		}
	}))
	mux.HandleFunc("/api/verify", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			jsonOut(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		err := verify(s.events)
		head := ""
		if len(s.events) > 0 {
			head = s.events[len(s.events)-1].Hash
		}
		jsonOut(w, 200, map[string]any{"valid": err == nil, "count": len(s.events), "head": head, "scope": "in-memory chain validated from disk at startup"})
	}))
	return mux
}
func main() {
	token := os.Getenv("TRAIL_TOKEN")
	if len(token) < 24 {
		log.Fatal("TRAIL_TOKEN must be at least 24 characters")
	}
	path := os.Getenv("TRAIL_DATA")
	if path == "" {
		path = "data/events.jsonl"
	}
	s, err := openStore(path)
	if err != nil {
		log.Fatal(err)
	}
	addr := os.Getenv("TRAIL_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	server := http.Server{Addr: addr, Handler: api(s, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("Trailvault listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}
