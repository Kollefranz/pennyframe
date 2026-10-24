package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pennyframe/internal/openrouter"
	"pennyframe/internal/store"
)

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg=="

// runTestJob runs one job against srv and returns its updates in order.
func runTestJob(t *testing.T, srv *httptest.Server, p Params, refs []string) []JobUpdate {
	t.Helper()
	g, err := store.OpenGallery(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []JobUpdate
	a := &App{
		gal:     g,
		or:      &openrouter.Client{BaseURL: srv.URL, HTTP: srv.Client()},
		preview: map[string][]byte{},
		sem:     make(chan struct{}, 1),
		emit: func(event string, data any) {
			if u, ok := data.(JobUpdate); ok && event == "job" {
				mu.Lock()
				got = append(got, u)
				mu.Unlock()
			}
		},
	}
	m := openrouter.Model{ID: "acme/pix", Name: "Pix", SupportsStreaming: true, SupportedParams: map[string]openrouter.Capability{
		"input_references": {Type: "range", Min: 0, Max: 4},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.runJob(ctx, "b1", JobInfo{ID: "j1", Model: m.ID, ModelName: m.Name}, m, "k", "key", GenerateInput{Prompt: "p", Params: p}, refs)
	mu.Lock()
	defer mu.Unlock()
	return append([]JobUpdate(nil), got...)
}

func TestRunJobRetriesRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"Provider returned error","code":429}}`)
			return
		}
		fmt.Fprintf(w, `{"data":[{"b64_json":%q,"media_type":"image/png"}]}`, tinyPNG)
	}))
	defer srv.Close()

	got := runTestJob(t, srv, Params{}, nil)
	last := got[len(got)-1]
	if last.State != "done" || len(last.Images) != 1 || calls.Load() != 3 {
		t.Fatalf("state=%s images=%d calls=%d err=%q", last.State, len(last.Images), calls.Load(), last.Error)
	}
	if !strings.Contains(strings.Join(last.Notes, "|"), "2 rate-limit retries") {
		t.Fatalf("notes = %v", last.Notes)
	}
	var sawRetry bool
	for _, u := range got {
		if u.State == "running" && len(u.Notes) > 0 && strings.HasPrefix(u.Notes[0], "Rate limited") {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Fatalf("no running update announced the retry: %+v", got)
	}
}

func TestRunJobGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0.01")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"nope","code":429}}`)
	}))
	defer srv.Close()

	got := runTestJob(t, srv, Params{}, nil)
	last := got[len(got)-1]
	if last.State != "error" || !strings.Contains(last.Error, "429") || int(calls.Load()) != maxRateRetries+1 {
		t.Fatalf("state=%s err=%q calls=%d", last.State, last.Error, calls.Load())
	}
}

// Edits stream as image_edit.* events; the preview and the final image must still arrive.
func TestRunJobStreamsEditWithReferences(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1<<20)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"image_edit.partial_image\",\"b64_json\":%q}\n\n", tinyPNG)
		fmt.Fprintf(w, "data: {\"type\":\"image_edit.completed\",\"b64_json\":%q,\"media_type\":\"image/png\"}\n\n", tinyPNG)
	}))
	defer srv.Close()

	refs := []string{"data:image/png;base64," + tinyPNG, "data:image/png;base64," + tinyPNG, "data:image/png;base64," + tinyPNG}
	got := runTestJob(t, srv, Params{Stream: true}, refs)
	last := got[len(got)-1]
	if last.State != "done" || len(last.Images) != 1 {
		t.Fatalf("state=%s err=%q", last.State, last.Error)
	}
	if strings.Count(body, `"type":"image_url"`) != 3 || !strings.Contains(body, `"stream":true`) {
		t.Fatalf("request body = %.300s", body)
	}
	if last.Images[0].Refs != 3 {
		t.Fatalf("refs recorded = %d", last.Images[0].Refs)
	}
}

func TestRateRetryDelay(t *testing.T) {
	if d := rateRetryDelay(0, 7*time.Second); d != 7*time.Second {
		t.Errorf("hint ignored: %v", d)
	}
	if d := rateRetryDelay(0, time.Hour); d != maxRateWait {
		t.Errorf("hint not capped: %v", d)
	}
	for i := range maxRateRetries {
		lo := 4 * time.Second << i
		if d := rateRetryDelay(i, 0); d < lo || d > min(lo+lo/4, maxRateWait) {
			t.Errorf("attempt %d delay %v outside [%v, %v]", i, d, lo, lo+lo/4)
		}
	}
}
