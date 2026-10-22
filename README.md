# Pennyframe

Fast desktop app for generating, saving and browsing images with [OpenRouter](https://openrouter.ai) image models. Go backend, [Wails v2](https://wails.io) shell (WebView2), plain HTML/CSS/JS frontend — no Node toolchain, no CGO.

## Install (Scoop)

This repo is also a Scoop bucket.

```powershell
scoop bucket add pennyframe https://github.com/Kollefranz/pennyframe
scoop install pennyframe/pennyframe
```

That puts Pennyframe in the Start Menu and on the desktop. Update with `scoop update pennyframe`; remove with `scoop uninstall pennyframe` (your images and settings are kept). Requires the Microsoft Edge WebView2 runtime, which is built into Windows 11.

## Use it

1. Run `build/bin/pennyframe.exe`.
2. Click **Connect with OpenRouter**. Your browser opens on OpenRouter where you sign in (or create an account) and click Authorize; Pennyframe receives a key for your account (OpenRouter's OAuth PKCE flow with a one-time localhost callback — Pennyframe never sees your password). Already have a key? Use "I already have an API key". Keys are kept in the OS credential vault (Windows Credential Manager), never in a file, and you can add as many as you like.
3. Pick a model, write a prompt, **Ctrl+Enter**.

| Shortcut | Action |
| --- | --- |
| `Ctrl+K` | Model palette (search, ★ pin, tick boxes to compare) |
| `Ctrl+Enter` | Generate |
| `Alt+1…9` | Switch API key |
| `Ctrl+Shift+K` | Open the key menu (switch, add, details) |
| `←` `→` `F` | Previous / next / favorite in the viewer |
| `Ctrl`/`Shift`+click | Select several images (Shift = range); then Favorite, Archive or Delete them |
| `Ctrl+A` · `Esc` · `Del` | Select all · clear selection · delete selection |

**Quick model switching:** pinned (★) and recent models sit in the top bar; click to switch, Ctrl/Shift-click to add to a comparison. With several models selected, one prompt runs on all of them in parallel and the results land side by side.

**Live previews.** Models that support streaming show partial renders on the card as they arrive (toggle under the settings; single-image requests only). If a provider rejects streaming, the request is retried normally.

**Value chart.** In the model picker, *Value chart* plots each model's price per image against its image-arena Elo rating (Design Arena data, fetched through OpenRouter's `/benchmarks` endpoint with your key and cached for a day). Models that nothing beats on both price and rating are marked *Best value*, and one click selects a spread of them to compare. Prices you actually paid replace list prices. Only models with a flat per-image price can be plotted; token- or size-billed models appear once you've generated with them. If OpenRouter returns no ratings, the chart and rating sorts stay hidden.

**Settings adapt to the model.** Aspect ratio, resolution, quality, format, background, count, seed and compression controls come from each model's capabilities (`/api/v1/images/models`). Anything a model can't do is dropped with a note rather than failing the request.

**Archive** hides images from the main view but keeps them (and their metadata) on disk; open **Archived** in the toolbar to see or restore them. Delete removes the files permanently after a confirmation.

Images are saved to `~/Pictures/Pennyframe` (changeable) as the original bytes plus a `.json` sidecar (prompt, model, params, cost, time, key name). Thumbnails are cached under `%APPDATA%\Pennyframe\thumbs`. Reference images (image-to-image) can be dropped, pasted, or taken from the gallery.

## Build

```bash
go test ./...
go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0
wails build -s          # icon + manifest → build/bin/pennyframe.exe
```

Plain `go build -tags desktop,production -ldflags "-H windowsgui"` also works but skips the icon/manifest.

## Releasing

```bash
git tag v0.2.0 && git push origin v0.2.0
```

The `release` workflow builds the exe, publishes `pennyframe-<version>-windows-amd64.zip` (+ `.sha256`) as a GitHub release, and commits the new version and hash to `bucket/pennyframe.json` on `main`. After that, `scoop update` picks it up. To build the same package locally: `./scripts/release.ps1 -Version 0.2.0` (needs PowerShell 7 and the wails CLI); it rewrites the manifest too, so don't commit that unless you're also publishing that exact zip.

## Development

- `OPENROUTER_BASE_URL` / `OPENROUTER_AUTH_URL` point the client at another API / sign-in page (proxy, mock).
- `PENNYFRAME_DATA_DIR` uses an isolated profile (config, thumbs, `images/`).
- `go run -tags devserver .` serves the UI at http://127.0.0.1:8800 over a small RPC bridge so it can be driven in a normal browser (requires `PENNYFRAME_DATA_DIR`). File dialogs are not available there.

## Layout

```
main.go, app.go      Wails entry; bound methods (keys, models, jobs, gallery)
request.go           per-model parameter adaptation
handler.go           /img/<id> and /thumb/<id> served straight from disk
internal/openrouter  API client (models, endpoints/pricing, images, key info)
internal/store       config, keyring secrets, gallery + thumbnails
frontend/            index.html, style.css, app.js
```
