package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Saved prompts live in <images>/prompts, one file per folder: "_.json" for prompts in no folder,
// "<folder>.json" for a plain folder and "<folder>.enc" for an encrypted one. An encrypted folder's
// file is sealed as a whole (titles, texts and even how many there are) with the folder's key, and
// its prompts are only held in memory while the folder is unlocked.
const (
	promptsDir       = "prompts"
	maxPromptText    = 20000
	maxPromptTitle   = 80
	unfiledPromptKey = ""
)

// Prompt is a saved prompt. Folder and Encrypted are filled in when listed.
type Prompt struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	Folder    string    `json:"folder,omitempty"`
	Encrypted bool      `json:"encrypted,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type promptFile struct {
	Prompts []Prompt `json:"prompts"`
}

func promptsAAD(folder string) []byte { return []byte("pennyframe:prompts:" + folder) }

func (g *Gallery) promptPath(folder string, sealed bool) string {
	name, ext := folder, ".json"
	if folder == unfiledPromptKey {
		name = "_"
	}
	if sealed {
		ext = ".enc"
	}
	return filepath.Join(g.dir, promptsDir, name+ext)
}

// folderEncrypted reports whether a folder uses encryption; callers hold g.mu.
func (g *Gallery) folderEncrypted(id string) bool {
	i := g.findRec(id)
	return i >= 0 && g.folders[i].Crypto != nil
}

func readPromptFile(p string) ([]Prompt, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var f promptFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Prompts, nil
}

// loadPrompts reads the unencrypted prompt files; sealed ones wait for their folder to be unlocked.
func (g *Gallery) loadPrompts() {
	g.prompts = map[string][]Prompt{}
	entries, _ := os.ReadDir(filepath.Join(g.dir, promptsDir))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		folder := strings.TrimSuffix(name, ".json")
		if folder == "_" {
			folder = unfiledPromptKey
		} else if g.findRec(folder) < 0 || g.folderEncrypted(folder) {
			continue // an orphan, or leftovers of a folder that has since been encrypted: unlock merges and wipes it
		}
		if list, err := readPromptFile(filepath.Join(g.dir, promptsDir, name)); err == nil && len(list) > 0 {
			g.prompts[folder] = list
		}
	}
}

// savePrompts writes (or removes) one folder's prompt file; callers hold g.mu.
func (g *Gallery) savePrompts(folder string) error {
	list := g.prompts[folder]
	enc := folder != unfiledPromptKey && g.folderEncrypted(folder)
	path := g.promptPath(folder, enc)
	if len(list) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(promptFile{Prompts: list}, "", "  ")
	if err != nil {
		return err
	}
	if enc {
		key := g.keys[folder]
		if key == nil {
			return ErrLocked
		}
		if b, err = seal(key, b, promptsAAD(folder)); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// unlockPrompts loads a folder's sealed prompts, and finishes sealing any plaintext left by an
// interrupted encryption; callers hold g.mu.
func (g *Gallery) unlockPrompts(folder string, key []byte) {
	var list []Prompt
	if sealed, err := os.ReadFile(g.promptPath(folder, true)); err == nil {
		if plain, err := open(key, sealed, promptsAAD(folder)); err == nil {
			var f promptFile
			if json.Unmarshal(plain, &f) == nil {
				list = f.Prompts
			}
		}
	}
	leftover := g.promptPath(folder, false)
	if extra, err := readPromptFile(leftover); err == nil {
		have := map[string]bool{}
		for _, p := range list {
			have[p.ID] = true
		}
		for _, p := range extra {
			if !have[p.ID] {
				list = append(list, p)
			}
		}
		g.prompts[folder] = list
		if g.savePrompts(folder) == nil {
			wipeFile(leftover)
		}
		return
	}
	if len(list) > 0 {
		g.prompts[folder] = list
	}
}

// sealPrompts moves a folder's prompts into an encrypted file (the folder's key must be set), then
// wipes the plaintext one; callers hold g.mu.
func (g *Gallery) sealPrompts(folder string) error {
	if len(g.prompts[folder]) == 0 {
		return nil
	}
	if err := g.savePrompts(folder); err != nil {
		return err
	}
	wipeFile(g.promptPath(folder, false))
	return nil
}

// unsealPrompts writes a folder's prompts back unencrypted and drops the sealed file; callers hold g.mu.
func (g *Gallery) unsealPrompts(folder string) error {
	list := g.prompts[folder]
	if len(list) > 0 {
		b, err := json.MarshalIndent(promptFile{Prompts: list}, "", "  ")
		if err != nil {
			return err
		}
		path := g.promptPath(folder, false)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(path, b); err != nil {
			return err
		}
	}
	if err := os.Remove(g.promptPath(folder, true)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// releasePrompts is for deleting a folder: its prompts either move to "no folder" or are deleted
// with it (which works while locked: the sealed file is simply removed); callers hold g.mu.
func (g *Gallery) releasePrompts(folder string, deleteThem bool) error {
	list := g.prompts[folder]
	if !deleteThem && len(list) > 0 {
		g.prompts[unfiledPromptKey] = append(g.prompts[unfiledPromptKey], list...)
		if err := g.savePrompts(unfiledPromptKey); err != nil {
			g.prompts[unfiledPromptKey] = g.prompts[unfiledPromptKey][:len(g.prompts[unfiledPromptKey])-len(list)]
			return err
		}
	}
	delete(g.prompts, folder)
	wipeFile(g.promptPath(folder, false))
	_ = os.Remove(g.promptPath(folder, true))
	return nil
}

// Prompts lists every saved prompt that is currently readable (none from locked folders), newest first.
func (g *Gallery) Prompts() []Prompt {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := []Prompt{}
	for folder, list := range g.prompts {
		enc := folder != unfiledPromptKey && g.folderEncrypted(folder)
		for _, p := range list {
			p.Folder, p.Encrypted = folder, enc
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func cleanPromptTitle(title, text string) string {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
		title = strings.Join(strings.Fields(first), " ")
	}
	if utf8.RuneCountInString(title) > maxPromptTitle {
		title = strings.TrimSpace(string([]rune(title)[:maxPromptTitle-1])) + "…"
	}
	return title
}

func cleanPromptText(text string) (string, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", errors.New("write a prompt to save")
	case utf8.RuneCountInString(text) > maxPromptText:
		return "", errors.New("that prompt is too long to save")
	}
	return text, nil
}

// SavePrompt stores a prompt, optionally in a folder (an encrypted one must be unlocked).
func (g *Gallery) SavePrompt(title, text, folderID string) (Prompt, error) {
	text, err := cleanPromptText(text)
	if err != nil {
		return Prompt{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	enc := false
	if folderID != unfiledPromptKey {
		if g.findRec(folderID) < 0 {
			return Prompt{}, errors.New("that folder no longer exists")
		}
		if enc = g.folderEncrypted(folderID); enc && g.keys[folderID] == nil {
			return Prompt{}, ErrLocked
		}
	}
	now := time.Now()
	p := Prompt{ID: NewID(), Title: cleanPromptTitle(title, text), Text: text, CreatedAt: now, UpdatedAt: now}
	g.prompts[folderID] = append(g.prompts[folderID], p)
	if err := g.savePrompts(folderID); err != nil {
		g.prompts[folderID] = g.prompts[folderID][:len(g.prompts[folderID])-1]
		return Prompt{}, err
	}
	p.Folder, p.Encrypted = folderID, enc
	return p, nil
}

// findPrompt returns the folder key and index of a prompt; callers hold g.mu.
func (g *Gallery) findPrompt(id string) (string, int, bool) {
	for folder, list := range g.prompts {
		for i := range list {
			if list[i].ID == id {
				return folder, i, true
			}
		}
	}
	return "", 0, false
}

func (g *Gallery) UpdatePrompt(id, title, text string) error {
	text, err := cleanPromptText(text)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	folder, i, ok := g.findPrompt(id)
	if !ok {
		return errors.New("no such prompt (is its folder locked?)")
	}
	prev := g.prompts[folder][i]
	g.prompts[folder][i].Title = cleanPromptTitle(title, text)
	g.prompts[folder][i].Text = text
	g.prompts[folder][i].UpdatedAt = time.Now()
	if err := g.savePrompts(folder); err != nil {
		g.prompts[folder][i] = prev
		return err
	}
	return nil
}

func (g *Gallery) DeletePrompt(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	folder, i, ok := g.findPrompt(id)
	if !ok {
		return errors.New("no such prompt (is its folder locked?)")
	}
	prevList := g.prompts[folder]
	g.prompts[folder] = append(append([]Prompt{}, prevList[:i]...), prevList[i+1:]...)
	if len(g.prompts[folder]) == 0 && folder != unfiledPromptKey && !g.folderEncrypted(folder) {
		delete(g.prompts, folder)
	}
	if err := g.savePrompts(folder); err != nil {
		g.prompts[folder] = prevList
		return err
	}
	return nil
}

// MovePrompt files a prompt into another folder ("" for none). An encrypted folder must be unlocked
// at both ends; the prompt is sealed or unsealed as it moves.
func (g *Gallery) MovePrompt(id, folderID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	src, i, ok := g.findPrompt(id)
	if !ok {
		return errors.New("no such prompt (is its folder locked?)")
	}
	if src == folderID {
		return nil
	}
	if folderID != unfiledPromptKey {
		if g.findRec(folderID) < 0 {
			return errors.New("that folder no longer exists")
		}
		if g.folderEncrypted(folderID) && g.keys[folderID] == nil {
			return ErrLocked
		}
	}
	p := g.prompts[src][i]
	srcList := g.prompts[src]
	g.prompts[folderID] = append(g.prompts[folderID], p)
	if err := g.savePrompts(folderID); err != nil { // destination first: a crash leaves a copy, never a loss
		g.prompts[folderID] = g.prompts[folderID][:len(g.prompts[folderID])-1]
		return err
	}
	g.prompts[src] = append(append([]Prompt{}, srcList[:i]...), srcList[i+1:]...)
	if len(g.prompts[src]) == 0 && src != unfiledPromptKey && !g.folderEncrypted(src) {
		delete(g.prompts, src)
	}
	if err := g.savePrompts(src); err != nil {
		g.prompts[src] = srcList
		return err
	}
	return nil
}
