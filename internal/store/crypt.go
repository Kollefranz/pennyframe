package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

// Encrypted folders use a random 256-bit data key per folder. The data key is stored only wrapped
// (AES-256-GCM) under a key derived from the folder's password with Argon2id, so a password change
// re-wraps one key instead of re-encrypting every image. Images, their thumbnails and their metadata
// (prompt, model, parameters) are sealed with AES-256-GCM under the data key. The data key is held in
// memory only while the folder is unlocked.

const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	keyLen       = 32

	MinPasswordLen = 8
)

var (
	ErrLocked        = errors.New("this folder is locked: enter its password first")
	ErrWrongPassword = errors.New("wrong password")

	encMagic = []byte("PFENC1")
)

func seal(key, plain, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append(append(make([]byte, 0, len(encMagic)+len(nonce)+len(plain)+gcm.Overhead()), encMagic...), nonce...)
	return gcm.Seal(out, nonce, plain, aad), nil
}

func open(key, blob, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	head := len(encMagic) + gcm.NonceSize()
	if len(blob) < head+gcm.Overhead() || string(blob[:len(encMagic)]) != string(encMagic) {
		return nil, errors.New("not an encrypted Pennyframe file")
	}
	return gcm.Open(nil, blob[len(encMagic):head], blob[head:], aad)
}

// The associated data ties each ciphertext to what it belongs to, so files can't be swapped around.
func imgAAD(id string) []byte  { return []byte("pennyframe:image:" + id) }
func metaAAD(id string) []byte { return []byte("pennyframe:meta:" + id) }
func thumbAAD(id string, size int) []byte {
	return []byte(fmt.Sprintf("pennyframe:thumb:%s:%d", id, size))
}
func dekAAD(folderID string) []byte    { return []byte("pennyframe:folder-key:" + folderID) }
func normalize(password string) []byte { return []byte(norm.NFC.String(password)) }
func validPassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return fmt.Errorf("use a password of at least %d characters", MinPasswordLen)
	}
	return nil
}

// folderCrypto is what is stored on disk for an encrypted folder: no secrets, only the wrapped key.
type folderCrypto struct {
	Salt    []byte `json:"salt"`
	Time    uint32 `json:"t"`
	Memory  uint32 `json:"m"`
	Threads uint8  `json:"p"`
	Wrapped []byte `json:"wrapped"`
}

func (c *folderCrypto) kek(password string) []byte {
	return argon2.IDKey(normalize(password), c.Salt, c.Time, c.Memory, c.Threads, keyLen)
}

// newFolderCrypto makes a fresh data key for the folder and wraps it under the password.
func newFolderCrypto(folderID, password string) (*folderCrypto, []byte, error) {
	dek := make([]byte, keyLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, nil, err
	}
	c, err := wrapKey(folderID, password, dek)
	return c, dek, err
}

func wrapKey(folderID, password string, dek []byte) (*folderCrypto, error) {
	c := &folderCrypto{Salt: make([]byte, 16), Time: argonTime, Memory: argonMemory, Threads: argonThreads}
	if _, err := rand.Read(c.Salt); err != nil {
		return nil, err
	}
	w, err := seal(c.kek(password), dek, dekAAD(folderID))
	if err != nil {
		return nil, err
	}
	c.Wrapped = w
	return c, nil
}

// unwrap returns the data key, or ErrWrongPassword. The GCM tag is what tells a wrong password apart.
func (c *folderCrypto) unwrap(folderID, password string) ([]byte, error) {
	dek, err := open(c.kek(password), c.Wrapped, dekAAD(folderID))
	if err != nil {
		return nil, ErrWrongPassword
	}
	return dek, nil
}

// wipeFile overwrites a file with zeros before deleting it. Best effort: SSDs and journaling file
// systems may keep old copies of the data, so this lowers the risk without removing it.
func wipeFile(path string) {
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		if st, err := f.Stat(); err == nil {
			zeros := make([]byte, 64<<10)
			for left := st.Size(); left > 0; {
				n := min(left, int64(len(zeros)))
				if _, err := f.Write(zeros[:n]); err != nil {
					break
				}
				left -= n
			}
			_ = f.Sync()
		}
		_ = f.Close()
	}
	_ = os.Remove(path)
}
