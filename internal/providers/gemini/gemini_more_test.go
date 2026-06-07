package gemini

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

func TestStream_RoundTripsThroughOacompat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"hello"}}]}`+"\n\n")
			io.WriteString(w, "data: "+`{"choices":[{"finish_reason":"stop"}]}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
			f.Flush()
		}
	}))
	defer ts.Close()
	c := New("gemini-key")
	c.BaseURL = ts.URL
	ch, err := c.Stream(context.Background(), providers.Request{Model: "gemini-2"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var sawText, sawStop bool
	for ev := range ch {
		if ev.Kind == providers.EventTextDelta && ev.Text == "hello" {
			sawText = true
		}
		if ev.Kind == providers.EventStopReason {
			sawStop = true
		}
	}
	if !sawText {
		t.Error("never saw text delta")
	}
	if !sawStop {
		t.Error("never saw stop reason")
	}
}

func TestStream_MapsToolCallsFinishReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			io.WriteString(w, "data: "+`{"choices":[{"finish_reason":"tool_calls"}]}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
			f.Flush()
		}
	}))
	defer ts.Close()
	c := New("k")
	c.BaseURL = ts.URL
	ch, err := c.Stream(context.Background(), providers.Request{Model: "gemini"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got string
	for ev := range ch {
		if ev.Kind == providers.EventStopReason {
			got = ev.Stop
		}
	}
	if got != "tool_use" {
		t.Errorf("tool_calls finish_reason mapped to %q, want tool_use", got)
	}
}

func TestStream_HTTPErrorPropagates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "boom")
	}))
	defer ts.Close()
	c := New("k")
	c.BaseURL = ts.URL
	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		return
	}
	if ch == nil {
		t.Fatal("ch nil with no err")
	}
	gotErr := false
	for ev := range ch {
		if ev.Kind == providers.EventError {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected EventError on 500")
	}
}
