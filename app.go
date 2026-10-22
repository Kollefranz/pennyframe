package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"pennyframe/internal/openrouter"
	"pennyframe/internal/store"
)

const (
	maxParallelJobs = 6
	jobTimeout      = 10 * time.Minute
	modelsTTL       = 6 * time.Hour
	priceTTL        = 24 * time.Hour
)

// App is bound to the frontend; every exported method is callable from JS.
type App struct {
	ctx  context.Context
	cfg  *store.Config
	or   *openrouter.Client
	emit func(event string, data any)

	galMu sync.RWMutex
	gal   *store.Gallery

	mu      sync.Mutex
	models  []openrouter.Model
	quality Quality
	fetched time.Time
	priced  time.Time
	jobs    map[string]context.CancelFunc
	preview map[string][]byte // latest partial render per running job

	connectCancel context.CancelFunc
	authURL       string
	openURL       func(string) error // opens a URL in the user's browser
	sem           chan struct{}
}

func NewApp(cfg *store.Config, gal *store.Gallery) *App {
	a := &App{
		cfg:     cfg,
		gal:     gal,
		or:      openrouter.New(),
		jobs:    map[string]context.CancelFunc{},
		preview: map[string][]byte{},
		sem:     make(chan struct{}, maxParallelJobs),
	}
	a.openURL = func(u string) error {
		if a.ctx == nil {
			return errors.New("no window to open a browser from")
		}
		wruntime.BrowserOpenURL(a.ctx, u)
		return nil
	}
	a.emit = func(event string, data any) {
		if a.ctx != nil {
			wruntime.EventsEmit(a.ctx, event, data)
		}
	}
	a.loadModelCache()
	a.loadQuality()
	return a
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) gallery() *store.Gallery {
	a.galMu.RLock()
	defer a.galMu.RUnlock()
	return a.gal
}

// ---------- boot ----------

type KeyView struct {
	store.Key
	Active bool `json:"active"`
}

type Boot struct {
	Keys      []KeyView          `json:"keys"`
	Models    []openrouter.Model `json:"models"`
	Images    []store.Meta       `json:"images"`
	Prefs     string             `json:"prefs"`
	OutputDir string             `json:"outputDir"`
	EnvKey    bool               `json:"envKey"`
	Quality   Quality            `json:"quality"`
}

// Bootstrap returns everything the UI needs for first paint, without touching the network.
func (a *App) Bootstrap() Boot {
	a.mu.Lock()
	models := append([]openrouter.Model(nil), a.models...)
	a.mu.Unlock()
	return Boot{
		Keys:      a.keyViews(),
		Models:    models,
		Images:    a.gallery().List(),
		Prefs:     a.cfg.Prefs(),
		OutputDir: a.cfg.OutputDir(),
		EnvKey:    os.Getenv("OPENROUTER_API_KEY") != "",
		Quality:   a.qualitySnapshot(),
	}
}

func (a *App) GetPrefs() string        { return a.cfg.Prefs() }
func (a *App) SetPrefs(s string) error { return a.cfg.SetPrefs(s) }

// ---------- keys ----------

func (a *App) keyViews() []KeyView {
	active := a.cfg.ActiveKeyID()
	keys := a.cfg.Keys()
	out := make([]KeyView, len(keys))
	for i, k := range keys {
		out[i] = KeyView{Key: k, Active: k.ID == active}
	}
	return out
}

type KeyStatus struct {
	Label     string   `json:"label"`
	Usage     float64  `json:"usage"`
	Limit     *float64 `json:"limit"`
	Remaining *float64 `json:"remaining"`
	FreeTier  bool     `json:"freeTier"`
	ExpiresAt *string  `json:"expiresAt"`
	Error     string   `json:"error,omitempty"`
}

// KeyStatus reports usage/credit for a stored key. Failures are reported in-band so the UI can show them inline.
func (a *App) KeyStatus(id string) KeyStatus {
	secret, err := a.cfg.Secret(id)
	if err != nil {
		return KeyStatus{Error: "key not found in credential store"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := a.or.KeyInfo(ctx, secret)
	if err != nil {
		return KeyStatus{Error: err.Error()}
	}
	return KeyStatus{Label: info.Label, Usage: info.Usage, Limit: info.Limit, Remaining: info.LimitRemaining, FreeTier: info.IsFreeTier, ExpiresAt: info.ExpiresAt}
}

// KeyDetails fetches everything OpenRouter reports about a stored key (expiry, limits, usage).
func (a *App) KeyDetails(id string) (*openrouter.KeyInfo, error) {
	secret, err := a.cfg.Secret(id)
	if err != nil {
		return nil, errors.New("key not found in credential store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return a.or.KeyInfo(ctx, secret)
}

// RevealKey returns the stored secret so the user can view or copy it.
func (a *App) RevealKey(id string) (string, error) {
	secret, err := a.cfg.Secret(id)
	if err != nil {
		return "", errors.New("key not found in credential store")
	}
	return secret, nil
}

type AddKeyResult struct {
	Keys    []KeyView `json:"keys"`
	Warning string    `json:"warning,omitempty"`
}

// AddKey validates and stores a key, making it active. A network failure only warns; a 401 rejects.
func (a *App) AddKey(name, secret string) (AddKeyResult, error) {
	secret = strings.TrimSpace(secret)
	name = strings.TrimSpace(name)
	if secret == "" {
		return AddKeyResult{}, errors.New("paste an API key first")
	}
	var warning string
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := a.or.KeyInfo(ctx, secret)
	var apiErr *openrouter.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == 401:
		return AddKeyResult{}, errors.New("OpenRouter rejected that key — check it and try again")
	case err != nil:
		warning = "Couldn't verify the key (" + err.Error() + "); saved anyway."
	}
	if name == "" {
		name = a.defaultKeyName(info)
	}
	if _, err := a.cfg.AddKey(name, secret); err != nil {
		return AddKeyResult{}, fmt.Errorf("saving key: %w", err)
	}
	return AddKeyResult{Keys: a.keyViews(), Warning: warning}, nil
}

func (a *App) defaultKeyName(info *openrouter.KeyInfo) string {
	if info != nil && info.Label != "" {
		return info.Label
	}
	return fmt.Sprintf("Key %d", len(a.cfg.Keys())+1)
}

// ConnectOpenRouter signs the user in through OpenRouter's OAuth flow in their browser and stores the key it creates.
// It blocks until the user finishes, cancels, or 10 minutes pass.
func (a *App) ConnectOpenRouter() (AddKeyResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	a.mu.Lock()
	if a.connectCancel != nil {
		a.connectCancel() // a newer attempt replaces an older one
	}
	a.connectCancel = cancel
	a.mu.Unlock()
	defer cancel()

	secret, err := a.or.Authorize(ctx, openrouter.AuthOptions{
		Label: "Pennyframe",
		Open: func(u string) error {
			a.mu.Lock()
			a.authURL = u
			a.mu.Unlock()
			return a.openURL(u)
		},
	})
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, openrouter.ErrAuthDenied):
		return AddKeyResult{}, errors.New("cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return AddKeyResult{}, errors.New("That took too long — click Connect to try again.")
	case err != nil:
		return AddKeyResult{}, err
	}
	return a.AddKey(a.uniqueKeyName("OpenRouter"), secret)
}

// CancelConnect abandons a pending ConnectOpenRouter.
func (a *App) CancelConnect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connectCancel != nil {
		a.connectCancel()
	}
}

// ReopenConnect shows the OpenRouter sign-in page again (e.g. the user closed the tab).
func (a *App) ReopenConnect() error {
	a.mu.Lock()
	u := a.authURL
	a.mu.Unlock()
	if u == "" {
		return errors.New("no connection in progress")
	}
	return a.openURL(u)
}

func (a *App) uniqueKeyName(base string) string {
	taken := map[string]bool{}
	for _, k := range a.cfg.Keys() {
		taken[k.Name] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s %d", base, i)
	}
	return name
}

func (a *App) AddKeyFromEnv() (AddKeyResult, error) {
	v := os.Getenv("OPENROUTER_API_KEY")
	if v == "" {
		return AddKeyResult{}, errors.New("OPENROUTER_API_KEY isn't set")
	}
	return a.AddKey("Environment", v)
}

func (a *App) SetActiveKey(id string) ([]KeyView, error) {
	if err := a.cfg.SetActiveKey(id); err != nil {
		return nil, err
	}
	return a.keyViews(), nil
}

func (a *App) RenameKey(id, name string) ([]KeyView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name can't be empty")
	}
	if err := a.cfg.RenameKey(id, name); err != nil {
		return nil, err
	}
	return a.keyViews(), nil
}

func (a *App) RemoveKey(id string) ([]KeyView, error) {
	if err := a.cfg.RemoveKey(id); err != nil {
		return nil, err
	}
	return a.keyViews(), nil
}

// ---------- models ----------

// priceFormat is bumped whenever PriceSummary's rules change, so stale cached labels are dropped.
const priceFormat = 3

type modelCache struct {
	Format  int                `json:"format"`
	Fetched time.Time          `json:"fetched"`
	Priced  time.Time          `json:"priced"`
	Models  []openrouter.Model `json:"models"`
}

func (a *App) modelCachePath() string { return filepath.Join(a.cfg.Dir(), "models.json") }

func (a *App) loadModelCache() {
	b, err := os.ReadFile(a.modelCachePath())
	if err != nil {
		return
	}
	var c modelCache
	if json.Unmarshal(b, &c) != nil {
		return
	}
	a.models, a.fetched, a.priced = c.Models, c.Fetched, c.Priced
	if c.Format != priceFormat {
		for i := range a.models {
			a.models[i].Price, a.models[i].PriceMin, a.models[i].PriceMax = "", 0, 0
		}
		a.priced = time.Time{}
	}
}

func (a *App) saveModelCacheLocked() {
	b, err := json.Marshal(modelCache{Format: priceFormat, Fetched: a.fetched, Priced: a.priced, Models: a.models})
	if err == nil {
		_ = os.WriteFile(a.modelCachePath(), b, 0o644)
	}
}

// RefreshModels updates the image-model list (skipped if fresh unless force) and kicks off price lookups.
func (a *App) RefreshModels(force bool) ([]openrouter.Model, error) {
	a.mu.Lock()
	fresh := !force && len(a.models) > 0 && time.Since(a.fetched) < modelsTTL
	cached := append([]openrouter.Model(nil), a.models...)
	a.mu.Unlock()
	if fresh {
		go a.fetchPrices(false)
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := a.or.ImageModels(ctx)
	if err != nil {
		if len(cached) > 0 {
			return cached, nil // offline: stale list still works
		}
		return nil, fmt.Errorf("loading models: %w", err)
	}
	a.mu.Lock()
	old := map[string]openrouter.Model{}
	for _, m := range a.models {
		old[m.ID] = m
	}
	for i := range list {
		o := old[list[i].ID]
		list[i].Price, list[i].PriceMin, list[i].PriceMax = o.Price, o.PriceMin, o.PriceMax
	}
	a.models, a.fetched = list, time.Now()
	a.saveModelCacheLocked()
	out := append([]openrouter.Model(nil), list...)
	a.mu.Unlock()
	go a.fetchPrices(force)
	return out, nil
}

var priceRunning sync.Mutex

func (a *App) fetchPrices(all bool) {
	if !priceRunning.TryLock() {
		return
	}
	defer priceRunning.Unlock()

	a.mu.Lock()
	stale := time.Since(a.priced) > priceTTL
	var todo []string
	for _, m := range a.models {
		if all || stale || m.Price == "" {
			todo = append(todo, m.ID)
		}
	}
	a.mu.Unlock()

	var wg sync.WaitGroup
	ids := make(chan string)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				eps, err := a.or.Endpoints(ctx, id)
				cancel()
				if err != nil {
					continue
				}
				price := openrouter.PriceSummary(eps)
				lo, hi, _ := openrouter.PriceRange(eps)
				a.mu.Lock()
				for i := range a.models {
					if a.models[i].ID == id {
						a.models[i].Price, a.models[i].PriceMin, a.models[i].PriceMax = price, lo, hi
					}
				}
				a.mu.Unlock()
				a.emit("model:price", map[string]any{"id": id, "price": price, "min": lo, "max": hi})
			}
		}()
	}
	for _, id := range todo {
		ids <- id
	}
	close(ids)
	wg.Wait()

	a.mu.Lock()
	a.priced = time.Now()
	a.saveModelCacheLocked()
	a.mu.Unlock()
}

func (a *App) model(id string) (openrouter.Model, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.models {
		if m.ID == id {
			return m, true
		}
	}
	return openrouter.Model{}, false
}

// ---------- quality ratings ----------

const qualityTTL = 24 * time.Hour

// Quality holds crowd-sourced image-arena Elo ratings (Design Arena, via OpenRouter's benchmarks API).
type Quality struct {
	Scores  []openrouter.ArenaScore `json:"scores"`
	Meta    openrouter.BenchMeta    `json:"meta"`
	Fetched time.Time               `json:"fetched"`
}

func (a *App) qualityPath() string { return filepath.Join(a.cfg.Dir(), "quality.json") }

func (a *App) loadQuality() {
	if b, err := os.ReadFile(a.qualityPath()); err == nil {
		_ = json.Unmarshal(b, &a.quality)
	}
}

func (a *App) qualitySnapshot() Quality {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.quality
}

// RefreshQuality updates the arena ratings (at most daily unless forced). It never fails loudly: ratings are
// optional, so on any problem the cached copy (possibly empty) is returned.
func (a *App) RefreshQuality(force bool) Quality {
	cur := a.qualitySnapshot()
	if !force && len(cur.Scores) > 0 && time.Since(cur.Fetched) < qualityTTL {
		return cur
	}
	keyID := a.cfg.ActiveKeyID()
	if keyID == "" {
		return cur
	}
	secret, err := a.cfg.Secret(keyID)
	if err != nil {
		return cur
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	scores, meta, err := a.or.ImageArena(ctx, secret)
	if err != nil || len(scores) == 0 {
		return cur
	}
	q := Quality{Scores: scores, Meta: meta, Fetched: time.Now()}
	a.mu.Lock()
	a.quality = q
	a.mu.Unlock()
	if b, err := json.Marshal(q); err == nil {
		_ = os.WriteFile(a.qualityPath(), b, 0o644)
	}
	return q
}

// ---------- generation ----------

type GenerateInput struct {
	Prompt string   `json:"prompt"`
	Models []string `json:"models"`
	Params Params   `json:"params"`
	// Refs are data: URLs, http(s) URLs, or "gallery:<id>".
	Refs []string `json:"refs"`
}

type JobInfo struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	ModelName string `json:"modelName"`
}

type Batch struct {
	ID   string    `json:"id"`
	Jobs []JobInfo `json:"jobs"`
}

type JobUpdate struct {
	ID      string       `json:"id"`
	Batch   string       `json:"batch"`
	State   string       `json:"state"` // queued | running | done | error | cancelled
	Error   string       `json:"error,omitempty"`
	Notes   []string     `json:"notes,omitempty"`
	Images  []store.Meta `json:"images,omitempty"`
	Seconds float64      `json:"seconds,omitempty"`
}

// Generate starts one job per selected model, in parallel, and returns immediately.
func (a *App) Generate(in GenerateInput) (Batch, error) {
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" {
		return Batch{}, errors.New("write a prompt first")
	}
	if len(in.Models) == 0 {
		return Batch{}, errors.New("pick at least one model")
	}
	keyID := a.cfg.ActiveKeyID()
	if keyID == "" {
		return Batch{}, errors.New("add an OpenRouter API key first")
	}
	secret, err := a.cfg.Secret(keyID)
	if err != nil {
		return Batch{}, fmt.Errorf("reading key from credential store: %w", err)
	}
	keyName := keyID
	for _, k := range a.cfg.Keys() {
		if k.ID == keyID {
			keyName = k.Name
		}
	}
	refs, err := a.resolveRefs(in.Refs)
	if err != nil {
		return Batch{}, err
	}

	batch := Batch{ID: store.NewID()}
	type work struct {
		info  JobInfo
		model openrouter.Model
	}
	var todo []work
	seen := map[string]bool{}
	for _, id := range in.Models {
		if seen[id] {
			continue
		}
		seen[id] = true
		m, ok := a.model(id)
		if !ok {
			m = openrouter.Model{ID: id, Name: id} // model list may be stale; let the API decide
		}
		info := JobInfo{ID: store.NewID(), Model: id, ModelName: m.Name}
		batch.Jobs = append(batch.Jobs, info)
		todo = append(todo, work{info, m})
	}

	for _, w := range todo {
		ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
		a.mu.Lock()
		a.jobs[w.info.ID] = cancel
		a.mu.Unlock()
		go func() {
			defer cancel()
			defer func() {
				a.mu.Lock()
				delete(a.jobs, w.info.ID)
				a.mu.Unlock()
			}()
			a.runJob(ctx, batch.ID, w.info, w.model, secret, keyName, in, refs)
		}()
	}
	return batch, nil
}

func (a *App) CancelJobs(ids []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		if c, ok := a.jobs[id]; ok {
			c()
		}
	}
}

func (a *App) runJob(ctx context.Context, batch string, info JobInfo, m openrouter.Model, secret, keyName string, in GenerateInput, refs []string) {
	upd := func(u JobUpdate) {
		u.ID, u.Batch = info.ID, batch
		a.emit("job", u)
	}
	upd(JobUpdate{State: "queued"})
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	case <-ctx.Done():
		upd(JobUpdate{State: "cancelled"})
		return
	}

	req, sent, notes := buildRequest(m, in.Prompt, in.Params, refs)
	upd(JobUpdate{State: "running", Notes: notes})

	start := time.Now()
	var resp *openrouter.GenerateResponse
	var err error
	if req.Stream {
		defer a.clearPreview(info.ID)
		n := 0
		resp, err = a.or.GenerateStream(ctx, secret, req, func(b64 string) {
			data, derr := base64.StdEncoding.DecodeString(b64)
			if derr != nil {
				return
			}
			n++
			a.setPreview(info.ID, data)
			a.emit("job:preview", map[string]any{"id": info.ID, "n": n})
		})
		var ae *openrouter.APIError
		if errors.As(err, &ae) && (ae.Status == 400 || ae.Status == 422) && ctx.Err() == nil {
			// Rejected up front (so not billed): retry as a normal request.
			req.Stream = false
			notes = append(notes, "Provider rejected streaming — used a normal request")
			upd(JobUpdate{State: "running", Notes: notes})
			resp, err = a.or.Generate(ctx, secret, req)
		}
	} else {
		resp, err = a.or.Generate(ctx, secret, req)
	}
	secs := time.Since(start).Seconds()
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			upd(JobUpdate{State: "cancelled", Notes: notes, Seconds: secs})
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			upd(JobUpdate{State: "error", Error: "timed out after " + jobTimeout.String(), Notes: notes, Seconds: secs})
		default:
			upd(JobUpdate{State: "error", Error: err.Error(), Notes: notes, Seconds: secs})
		}
		return
	}

	// usage.cost is OpenRouter's billed total for the whole request; split it across the images.
	var perImage, total *float64
	if resp.Usage != nil {
		t := resp.Usage.Cost
		c := t / float64(len(resp.Data))
		perImage, total = &c, &t
	}
	var saved []store.Meta
	var saveErr error
	for _, img := range resp.Data {
		data, err := base64.StdEncoding.DecodeString(stripDataURL(img.B64JSON))
		if err != nil || len(data) == 0 {
			saveErr = errors.New("provider returned an undecodable image")
			continue
		}
		meta, err := a.gallery().Add(store.Meta{
			MediaType: img.MediaType,
			Prompt:    in.Prompt,
			Model:     m.ID,
			ModelName: m.Name,
			KeyName:   keyName,
			Params:    sent,
			Refs:      len(req.InputReferences),
			Cost:      perImage,
			CostTotal: total,
			CostCount: len(resp.Data),
			Seconds:   secs,
			Batch:     batch,
		}, data)
		if err != nil {
			saveErr = fmt.Errorf("saving image: %w", err)
			continue
		}
		saved = append(saved, meta)
	}
	if len(saved) == 0 {
		upd(JobUpdate{State: "error", Error: saveErr.Error(), Notes: notes, Seconds: secs})
		return
	}
	if saveErr != nil {
		notes = append(notes, saveErr.Error())
	}
	upd(JobUpdate{State: "done", Images: saved, Notes: notes, Seconds: secs})
}

func (a *App) setPreview(id string, data []byte) {
	a.mu.Lock()
	a.preview[id] = data
	a.mu.Unlock()
}

func (a *App) clearPreview(id string) {
	a.mu.Lock()
	delete(a.preview, id)
	a.mu.Unlock()
}

func (a *App) previewBytes(id string) ([]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.preview[id]
	return b, ok
}

func stripDataURL(s string) string {
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i >= 0 {
			return s[i+1:]
		}
	}
	return s
}

func (a *App) resolveRefs(refs []string) ([]string, error) {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		id, ok := strings.CutPrefix(r, "gallery:")
		if !ok {
			out = append(out, r)
			continue
		}
		path, found := a.gallery().Path(id)
		if !found {
			return nil, errors.New("a reference image no longer exists")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		mt := store.SniffMediaType(data)
		out = append(out, "data:"+mt+";base64,"+base64.StdEncoding.EncodeToString(data))
	}
	return out, nil
}

// ---------- gallery ----------

func (a *App) SetFavorite(id string, fav bool) error { return a.gallery().SetFavorite(id, fav) }

// SetFavorites and ArchiveImages apply to every id and report all failures together.
func (a *App) SetFavorites(ids []string, fav bool) error {
	return a.each(ids, func(id string) error { return a.gallery().SetFavorite(id, fav) })
}

func (a *App) ArchiveImages(ids []string, archived bool) error {
	return a.each(ids, func(id string) error { return a.gallery().SetArchived(id, archived) })
}

func (a *App) each(ids []string, fn func(string) error) error {
	var errs []error
	for _, id := range ids {
		errs = append(errs, fn(id))
	}
	return errors.Join(errs...)
}

func (a *App) DeleteImages(ids []string) error {
	var errs []error
	for _, id := range ids {
		errs = append(errs, a.gallery().Delete(id))
	}
	return errors.Join(errs...)
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// SaveImageAs copies an image to a user-chosen path; returns "" if the dialog was cancelled.
func (a *App) SaveImageAs(id string) (string, error) {
	m, ok := a.gallery().Get(id)
	if !ok {
		return "", errors.New("no such image")
	}
	src, _ := a.gallery().Path(id)
	slug := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(m.Prompt), "-"), "-")
	if r := []rune(slug); len(r) > 40 {
		slug = strings.Trim(string(r[:40]), "-")
	}
	if slug == "" {
		slug = m.ID
	}
	ext := filepath.Ext(src)
	dst, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		DefaultFilename: slug + ext,
		Filters:         []wruntime.FileFilter{{DisplayName: "Image (*" + ext + ")", Pattern: "*" + ext}},
	})
	if err != nil || dst == "" {
		return "", err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	return dst, os.WriteFile(dst, data, 0o644)
}

func (a *App) RevealImage(id string) error {
	p, ok := a.gallery().Path(id)
	if !ok {
		return errors.New("no such image")
	}
	return reveal(p)
}

func (a *App) OpenOutputDir() error { return openDir(a.gallery().Dir()) }

// ChooseOutputDir lets the user pick a new image folder and reloads the gallery from it.
func (a *App) ChooseOutputDir() (*Boot, error) {
	dir, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:            "Where should images be saved?",
		DefaultDirectory: a.cfg.OutputDir(),
	})
	if err != nil || dir == "" {
		return nil, err
	}
	gal, err := store.OpenGallery(dir, filepath.Join(a.cfg.Dir(), "thumbs"))
	if err != nil {
		return nil, err
	}
	if err := a.cfg.SetOutputDir(dir); err != nil {
		return nil, err
	}
	a.galMu.Lock()
	a.gal = gal
	a.galMu.Unlock()
	b := a.Bootstrap()
	return &b, nil
}
