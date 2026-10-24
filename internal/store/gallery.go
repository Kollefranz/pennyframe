package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const thumbSize = 512

var ErrNoThumb = errors.New("no thumbnail for this format")

// Meta is the sidecar record saved next to every image.
type Meta struct {
	ID        string         `json:"id"`
	File      string         `json:"file"`
	MediaType string         `json:"mediaType"`
	Prompt    string         `json:"prompt"`
	Model     string         `json:"model"`
	ModelName string         `json:"modelName"`
	KeyName   string         `json:"keyName"`
	Params    map[string]any `json:"params"`
	Refs      int            `json:"refs"`
	Width     int            `json:"width"`
	Height    int            `json:"height"`
	Bytes     int64          `json:"bytes"`
	Cost      *float64       `json:"cost,omitempty"`
	CostTotal *float64       `json:"costTotal,omitempty"` // billed total for the request this image came from
	CostCount int            `json:"costCount,omitempty"` // images in that request
	Seconds   float64        `json:"seconds"`
	Batch     string         `json:"batch"`
	Favorite  bool           `json:"favorite"`
	Archived  bool           `json:"archived,omitempty"`
	Folder    string         `json:"folder,omitempty"` // Folder.ID
	CreatedAt time.Time      `json:"createdAt"`
}

// Gallery is a directory of images with JSON sidecars, indexed in memory.
type Gallery struct {
	dir      string
	thumbDir string

	mu      sync.RWMutex
	items   map[string]*Meta
	folders []Folder

	thumbMu sync.Mutex
	thumbs  map[string]*sync.Mutex
}

func OpenGallery(dir, thumbDir string) (*Gallery, error) {
	g := &Gallery{dir: dir, thumbDir: thumbDir, items: map[string]*Meta{}, thumbs: map[string]*sync.Mutex{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(b, &m) != nil || m.ID == "" || m.File == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, m.File)); err != nil {
			continue
		}
		g.items[m.ID] = &m
	}
	g.loadFolders()
	return g, nil
}

func (g *Gallery) Dir() string { return g.dir }

// List returns all items, newest first.
func (g *Gallery) List() []Meta {
	g.mu.RLock()
	out := make([]Meta, 0, len(g.items))
	for _, m := range g.items {
		out = append(out, *m)
	}
	g.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (g *Gallery) Get(id string) (Meta, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	m, ok := g.items[id]
	if !ok {
		return Meta{}, false
	}
	return *m, true
}

// Path returns the image file path for id.
func (g *Gallery) Path(id string) (string, bool) {
	m, ok := g.Get(id)
	if !ok {
		return "", false
	}
	return filepath.Join(g.dir, m.File), true
}

// NewID returns a sortable, collision-resistant id.
func NewID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Add stores the image bytes and metadata. m.ID, m.MediaType and m.CreatedAt are filled in if empty.
func (g *Gallery) Add(m Meta, data []byte) (Meta, error) {
	if m.ID == "" {
		m.ID = NewID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	if m.MediaType == "" {
		m.MediaType = SniffMediaType(data)
	}
	m.File = m.ID + ExtFor(m.MediaType)
	m.Bytes = int64(len(data))
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		m.Width, m.Height = cfg.Width, cfg.Height
	}
	if err := writeFileAtomic(filepath.Join(g.dir, m.File), data); err != nil {
		return Meta{}, err
	}
	if err := g.writeMeta(&m); err != nil {
		_ = os.Remove(filepath.Join(g.dir, m.File))
		return Meta{}, err
	}
	g.mu.Lock()
	g.items[m.ID] = &m
	g.mu.Unlock()
	return m, nil
}

func (g *Gallery) writeMeta(m *Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(g.dir, m.ID+".json"), b)
}

// Update applies fn to the stored metadata and persists it, rolling back if the write fails.
func (g *Gallery) Update(id string, fn func(*Meta)) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.items[id]
	if !ok {
		return errors.New("no such image")
	}
	prev := *m
	fn(m)
	if err := g.writeMeta(m); err != nil {
		*m = prev
		return err
	}
	return nil
}

func (g *Gallery) SetFavorite(id string, fav bool) error {
	return g.Update(id, func(m *Meta) { m.Favorite = fav })
}

// SetArchived hides an image from the main view without deleting anything.
func (g *Gallery) SetArchived(id string, archived bool) error {
	return g.Update(id, func(m *Meta) { m.Archived = archived })
}

func (g *Gallery) Delete(id string) error {
	g.mu.Lock()
	m, ok := g.items[id]
	if !ok {
		g.mu.Unlock()
		return errors.New("no such image")
	}
	delete(g.items, id)
	g.mu.Unlock()
	_ = os.Remove(filepath.Join(g.dir, m.File))
	_ = os.Remove(filepath.Join(g.dir, m.ID+".json"))
	_ = os.Remove(g.thumbPath(id, false))
	_ = os.Remove(g.thumbPath(id, true))
	return nil
}

func (g *Gallery) thumbPath(id string, png bool) string {
	ext := ".jpg"
	if png {
		ext = ".png"
	}
	return filepath.Join(g.thumbDir, id+ext)
}

// Thumb returns the path of a cached thumbnail, generating it on first use.
func (g *Gallery) Thumb(id string) (string, error) {
	for _, isPNG := range []bool{false, true} {
		if p := g.thumbPath(id, isPNG); fileExists(p) {
			return p, nil
		}
	}
	src, ok := g.Path(id)
	if !ok {
		return "", errors.New("no such image")
	}
	g.thumbMu.Lock()
	lock := g.thumbs[id]
	if lock == nil {
		lock = &sync.Mutex{}
		g.thumbs[id] = lock
	}
	g.thumbMu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	// Another goroutine may have finished while we waited.
	for _, isPNG := range []bool{false, true} {
		if p := g.thumbPath(id, isPNG); fileExists(p) {
			return p, nil
		}
	}
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return "", ErrNoThumb
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > thumbSize || h > thumbSize {
		if w >= h {
			h, w = h*thumbSize/w, thumbSize
		} else {
			w, h = w*thumbSize/h, thumbSize
		}
		w, h = max(w, 1), max(h, 1)
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		img = dst
	}
	var buf bytes.Buffer
	isPNG := !isOpaque(img)
	if isPNG {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 84})
	}
	if err != nil {
		return "", err
	}
	p := g.thumbPath(id, isPNG)
	return p, writeFileAtomic(p, buf.Bytes())
}

func isOpaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	return true
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// SniffMediaType detects the image type from content, treating SVG specially.
func SniffMediaType(data []byte) string {
	head := data[:min(len(data), 512)]
	if bytes.Contains(bytes.ToLower(head), []byte("<svg")) {
		return "image/svg+xml"
	}
	t := strings.SplitN(http.DetectContentType(head), ";", 2)[0]
	if strings.HasPrefix(t, "image/") {
		return t
	}
	return "image/png"
}

func ExtFor(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/gif":
		return ".gif"
	}
	return ".png"
}
