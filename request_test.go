package main

import (
	"slices"
	"testing"

	"pennyframe/internal/openrouter"
)

func testModel() openrouter.Model {
	return openrouter.Model{
		ID: "acme/pix",
		SupportedParams: map[string]openrouter.Capability{
			"aspect_ratio":     {Type: "enum", Values: []string{"1:1", "16:9"}},
			"quality":          {Type: "enum", Values: []string{"low", "high"}},
			"n":                {Type: "range", Min: 1, Max: 3},
			"seed":             {Type: "boolean"},
			"input_references": {Type: "range", Min: 0, Max: 2},
		},
	}
}

func TestBuildRequestKeepsSupported(t *testing.T) {
	seed := int64(7)
	req, sent, notes := buildRequest(testModel(), "a cat", Params{
		AspectRatio: "16:9", Quality: "high", N: 2, Seed: &seed,
	}, []string{"data:image/png;base64,AA=="})
	if req.AspectRatio != "16:9" || req.Quality != "high" || req.N != 2 || *req.Seed != 7 {
		t.Fatalf("unexpected request: %+v", req)
	}
	if len(req.InputReferences) != 1 || req.InputReferences[0].Type != "image_url" {
		t.Fatalf("references not forwarded: %+v", req.InputReferences)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if sent["n"] != 2 || sent["quality"] != "high" {
		t.Fatalf("sent params wrong: %v", sent)
	}
}

func TestBuildRequestDropsUnsupported(t *testing.T) {
	seed := int64(1)
	m := testModel()
	delete(m.SupportedParams, "seed")
	req, _, notes := buildRequest(m, "x", Params{
		AspectRatio: "21:9", Resolution: "4K", N: 9, Seed: &seed,
	}, []string{"a", "b", "c"})
	if req.AspectRatio != "" || req.Resolution != "" || req.Seed != nil {
		t.Fatalf("unsupported params leaked: %+v", req)
	}
	if req.N != 3 {
		t.Fatalf("n should clamp to 3, got %d", req.N)
	}
	if len(req.InputReferences) != 2 {
		t.Fatalf("refs should truncate to 2, got %d", len(req.InputReferences))
	}
	if len(notes) != 5 {
		t.Fatalf("want 5 notes, got %d: %v", len(notes), notes)
	}
}

func TestBuildRequestAutoAndEmptyAreOmitted(t *testing.T) {
	req, sent, notes := buildRequest(testModel(), "x", Params{AspectRatio: "auto", Quality: ""}, nil)
	if req.AspectRatio != "" || len(sent) != 0 || len(notes) != 0 {
		t.Fatalf("expected nothing sent: %+v %v %v", req, sent, notes)
	}
}

func TestBuildRequestNoRefSupport(t *testing.T) {
	m := testModel()
	delete(m.SupportedParams, "input_references")
	req, _, notes := buildRequest(m, "x", Params{}, []string{"a"})
	if len(req.InputReferences) != 0 || !slices.ContainsFunc(notes, func(s string) bool { return len(s) > 0 }) {
		t.Fatalf("refs should be ignored with a note: %+v %v", req, notes)
	}
}

func TestBuildRequestStreaming(t *testing.T) {
	m := testModel()
	m.SupportsStreaming = true
	if req, _, _ := buildRequest(m, "x", Params{Stream: true}, nil); !req.Stream {
		t.Fatal("stream should be on for a streaming model")
	}
	req, _, notes := buildRequest(m, "x", Params{Stream: true, N: 2}, nil)
	if req.Stream || len(notes) != 1 {
		t.Fatalf("multi-image requests must not stream: %+v %v", req, notes)
	}
	m.SupportsStreaming = false
	if req, _, _ := buildRequest(m, "x", Params{Stream: true}, nil); req.Stream {
		t.Fatal("non-streaming model must not stream")
	}
	m.SupportsStreaming = true
	if req, _, _ := buildRequest(m, "x", Params{}, nil); req.Stream {
		t.Fatal("stream is opt-in per request")
	}
}
