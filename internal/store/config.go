// Package store persists keys, preferences and the image gallery.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

const keyringService = "pennyframe"

// Secrets holds API key material outside the config file.
type Secrets interface {
	Get(id string) (string, error)
	Set(id, secret string) error
	Delete(id string) error
}

type osSecrets struct{}

func (osSecrets) Get(id string) (string, error) { return keyring.Get(keyringService, id) }
func (osSecrets) Set(id, secret string) error   { return keyring.Set(keyringService, id, secret) }
func (osSecrets) Delete(id string) error {
	err := keyring.Delete(keyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// OSSecrets stores secrets in the OS credential store (Windows Credential Manager).
func OSSecrets() Secrets { return osSecrets{} }

// Key is the non-secret half of an API key.
type Key struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Hint string `json:"hint"`
}

type configFile struct {
	Keys      []Key  `json:"keys"`
	ActiveKey string `json:"activeKey"`
	OutputDir string `json:"outputDir"`
}

// Config is the on-disk app state: keys (secrets live in Secrets), output dir, and an opaque UI prefs blob.
type Config struct {
	dir     string
	secrets Secrets
	mu      sync.Mutex
	data    configFile
}

func LoadConfig(dir string, secrets Secrets) (*Config, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	c := &Config{dir: dir, secrets: secrets}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err == nil {
		if err := json.Unmarshal(b, &c.data); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

func (c *Config) Dir() string { return c.dir }

// saveLocked writes atomically so a crash cannot truncate the config.
func (c *Config) saveLocked() error {
	b, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(c.dir, "config.json"), b)
}

func (c *Config) Keys() []Key {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Key(nil), c.data.Keys...)
}

func (c *Config) ActiveKeyID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.ActiveKey
}

func (c *Config) AddKey(name, secret string) (Key, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := randID()
	if err := c.secrets.Set(id, secret); err != nil {
		return Key{}, err
	}
	k := Key{ID: id, Name: name, Hint: hint(secret)}
	c.data.Keys = append(c.data.Keys, k)
	c.data.ActiveKey = id
	if err := c.saveLocked(); err != nil {
		_ = c.secrets.Delete(id)
		return Key{}, err
	}
	return k, nil
}

func (c *Config) RenameKey(id, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.data.Keys {
		if c.data.Keys[i].ID == id {
			c.data.Keys[i].Name = name
			return c.saveLocked()
		}
	}
	return errors.New("no such key")
}

func (c *Config) RemoveKey(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := -1
	for i, k := range c.data.Keys {
		if k.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return errors.New("no such key")
	}
	if err := c.secrets.Delete(id); err != nil {
		return err
	}
	c.data.Keys = append(c.data.Keys[:idx], c.data.Keys[idx+1:]...)
	if c.data.ActiveKey == id {
		c.data.ActiveKey = ""
		if len(c.data.Keys) > 0 {
			c.data.ActiveKey = c.data.Keys[0].ID
		}
	}
	return c.saveLocked()
}

func (c *Config) SetActiveKey(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range c.data.Keys {
		if k.ID == id {
			c.data.ActiveKey = id
			return c.saveLocked()
		}
	}
	return errors.New("no such key")
}

func (c *Config) Secret(id string) (string, error) { return c.secrets.Get(id) }

func (c *Config) OutputDir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data.OutputDir != "" {
		return c.data.OutputDir
	}
	return DefaultOutputDir()
}

func DefaultOutputDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Pictures", "Pennyframe")
}

func (c *Config) SetOutputDir(dir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.OutputDir = dir
	return c.saveLocked()
}

// Prefs is an opaque JSON string owned by the frontend (favorites, last-used params, ...).
func (c *Config) Prefs() string {
	b, err := os.ReadFile(filepath.Join(c.dir, "prefs.json"))
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (c *Config) SetPrefs(s string) error {
	if !json.Valid([]byte(s)) {
		return errors.New("prefs must be valid JSON")
	}
	return writeFileAtomic(filepath.Join(c.dir, "prefs.json"), []byte(s))
}

func hint(secret string) string {
	if len(secret) <= 12 {
		return "…" + secret[max(0, len(secret)-4):]
	}
	return secret[:8] + "…" + secret[len(secret)-4:]
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
