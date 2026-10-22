// Package openrouter is a small client for OpenRouter's image API.
package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const DefaultBaseURL = "https://openrouter.ai/api/v1"

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a client for OpenRouter; OPENROUTER_BASE_URL overrides the endpoint (proxies, tests).
func New() *Client {
	base := DefaultBaseURL
	if v := os.Getenv("OPENROUTER_BASE_URL"); v != "" {
		base = strings.TrimRight(v, "/")
	}
	return &Client{
		BaseURL: base,
		// No overall timeout: big images can take minutes. Callers use ctx.
		HTTP: &http.Client{Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 15 * time.Second,
		}},
	}
}

// APIError is a non-2xx response from OpenRouter.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "OpenRouter rejected the API key (401)"
	case http.StatusPaymentRequired:
		return "Insufficient OpenRouter credits (402): " + e.Message
	case http.StatusTooManyRequests:
		return "Rate limited by OpenRouter (429): " + e.Message
	}
	return fmt.Sprintf("OpenRouter error %d: %s", e.Status, e.Message)
}

func (c *Client) do(ctx context.Context, method, path, key string, body any, out any) error {
	resp, err := c.send(ctx, method, path, key, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// send performs a request and returns the response only for 2xx; the caller closes the body.
func (c *Client) send(ctx context.Context, method, path, key string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Title", "Pennyframe")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, parseError(resp)
	}
	return resp, nil
}

func parseError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Error struct {
			Message  string          `json:"message"`
			Code     json.RawMessage `json:"code"`
			Metadata struct {
				Raw          string `json:"raw"`
				ProviderName string `json:"provider_name"`
			} `json:"metadata"`
		} `json:"error"`
	}
	e := &APIError{Status: resp.StatusCode}
	if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
		e.Message = env.Error.Message
		e.Code = strings.Trim(string(env.Error.Code), `"`)
		if p := env.Error.Metadata.ProviderName; p != "" {
			e.Message += " (" + p + ")"
		}
	} else {
		e.Message = strings.TrimSpace(string(raw))
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
	}
	return e
}

// KeyInfo describes the current API key (GET /key).
type KeyInfo struct {
	Label              string   `json:"label"`
	Usage              float64  `json:"usage"`
	UsageDaily         float64  `json:"usage_daily"`
	UsageWeekly        float64  `json:"usage_weekly"`
	UsageMonthly       float64  `json:"usage_monthly"`
	ByokUsage          float64  `json:"byok_usage"`
	Limit              *float64 `json:"limit"`
	LimitRemaining     *float64 `json:"limit_remaining"`
	LimitReset         *string  `json:"limit_reset"`
	IncludeBYOKInLimit bool     `json:"include_byok_in_limit"`
	ExpiresAt          *string  `json:"expires_at"`
	IsFreeTier         bool     `json:"is_free_tier"`
	IsManagementKey    bool     `json:"is_management_key"`
	IsProvisioningKey  bool     `json:"is_provisioning_key"`
	AllowedDataRegions []string `json:"allowed_data_regions"`
}

func (c *Client) KeyInfo(ctx context.Context, key string) (*KeyInfo, error) {
	var out struct {
		Data KeyInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/key", key, nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Capability mirrors a "capability descriptor" from the image models API.
type Capability struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
	Min    int      `json:"min,omitempty"`
	Max    int      `json:"max,omitempty"`
}

type Model struct {
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	Description       string                `json:"description"`
	Created           int64                 `json:"created"`
	Architecture      Architecture          `json:"architecture"`
	SupportedParams   map[string]Capability `json:"supported_parameters"`
	SupportsStreaming bool                  `json:"supports_streaming"`
	// Price is filled in from the endpoints API, not the list.
	Price string `json:"price,omitempty"`
}

type Architecture struct {
	Input  []string `json:"input_modalities"`
	Output []string `json:"output_modalities"`
}

func (c *Client) ImageModels(ctx context.Context) ([]Model, error) {
	var out struct {
		Data []Model `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/images/models", "", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

type PriceLine struct {
	Billable string  `json:"billable"`
	Unit     string  `json:"unit"`
	CostUSD  float64 `json:"cost_usd"`
	Variant  string  `json:"variant"`
}

type Endpoint struct {
	ProviderName string      `json:"provider_name"`
	ProviderSlug string      `json:"provider_slug"`
	Pricing      []PriceLine `json:"pricing"`
}

func (c *Client) Endpoints(ctx context.Context, modelID string) ([]Endpoint, error) {
	var out struct {
		Endpoints []Endpoint `json:"endpoints"`
	}
	// Model IDs contain a slash that is part of the path.
	path := "/images/models/" + strings.ReplaceAll(url.PathEscape(modelID), "%2F", "/") + "/endpoints"
	if err := c.do(ctx, http.MethodGet, path, "", nil, &out); err != nil {
		return nil, err
	}
	return out.Endpoints, nil
}

// PriceSummary returns a per-image price label, or "" when no reliable per-image price exists.
// Only flat per-image pricing qualifies: token- and megapixel-billed models depend on output size and
// token usage, so a per-image figure would be a guess. Resolution/quality tiers yield a range.
func PriceSummary(eps []Endpoint) string {
	lo, hi, found := 0.0, 0.0, false
	for _, ep := range eps {
		for _, p := range ep.Pricing {
			if p.Billable != "output_image" || p.Unit != "image" {
				continue
			}
			if !found {
				lo, hi, found = p.CostUSD, p.CostUSD, true
			}
			lo, hi = min(lo, p.CostUSD), max(hi, p.CostUSD)
		}
	}
	switch {
	case !found:
		return ""
	case hi > lo*1.05:
		return fmt.Sprintf("$%s–$%s/img", trimFloat(lo), trimFloat(hi))
	}
	return fmt.Sprintf("$%s/img", trimFloat(lo))
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.4f", f)
	if f >= 1 {
		s = fmt.Sprintf("%.2f", f)
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

type ImageURL struct {
	URL string `json:"url"`
}

type InputReference struct {
	Type     string   `json:"type"`
	ImageURL ImageURL `json:"image_url"`
}

type GenerateRequest struct {
	Model             string           `json:"model"`
	Prompt            string           `json:"prompt"`
	N                 int              `json:"n,omitempty"`
	Resolution        string           `json:"resolution,omitempty"`
	AspectRatio       string           `json:"aspect_ratio,omitempty"`
	Quality           string           `json:"quality,omitempty"`
	OutputFormat      string           `json:"output_format,omitempty"`
	Background        string           `json:"background,omitempty"`
	OutputCompression *int             `json:"output_compression,omitempty"`
	Seed              *int64           `json:"seed,omitempty"`
	InputReferences   []InputReference `json:"input_references,omitempty"`
	Stream            bool             `json:"stream,omitempty"`
}

type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
}

type GeneratedImage struct {
	B64JSON   string `json:"b64_json"`
	MediaType string `json:"media_type"`
	URL       string `json:"url"`
}

type GenerateResponse struct {
	Created int64            `json:"created"`
	Data    []GeneratedImage `json:"data"`
	Usage   *Usage           `json:"usage"`
}

func (c *Client) Generate(ctx context.Context, key string, req GenerateRequest) (*GenerateResponse, error) {
	var out GenerateResponse
	if err := c.do(ctx, http.MethodPost, "/images", key, req, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("OpenRouter returned no images")
	}
	return &out, nil
}

// GenerateStream is Generate over SSE. onPartial receives each intermediate render (base64) as it arrives.
func (c *Client) GenerateStream(ctx context.Context, key string, req GenerateRequest, onPartial func(b64 string)) (*GenerateResponse, error) {
	req.Stream = true
	resp, err := c.send(ctx, http.MethodPost, "/images", key, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// A plain JSON body means the provider ignored stream:true.
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		var out GenerateResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		if len(out.Data) == 0 {
			return nil, fmt.Errorf("OpenRouter returned no images")
		}
		return &out, nil
	}

	type event struct {
		Type      string `json:"type"`
		B64JSON   string `json:"b64_json"`
		MediaType string `json:"media_type"`
		Created   int64  `json:"created"`
		Usage     *Usage `json:"usage"`
		Error     *struct {
			Message string          `json:"message"`
			Code    json.RawMessage `json:"code"`
		} `json:"error"`
	}
	// Lines carry whole base64 images, so no fixed-size scanner buffer.
	rd := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := rd.ReadString('\n')
		line = strings.TrimSpace(line)
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				break
			}
			var ev event
			if json.Unmarshal([]byte(data), &ev) == nil {
				switch ev.Type {
				case "image_generation.partial_image":
					if onPartial != nil && ev.B64JSON != "" {
						onPartial(ev.B64JSON)
					}
				case "image_generation.completed":
					if ev.B64JSON == "" {
						return nil, fmt.Errorf("OpenRouter completed the stream without an image")
					}
					return &GenerateResponse{
						Created: ev.Created,
						Data:    []GeneratedImage{{B64JSON: ev.B64JSON, MediaType: ev.MediaType}},
						Usage:   ev.Usage,
					}, nil
				case "error":
					msg := "generation failed mid-stream"
					if ev.Error != nil && ev.Error.Message != "" {
						msg = ev.Error.Message
					}
					return nil, &APIError{Status: http.StatusBadGateway, Message: msg}
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("stream ended before the final image: %w", err)
		}
	}
	return nil, fmt.Errorf("stream ended before the final image")
}
