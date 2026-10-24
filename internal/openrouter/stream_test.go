package openrouter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func sse(events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
		}
	}
}

func TestStreamDeliversPartialsThenFinal(t *testing.T) {
	big := strings.Repeat("A", 200_000) // exceeds bufio.Scanner's default line limit
	c := newTestClient(t, sse(
		`{"type":"image_generation.partial_image","partial_image_index":0,"b64_json":"UDE="}`,
		`{"type":"image_generation.partial_image","partial_image_index":1,"b64_json":"`+big+`"}`,
		`{"type":"image_generation.completed","b64_json":"RklOQUw=","media_type":"image/png","usage":{"cost":0.011}}`,
		`[DONE]`,
	))
	var partials []int
	resp, err := c.GenerateStream(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"}, func(b64 string) { partials = append(partials, len(b64)) })
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 2 || partials[1] != 200_000 {
		t.Fatalf("partials = %v", partials)
	}
	if resp.Data[0].B64JSON != "RklOQUw=" || resp.Data[0].MediaType != "image/png" || resp.Usage.Cost != 0.011 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestStreamUnderstandsImageEditEvents(t *testing.T) {
	c := newTestClient(t, sse(
		`{"type":"image_edit.partial_image","partial_image_index":0,"b64_json":"UDE="}`,
		`{"type":"image_edit.completed","b64_json":"RklOQUw=","media_type":"image/png"}`,
	))
	partials := 0
	resp, err := c.GenerateStream(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"}, func(string) { partials++ })
	if err != nil || partials != 1 || resp.Data[0].B64JSON != "RklOQUw=" {
		t.Fatalf("partials=%d resp=%+v err=%v", partials, resp, err)
	}
}

func TestStreamMidStreamRateLimit(t *testing.T) {
	c := newTestClient(t, sse(`{"type":"error","error":{"message":"slow down","code":429}}`))
	_, err := c.GenerateStream(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"}, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests {
		t.Fatalf("got %v", err)
	}
}

func TestRateLimitCarriesRetryAfter(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Provider returned error","code":429}}`))
	})
	_, err := c.Generate(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 || ae.RetryAfter != 7*time.Second {
		t.Fatalf("got %#v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "abc": 0, "3": 3 * time.Second, "0.5": 500 * time.Millisecond, "-2": 0} {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
	if d := parseRetryAfter(time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)); d < 25*time.Second || d > 31*time.Second {
		t.Errorf("http-date form = %v", d)
	}
}

func TestStreamSendsStreamFlag(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		if !strings.Contains(string(buf[:n]), `"stream":true`) {
			t.Errorf("stream flag missing: %s", buf[:n])
		}
		sse(`{"type":"image_generation.completed","b64_json":"QQ=="}`)(w, r)
	})
	if _, err := c.GenerateStream(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStreamMidStreamError(t *testing.T) {
	c := newTestClient(t, sse(`{"type":"error","error":{"message":"Generation failed","code":"server_error"}}`))
	_, err := c.GenerateStream(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Generation failed") {
		t.Fatalf("got %v", err)
	}
}

func TestStreamTruncated(t *testing.T) {
	c := newTestClient(t, sse(`{"type":"image_generation.partial_image","b64_json":"QQ=="}`))
	_, err := c.GenerateStream(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"}, nil)
	if err == nil {
		t.Fatal("expected error when stream ends without a completed event")
	}
}

func TestStreamFallsBackToJSONBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"QQ==","media_type":"image/png"}]}`))
	})
	resp, err := c.GenerateStream(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"}, nil)
	if err != nil || resp.Data[0].B64JSON != "QQ==" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}
