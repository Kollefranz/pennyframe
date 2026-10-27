package store

import (
	"encoding/json"
	"errors"
	"fmt"
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

// Folder is a user-made, in-app group of images. An image is in at most one folder. Encrypted
// folders keep their images, thumbnails and image details sealed on disk until unlocked.
type Folder struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Encrypted bool      `json:"encrypted,omitempty"`
	Locked    bool      `json:"locked,omitempty"` // encrypted and not currently unlocked; never stored
}

// folderRec is a folder as stored: the wrapped key is the only crypto material kept on disk.
type folderRec struct {
	Folder
	Crypto *folderCrypto `json:"crypto,omitempty"`
}

func (g *Gallery) loadFolders() {
	b, err := os.ReadFile(filepath.Join(g.dir, foldersFile))
	if err != nil {
		return
	}
	var f struct {
		Folders []folderRec `json:"folders"`
	}
	if json.Unmarshal(b, &f) == nil {
		g.folders = f.Folders
		for i := range g.folders {
			g.folders[i].Locked = false
			g.folders[i].Encrypted = g.folders[i].Crypto != nil
		}
	}
}

// saveFolders persists g.folders; callers hold g.mu.
func (g *Gallery) saveFolders() error {
	b, err := json.MarshalIndent(struct {
		Folders []folderRec `json:"folders"`
	}{g.folders}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(g.dir, foldersFile), b)
}

// findRec returns the index of a folder, or -1; callers hold g.mu.
func (g *Gallery) findRec(id string) int {
	for i := range g.folders {
		if g.folders[i].ID == id {
			return i
		}
	}
	return -1
}

func (g *Gallery) view(r folderRec) Folder {
	f := r.Folder
	f.Encrypted = r.Crypto != nil
	f.Locked = f.Encrypted && g.keys[r.ID] == nil
	return f
}

// Folders returns all folders sorted by name.
func (g *Gallery) Folders() []Folder {
	g.mu.RLock()
	out := make([]Folder, 0, len(g.folders))
	for _, r := range g.folders {
		out = append(out, g.view(r))
	}
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

// CreateFolder makes a folder; with a password it is an encrypted folder, left unlocked.
func (g *Gallery) CreateFolder(name, password string) (Folder, error) {
	g.mu.RLock()
	_, err := g.cleanFolderName(name, "")
	g.mu.RUnlock()
	if err != nil {
		return Folder{}, err
	}
	id := NewID()
	var c *folderCrypto
	var dek []byte
	if password != "" {
		if err := validPassword(password); err != nil {
			return Folder{}, err
		}
		if c, dek, err = newFolderCrypto(id, password); err != nil { // slow on purpose, so outside the lock
			return Folder{}, err
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	name, err = g.cleanFolderName(name, "")
	if err != nil {
		return Folder{}, err
	}
	rec := folderRec{Folder: Folder{ID: id, Name: name, CreatedAt: time.Now(), Encrypted: c != nil}, Crypto: c}
	g.folders = append(g.folders, rec)
	if err := g.saveFolders(); err != nil {
		g.folders = g.folders[:len(g.folders)-1]
		return Folder{}, err
	}
	if dek != nil {
		g.keys[id] = dek
	}
	return g.view(rec), nil
}

func (g *Gallery) RenameFolder(id, name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	name, err := g.cleanFolderName(name, id)
	if err != nil {
		return err
	}
	i := g.findRec(id)
	if i < 0 {
		return errors.New("no such folder")
	}
	prev := g.folders[i].Name
	g.folders[i].Name = name
	if err := g.saveFolders(); err != nil {
		g.folders[i].Name = prev
		return err
	}
	return nil
}

// members returns the ids of every image whose record says it is in the folder, locked or not.
func (g *Gallery) members(folderID string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var ids []string
	for id, m := range g.items {
		if m.Folder == folderID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// UnlockFolder checks the password and keeps the folder's key in memory until it is locked again.
func (g *Gallery) UnlockFolder(id, password string) error {
	g.mu.RLock()
	i := g.findRec(id)
	var c *folderCrypto
	if i >= 0 {
		c = g.folders[i].Crypto
	}
	g.mu.RUnlock()
	if i < 0 {
		return errors.New("no such folder")
	}
	if c == nil {
		return errors.New("this folder isn't encrypted")
	}
	dek, err := c.unwrap(id, password) // slow on purpose, so outside the lock
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.keys[id] = dek
	for _, m := range g.items {
		if m.Folder == id && m.Encrypted && m.Locked {
			g.unlockItem(m, dek)
		}
	}
	g.mu.Unlock()
	return g.healFolder(id)
}

// healFolder encrypts any image of an unlocked encrypted folder that is still stored in the clear
// (left over from an interrupted conversion).
func (g *Gallery) healFolder(id string) error {
	g.mu.RLock()
	key := g.keys[id]
	var plain []string
	for mid, m := range g.items {
		if m.Folder == id && !m.Encrypted {
			plain = append(plain, mid)
		}
	}
	g.mu.RUnlock()
	var errs []error
	for _, mid := range plain {
		errs = append(errs, g.convertTo(mid, id, true, key))
	}
	return errors.Join(errs...)
}

// LockFolder forgets the folder's key and everything decrypted from it.
func (g *Gallery) LockFolder(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lockLocked(id)
}

func (g *Gallery) lockLocked(id string) {
	delete(g.keys, id)
	for _, m := range g.items {
		if m.Folder == id && m.Encrypted {
			lockItem(m)
		}
	}
}

func (g *Gallery) LockAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id := range g.keys {
		g.lockLocked(id)
	}
}

// EncryptFolder turns a plain folder into an encrypted one and seals the images already in it.
func (g *Gallery) EncryptFolder(id, password string) error {
	if err := validPassword(password); err != nil {
		return err
	}
	g.mu.RLock()
	i := g.findRec(id)
	already := i >= 0 && g.folders[i].Crypto != nil
	g.mu.RUnlock()
	if i < 0 {
		return errors.New("no such folder")
	}
	if already {
		return errors.New("this folder is already encrypted")
	}
	c, dek, err := newFolderCrypto(id, password)
	if err != nil {
		return err
	}
	g.mu.Lock()
	i = g.findRec(id)
	if i < 0 || g.folders[i].Crypto != nil {
		g.mu.Unlock()
		return errors.New("the folder changed while encrypting; try again")
	}
	// The wrapped key is saved before any image is sealed with it, so a crash can't strand them.
	g.folders[i].Crypto = c
	if err := g.saveFolders(); err != nil {
		g.folders[i].Crypto = nil
		g.mu.Unlock()
		return err
	}
	g.keys[id] = dek
	g.mu.Unlock()
	return g.healFolder(id)
}

// DecryptFolder removes a folder's encryption (it must be unlocked), writing its images back in the clear.
func (g *Gallery) DecryptFolder(id string) error {
	g.mu.RLock()
	i := g.findRec(id)
	enc := i >= 0 && g.folders[i].Crypto != nil
	unlocked := g.keys[id] != nil
	g.mu.RUnlock()
	switch {
	case i < 0:
		return errors.New("no such folder")
	case !enc:
		return errors.New("this folder isn't encrypted")
	case !unlocked:
		return ErrLocked
	}
	var errs []error
	for _, mid := range g.members(id) {
		errs = append(errs, g.convertTo(mid, id, false, nil))
	}
	if err := errors.Join(errs...); err != nil {
		return err // the folder stays encrypted; images already converted are re-sealed on the next unlock
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	i = g.findRec(id)
	if i < 0 {
		return errors.New("no such folder")
	}
	prev := g.folders[i].Crypto
	g.folders[i].Crypto = nil
	if err := g.saveFolders(); err != nil {
		g.folders[i].Crypto = prev
		return err
	}
	delete(g.keys, id)
	return nil
}

// ChangeFolderPassword re-wraps the folder's key under a new password. No image is touched.
func (g *Gallery) ChangeFolderPassword(id, oldPassword, newPassword string) error {
	if err := validPassword(newPassword); err != nil {
		return err
	}
	g.mu.RLock()
	i := g.findRec(id)
	var c *folderCrypto
	if i >= 0 {
		c = g.folders[i].Crypto
	}
	g.mu.RUnlock()
	if i < 0 {
		return errors.New("no such folder")
	}
	if c == nil {
		return errors.New("this folder isn't encrypted")
	}
	dek, err := c.unwrap(id, oldPassword)
	if err != nil {
		return err
	}
	nc, err := wrapKey(id, newPassword, dek)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	i = g.findRec(id)
	if i < 0 {
		return errors.New("no such folder")
	}
	g.folders[i].Crypto = nc
	if err := g.saveFolders(); err != nil {
		g.folders[i].Crypto = c
		return err
	}
	return nil
}

// DeleteFolder removes a folder. By default its images stay in the gallery, unfiled (an encrypted
// folder must be unlocked, and its images are written back in the clear). With deleteImages the
// images are deleted too, which works on a locked folder: it is how a forgotten password is dealt with.
func (g *Gallery) DeleteFolder(id string, deleteImages bool) error {
	g.mu.RLock()
	i := g.findRec(id)
	enc := i >= 0 && g.folders[i].Crypto != nil
	unlocked := g.keys[id] != nil
	g.mu.RUnlock()
	if i < 0 {
		return errors.New("no such folder")
	}
	if enc && !deleteImages && !unlocked {
		return ErrLocked
	}
	var errs []error
	for _, mid := range g.members(id) {
		switch {
		case deleteImages:
			errs = append(errs, g.Delete(mid))
		case enc:
			errs = append(errs, g.convertTo(mid, "", false, nil))
		default:
			errs = append(errs, g.Update(mid, func(m *Meta) { m.Folder = "" }))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	i = g.findRec(id)
	if i < 0 {
		return nil
	}
	prev := g.folders
	g.folders = append(append([]folderRec{}, g.folders[:i]...), g.folders[i+1:]...)
	if err := g.saveFolders(); err != nil {
		g.folders = prev
		return err
	}
	delete(g.keys, id)
	return nil
}

// SetFolder files an image into a folder; an empty folderID takes it out of its folder. Moving into
// an encrypted folder seals the image (the folder must be unlocked), and moving out of one writes it
// back in the clear.
func (g *Gallery) SetFolder(id, folderID string) error {
	g.mu.RLock()
	m, ok := g.items[id]
	var cur Meta
	if ok {
		cur = *m
	}
	var dstKey []byte
	dstEnc, dstFound := false, folderID == ""
	if i := g.findRec(folderID); folderID != "" && i >= 0 {
		dstFound, dstEnc, dstKey = true, g.folders[i].Crypto != nil, g.keys[folderID]
	}
	g.mu.RUnlock()
	switch {
	case !ok:
		return errors.New("no such image")
	case !dstFound:
		return errors.New("that folder no longer exists")
	case cur.Locked:
		return ErrLocked
	case dstEnc && dstKey == nil:
		return fmt.Errorf("%w", ErrLocked)
	case cur.Folder == folderID && cur.Encrypted == dstEnc:
		return nil
	case !cur.Encrypted && !dstEnc:
		return g.Update(id, func(m *Meta) { m.Folder = folderID })
	}
	return g.convertTo(id, folderID, dstEnc, dstKey)
}
