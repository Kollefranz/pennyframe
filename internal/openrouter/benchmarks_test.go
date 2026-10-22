package openrouter

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestImageArenaParsesFiltersAndNormalizes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/benchmarks" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("bad request %s auth=%q", r.URL.String(), r.Header.Get("Authorization"))
		}
		q := r.URL.Query()
		if q.Get("source") != "design-arena" || q.Get("category") != "image" {
			t.Errorf("query = %v", q)
		}
		_, _ = w.Write([]byte(`{"data":[
		 {"source":"design-arena","model_permaslug":"qwen/qwen-image-2.1-turbo-20261016","display_name":"Qwen Turbo","category":"image","elo":1180.5,"win_rate":55,"avg_generation_time_ms":4200,"tournament_stats":{"total":120}},
		 {"source":"design-arena","model_permaslug":"qwen/qwen-image-2.1-turbo","display_name":"Qwen Turbo","category":"image","elo":1100,"win_rate":40,"avg_generation_time_ms":null,"tournament_stats":{"total":30}},
		 {"source":"design-arena","model_permaslug":"openai/gpt-image-2","display_name":"GPT Image 2","category":"image","elo":1300,"win_rate":70,"tournament_stats":{"total":500}},
		 {"source":"design-arena","model_permaslug":"x/svg-model","display_name":"SVG","category":"svg","elo":1500,"win_rate":90,"tournament_stats":{"total":10}},
		 {"source":"artificial-analysis","model_permaslug":"openai/gpt-4o","display_name":"4o","intelligence_index":70},
		 {"source":"design-arena","model_permaslug":"no/elo","display_name":"No Elo","category":"image","win_rate":10,"tournament_stats":{"total":1}}
		],"meta":{"as_of":"2026-10-01T00:00:00Z","citation":"Source: Design Arena via OpenRouter.","source_url":"https://designarena.ai"}}`))
	})
	scores, meta, err := c.ImageArena(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 {
		t.Fatalf("want 2 scores (image category with an Elo), got %+v", scores)
	}
	if scores[0].ModelID != "openai/gpt-image-2" || scores[0].Elo != 1300 {
		t.Fatalf("not sorted by Elo: %+v", scores)
	}
	q := scores[1]
	if q.ModelID != "qwen/qwen-image-2.1-turbo" || q.Games != 120 || q.Elo != 1180.5 || q.AvgMs != 4200 {
		t.Fatalf("duplicate rows should collapse to the best-evidenced one: %+v", q)
	}
	if !strings.Contains(meta.Citation, "Design Arena") || meta.AsOf == "" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestImageArenaUnauthorized(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad","code":401}}`))
	})
	if _, _, err := c.ImageArena(context.Background(), "k"); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeSlug(t *testing.T) {
	cases := map[string]string{
		"vendor/model-20261016": "vendor/model",
		"vendor/model-2.1":      "vendor/model-2.1",
		"vendor/model":          "vendor/model",
		"vendor/model-v2-1234":  "vendor/model-v2-1234",
	}
	for in, want := range cases {
		if got := NormalizeSlug(in); got != want {
			t.Errorf("%s: want %s got %s", in, want, got)
		}
	}
}
