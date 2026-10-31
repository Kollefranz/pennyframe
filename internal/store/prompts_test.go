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
	secretText  = "A velvet-green dragon over SECRET-HARBOR-9147 at dusk"
	secretTitle = "TITLE-moonlit-quartz"
)

func (v *vault) promptFiles() map[string][]byte {
	out := map[string][]byte{}
	_ = filepath.Walk(filepath.Join(v.dir, promptsDir), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			out[filepath.Base(p)] = b
		}
		return nil
	})
	return out
}

func promptTexts(ps []Prompt) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Text)
	}
	return out
}

func TestSavedPromptsPersistAndAreEditable(t *testing.T) {
	v := newVault(t)
	p, err := v.g.SavePrompt("  Dragon   scene ", "  a dragon at dusk\nwith two moons ", "")
	if err != nil || p.Title != "Dragon scene" || p.Text != "a dragon at dusk\nwith two moons" || p.ID == "" {
		t.Fatalf("saved %+v err=%v", p, err)
	}
	auto, _ := v.g.SavePrompt("", "first line becomes the title\nsecond line", "")
	if auto.Title != "first line becomes the title" {
		t.Fatalf("auto title = %q", auto.Title)
	}
	for _, bad := range []string{"", "   \n "} {
		if _, err := v.g.SavePrompt("t", bad, ""); err == nil {
			t.Errorf("empty prompt %q should be refused", bad)
		}
	}
	if _, err := v.g.SavePrompt("t", "x", "ghost"); err == nil {
		t.Error("saving into a missing folder should fail")
	}
	if err := v.g.UpdatePrompt(p.ID, "Renamed", "edited text"); err != nil {
		t.Fatal(err)
	}

	v.reopen()
	got := v.g.Prompts()
	if len(got) != 2 || got[0].ID != p.ID || got[0].Title != "Renamed" || got[0].Text != "edited text" {
		t.Fatalf("after reopen: %+v", got)
	}
	if err := v.g.DeletePrompt(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := v.g.DeletePrompt(p.ID); err == nil {
		t.Error("deleting twice should fail")
	}
	v.reopen()
	if len(v.g.Prompts()) != 1 {
		t.Fatalf("delete did not persist: %+v", v.g.Prompts())
	}
}

func TestPromptsInEncryptedFoldersAreSealedAndHiddenWhenLocked(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	p, err := v.g.SavePrompt(secretTitle, secretText, f.ID)
	if err != nil || !p.Encrypted || p.Folder != f.ID {
		t.Fatalf("saved %+v err=%v", p, err)
	}
	plain, _ := v.g.SavePrompt("harmless", "a harmless cat", "")
	if err := v.g.UpdatePrompt(p.ID, secretTitle, secretText+" (edited)"); err != nil {
		t.Fatal(err)
	}

	files := v.promptFiles()
	if len(files) != 2 {
		t.Fatalf("expected one sealed and one plain prompt file, got %d", len(files))
	}
	for name, b := range files {
		if strings.HasSuffix(name, ".enc") {
			for _, needle := range []string{"SECRET-HARBOR", "moonlit", "dragon", secretTitle} {
				if bytes.Contains(b, []byte(needle)) {
					t.Errorf("sealed prompt file leaks %q", needle)
				}
			}
		} else if !bytes.Contains(b, []byte("harmless")) {
			t.Errorf("the unfiled prompt should be readable: %s", name)
		}
	}
	v.assertNoPlaintext("prompts", []byte("SECRET-HARBOR"), []byte(secretTitle))

	// a fresh start: the sealed prompts are simply not there, and a wrong password reveals nothing
	v.reopen()
	if got := v.g.Prompts(); len(got) != 1 || got[0].ID != plain.ID {
		t.Fatalf("locked prompts leak: %+v", got)
	}
	if err := v.g.UpdatePrompt(p.ID, "x", "y"); err == nil {
		t.Fatal("a locked prompt must not be editable")
	}
	if _, err := v.g.SavePrompt("t", "x", f.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("saving into a locked folder: %v", err)
	}
	if err := v.g.UnlockFolder(f.ID, "wrong password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatal(err)
	}
	if len(v.g.Prompts()) != 1 {
		t.Fatal("a wrong password must not reveal prompts")
	}
	if err := v.g.UnlockFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
	got := v.g.Prompts()
	if len(got) != 2 || !strings.Contains(strings.Join(promptTexts(got), "|"), secretText+" (edited)") {
		t.Fatalf("unlocked prompts: %+v", got)
	}
	v.g.LockFolder(f.ID)
	if len(v.g.Prompts()) != 1 {
		t.Fatal("locking must drop decrypted prompts from memory")
	}
}

func TestMovingPromptsSealsAndUnseals(t *testing.T) {
	v := newVault(t)
	a := v.folder("A", "alpha-password-1")
	b := v.folder("B", "bravo-password-2")
	p, _ := v.g.SavePrompt(secretTitle, secretText, "")

	if err := v.g.MovePrompt(p.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	v.assertNoPlaintext("moved into A", []byte("SECRET-HARBOR"), []byte(secretTitle))
	if names := v.promptFiles(); len(names) != 1 {
		t.Fatalf("the unfiled file should be gone once empty: %v", names)
	}
	if err := v.g.MovePrompt(p.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	v.assertNoPlaintext("moved A to B", []byte("SECRET-HARBOR"))
	v.g.LockFolder(b.ID)
	if err := v.g.MovePrompt(p.ID, ""); err == nil {
		t.Fatal("a locked prompt can't be moved")
	}
	_ = v.g.UnlockFolder(b.ID, "bravo-password-2")
	if err := v.g.MovePrompt(p.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := v.g.Prompts(); len(got) != 1 || got[0].Encrypted || got[0].Folder != "" {
		t.Fatalf("after moving out: %+v", got)
	}
	v.reopen()
	if got := v.g.Prompts(); len(got) != 1 || got[0].Text != secretText {
		t.Fatalf("moved prompt lost: %+v", got)
	}
	// moving into a locked folder is refused and loses nothing
	v.g.LockAll()
	if err := v.g.MovePrompt(p.ID, a.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("into locked folder: %v", err)
	}
	if len(v.g.Prompts()) != 1 {
		t.Fatal("a refused move must not lose the prompt")
	}
}

func TestEncryptingAndDecryptingAFolderCarriesItsPrompts(t *testing.T) {
	v := newVault(t)
	f := v.folder("Later", "")
	_, _ = v.g.SavePrompt(secretTitle, secretText, f.ID)
	if names := v.promptFiles(); len(names) != 1 {
		t.Fatal("expected a plain prompt file")
	}
	if err := v.g.EncryptFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
	v.assertNoPlaintext("after encrypting the folder", []byte("SECRET-HARBOR"), []byte(secretTitle))
	for name := range v.promptFiles() {
		if !strings.HasSuffix(name, ".enc") {
			t.Errorf("plaintext prompt file left behind: %s", name)
		}
	}
	if got := v.g.Prompts(); len(got) != 1 || !got[0].Encrypted {
		t.Fatalf("still readable while unlocked: %+v", got)
	}

	v.reopen()
	if len(v.g.Prompts()) != 0 {
		t.Fatal("locked after restart")
	}
	_ = v.g.UnlockFolder(f.ID, goodPass)
	if err := v.g.DecryptFolder(f.ID); err != nil {
		t.Fatal(err)
	}
	for name := range v.promptFiles() {
		if strings.HasSuffix(name, ".enc") {
			t.Errorf("sealed file left behind: %s", name)
		}
	}
	v.reopen()
	if got := v.g.Prompts(); len(got) != 1 || got[0].Text != secretText || got[0].Encrypted || got[0].Folder != f.ID {
		t.Fatalf("after removing encryption: %+v", got)
	}
}

func TestDeletingAFolderKeepsOrRemovesItsPrompts(t *testing.T) {
	v := newVault(t)
	keep := v.folder("Keep", goodPass)
	gone := v.folder("Gone", "another long password")
	_, _ = v.g.SavePrompt("kept", secretText, keep.ID)
	_, _ = v.g.SavePrompt("lost", "to be deleted "+secretText, gone.ID)

	if err := v.g.DeleteFolder(keep.ID, false); err != nil {
		t.Fatal(err)
	}
	var movedOut *Prompt
	for _, p := range v.g.Prompts() {
		if p.Title == "kept" {
			movedOut = &p
		}
	}
	if movedOut == nil || movedOut.Encrypted || movedOut.Folder != "" {
		t.Fatalf("a deleted folder's prompts should move out in the clear: %+v", v.g.Prompts())
	}

	v.reopen() // "Gone" is locked: its prompts can only be deleted with the folder
	if err := v.g.DeleteFolder(gone.ID, true); err != nil {
		t.Fatal(err)
	}
	v.reopen()
	got := v.g.Prompts()
	if len(got) != 1 || got[0].Title != "kept" {
		t.Fatalf("after deleting the locked folder: %+v", got)
	}
	for name := range v.promptFiles() {
		if strings.HasSuffix(name, ".enc") {
			t.Errorf("leftover sealed prompt file %s", name)
		}
	}
}

func TestTamperedPromptFileIsIgnoredNotTrusted(t *testing.T) {
	v := newVault(t)
	f := v.folder("Private", goodPass)
	_, _ = v.g.SavePrompt("t", secretText, f.ID)
	path := filepath.Join(v.dir, promptsDir, f.ID+".enc")
	raw, _ := os.ReadFile(path)
	raw[len(raw)/2] ^= 1
	_ = os.WriteFile(path, raw, 0o644)
	v.reopen()
	if err := v.g.UnlockFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
	if got := v.g.Prompts(); len(got) != 0 {
		t.Fatalf("a modified file must not yield prompts: %+v", got)
	}
}

func TestInterruptedEncryptionOfPromptsIsFinishedOnUnlock(t *testing.T) {
	v := newVault(t)
	f := v.folder("Later", "")
	_, _ = v.g.SavePrompt(secretTitle, secretText, f.ID)
	plainFile := filepath.Join(v.dir, promptsDir, f.ID+".json")
	saved, _ := os.ReadFile(plainFile)
	if err := v.g.EncryptFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
	// simulate a crash after the key was stored but before the plaintext copy was wiped
	_ = os.WriteFile(plainFile, saved, 0o644)
	_ = os.Remove(filepath.Join(v.dir, promptsDir, f.ID+".enc"))
	v.reopen()
	if err := v.g.UnlockFolder(f.ID, goodPass); err != nil {
		t.Fatal(err)
	}
	if got := v.g.Prompts(); len(got) != 1 || got[0].Text != secretText {
		t.Fatalf("the plaintext prompts must be recovered: %+v", got)
	}
	v.assertNoPlaintext("after healing", []byte("SECRET-HARBOR"), []byte(secretTitle))
}

func TestLockExceptKeepsOnlyTheViewedFolderOpen(t *testing.T) {
	v := newVault(t)
	a, b := v.folder("A", "alpha-password-1"), v.folder("B", "bravo-password-2")
	ma, _ := v.add("img a")
	mb, _ := v.add("img b")
	_ = v.g.SetFolder(ma.ID, a.ID)
	_ = v.g.SetFolder(mb.ID, b.ID)
	_, _ = v.g.SavePrompt("a", "prompt in a", a.ID)
	_, _ = v.g.SavePrompt("b", "prompt in b", b.ID)

	v.g.LockExcept(a.ID) // navigated from B to A
	if got := v.g.Prompts(); len(got) != 1 || got[0].Text != "prompt in a" {
		t.Fatalf("prompts: %+v", got)
	}
	if _, err := v.g.ReadImage(ma.ID); err != nil {
		t.Fatalf("A should stay readable: %v", err)
	}
	if _, err := v.g.ReadImage(mb.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("B should be locked: %v", err)
	}
	if got, _ := v.g.Get(mb.ID); !got.Locked || got.Prompt != "" {
		t.Fatalf("B's details must be dropped: %+v", got)
	}
	locked := map[string]bool{}
	for _, f := range v.g.Folders() {
		locked[f.Name] = f.Locked
	}
	if locked["A"] || !locked["B"] {
		t.Fatalf("folder states: %v", locked)
	}
	v.g.LockExcept("") // back to All
	if len(v.g.Prompts()) != 0 {
		t.Fatal("navigating to All must lock everything")
	}
}

func TestLockAllHidesEveryEncryptedPrompt(t *testing.T) {
	v := newVault(t)
	a, b := v.folder("A", "alpha-password-1"), v.folder("B", "bravo-password-2")
	_, _ = v.g.SavePrompt("a", "in a", a.ID)
	_, _ = v.g.SavePrompt("b", "in b", b.ID)
	_, _ = v.g.SavePrompt("n", "in none", "")
	v.g.LockAll()
	if got := v.g.Prompts(); len(got) != 1 || got[0].Text != "in none" {
		t.Fatalf("%+v", got)
	}
}
