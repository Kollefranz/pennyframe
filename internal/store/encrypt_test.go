package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	secretPrompt = "SECRET-PROMPT-zebra-quartz"
	secretModel  = "secret-lab/hidden-model-9"
	goodPass     = "correct horse battery"
)

type vault struct {
	t          *testing.T
	dir, thumb string
	g          *Gallery
}

func newVault(t *testing.T) *vault {
	t.Helper()
	v := &vault{t: t, dir: t.TempDir(), thumb: t.TempDir()}
	v.reopen()
	return v
}

// reopen loads the gallery fresh from disk, like a new app start: every folder comes up locked.
func (v *vault) reopen() {
	g, err := OpenGallery(v.dir, v.thumb)
	if err != nil {
		v.t.Fatal(err)
	}
	v.g = g
}

func (v *vault) add(prompt string) (Meta, []byte) {
	data := pngBytes(900, 600, 255)
	m, err := v.g.Add(Meta{Prompt: prompt, Model: secretModel, ModelName: secretModel, KeyName: "k"}, data)
	if err != nil {
		v.t.Fatal(err)
	}
	return m, data
}

func (v *vault) folder(name, password string) Folder {
	f, err := v.g.CreateFolder(name, password)
	if err != nil {
		v.t.Fatal(err)
	}
	return f
}

func (v *vault) files() map[string][]byte {
	out := map[string][]byte{}
	for _, root := range []string{v.dir, v.thumb} {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				b, _ := os.ReadFile(p)
				out[p] = b
			}
			return nil
		})
	}
	return out
}

// assertNoPlaintext fails if any file on disk contains a needle or looks like a readable image.
func (v *vault) assertNoPlaintext(label string, needles ...[]byte) {
	v.t.Helper()
	magics := [][]byte{[]byte("\x89PNG\r\n\x1a\n"), {0xff, 0xd8, 0xff}, []byte("RIFF"), []byte("GIF8")}
	for p, b := range v.files() {
		if strings.HasSuffix(p, foldersFile) {
			continue
		}
		for _, n := range needles {
			if len(n) > 0 && bytes.Contains(b, n) {
				v.t.Errorf("%s: %s contains %q", label, filepath.Base(p), n[:min(len(n), 24)])
			}
		}
		if !strings.HasSuffix(p, ".json") {
			for _, m := range magics {
				if bytes.HasPrefix(b, m) {
					v.t.Errorf("%s: %s is a readable image", label, filepath.Base(p))
				}
			}
		}
	}
}

func slice(b []byte) []byte { return b[len(b)/2 : len(b)/2+48] }

func TestEncryptedFolderLeavesNothingReadableOnDisk(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	m, data := v.add(secretPrompt)
	// look at the image before filing it, so a plaintext thumbnail exists to be cleaned up
	if _, err := v.g.Thumb(m.ID, 400); err != nil {
		t.Fatal(err)
	}
	if err := v.g.SetFolder(m.ID, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.g.ThumbBytes(m.ID, 400); err != nil {
		t.Fatal(err)
	}
	if _, err := v.g.ThumbBytes(m.ID, 1600); err != nil {
		t.Fatal(err)
	}
	if err := v.g.SetFavorite(m.ID, true); err != nil {
		t.Fatal(err)
	}
	v.assertNoPlaintext("after filing", []byte(secretPrompt), []byte(secretModel), slice(data), data[:64])

	// the data key itself must not be on disk either
	g := v.g
	g.mu.RLock()
	dek := append([]byte(nil), g.keys[f.ID]...)
	g.mu.RUnlock()
	if len(dek) != keyLen {
		t.Fatalf("no data key in memory: %d", len(dek))
	}
	v.assertNoPlaintext("key", dek)

	cur, _ := v.g.Get(m.ID)
	if !cur.Encrypted || strings.HasSuffix(cur.File, ".png") || !strings.HasSuffix(cur.File, ".enc") {
		t.Fatalf("stored as %+v", cur)
	}
	if _, err := os.Stat(filepath.Join(v.dir, m.File)); err == nil {
		t.Fatalf("the original %s is still on disk", m.File)
	}

	// a fresh start sees only a stub, and every read path is closed
	v.reopen()
	stub, ok := v.g.Get(m.ID)
	if !ok || !stub.Locked || stub.Prompt != "" || stub.Model != "" || stub.Width != 0 {
		t.Fatalf("locked stub leaks: %+v", stub)
	}
	if _, err := v.g.ReadImage(m.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("ReadImage while locked: %v", err)
	}
	if _, err := v.g.ThumbBytes(m.ID, 400); !errors.Is(err, ErrLocked) {
		t.Fatalf("ThumbBytes while locked: %v", err)
	}
	if err := v.g.SetFavorite(m.ID, false); !errors.Is(err, ErrLocked) {
		t.Fatalf("Update while locked: %v", err)
	}
	if fs := v.g.Folders(); len(fs) != 1 || !fs[0].Encrypted || !fs[0].Locked {
		t.Fatalf("folder state: %+v", fs)
	}
}

func TestUnlockNeedsTheRightPasswordAndRestoresEverything(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	m, data := v.add(secretPrompt)
	_ = v.g.SetFolder(m.ID, f.ID)
	_ = v.g.SetFavorite(m.ID, true)
	v.reopen()

	for _, bad := range []string{"", "wrong password", strings.ToUpper(goodPass), goodPass + " "} {
		if err := v.g.UnlockFolder(f.ID, bad); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("password %q: %v", bad, err)
		}
	}
	if got, _ := v.g.Get(m.ID); !got.Locked || got.Prompt != "" {
		t.Fatal("a wrong password must not reveal anything")
	}

	if err := v.g.UnlockFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
	got, _ := v.g.Get(m.ID)
	if got.Locked || got.Prompt != secretPrompt || got.ModelName != secretModel || !got.Favorite || got.Folder != f.ID || got.Width != 900 {
		t.Fatalf("unlocked meta wrong: %+v", got)
	}
	back, err := v.g.ReadImage(m.ID)
	if err != nil || !bytes.Equal(back, data) {
		t.Fatalf("image did not round-trip: err=%v", err)
	}
	th, err := v.g.ThumbBytes(m.ID, 400)
	if err != nil || len(th) == 0 {
		t.Fatalf("thumb: %v", err)
	}

	v.g.LockFolder(f.ID)
	if got, _ := v.g.Get(m.ID); !got.Locked || got.Prompt != "" {
		t.Fatal("locking must drop decrypted details from memory")
	}
	if _, err := v.g.ReadImage(m.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("after lock: %v", err)
	}
	// and the key really is gone: the same password is needed again
	if err := v.g.UnlockFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
}

func TestMovingOutOfAnEncryptedFolderDecryptsAndLockedFolderRefusesMoves(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	m, data := v.add("p")
	_ = v.g.SetFolder(m.ID, f.ID)

	if err := v.g.SetFolder(m.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := v.g.Get(m.ID)
	if got.Encrypted || got.Folder != "" || !strings.HasSuffix(got.File, ".png") {
		t.Fatalf("not restored: %+v", got)
	}
	plain, _ := os.ReadFile(filepath.Join(v.dir, got.File))
	if !bytes.Equal(plain, data) {
		t.Fatal("restored file differs from the original")
	}
	if left, _ := filepath.Glob(filepath.Join(v.dir, "*.enc")); len(left) != 0 {
		t.Fatalf("sealed copy left behind: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(v.thumb, "*.enc")); len(left) != 0 {
		t.Fatalf("sealed thumbnails left behind: %v", left)
	}

	// into a locked encrypted folder: refused, and nothing changes
	v.g.LockFolder(f.ID)
	if err := v.g.SetFolder(m.ID, f.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("filing into a locked folder: %v", err)
	}
	if again, _ := v.g.Get(m.ID); again.Encrypted || again.Folder != "" {
		t.Fatalf("a refused move changed the image: %+v", again)
	}
}

func TestMovingBetweenEncryptedFoldersReSeals(t *testing.T) {
	v := newVault(t)
	a := v.folder("A", "alpha-password-1")
	b := v.folder("B", "bravo-password-2")
	m, data := v.add(secretPrompt)
	_ = v.g.SetFolder(m.ID, a.ID)
	_, _ = v.g.ThumbBytes(m.ID, 400)
	if err := v.g.SetFolder(m.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	v.assertNoPlaintext("after A to B", []byte(secretPrompt), slice(data))

	v.reopen()
	// A's password must not open B's image
	if err := v.g.UnlockFolder(a.ID, "alpha-password-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.g.ReadImage(m.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("A's key opened B's image: %v", err)
	}
	if err := v.g.UnlockFolder(b.ID, "bravo-password-2"); err != nil {
		t.Fatal(err)
	}
	back, err := v.g.ReadImage(m.ID)
	if err != nil || !bytes.Equal(back, data) {
		t.Fatalf("round trip: %v", err)
	}
}

func TestChangePasswordKeepsImagesUntouched(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	m, data := v.add("p")
	_ = v.g.SetFolder(m.ID, f.ID)
	cur, _ := v.g.Get(m.ID)
	before, _ := os.ReadFile(filepath.Join(v.dir, cur.File))

	if err := v.g.ChangeFolderPassword(f.ID, "not the password", "brand new passphrase"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong old password: %v", err)
	}
	if err := v.g.ChangeFolderPassword(f.ID, goodPass, "short"); err == nil {
		t.Fatal("a too-short new password must be refused")
	}
	if err := v.g.ChangeFolderPassword(f.ID, goodPass, "brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(v.dir, cur.File))
	if !bytes.Equal(before, after) {
		t.Fatal("changing the password must not rewrite images")
	}

	v.reopen()
	if err := v.g.UnlockFolder(f.ID, goodPass); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("old password still works: %v", err)
	}
	if err := v.g.UnlockFolder(f.ID, "brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if back, err := v.g.ReadImage(m.ID); err != nil || !bytes.Equal(back, data) {
		t.Fatalf("round trip: %v", err)
	}
}

func TestEncryptAnExistingFolderThenRemoveEncryption(t *testing.T) {
	v := newVault(t)
	f := v.folder("Later", "")
	var ids []string
	var datas [][]byte
	for range 3 {
		m, d := v.add(secretPrompt)
		_ = v.g.SetFolder(m.ID, f.ID)
		_, _ = v.g.Thumb(m.ID, 400)
		ids, datas = append(ids, m.ID), append(datas, d)
	}
	if err := v.g.EncryptFolder(f.ID, "short"); err == nil {
		t.Fatal("a too-short password must be refused")
	}
	if err := v.g.EncryptFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
	v.assertNoPlaintext("after encrypting", []byte(secretPrompt), slice(datas[0]))
	if err := v.g.EncryptFolder(f.ID, goodPass); err == nil {
		t.Fatal("encrypting twice should fail")
	}

	v.reopen()
	_ = v.g.UnlockFolder(f.ID, goodPass)
	if err := v.g.DecryptFolder(f.ID); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		got, _ := v.g.Get(id)
		raw, err := os.ReadFile(filepath.Join(v.dir, got.File))
		if got.Encrypted || err != nil || !bytes.Equal(raw, datas[i]) || got.Folder != f.ID || got.Prompt != secretPrompt {
			t.Fatalf("image %d not restored: %+v err=%v", i, got, err)
		}
	}
	if fs := v.g.Folders(); len(fs) != 1 || fs[0].Encrypted || fs[0].Locked {
		t.Fatalf("folder still encrypted: %+v", fs)
	}
	v.reopen()
	if err := v.g.UnlockFolder(f.ID, goodPass); err == nil {
		t.Fatal("the folder should no longer be encrypted")
	}
}

func TestDeleteEncryptedFolder(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	keep, keepData := v.add("keep")
	_ = v.g.SetFolder(keep.ID, f.ID)
	v.g.LockFolder(f.ID)

	if err := v.g.DeleteFolder(f.ID, false); !errors.Is(err, ErrLocked) {
		t.Fatalf("keeping the images of a locked folder needs the password: %v", err)
	}
	_ = v.g.UnlockFolder(f.ID, goodPass)
	if err := v.g.DeleteFolder(f.ID, false); err != nil {
		t.Fatal(err)
	}
	got, ok := v.g.Get(keep.ID)
	raw, _ := os.ReadFile(filepath.Join(v.dir, got.File))
	if !ok || got.Encrypted || got.Folder != "" || !bytes.Equal(raw, keepData) || len(v.g.Folders()) != 0 {
		t.Fatalf("image should survive decrypted and unfiled: %+v", got)
	}

	// forgotten password: the only way out is deleting the images with the folder, and that works locked
	g2 := v.folder("Forgotten", "another long password")
	lost, _ := v.add("lost")
	_ = v.g.SetFolder(lost.ID, g2.ID)
	v.reopen()
	if err := v.g.DeleteFolder(g2.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.g.Get(lost.ID); ok {
		t.Fatal("image should be gone")
	}
	for p := range v.files() {
		if strings.HasSuffix(p, ".enc") {
			t.Fatalf("leftover encrypted file %s", p)
		}
	}
}

func TestTamperedAndSwappedFilesAreRejected(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	a, _ := v.add("a")
	b, _ := v.add("b")
	_ = v.g.SetFolder(a.ID, f.ID)
	_ = v.g.SetFolder(b.ID, f.ID)
	ga, _ := v.g.Get(a.ID)
	gb, _ := v.g.Get(b.ID)
	pa, pb := filepath.Join(v.dir, ga.File), filepath.Join(v.dir, gb.File)

	// flip one bit
	raw, _ := os.ReadFile(pa)
	raw[len(raw)/2] ^= 1
	_ = os.WriteFile(pa, raw, 0o644)
	if _, err := v.g.ReadImage(a.ID); err == nil {
		t.Fatal("a modified file must not decrypt")
	}
	// swap the two ciphertexts: same key, but each is bound to its own image id
	rawA, _ := os.ReadFile(pb)
	_ = os.WriteFile(pa, rawA, 0o644)
	if _, err := v.g.ReadImage(a.ID); err == nil {
		t.Fatal("a ciphertext moved to another image must not decrypt")
	}
}

func TestEncryptedFolderCanBeCreatedOnlyWithAStrongEnoughPassword(t *testing.T) {
	v := newVault(t)
	if _, err := v.g.CreateFolder("X", "1234567"); err == nil {
		t.Fatal("7 characters should be refused")
	}
	if fs := v.g.Folders(); len(fs) != 0 {
		t.Fatalf("a refused folder must not be created: %+v", fs)
	}
	if _, err := v.g.CreateFolder("X", "12345678"); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedConversionIsFinishedOnUnlock(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	m, data := v.add(secretPrompt)
	// simulate a crash: the record already says the image is in the folder, but it is still plain
	if err := v.g.Update(m.ID, func(x *Meta) { x.Folder = f.ID }); err != nil {
		t.Fatal(err)
	}
	v.reopen()
	if err := v.g.UnlockFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
	got, _ := v.g.Get(m.ID)
	if !got.Encrypted {
		t.Fatalf("unlock should finish sealing: %+v", got)
	}
	v.assertNoPlaintext("after healing", []byte(secretPrompt), slice(data))
}

func TestLockAllAndGalleryListHidesLockedDetails(t *testing.T) {
	v := newVault(t)
	a, b := v.folder("A", "alpha-password-1"), v.folder("B", "bravo-password-2")
	ma, _ := v.add(secretPrompt)
	mb, _ := v.add(secretPrompt)
	_ = v.g.SetFolder(ma.ID, a.ID)
	_ = v.g.SetFolder(mb.ID, b.ID)
	v.g.LockAll()
	for _, m := range v.g.List() {
		if !m.Locked || m.Prompt != "" || m.ModelName != "" {
			t.Fatalf("list leaks: %+v", m)
		}
	}
	for _, f := range v.g.Folders() {
		if !f.Locked {
			t.Fatalf("folder %s still unlocked", f.Name)
		}
	}
}
