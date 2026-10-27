package store

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// checkerPNG is a 1px black/white checkerboard: the worst case for aliasing when shrunk.
func checkerPNG(n int) []byte {
	img := image.NewGray(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if (x+y)%2 == 0 {
				img.SetGray(x, y, color.Gray{255})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func decodeFile(t *testing.T, p string) image.Image {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestThumbDownscaleAveragesInsteadOfAliasing(t *testing.T) {
	g, _ := OpenGallery(t.TempDir(), t.TempDir())
	m, err := g.Add(Meta{}, checkerPNG(1600))
	if err != nil {
		t.Fatal(err)
	}
	tp, err := g.Thumb(m.ID, ThumbSizes[0])
	if err != nil {
		t.Fatal(err)
	}
	img := decodeFile(t, tp)
	b := img.Bounds()
	if b.Dx() != ThumbSizes[0] || b.Dy() != ThumbSizes[0] {
		t.Fatalf("thumb is %v", b)
	}
	// A fine checkerboard averages to mid grey. Point sampling or a 2x2 filter leaves black or white pixels.
	for y := 8; y < b.Dy()-8; y += 7 {
		for x := 8; x < b.Dx()-8; x += 7 {
			r, _, _, _ := img.At(x, y).RGBA()
			if v := r >> 8; v < 110 || v > 146 {
				t.Fatalf("pixel (%d,%d) = %d, want about 128: downscale is aliasing", x, y, v)
			}
		}
	}
}

func TestThumbSizeTiers(t *testing.T) {
	for want, size := range map[int]int{0: 320, 1: 320, 320: 320, 321: 480, 700: 720, 1080: 1080, 1081: 1620, 99999: 1620} {
		if got := thumbTier(want); got != size {
			t.Errorf("thumbTier(%d) = %d, want %d", want, got, size)
		}
	}

	g, _ := OpenGallery(t.TempDir(), t.TempDir())
	m, _ := g.Add(Meta{}, pngBytes(2000, 1000, 255))
	small, _ := g.Thumb(m.ID, 100)
	big, _ := g.Thumb(m.ID, 1000)
	if small == big {
		t.Fatal("different sizes must be cached separately")
	}
	if w := decodeFile(t, small).Bounds().Dx(); w != 320 {
		t.Errorf("small thumb width %d", w)
	}
	if w := decodeFile(t, big).Bounds().Dx(); w != 1080 {
		t.Errorf("big thumb width %d", w)
	}
}

func TestThumbNeverUpscales(t *testing.T) {
	g, _ := OpenGallery(t.TempDir(), t.TempDir())
	m, _ := g.Add(Meta{}, pngBytes(200, 100, 255))
	tp, _ := g.Thumb(m.ID, 1620)
	if b := decodeFile(t, tp).Bounds(); b.Dx() != 200 || b.Dy() != 100 {
		t.Fatalf("small image was resized to %v", b)
	}
}

func TestDeleteRemovesEverySize(t *testing.T) {
	dir, thumbs := t.TempDir(), t.TempDir()
	g, _ := OpenGallery(dir, thumbs)
	m, _ := g.Add(Meta{}, pngBytes(2000, 1000, 255))
	_, _ = g.Thumb(m.ID, 100)
	_, _ = g.Thumb(m.ID, 1000)
	if err := g.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(thumbs); len(left) != 0 {
		t.Fatalf("thumbnails left behind: %v", left)
	}
}

func TestLegacyThumbsAreSwept(t *testing.T) {
	thumbs := t.TempDir()
	legacy := filepath.Join(thumbs, "20261024-120000-abcdef.jpg")
	current := filepath.Join(thumbs, "20261024-120000-abcdef_320.jpg")
	for _, p := range []string{legacy, current} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := OpenGallery(t.TempDir(), thumbs); err != nil {
		t.Fatal(err)
	}
	if fileExists(legacy) || !fileExists(current) {
		t.Fatalf("legacy exists=%v current exists=%v", fileExists(legacy), fileExists(current))
	}
}
