//go:build !devserver

package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"pennyframe/internal/store"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	base, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	cfgDir := filepath.Join(base, "Pennyframe")
	if d := os.Getenv("PENNYFRAME_DATA_DIR"); d != "" {
		cfgDir = d // isolated profile for testing; images default to <dir>/images
	}
	cfg, err := store.LoadConfig(cfgDir, store.OSSecrets())
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	if os.Getenv("PENNYFRAME_DATA_DIR") != "" && cfg.OutputDir() == store.DefaultOutputDir() {
		_ = cfg.SetOutputDir(filepath.Join(cfgDir, "images"))
	}
	gal, err := store.OpenGallery(cfg.OutputDir(), filepath.Join(cfgDir, "thumbs"))
	if err != nil {
		log.Fatalf("opening gallery %s: %v", cfg.OutputDir(), err)
	}
	app := NewApp(cfg, gal)

	err = wails.Run(&options.App{
		Title:            "Pennyframe",
		Width:            1380,
		Height:           880,
		MinWidth:         960,
		MinHeight:        620,
		BackgroundColour: &options.RGBA{R: 20, G: 20, B: 23, A: 255},
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: imageHandler(app),
		},
		OnStartup:                app.startup,
		Bind:                     []any{app},
		EnableDefaultContextMenu: true,
		Windows: &windows.Options{
			Theme: windows.SystemDefault,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
