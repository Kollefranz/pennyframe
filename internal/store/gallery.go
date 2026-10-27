package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

var ErrNoThumb = errors.New("no thumbnail for this format")

// Meta is the sidecar record saved next to every image. For an image in an encrypted folder only
// ID, File, Folder, Encrypted and CreatedAt are stored in the clear; the rest is sealed. While that
// folder is locked, Meta holds just those fields with Locked set.
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
	Encrypted bool           `json:"encrypted,omitempty"`
	Locked    bool           `json:"locked,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

// encSidecar is what is written to <id>.json for an encrypted image.
type encSidecar struct {
	ID        string    `json:"id"`
	File      string    `json:"file"`
	Folder    string    `json:"folder"`
	Encrypted bool      `json:"encrypted"`
	CreatedAt time.Time `json:"createdAt"`
	Blob      []byte    `json:"blob"`
}

// Gallery is a directory of images with JSON sidecars, indexed in memory.
type Gallery struct {
	dir      string
	thumbDir string

	mu      sync.RWMutex
	items   map[string]*Meta
	folders []folderRec
	keys    map[string][]byte // folder id -> data key, only while unlocked

	thumbMu sync.Mutex
	thumbs  map[string]*sync.Mutex
}

func OpenGallery(dir, thumbDir string) (*Gallery, error) {
	g := &Gallery{dir: dir, thumbDir: thumbDir, items: map[string]*Meta{}, keys: map[string][]byte{}, thumbs: map[string]*sync.Mutex{}}
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
		if m.Encrypted {
			m = Meta{ID: m.ID, File: m.File, Folder: m.Folder, Encrypted: true, Locked: true, CreatedAt: m.CreatedAt}
		}
		g.items[m.ID] = &m
	}
	g.loadFolders()
	g.sweepLegacyThumbs()
	g.sweepStale()
	return g, nil
}

func (g *Gallery) Dir() string { return g.dir }

// List returns all items, newest first. Items in locked folders are bare stubs.
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

// Path returns the file path of an unencrypted image. Encrypted images have no usable path: use ReadImage.
func (g *Gallery) Path(id string) (string, bool) {
	m, ok := g.Get(id)
	if !ok || m.Encrypted {
		return "", false
	}
	return filepath.Join(g.dir, m.File), true
}

// ReadImage returns the original image bytes, decrypting if needed (ErrLocked if the folder is locked).
func (g *Gallery) ReadImage(id string) ([]byte, error) {
	g.mu.RLock()
	m, ok := g.items[id]
	var file string
	var enc bool
	var key []byte
	if ok {
		file, enc, key = m.File, m.Encrypted, g.keys[m.Folder]
	}
	g.mu.RUnlock()
	if !ok {
		return nil, errors.New("no such image")
	}
	raw, err := os.ReadFile(filepath.Join(g.dir, file))
	if err != nil {
		return nil, err
	}
	if !enc {
		return raw, nil
	}
	if key == nil {
		return nil, ErrLocked
	}
	return open(key, raw, imgAAD(id))
}

// NewID returns a sortable, collision-resistant id.
func NewID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Add stores a new, unencrypted image. m.ID, m.MediaType and m.CreatedAt are filled in if empty.
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
	m.Encrypted, m.Locked = false, false
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		m.Width, m.Height = cfg.Width, cfg.Height
	}
	if err := writeFileAtomic(filepath.Join(g.dir, m.File), data); err != nil {
		return Meta{}, err
	}
	if err := g.writeMetaWith(&m, nil); err != nil {
		_ = os.Remove(filepath.Join(g.dir, m.File))
		return Meta{}, err
	}
	g.mu.Lock()
	g.items[m.ID] = &m
	g.mu.Unlock()
	return m, nil
}

// writeMetaWith persists m; an encrypted image's record is sealed with key (ErrLocked if nil).
func (g *Gallery) writeMetaWith(m *Meta, key []byte) error {
	var b []byte
	var err error
	if !m.Encrypted {
		b, err = json.MarshalIndent(m, "", "  ")
	} else {
		if key == nil {
			return ErrLocked
		}
		var full []byte
		if full, err = json.Marshal(m); err == nil {
			var blob []byte
			if blob, err = seal(key, full, metaAAD(m.ID)); err == nil {
				b, err = json.Marshal(encSidecar{ID: m.ID, File: m.File, Folder: m.Folder, Encrypted: true, CreatedAt: m.CreatedAt, Blob: blob})
			}
		}
	}
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(g.dir, m.ID+".json"), b)
}

// unlockItem fills in a stub from its sealed sidecar; callers hold g.mu.
func (g *Gallery) unlockItem(m *Meta, key []byte) {
	raw, err := os.ReadFile(filepath.Join(g.dir, m.ID+".json"))
	if err != nil {
		return
	}
	var sc encSidecar
	if json.Unmarshal(raw, &sc) != nil {
		return
	}
	plain, err := open(key, sc.Blob, metaAAD(m.ID))
	if err != nil {
		return
	}
	var full Meta
	if json.Unmarshal(plain, &full) != nil {
		return
	}
	full.ID, full.File, full.Folder, full.CreatedAt = m.ID, m.File, m.Folder, m.CreatedAt
	full.Encrypted, full.Locked = true, false
	*m = full
}

// lockItem forgets everything but the stub; callers hold g.mu.
func lockItem(m *Meta) {
	*m = Meta{ID: m.ID, File: m.File, Folder: m.Folder, Encrypted: true, Locked: true, CreatedAt: m.CreatedAt}
}

// Update applies fn to the stored metadata and persists it, rolling back if the write fails.
func (g *Gallery) Update(id string, fn func(*Meta)) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.items[id]
	if !ok {
		return errors.New("no such image")
	}
	if m.Locked {
		return ErrLocked
	}
	prev := *m
	fn(m)
	if err := g.writeMetaWith(m, g.keys[m.Folder]); err != nil {
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

// Delete removes an image and its thumbnails. It works on locked images too: deleting needs no key.
func (g *Gallery) Delete(id string) error {
	g.mu.Lock()
	m, ok := g.items[id]
	if !ok {
		g.mu.Unlock()
		return errors.New("no such image")
	}
	delete(g.items, id)
	file, enc := m.File, m.Encrypted
	g.mu.Unlock()
	// Unencrypted files are overwritten first (best effort) so a deleted image isn't left readable on disk.
	if enc {
		_ = os.Remove(filepath.Join(g.dir, file))
	} else {
		wipeFile(filepath.Join(g.dir, file))
	}
	wipeFile(filepath.Join(g.dir, id+".json"))
	g.removeThumbs(id, !enc)
	return nil
}

// sweepStale removes leftovers of interrupted moves: a second copy of a known image that its record
// doesn't point at (possibly still readable), and unsealed thumbnails of encrypted images. It only
// touches files named after an image id in this gallery, never anything else in the folder.
func (g *Gallery) sweepStale() {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		i := strings.IndexByte(name, '.')
		if e.IsDir() || i <= 0 || strings.HasSuffix(name, ".json") {
			continue
		}
		if m, ok := g.items[name[:i]]; ok && m.File != name {
			wipeFile(filepath.Join(g.dir, name))
		}
	}
	for id, m := range g.items {
		if !m.Encrypted {
			continue
		}
		thumbs, _ := filepath.Glob(filepath.Join(g.thumbDir, id+"_*"))
		for _, p := range thumbs {
			if !strings.HasSuffix(p, ".enc") {
				wipeFile(p)
			}
		}
	}
}

// convertTo re-stores one image for a new home: the folder it will be in and whether that folder
// encrypts (with key). The new files are written before the record switches over and the old ones
// are removed last, so a failure part-way never loses the image. Old plaintext is wiped, best effort.
func (g *Gallery) convertTo(id, folderID string, encrypt bool, key []byte) error {
	g.mu.RLock()
	cur, ok := g.items[id]
	var m Meta
	if ok {
		m = *cur
	}
	g.mu.RUnlock()
	if !ok {
		return errors.New("no such image")
	}
	if m.Locked {
		return ErrLocked
	}
	if encrypt && key == nil {
		return ErrLocked
	}
	plain, err := g.ReadImage(id)
	if err != nil {
		return err
	}
	if m.MediaType == "" {
		m.MediaType = SniffMediaType(plain)
	}

	old, wasEncrypted := m.File, m.Encrypted
	m.Folder, m.Encrypted = folderID, encrypt
	data := plain
	if encrypt {
		m.File = id + "." + randHex(4) + ".enc"
		if data, err = seal(key, plain, imgAAD(id)); err != nil {
			return err
		}
	} else {
		m.File = id + ExtFor(m.MediaType)
	}
	newPath := filepath.Join(g.dir, m.File)
	if err := writeFileAtomic(newPath, data); err != nil {
		return err
	}
	var recKey []byte
	if encrypt {
		recKey = key
	}
	g.mu.Lock()
	if err := g.writeMetaWith(&m, recKey); err != nil {
		g.mu.Unlock()
		_ = os.Remove(newPath)
		return err
	}
	*cur = m
	g.mu.Unlock()

	if old != m.File {
		if wasEncrypted {
			_ = os.Remove(filepath.Join(g.dir, old))
		} else {
			wipeFile(filepath.Join(g.dir, old))
		}
	}
	g.removeThumbs(id, !wasEncrypted)
	return nil
}

// ---- thumbnails

// ThumbSizes are the long-edge sizes thumbnails are cached at. Thumb serves the smallest one that is
// big enough, so the browser never has to stretch a thumbnail or shrink it by a large factor.
var ThumbSizes = []int{320, 480, 720, 1080, 1620}

func thumbTier(want int) int {
	for _, s := range ThumbSizes {
		if s >= want {
			return s
		}
	}
	return ThumbSizes[len(ThumbSizes)-1]
}

func (g *Gallery) thumbPath(id string, size int, png bool) string {
	ext := ".jpg"
	if png {
		ext = ".png"
	}
	return filepath.Join(g.thumbDir, fmt.Sprintf("%s_%d%s", id, size, ext))
}

// encThumbPath is where the sealed thumbnail of an encrypted image lives; its type isn't in the name.
func (g *Gallery) encThumbPath(id string, size int) string {
	return filepath.Join(g.thumbDir, fmt.Sprintf("%s_%d.enc", id, size))
}

// removeThumbs deletes every cached size of one image's thumbnail, wiping them first if asked.
func (g *Gallery) removeThumbs(id string, wipe bool) {
	old, _ := filepath.Glob(filepath.Join(g.thumbDir, id+"_*"))
	for _, p := range old {
		if wipe {
			wipeFile(p)
		} else {
			_ = os.Remove(p)
		}
	}
}

// sweepLegacyThumbs drops thumbnails from before sizes were part of the name: they were made with a
// low-quality filter and are only a cache.
func (g *Gallery) sweepLegacyThumbs() {
	entries, err := os.ReadDir(g.thumbDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && !strings.Contains(e.Name(), "_") && (strings.HasSuffix(e.Name(), ".jpg") || strings.HasSuffix(e.Name(), ".png")) {
			_ = os.Remove(filepath.Join(g.thumbDir, e.Name()))
		}
	}
}

// renderThumb scales src down to at most size on the long edge (never up) and encodes it.
func renderThumb(src []byte, size int) (data []byte, isPNG bool, err error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, false, ErrNoThumb
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > size || h > size {
		if w >= h {
			h, w = h*size/w, size
		} else {
			w, h = w*size/h, size
		}
		w, h = max(w, 1), max(h, 1)
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		// Catmull-Rom widens its kernel when shrinking, so it averages every source pixel it covers
		// (the bilinear approximation only looks at four and aliases on large reductions).
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	isPNG = !isOpaque(img)
	if isPNG {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	}
	return buf.Bytes(), isPNG, err
}

func (g *Gallery) thumbLock(key string) *sync.Mutex {
	g.thumbMu.Lock()
	defer g.thumbMu.Unlock()
	lock := g.thumbs[key]
	if lock == nil {
		lock = &sync.Mutex{}
		g.thumbs[key] = lock
	}
	return lock
}

// Thumb returns the path of a cached thumbnail of an unencrypted image whose long edge is at least
// `want` pixels (when the original is that large), generating it on first use. Images already
// smaller are only re-encoded.
func (g *Gallery) Thumb(id string, want int) (string, error) {
	size := thumbTier(want)
	for _, isPNG := range []bool{false, true} {
		if p := g.thumbPath(id, size, isPNG); fileExists(p) {
			return p, nil
		}
	}
	src, ok := g.Path(id)
	if !ok {
		return "", errors.New("no such image")
	}
	lock := g.thumbLock(fmt.Sprintf("%s_%d", id, size))
	lock.Lock()
	defer lock.Unlock()

	// Another goroutine may have finished while we waited.
	for _, isPNG := range []bool{false, true} {
		if p := g.thumbPath(id, size, isPNG); fileExists(p) {
			return p, nil
		}
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	data, isPNG, err := renderThumb(raw, size)
	if err != nil {
		return "", err
	}
	p := g.thumbPath(id, size, isPNG)
	return p, writeFileAtomic(p, data)
}

// ThumbBytes is Thumb for an encrypted image: the thumbnail is cached sealed on disk and returned
// decrypted in memory. ErrLocked if the folder is locked, ErrNoThumb for formats that can't be scaled.
func (g *Gallery) ThumbBytes(id string, want int) ([]byte, error) {
	size := thumbTier(want)
	g.mu.RLock()
	m, ok := g.items[id]
	var key []byte
	var enc bool
	if ok {
		key, enc = g.keys[m.Folder], m.Encrypted
	}
	g.mu.RUnlock()
	switch {
	case !ok:
		return nil, errors.New("no such image")
	case !enc:
		return nil, errors.New("image is not encrypted")
	case key == nil:
		return nil, ErrLocked
	}
	p := g.encThumbPath(id, size)
	read := func() ([]byte, bool) {
		sealed, err := os.ReadFile(p)
		if err != nil {
			return nil, false
		}
		data, err := open(key, sealed, thumbAAD(id, size))
		return data, err == nil
	}
	if data, ok := read(); ok {
		return data, nil
	}
	lock := g.thumbLock(fmt.Sprintf("%s_%d.enc", id, size))
	lock.Lock()
	defer lock.Unlock()
	if data, ok := read(); ok {
		return data, nil
	}
	src, err := g.ReadImage(id)
	if err != nil {
		return nil, err
	}
	data, _, err := renderThumb(src, size)
	if err != nil {
		return nil, err
	}
	sealed, err := seal(key, data, thumbAAD(id, size))
	if err != nil {
		return nil, err
	}
	return data, writeFileAtomic(p, sealed)
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
