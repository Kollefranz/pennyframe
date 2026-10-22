package main

import (
	"os/exec"
	"syscall"
)

// explorer needs /select,"path" verbatim; Go's default quoting breaks on spaces.
func reveal(path string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	_ = cmd.Start() // explorer returns exit status 1 even on success
	return nil
}

func openDir(dir string) error {
	return exec.Command("explorer.exe", dir).Start()
}
