package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New()
	c.BaseURL = srv.URL
	return c
}

func TestGenerateSendsAuthAndParsesResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("bad request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body GenerateRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "a/b" || body.AspectRatio != "1:1" || body.N != 0 {
			t.Errorf("unexpected body: %+v", body)
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"QQ==","media_type":"image/png"}],"usage":{"cost":0.04}}`))
	})
	resp, err := c.Generate(context.Background(), "sk-test", GenerateRequest{Model: "a/b", Prompt: "x", AspectRatio: "1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "QQ==" || resp.Usage.Cost != 0.04 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestAPIErrorParsing(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"out of credits","code":402}}`))
	})
	_, err := c.Generate(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 402 || ae.Message != "out of credits" {
		t.Fatalf("want 402 APIError, got %v", err)
	}
}

func TestNonJSONErrorBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	})
	_, err := c.KeyInfo(context.Background(), "k")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Message != "upstream exploded" {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyDataIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":[]}`)) })
	if _, err := c.Generate(context.Background(), "k", GenerateRequest{Model: "m", Prompt: "p"}); err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestEndpointsKeepsSlashInModelID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/models/google/nano-banana/endpoints" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"endpoints":[{"pricing":[{"billable":"output_image","unit":"image","cost_usd":0.05},{"billable":"output_image","unit":"image","cost_usd":0.02,"variant":"1k"}]}]}`))
	})
	eps, err := c.Endpoints(context.Background(), "google/nano-banana")
	if err != nil {
		t.Fatal(err)
	}
	if got := PriceSummary(eps); got != "$0.02–$0.05/img" {
		t.Fatalf("price = %q", got)
	}
}

func TestPriceSummary(t *testing.T) {
	img := func(cost float64, variant string) PriceLine {
		return PriceLine{Billable: "output_image", Unit: "image", CostUSD: cost, Variant: variant}
	}
	cases := []struct {
		name  string
		lines []PriceLine
		want  string
	}{
		{"flat", []PriceLine{img(0.04, "")}, "$0.04/img"},
		{"same across tiers", []PriceLine{img(0.0184, "1k"), img(0.0184, "2k")}, "$0.0184/img"},
		{"tiered range", []PriceLine{img(0.008, "768"), img(0.036, "4k"), img(0.014, "2k")}, "$0.008–$0.036/img"},
		{"token billed is skipped", []PriceLine{{Billable: "output_image", Unit: "token", CostUSD: 0.00003}}, ""},
		{"megapixel billed is skipped", []PriceLine{{Billable: "output_image", Unit: "megapixel", CostUSD: 0.03}}, ""},
		{"input pricing ignored", []PriceLine{{Billable: "input_image", Unit: "image", CostUSD: 0.5}}, ""},
		{"mixed units use only per-image", []PriceLine{{Billable: "output_image", Unit: "token", CostUSD: 0.00003}, img(0.05, "")}, "$0.05/img"},
	}
	for _, c := range cases {
		if got := PriceSummary([]Endpoint{{Pricing: c.lines}}); got != c.want {
			t.Errorf("%s: want %q got %q", c.name, c.want, got)
		}
	}
	if PriceSummary(nil) != "" {
		t.Error("no endpoints should give empty price")
	}
}
