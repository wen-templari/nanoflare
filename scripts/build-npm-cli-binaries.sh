#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

build() {
  goos=$1
  goarch=$2
  package_dir=$3
  output=$4

  mkdir -p "$ROOT/packages/cli/npm/$package_dir/bin"
  GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -trimpath -o "$ROOT/packages/cli/npm/$package_dir/bin/$output" "$ROOT/cmd/nanoflare"
}

build darwin arm64 darwin-arm64 nanoflare
build darwin amd64 darwin-x64 nanoflare
build linux arm64 linux-arm64 nanoflare
build linux amd64 linux-x64 nanoflare
build windows arm64 win32-arm64 nanoflare.exe
build windows amd64 win32-x64 nanoflare.exe
