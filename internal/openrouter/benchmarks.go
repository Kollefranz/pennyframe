package openrouter

import (
	"context"
	"net/http"
	"regexp"
	"sort"
)

// ArenaScore is one model's image-arena result (Design Arena, served through OpenRouter's /benchmarks).
type ArenaScore struct {
	ModelID string  `json:"modelId"` // OpenRouter model id, date suffix removed
	Name    string  `json:"name"`
	Elo     float64 `json:"elo"`
	WinRate float64 `json:"winRate"` // percent
	Games   int     `json:"games"`
	AvgMs   float64 `json:"avgMs,omitempty"`
}

// BenchMeta carries the attribution OpenRouter asks for when republishing this data.
type BenchMeta struct {
	AsOf      string `json:"asOf"`
	Citation  string `json:"citation"`
	SourceURL string `json:"sourceUrl"`
}

var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// NormalizeSlug turns a dated permaslug ("vendor/model-20261016") into the id used by the models list.
func NormalizeSlug(s string) string { return dateSuffix.ReplaceAllString(s, "") }

// ImageArena fetches Design Arena's image Elo ratings. Needs any valid API key; rate-limited by OpenRouter.
func (c *Client) ImageArena(ctx context.Context, key string) ([]ArenaScore, BenchMeta, error) {
	var out struct {
		Data []struct {
			Source   string   `json:"source"`
			Slug     string   `json:"model_permaslug"`
			Name     string   `json:"display_name"`
			Category string   `json:"category"`
			Elo      *float64 `json:"elo"`
			WinRate  *float64 `json:"win_rate"`
			AvgMs    *float64 `json:"avg_generation_time_ms"`
			Stats    struct {
				Total *int `json:"total"`
			} `json:"tournament_stats"`
		} `json:"data"`
		Meta struct {
			AsOf      string  `json:"as_of"`
			Citation  *string `json:"citation"`
			SourceURL *string `json:"source_url"`
		} `json:"meta"`
	}
	if err := c.do(ctx, http.MethodGet, "/benchmarks?source=design-arena&arena=models&category=image", key, nil, &out); err != nil {
		return nil, BenchMeta{}, err
	}

	best := map[string]ArenaScore{}
	for _, it := range out.Data {
		if it.Source != "design-arena" || it.Category != "image" || it.Elo == nil {
			continue
		}
		s := ArenaScore{ModelID: NormalizeSlug(it.Slug), Name: it.Name, Elo: *it.Elo}
		if it.WinRate != nil {
			s.WinRate = *it.WinRate
		}
		if it.AvgMs != nil {
			s.AvgMs = *it.AvgMs
		}
		if it.Stats.Total != nil {
			s.Games = *it.Stats.Total
		}
		// Several rows can map to one model; keep the best-evidenced one.
		if prev, ok := best[s.ModelID]; !ok || s.Games > prev.Games {
			best[s.ModelID] = s
		}
	}
	scores := make([]ArenaScore, 0, len(best))
	for _, s := range best {
		scores = append(scores, s)
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].Elo > scores[j].Elo })

	meta := BenchMeta{AsOf: out.Meta.AsOf}
	if out.Meta.Citation != nil {
		meta.Citation = *out.Meta.Citation
	}
	if out.Meta.SourceURL != nil {
		meta.SourceURL = *out.Meta.SourceURL
	}
	return scores, meta, nil
}
