package main

// version is set at release time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func (a *App) Version() string { return version }
