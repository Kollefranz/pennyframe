package store

import (
	"strings"
	"testing"
)

func TestFoldersPersistAndFileImages(t *testing.T) {
	dir := t.TempDir()
	g, _ := OpenGallery(dir, t.TempDir())
	a, _ := g.Add(Meta{Prompt: "a"}, pngBytes(10, 10, 255))
	b, _ := g.Add(Meta{Prompt: "b"}, pngBytes(10, 10, 255))
	f, err := g.CreateFolder("  Cats   and dogs ")
	if err != nil || f.Name != "Cats and dogs" {
		t.Fatalf("folder=%+v err=%v", f, err)
	}
	if err := g.SetFolder(a.ID, f.ID); err != nil {
		t.Fatal(err)
	}

	g2, err := OpenGallery(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if fs := g2.Folders(); len(fs) != 1 || fs[0].ID != f.ID {
		t.Fatalf("folders not reloaded: %+v", fs)
	}
	ga, _ := g2.Get(a.ID)
	gb, _ := g2.Get(b.ID)
	if ga.Folder != f.ID || gb.Folder != "" {
		t.Fatalf("folder membership wrong: a=%q b=%q", ga.Folder, gb.Folder)
	}
	if len(g2.List()) != 2 {
		t.Fatal("folders file must not show up as an image")
	}
}

func TestFolderNamesAreValidated(t *testing.T) {
	g, _ := OpenGallery(t.TempDir(), t.TempDir())
	if _, err := g.CreateFolder("Mood"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", "mood", " MOOD ", strings.Repeat("x", 61)} {
		if _, err := g.CreateFolder(bad); err == nil {
			t.Errorf("name %q should be rejected", bad)
		}
	}
	other, _ := g.CreateFolder("Other")
	if err := g.RenameFolder(other.ID, "mood"); err == nil {
		t.Error("renaming onto an existing name should fail")
	}
	if err := g.RenameFolder(other.ID, "OTHER"); err != nil {
		t.Errorf("changing only the case of its own name should work: %v", err)
	}
	if err := g.RenameFolder("nope", "x"); err == nil {
		t.Error("unknown folder should fail")
	}
}

func TestDeleteFolderKeepsImages(t *testing.T) {
	dir := t.TempDir()
	g, _ := OpenGallery(dir, t.TempDir())
	a, _ := g.Add(Meta{}, pngBytes(10, 10, 255))
	f, _ := g.CreateFolder("Temp")
	_ = g.SetFolder(a.ID, f.ID)
	if err := g.DeleteFolder(f.ID); err != nil {
		t.Fatal(err)
	}
	g2, _ := OpenGallery(dir, t.TempDir())
	got, ok := g2.Get(a.ID)
	if !ok || got.Folder != "" || len(g2.Folders()) != 0 {
		t.Fatalf("image=%+v ok=%v folders=%v", got, ok, g2.Folders())
	}
	if err := g.DeleteFolder(f.ID); err == nil {
		t.Error("deleting twice should fail")
	}
}

func TestSetFolderRejectsUnknownFolderAndCanClear(t *testing.T) {
	g, _ := OpenGallery(t.TempDir(), t.TempDir())
	a, _ := g.Add(Meta{}, pngBytes(10, 10, 255))
	if err := g.SetFolder(a.ID, "ghost"); err == nil {
		t.Fatal("filing into a missing folder should fail")
	}
	f, _ := g.CreateFolder("F")
	_ = g.SetFolder(a.ID, f.ID)
	if err := g.SetFolder(a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := g.Get(a.ID); got.Folder != "" {
		t.Fatalf("folder not cleared: %q", got.Folder)
	}
}

func TestFoldersSortedByName(t *testing.T) {
	g, _ := OpenGallery(t.TempDir(), t.TempDir())
	for _, n := range []string{"banana", "Apple", "cherry"} {
		_, _ = g.CreateFolder(n)
	}
	var got []string
	for _, f := range g.Folders() {
		got = append(got, f.Name)
	}
	if strings.Join(got, ",") != "Apple,banana,cherry" {
		t.Fatalf("order = %v", got)
	}
}
