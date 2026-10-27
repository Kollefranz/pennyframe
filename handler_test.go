package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pennyframe/internal/store"
)

func testPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 900, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 900; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestImageHandlerServesEncryptedImagesOnlyWhileUnlocked(t *testing.T) {
	g, err := store.OpenGallery(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{gal: g}
	h := imageHandler(a)

	data := testPNG()
	m, _ := g.Add(store.Meta{Prompt: "p"}, data)
	f, err := g.CreateFolder("Private", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	plain := get(h, "/img/"+m.ID)
	if plain.Code != 200 || !bytes.Equal(plain.Body.Bytes(), data) {
		t.Fatalf("plain image: %d", plain.Code)
	}
	if err := g.SetFolder(m.ID, f.ID); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/img/" + m.ID, "/thumb/" + m.ID + "?s=320", "/img/" + m.ID + ".png"} {
		rec := get(h, path)
		if rec.Code != 200 {
			t.Fatalf("%s unlocked: %d", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("%s Cache-Control = %q", path, cc)
		}
	}
	if img := get(h, "/img/"+m.ID); !bytes.Equal(img.Body.Bytes(), data) {
		t.Fatal("the decrypted image differs from the original")
	}
	if th := get(h, "/thumb/"+m.ID+"?s=320"); !strings.HasPrefix(th.Header().Get("Content-Type"), "image/") || th.Body.Len() == 0 {
		t.Fatalf("thumb: %q %d bytes", th.Header().Get("Content-Type"), th.Body.Len())
	}

	g.LockFolder(f.ID)
	for _, path := range []string{"/img/" + m.ID, "/thumb/" + m.ID + "?s=320", "/thumb/" + m.ID} {
		rec := get(h, path)
		if rec.Code != http.StatusForbidden || rec.Body.Len() > 20 {
			t.Fatalf("%s while locked: %d %q", path, rec.Code, rec.Body.String())
		}
	}
	if rec := get(h, "/img/nope"); rec.Code != 404 {
		t.Fatalf("unknown id: %d", rec.Code)
	}
}

func TestPlainImagesAreNeverCacheable(t *testing.T) {
	g, _ := store.OpenGallery(t.TempDir(), t.TempDir())
	h := imageHandler(&App{gal: g})
	m, _ := g.Add(store.Meta{}, testPNG())
	for _, path := range []string{"/img/" + m.ID, "/thumb/" + m.ID + "?s=480"} {
		if cc := get(h, path).Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s Cache-Control = %q", path, cc)
		}
	}
}

func TestReferencesFromLockedFoldersAreRefusedUntilUnlocked(t *testing.T) {
	g, _ := store.OpenGallery(t.TempDir(), t.TempDir())
	a := &App{gal: g}
	m, _ := g.Add(store.Meta{}, testPNG())
	f, _ := g.CreateFolder("Private", "correct horse battery")
	_ = g.SetFolder(m.ID, f.ID)

	if _, err := a.resolveRefs([]string{"gallery:" + m.ID}); err != nil {
		t.Fatalf("unlocked reference: %v", err)
	}
	g.LockFolder(f.ID)
	if _, err := a.resolveRefs([]string{"gallery:" + m.ID}); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("locked reference: %v", err)
	}
	if err := a.RevealImage(m.ID); err == nil {
		t.Fatal("encrypted images must not be revealed in the file explorer")
	}
}
