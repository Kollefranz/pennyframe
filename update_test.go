package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestNewerThan(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.2.0", "0.1.1", true},
		{"v0.10.0", "0.9.9", true}, // numeric, not lexical
		{"1.0.0", "0.99.99", true},
		{"0.2.0", "0.2.0", false},
		{"0.1.0", "0.2.0", false},
		{"0.2.1", "dev", false}, // dev builds never nag
		{"garbage", "0.1.0", false},
		{"0.3.0-rc1", "0.2.0", true},
	}
	for _, c := range cases {
		if got := newerThan(c.latest, c.current); got != c.want {
			t.Errorf("newerThan(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestDetectInstall(t *testing.T) {
	none := func(string) bool { return false }
	only := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, x := range paths {
				if x == p {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		name, goos, exe string
		exists          func(string) bool
		want            string
	}{
		{"scoop", "windows", `C:\Users\me\scoop\apps\pennyframe\current\pennyframe.exe`, none, "scoop"},
		{"scoop versioned dir", "windows", `C:\Users\me\scoop\apps\pennyframe\0.2.0\pennyframe.exe`, none, "scoop"},
		{"installer", "windows", `C:\Users\me\AppData\Local\Programs\Pennyframe\pennyframe.exe`, only(`C:\Users\me\AppData\Local\Programs\Pennyframe\unins000.exe`), "installer"},
		{"portable exe", "windows", `C:\Tools\pennyframe.exe`, none, "manual"},
		{"brew arm", "darwin", "/Applications/pennyframe.app/Contents/MacOS/pennyframe", only("/opt/homebrew/Caskroom/pennyframe"), "brew"},
		{"brew intel", "darwin", "/Applications/pennyframe.app/Contents/MacOS/pennyframe", only("/usr/local/Caskroom/pennyframe"), "brew"},
		{"mac dragged to Applications", "darwin", "/Applications/pennyframe.app/Contents/MacOS/pennyframe", none, "manual"},
		{"linux", "linux", "/usr/bin/pennyframe", none, "manual"},
	}
	for _, c := range cases {
		if got := detectInstall(c.goos, c.exe, c.exists); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestCheckForUpdateReadsRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("GitHub requires a User-Agent")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v9.9.9",
			"html_url": "https://example.test/release",
			"body":     "What's new",
			"assets": []map[string]string{
				{"name": "pennyframe-9.9.9-windows-amd64.zip", "browser_download_url": "https://example.test/zip"},
				{"name": "pennyframe-9.9.9-windows-setup.exe", "browser_download_url": "https://example.test/setup.exe"},
				{"name": "pennyframe-9.9.9-macos-universal.zip", "browser_download_url": "https://example.test/mac.zip"},
			},
		})
	}))
	defer srv.Close()
	t.Setenv("PENNYFRAME_UPDATE_URL", srv.URL)

	old := version
	version = "0.1.0"
	defer func() { version = old }()

	info, err := (&App{}).CheckForUpdate()
	if err != nil {
		t.Fatal(err)
	}
	if !info.Available || info.Latest != "9.9.9" || info.Current != "0.1.0" || info.Page != "https://example.test/release" || info.Notes != "What's new" {
		t.Fatalf("unexpected info: %+v", info)
	}
	switch runtime.GOOS {
	case "windows":
		if info.Download != "https://example.test/setup.exe" {
			t.Fatalf("windows should get the installer, got %q", info.Download)
		}
	case "darwin":
		if info.Download != "https://example.test/mac.zip" {
			t.Fatalf("mac should get the app zip, got %q", info.Download)
		}
	}
}

func TestCheckForUpdateHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	t.Setenv("PENNYFRAME_UPDATE_URL", srv.URL)
	if _, err := (&App{}).CheckForUpdate(); err == nil {
		t.Fatal("expected an error for a non-200 answer")
	}
}
