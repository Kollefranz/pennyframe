package store

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

type memSecrets map[string]string

func (m memSecrets) Get(id string) (string, error) {
	if v, ok := m[id]; ok {
		return v, nil
	}
	return "", errors.New("missing")
}
func (m memSecrets) Set(id, s string) error { m[id] = s; return nil }
func (m memSecrets) Delete(id string) error { delete(m, id); return nil }

func TestKeysLifecycle(t *testing.T) {
	sec := memSecrets{}
	cfg, err := LoadConfig(t.TempDir(), sec)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.AddKey("Personal", "sk-or-v1-aaaaaaaaaaaaaaaa1234")
	b, _ := cfg.AddKey("Work", "sk-or-v1-bbbbbbbbbbbbbbbb5678")
	if cfg.ActiveKeyID() != b.ID || a.Hint != "sk-or-v1…1234" {
		t.Fatalf("active=%s hint=%s", cfg.ActiveKeyID(), a.Hint)
	}
	if err := cfg.SetActiveKey(a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := cfg.Secret(a.ID); got != "sk-or-v1-aaaaaaaaaaaaaaaa1234" {
		t.Fatalf("secret = %q", got)
	}
	// Removing the active key falls back to another one and wipes the secret.
	if err := cfg.RemoveKey(a.ID); err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveKeyID() != b.ID || len(sec) != 1 {
		t.Fatalf("active=%s secrets=%d", cfg.ActiveKeyID(), len(sec))
	}
	raw, _ := os.ReadFile(filepath.Join(cfg.Dir(), "config.json"))
	if bytes.Contains(raw, []byte("bbbbbbbbbbbbbbbb")) {
		t.Fatal("secret leaked into config.json")
	}
	again, _ := LoadConfig(cfg.Dir(), sec)
	if len(again.Keys()) != 1 || again.ActiveKeyID() != b.ID {
		t.Fatalf("reload lost state: %+v", again.Keys())
	}
}

func TestPrefsRejectInvalidJSON(t *testing.T) {
	cfg, _ := LoadConfig(t.TempDir(), memSecrets{})
	if cfg.Prefs() != "{}" {
		t.Fatal("default prefs should be {}")
	}
	if err := cfg.SetPrefs("{nope"); err == nil {
		t.Fatal("expected error")
	}
	if err := cfg.SetPrefs(`{"a":1}`); err != nil || cfg.Prefs() != `{"a":1}` {
		t.Fatalf("prefs roundtrip failed: %v %q", err, cfg.Prefs())
	}
}

func pngBytes(w, h int, a uint8) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{200, 100, 50, a})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestGalleryAddReloadThumbDelete(t *testing.T) {
	dir, thumbs := t.TempDir(), t.TempDir()
	g, err := OpenGallery(dir, thumbs)
	if err != nil {
		t.Fatal(err)
	}
	m, err := g.Add(Meta{Prompt: "coin", Model: "a/b", ModelName: "B"}, pngBytes(1200, 600, 255))
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 1200 || m.Height != 600 || m.MediaType != "image/png" || filepath.Ext(m.File) != ".png" {
		t.Fatalf("meta = %+v", m)
	}

	tp, err := g.Thumb(m.ID, 0)
	if err != nil || filepath.Ext(tp) != ".jpg" {
		t.Fatalf("thumb %q err=%v", tp, err)
	}
	f, _ := os.Open(tp)
	cfg, _, _ := image.DecodeConfig(f)
	f.Close()
	if cfg.Width != ThumbSizes[0] || cfg.Height != ThumbSizes[0]/2 {
		t.Fatalf("thumb dims %dx%d", cfg.Width, cfg.Height)
	}

	if err := g.SetFavorite(m.ID, true); err != nil {
		t.Fatal(err)
	}
	g2, _ := OpenGallery(dir, thumbs)
	got, ok := g2.Get(m.ID)
	if !ok || !got.Favorite || got.Prompt != "coin" {
		t.Fatalf("reload lost data: %+v", got)
	}

	if err := g2.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	thumbEntries, _ := os.ReadDir(thumbs)
	if len(entries) != 0 || len(thumbEntries) != 0 || len(g2.List()) != 0 {
		t.Fatalf("delete left files behind: %v %v", entries, thumbEntries)
	}
}

func TestTransparentThumbIsPNG(t *testing.T) {
	g, _ := OpenGallery(t.TempDir(), t.TempDir())
	m, _ := g.Add(Meta{}, pngBytes(40, 40, 100))
	tp, err := g.Thumb(m.ID, 0)
	if err != nil || filepath.Ext(tp) != ".png" {
		t.Fatalf("thumb %q err=%v", tp, err)
	}
}

func TestSVGHasNoThumb(t *testing.T) {
	g, _ := OpenGallery(t.TempDir(), t.TempDir())
	m, err := g.Add(Meta{}, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>`))
	if err != nil || m.MediaType != "image/svg+xml" || filepath.Ext(m.File) != ".svg" {
		t.Fatalf("meta=%+v err=%v", m, err)
	}
	if _, err := g.Thumb(m.ID, 0); !errors.Is(err, ErrNoThumb) {
		t.Fatalf("want ErrNoThumb, got %v", err)
	}
}

func TestArchivePersistsAndKeepsFiles(t *testing.T) {
	dir := t.TempDir()
	g, _ := OpenGallery(dir, t.TempDir())
	a, _ := g.Add(Meta{Prompt: "a"}, pngBytes(10, 10, 255))
	b, _ := g.Add(Meta{Prompt: "b"}, pngBytes(10, 10, 255))
	if err := g.SetArchived(a.ID, true); err != nil {
		t.Fatal(err)
	}
	g2, _ := OpenGallery(dir, t.TempDir())
	ga, _ := g2.Get(a.ID)
	gb, _ := g2.Get(b.ID)
	if !ga.Archived || gb.Archived {
		t.Fatalf("archived flag lost or leaked: a=%v b=%v", ga.Archived, gb.Archived)
	}
	if _, ok := g2.Path(a.ID); !ok || len(g2.List()) != 2 {
		t.Fatal("archived images must stay in the gallery data")
	}
	if err := g2.SetArchived(a.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := g2.Get(a.ID); got.Archived {
		t.Fatal("unarchive failed")
	}
	if err := g2.SetArchived("missing", true); err == nil {
		t.Fatal("expected error for unknown id")
	}
}
