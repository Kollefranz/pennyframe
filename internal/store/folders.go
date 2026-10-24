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

// foldersFile lives beside the images so folders move with them; OpenGallery skips it (it has no image id).
const foldersFile = "_folders.json"

const maxFolderName = 60

// Folder is a user-made, in-app group of images. An image is in at most one folder.
type Folder struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func (g *Gallery) loadFolders() {
	b, err := os.ReadFile(filepath.Join(g.dir, foldersFile))
	if err != nil {
		return
	}
	var f struct {
		Folders []Folder `json:"folders"`
	}
	if json.Unmarshal(b, &f) == nil {
		g.folders = f.Folders
	}
}

// saveFolders persists g.folders; callers hold g.mu.
func (g *Gallery) saveFolders() error {
	b, err := json.MarshalIndent(struct {
		Folders []Folder `json:"folders"`
	}{g.folders}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(g.dir, foldersFile), b)
}

// Folders returns all folders sorted by name.
func (g *Gallery) Folders() []Folder {
	g.mu.RLock()
	out := append([]Folder{}, g.folders...)
	g.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// cleanFolderName trims and validates a name; callers hold g.mu and pass the id being renamed (or "").
func (g *Gallery) cleanFolderName(name, self string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	switch {
	case name == "":
		return "", errors.New("give the folder a name")
	case utf8.RuneCountInString(name) > maxFolderName:
		return "", errors.New("folder names can be at most 60 characters")
	}
	for _, f := range g.folders {
		if f.ID != self && strings.EqualFold(f.Name, name) {
			return "", errors.New("there's already a folder called " + f.Name)
		}
	}
	return name, nil
}

func (g *Gallery) CreateFolder(name string) (Folder, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	name, err := g.cleanFolderName(name, "")
	if err != nil {
		return Folder{}, err
	}
	f := Folder{ID: NewID(), Name: name, CreatedAt: time.Now()}
	g.folders = append(g.folders, f)
	if err := g.saveFolders(); err != nil {
		g.folders = g.folders[:len(g.folders)-1]
		return Folder{}, err
	}
	return f, nil
}

func (g *Gallery) RenameFolder(id, name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	name, err := g.cleanFolderName(name, id)
	if err != nil {
		return err
	}
	for i := range g.folders {
		if g.folders[i].ID != id {
			continue
		}
		prev := g.folders[i].Name
		g.folders[i].Name = name
		if err := g.saveFolders(); err != nil {
			g.folders[i].Name = prev
			return err
		}
		return nil
	}
	return errors.New("no such folder")
}

// DeleteFolder removes the folder only; its images stay in the gallery, unfiled.
func (g *Gallery) DeleteFolder(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	idx := -1
	for i, f := range g.folders {
		if f.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return errors.New("no such folder")
	}
	var errs []error
	for _, m := range g.items {
		if m.Folder != id {
			continue
		}
		m.Folder = ""
		if err := g.writeMeta(m); err != nil {
			m.Folder = id
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	prev := g.folders
	g.folders = append(append([]Folder{}, g.folders[:idx]...), g.folders[idx+1:]...)
	if err := g.saveFolders(); err != nil {
		g.folders = prev
		return err
	}
	return nil
}

// SetFolder files an image into a folder; an empty folderID takes it out of its folder.
func (g *Gallery) SetFolder(id, folderID string) error {
	if folderID != "" {
		g.mu.RLock()
		found := false
		for _, f := range g.folders {
			found = found || f.ID == folderID
		}
		g.mu.RUnlock()
		if !found {
			return errors.New("that folder no longer exists")
		}
	}
	return g.Update(id, func(m *Meta) { m.Folder = folderID })
}
