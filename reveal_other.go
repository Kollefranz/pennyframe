//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

func reveal(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Start()
	}
	return openDir(filepath.Dir(path))
}

func openDir(dir string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", dir).Start()
	}
	return exec.Command("xdg-open", dir).Start()
}
