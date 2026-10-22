package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const releasesURL = "https://api.github.com/repos/Kollefranz/pennyframe/releases/latest"

// UpdateInfo describes the newest release and how this install should be updated.
type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Method    string `json:"method"`   // scoop | brew | installer | manual
	Command   string `json:"command"`  // terminal command for scoop / brew
	Download  string `json:"download"` // direct download for installer / manual installs
	Page      string `json:"page"`     // release notes page
	Notes     string `json:"notes"`
}

type ghRelease struct {
	Tag    string `json:"tag_name"`
	URL    string `json:"html_url"`
	Body   string `json:"body"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// CheckForUpdate asks GitHub for the latest release and compares it with this build.
// It only reads public data and never downloads or installs anything.
func (a *App) CheckForUpdate() (UpdateInfo, error) {
	exe, _ := os.Executable()
	info := UpdateInfo{Current: version, Method: detectInstall(runtime.GOOS, exe, pathExists)}

	url := releasesURL
	if v := os.Getenv("PENNYFRAME_UPDATE_URL"); v != "" {
		url = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Pennyframe/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return info, err
	}
	if rel.Tag == "" {
		return info, errors.New("no release found")
	}

	info.Latest = strings.TrimPrefix(rel.Tag, "v")
	info.Page = rel.URL
	info.Notes = strings.TrimSpace(rel.Body)
	if r := []rune(info.Notes); len(r) > 700 {
		info.Notes = string(r[:700]) + "…"
	}
	info.Available = newerThan(info.Latest, version)
	switch info.Method {
	case "scoop":
		info.Command = "scoop update pennyframe"
	case "brew":
		info.Command = "brew update && brew upgrade --cask pennyframe"
	}
	names := make([]string, len(rel.Assets))
	for i, as := range rel.Assets {
		names[i] = as.Name
	}
	if want := assetSuffix(runtime.GOOS); want != "" {
		for _, as := range rel.Assets {
			if strings.HasSuffix(as.Name, want) {
				info.Download = as.URL
			}
		}
	}
	return info, nil
}

// detectInstall works out how the running copy was installed, so the update advice matches.
func detectInstall(goos, exe string, exists func(string) bool) string {
	switch goos {
	case "windows":
		if strings.Contains(strings.ToLower(strings.ReplaceAll(exe, "\\", "/")), "/scoop/apps/pennyframe/") {
			return "scoop"
		}
		if exists(filepath.Join(filepath.Dir(exe), "unins000.exe")) { // written by the Inno Setup installer
			return "installer"
		}
	case "darwin":
		if exists("/opt/homebrew/Caskroom/pennyframe") || exists("/usr/local/Caskroom/pennyframe") {
			return "brew"
		}
	}
	return "manual"
}

// assetSuffix is the release asset a person on this OS should download by hand.
func assetSuffix(goos string) string {
	switch goos {
	case "windows":
		return "-windows-setup.exe"
	case "darwin":
		return "-macos-universal.zip"
	}
	return ""
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// parseSemver reads "1.2.3" or "v1.2.3" (ignoring any -suffix); "dev" and other text are not versions.
func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

// newerThan reports whether latest is a higher version than current. Unparseable (dev) builds never update.
func newerThan(latest, current string) bool {
	l, ok1 := parseSemver(latest)
	c, ok2 := parseSemver(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}
