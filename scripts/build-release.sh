#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
version="${VERSION:-0.2.0}"
out="${OUTPUT_DIR:-$root/build/release}"
mkdir -p "$out"

build_one() {
  local goos="$1" goarch="$2" ext=""
  if [[ "$goos" == "windows" ]]; then ext=".exe"; fi
  echo "Building mailsend for ${goos}/${goarch}"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
    -trimpath -ldflags "-s -w -X github.com/example-account/mailsend/internal/cli.version=${version}" \
    -o "$out/mailsend-${goos}-${goarch}${ext}" ./cmd/mailsend
}

if [[ "$#" -eq 2 ]]; then
  build_one "$1" "$2"
else
  build_one darwin arm64
  build_one darwin amd64
  build_one linux arm64
  build_one linux amd64
  build_one windows amd64
  build_one windows arm64
fi
