#!/usr/bin/env bash
# Builds a universal (Apple silicon + Intel) Pennyframe.app and zips it into dist/. Run on a Mac.
#   ./scripts/package-macos.sh 0.3.0
set -euo pipefail
cd "$(dirname "$0")/.."

version="${1:?usage: package-macos.sh <version>}"
command -v wails >/dev/null || { echo "wails CLI not found: go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0" >&2; exit 1; }

wails build -s -clean -platform darwin/universal -ldflags "-X main.version=${version}"

built="$(find build/bin -maxdepth 1 -name '*.app' | head -n 1)"
[ -n "$built" ] || { echo "no .app produced in build/bin" >&2; exit 1; }
# A fixed, capitalised name is what the Homebrew cask and Finder show.
app="build/bin/Pennyframe.app"
[ "$built" = "$app" ] || { rm -rf "$app"; mv "$built" "$app"; }

# Apple silicon refuses unsigned code; an ad-hoc signature satisfies that (it is not a Developer ID signature).
codesign --force --deep --sign - "$app"
codesign --verify --deep --strict "$app"

mkdir -p dist
zip="dist/pennyframe-${version}-macos-universal.zip"
rm -f "$zip"
# ditto keeps the bundle's symlinks, permissions and signature intact (plain zip does not).
ditto -c -k --sequesterRsrc --keepParent "$app" "$zip"
echo "Built $zip ($(basename "$app"))"
