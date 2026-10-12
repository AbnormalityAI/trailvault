package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const testToken = "test-token-with-at-least-24-characters"

func fresh(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestPersistAndTamper(t *testing.T) {
	s := fresh(t)
	_, err := s.append(Input{Actor: "a", Action: "read", Outcome: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	re, err := openStore(s.path)
	if err != nil || len(re.events) != 1 {
		t.Fatal("reopen", err)
	}
	b, _ := os.ReadFile(s.path)
	b = bytes.Replace(b, []byte(`"action":"read"`), []byte(`"action":"edit"`), 1)
	_ = os.WriteFile(s.path, b, 0600)
	if _, err = openStore(s.path); err == nil {
		t.Fatal("tampering accepted")
	}
}
func TestConcurrentChain(t *testing.T) {
	s := fresh(t)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := s.append(Input{Actor: fmt.Sprint(n), Action: "read", Outcome: "ok"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(s.events) != 30 {
		t.Fatal(len(s.events))
	}
	if err := verify(s.events); err != nil {
		t.Fatal(err)
	}
}
func TestAPI(t *testing.T) {
	s := fresh(t)
	handler := api(s, testToken)
	tests := []struct {
		method, path, body, token string
		status                    int
	}{
		{"GET", "/api/events", "", "", 401},
		{"GET", "/api/events", "", "wrong", 401},
		{"POST", "/api/events", `{"actor":"a","action":"read","outcome":"ok","details":"hello"}`, testToken, 201},
		{"POST", "/api/events", `{"actor":"a","action":"read","outcome":"oops"}`, testToken, 400},
		{"POST", "/api/events", `{"actor":"a","action":"read","outcome":"ok","surprise":1}`, testToken, 400},
		{"POST", "/api/events", `{"actor":"a","action":"read","outcome":"ok"} {}`, testToken, 400},
		{"GET", "/api/events?after=bad", "", testToken, 400},
		{"GET", "/api/events?after=-1", "", testToken, 400},
		{"GET", "/api/verify", "", testToken, 200},
		{"DELETE", "/api/events", "", testToken, 405},
		{"GET", "/", "", "", 200},
		{"GET", "/nope", "", "", 404},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
		if tt.token != "" {
			r.Header.Set("Authorization", "Bearer "+tt.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Errorf("%s %s got %d want %d: %s", tt.method, tt.path, w.Code, tt.status, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/events?actor=a", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var rows []Event
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatal("filter", err, w.Body.String())
	}
}
func TestTruncatedJournalFailsClosed(t *testing.T) {
	s := fresh(t)
	_ = os.WriteFile(s.path, []byte(`{"sequence":`), 0600)
	if _, err := openStore(s.path); err == nil {
		t.Fatal("truncated journal accepted")
	}
}

func TestUnterminatedValidJSONFailsClosed(t *testing.T) {
	s := fresh(t)
	_, err := s.append(Input{Actor: "a", Action: "read", Outcome: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(s.path)
	_ = os.WriteFile(s.path, bytes.TrimSuffix(b, []byte("\n")), 0600)
	if _, err := openStore(s.path); err == nil {
		t.Fatal("unterminated journal accepted")
	}
}
func TestUnknownJournalFieldsRejected(t *testing.T) {
	s := fresh(t)
	_, _ = s.append(Input{Actor: "a", Action: "read", Outcome: "ok"})
	b, _ := os.ReadFile(s.path)
	b = bytes.Replace(b, []byte(`"sequence":1`), []byte(`"extra":"unhashed","sequence":1`), 1)
	_ = os.WriteFile(s.path, b, 0600)
	if _, err := openStore(s.path); err == nil {
		t.Fatal("unknown journal field accepted")
	}
}
func TestFailClosedStore(t *testing.T) {
	s := fresh(t)
	s.failed = true
	if _, err := s.append(Input{Actor: "a", Action: "read", Outcome: "ok"}); err == nil {
		t.Fatal("failed store accepts write")
	}
	if len(s.events) != 0 {
		t.Fatal("write mutated failed store")
	}
}
func TestPaginationAndFalseVerify(t *testing.T) {
	s := fresh(t)
	for i := 0; i < 105; i++ {
		_, err := s.append(Input{Actor: "a", Action: "read", Outcome: "ok"})
		if err != nil {
			t.Fatal(err)
		}
	}
	h := api(s, testToken)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	var rows []Event
	_ = json.Unmarshal(get("/api/events").Body.Bytes(), &rows)
	if len(rows) != 100 || rows[99].Sequence != 100 {
		t.Fatal("first page", len(rows))
	}
	_ = json.Unmarshal(get("/api/events?after=100").Body.Bytes(), &rows)
	if len(rows) != 5 || rows[0].Sequence != 101 {
		t.Fatal("cursor page", len(rows))
	}
	s.events[0].Hash = "changed"
	var result map[string]any
	_ = json.Unmarshal(get("/api/verify").Body.Bytes(), &result)
	if result["valid"] != false {
		t.Fatal("corruption not reported")
	}
}
func TestOversizedBody(t *testing.T) {
	s := fresh(t)
	r := httptest.NewRequest("POST", "/api/events", bytes.NewBufferString(`{"actor":"a","action":"read","outcome":"ok","details":"`+string(bytes.Repeat([]byte("x"), 14000))+`"}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	api(s, testToken).ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestRealWriteFailureClosesStore(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("requires /dev/full")
	}
	s := fresh(t)
	s.path = "/dev/full"
	_, err := s.append(Input{Actor: "a", Action: "read", Outcome: "ok"})
	if err == nil || !s.failed {
		t.Fatal("write failure did not close store", err)
	}
	s.path = filepath.Join(t.TempDir(), "events.jsonl")
	_ = os.WriteFile(s.path, nil, 0600)
	if _, err := s.append(Input{Actor: "a", Action: "read", Outcome: "ok"}); err == nil {
		t.Fatal("store resumed after uncertain write")
	}
}
