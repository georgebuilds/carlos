package todo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RESTStore is the reference external backend: a Store backed by any HTTP
// service that speaks the small "carlos todo" contract below. It is the proof
// that the Store seam supports out-of-vault systems over a real transport
// without baking in a specific vendor SDK. Pointing it at Todoist, Google
// Tasks, or a self-hosted service is a thin server-side adapter that maps the
// vendor's API onto this contract.
//
// Contract (all bodies are the JSON Item shape; auth via a configurable
// header):
//
//	GET    {base}/todos?filter=open[&<param>=<value>...]   → [Item, ...]
//	POST   {base}/todos        {text,due,tags,<params>}    → Item
//	POST   {base}/todos/{id}/complete                      → Item
//	PATCH  {base}/todos/{id}    {text?,done?,due?,tags?}   → Item
//
// A 404 on complete/update maps to ErrNotFound; any other non-2xx surfaces as
// an error carrying the status and a truncated body.
type RESTStore struct {
	name       string
	base       string
	authHeader string
	authValue  string
	client     httpDoer
}

// httpDoer is the subset of *http.Client RESTStore needs, so tests can inject a
// fake transport.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// RESTConfig configures a RESTStore. AuthHeader/AuthValue are sent verbatim on
// every request when both are non-empty (e.g. "Authorization" / "Bearer xyz").
type RESTConfig struct {
	Name       string
	BaseURL    string
	AuthHeader string
	AuthValue  string
	// Client is optional; a 15s-timeout *http.Client is used when nil.
	Client httpDoer
}

// NewRESTStore builds a REST-backed store from cfg.
func NewRESTStore(cfg RESTConfig) *RESTStore {
	name := cfg.Name
	if name == "" {
		name = "rest"
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			// Refuse cross-host redirects: a backend that 3xx-redirects to a
			// different host would otherwise receive the auth header, leaking
			// the credential to an unintended origin.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("todo: rest stopped after 10 redirects")
				}
				if len(via) > 0 && req.URL.Host != via[0].URL.Host {
					return fmt.Errorf("todo: rest refusing cross-host redirect to %s", req.URL.Host)
				}
				return nil
			},
		}
	}
	return &RESTStore{
		name:       name,
		base:       strings.TrimRight(cfg.BaseURL, "/"),
		authHeader: cfg.AuthHeader,
		authValue:  cfg.AuthValue,
		client:     client,
	}
}

// Name identifies this backend (configurable so multiple REST services can
// coexist under distinct names).
func (s *RESTStore) Name() string { return s.name }

// List queries the service once per scope (each scope's params become query
// parameters) and labels the returned items with the scope frame + backend.
func (s *RESTStore) List(ctx context.Context, q Query) ([]Item, error) {
	scopes := q.Scopes
	if len(scopes) == 0 {
		scopes = []Scope{{}}
	}
	filter := q.Filter
	if filter == "" {
		filter = FilterOpen
	}
	var out []Item
	for _, sc := range scopes {
		vals := url.Values{}
		vals.Set("filter", string(filter))
		for k, v := range sc.Params {
			vals.Set(k, v)
		}
		var items []Item
		if err := s.do(ctx, http.MethodGet, "/todos?"+vals.Encode(), nil, &items); err != nil {
			return nil, err
		}
		for i := range items {
			items[i].Backend = s.name
			items[i].Frame = sc.Frame
		}
		out = append(out, items...)
	}
	return out, nil
}

// Add posts a new task, merging the scope params into the body.
func (s *RESTStore) Add(ctx context.Context, sc Scope, draft Draft) (Item, error) {
	body := map[string]any{
		"text": draft.Text,
		"due":  draft.Due,
		"tags": normalizeTags(draft.Tags),
	}
	for k, v := range sc.Params {
		body[k] = v
	}
	var it Item
	if err := s.do(ctx, http.MethodPost, "/todos", body, &it); err != nil {
		return Item{}, err
	}
	it.Backend = s.name
	it.Frame = sc.Frame
	return it, nil
}

// Complete posts to the item's complete route.
func (s *RESTStore) Complete(ctx context.Context, sc Scope, id string) (Item, error) {
	var it Item
	if err := s.do(ctx, http.MethodPost, "/todos/"+url.PathEscape(id)+"/complete", nil, &it); err != nil {
		return Item{}, err
	}
	it.Backend = s.name
	it.Frame = sc.Frame
	return it, nil
}

// Update patches the item.
func (s *RESTStore) Update(ctx context.Context, sc Scope, id string, patch Patch) (Item, error) {
	body := map[string]any{}
	if patch.Text != nil {
		body["text"] = *patch.Text
	}
	if patch.Done != nil {
		body["done"] = *patch.Done
	}
	if patch.Due != nil {
		body["due"] = *patch.Due
	}
	if patch.Tags != nil {
		body["tags"] = normalizeTags(*patch.Tags)
	}
	var it Item
	if err := s.do(ctx, http.MethodPatch, "/todos/"+url.PathEscape(id), body, &it); err != nil {
		return Item{}, err
	}
	it.Backend = s.name
	it.Frame = sc.Frame
	return it, nil
}

// do issues one request and decodes a JSON response into out (when non-nil).
func (s *RESTStore) do(ctx context.Context, method, pathAndQuery string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("todo: rest marshal body: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+pathAndQuery, reader)
	if err != nil {
		return fmt.Errorf("todo: rest request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if s.authHeader != "" && s.authValue != "" {
		req.Header.Set(s.authHeader, s.authValue)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("todo: rest %s %s: %w", method, pathAndQuery, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("todo: rest %s %s: status %d: %s", method, pathAndQuery, resp.StatusCode, truncate(string(respBody), 200))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("todo: rest decode response: %w", err)
	}
	return nil
}

// truncate caps s at n runes for error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
