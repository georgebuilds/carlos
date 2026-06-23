package todo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestREST_ListWithFilterAndParams(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]Item{{ID: "r1", Text: "remote task", Due: "2026-09-09"}})
	}))
	defer srv.Close()

	s := NewRESTStore(RESTConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthValue: "Bearer tok", Client: srv.Client()})
	items, err := s.List(context.Background(), Query{
		Scopes: []Scope{{Frame: "work", Params: map[string]string{"project": "Ludus"}}},
		Filter: FilterOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "r1" {
		t.Fatalf("items wrong: %+v", items)
	}
	if items[0].Backend != "rest" || items[0].Frame != "work" {
		t.Errorf("labels wrong: %+v", items[0])
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth header not sent: %q", gotAuth)
	}
	// filter + project must be in the query string.
	if !contains(gotPath, "filter=open") || !contains(gotPath, "project=Ludus") {
		t.Errorf("query string missing params: %q", gotPath)
	}
}

func TestREST_Add(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/todos" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["text"] != "do it" {
			t.Errorf("body text = %v", body["text"])
		}
		if body["project"] != "Ludus" {
			t.Errorf("scope param not merged into body: %v", body["project"])
		}
		_ = json.NewEncoder(w).Encode(Item{ID: "srv-1", Text: "do it"})
	}))
	defer srv.Close()

	s := NewRESTStore(RESTConfig{BaseURL: srv.URL, Client: srv.Client()})
	it, err := s.Add(context.Background(), Scope{Frame: "work", Params: map[string]string{"project": "Ludus"}}, Draft{Text: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "srv-1" || it.Backend != "rest" || it.Frame != "work" {
		t.Errorf("add result wrong: %+v", it)
	}
}

func TestREST_Complete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/todos/abc/complete" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(Item{ID: "abc", Done: true})
	}))
	defer srv.Close()
	s := NewRESTStore(RESTConfig{BaseURL: srv.URL, Client: srv.Client()})
	it, err := s.Complete(context.Background(), Scope{}, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if !it.Done {
		t.Errorf("not completed: %+v", it)
	}
}

func TestREST_Update(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["done"] != true {
			t.Errorf("done not in patch body: %v", body)
		}
		if _, hasText := body["text"]; hasText {
			t.Error("nil patch field should be omitted from body")
		}
		_ = json.NewEncoder(w).Encode(Item{ID: "abc", Done: true})
	}))
	defer srv.Close()
	s := NewRESTStore(RESTConfig{BaseURL: srv.URL, Client: srv.Client()})
	done := true
	if _, err := s.Update(context.Background(), Scope{}, "abc", Patch{Done: &done}); err != nil {
		t.Fatal(err)
	}
}

func TestREST_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	s := NewRESTStore(RESTConfig{BaseURL: srv.URL, Client: srv.Client()})
	if _, err := s.Complete(context.Background(), Scope{}, "ghost"); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestREST_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := NewRESTStore(RESTConfig{BaseURL: srv.URL, Client: srv.Client()})
	if _, err := s.List(context.Background(), Query{}); err == nil {
		t.Error("5xx should surface as an error")
	}
}

// Regression: the default client must refuse a cross-host redirect so the auth
// header is never replayed to an unintended origin.
func TestREST_RefusesCrossHostRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/todos", http.StatusFound)
	}))
	defer srv.Close()
	// nil Client → the guarded default client is used.
	s := NewRESTStore(RESTConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthValue: "Bearer secret"})
	_, err := s.List(context.Background(), Query{})
	if err == nil {
		t.Fatal("cross-host redirect should be refused")
	}
	if !contains(err.Error(), "redirect") {
		t.Errorf("unexpected error (want redirect refusal): %v", err)
	}
}

func TestREST_DefaultName(t *testing.T) {
	if NewRESTStore(RESTConfig{}).Name() != "rest" {
		t.Error("default name should be 'rest'")
	}
	if NewRESTStore(RESTConfig{Name: "todoist"}).Name() != "todoist" {
		t.Error("name override failed")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
