package main

import (
	"fmt"
	"math/rand/v2"
	"slices"

	"pennyframe/internal/openrouter"
)

// Params are the user-facing generation knobs. Empty means "provider default".
type Params struct {
	AspectRatio  string `json:"aspectRatio"`
	Resolution   string `json:"resolution"`
	Quality      string `json:"quality"`
	OutputFormat string `json:"outputFormat"`
	Background   string `json:"background"`
	Compression  *int   `json:"compression"`
	Seed         *int64 `json:"seed"`
	N            int    `json:"n"`
	Stream       bool   `json:"stream"`
}

// withRandomSeed gives every request its own seed when the user hasn't fixed one, so providers that
// default to a constant seed can't return near-identical images. The seed is recorded with the image.
func withRandomSeed(m openrouter.Model, p Params) Params {
	if c, ok := m.SupportedParams["seed"]; p.Seed != nil || !ok || c.Type != "boolean" {
		return p
	}
	s := rand.Int64N(1 << 31)
	p.Seed = &s
	return p
}

// buildRequest adapts the user's params to what a specific model accepts.
// It returns the request, the params actually sent, and notes about anything dropped.
func buildRequest(m openrouter.Model, prompt string, p Params, refs []string) (openrouter.GenerateRequest, map[string]any, []string) {
	req := openrouter.GenerateRequest{Model: m.ID, Prompt: prompt}
	sent := map[string]any{}
	var notes []string

	enum := func(name, label, val string, dst *string) {
		if val == "" || val == "auto" {
			return
		}
		c, ok := m.SupportedParams[name]
		if !ok || c.Type != "enum" {
			notes = append(notes, fmt.Sprintf("%s isn't configurable on this model", label))
			return
		}
		if !slices.Contains(c.Values, val) {
			notes = append(notes, fmt.Sprintf("%s %q not supported — using model default", label, val))
			return
		}
		*dst = val
		sent[name] = val
	}
	enum("aspect_ratio", "Aspect ratio", p.AspectRatio, &req.AspectRatio)
	enum("resolution", "Resolution", p.Resolution, &req.Resolution)
	enum("quality", "Quality", p.Quality, &req.Quality)
	enum("output_format", "Format", p.OutputFormat, &req.OutputFormat)
	enum("background", "Background", p.Background, &req.Background)

	if p.N > 1 {
		if c, ok := m.SupportedParams["n"]; ok && c.Type == "range" {
			n := min(p.N, c.Max)
			switch {
			case c.Max <= 1:
				notes = append(notes, "This model returns one image per request")
			case n < p.N:
				notes = append(notes, fmt.Sprintf("Limited to %d images for this model", n))
			}
			if n > 1 {
				req.N = n
				sent["n"] = n
			}
		} else {
			notes = append(notes, "This model returns one image per request")
		}
	}

	if p.Stream && m.SupportsStreaming {
		if req.N > 1 {
			notes = append(notes, "Streaming is off for multi-image requests")
		} else {
			req.Stream = true
		}
	}

	if p.Seed != nil {
		if c, ok := m.SupportedParams["seed"]; ok && c.Type == "boolean" {
			req.Seed = p.Seed
			sent["seed"] = *p.Seed
		} else {
			notes = append(notes, "Seed isn't supported on this model")
		}
	}

	if p.Compression != nil {
		if c, ok := m.SupportedParams["output_compression"]; ok && c.Type == "range" {
			v := min(max(*p.Compression, c.Min), c.Max)
			req.OutputCompression = &v
			sent["output_compression"] = v
		}
	}

	if len(refs) > 0 {
		limit := 0
		if c, ok := m.SupportedParams["input_references"]; ok && c.Type == "range" {
			limit = c.Max
		}
		switch {
		case limit == 0:
			notes = append(notes, "Reference images ignored — model can't use them")
		default:
			use := refs
			if len(use) > limit {
				use = use[:limit]
				notes = append(notes, fmt.Sprintf("Using the first %d of %d reference images", limit, len(refs)))
			}
			for _, r := range use {
				req.InputReferences = append(req.InputReferences, openrouter.InputReference{
					Type:     "image_url",
					ImageURL: openrouter.ImageURL{URL: r},
				})
			}
		}
	}
	return req, sent, notes
}
