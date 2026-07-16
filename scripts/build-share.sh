#!/usr/bin/env bash
# build-share.sh VERSION
#
# Produces self-contained "share bundles" under dist/: the two static
# binaries plus share/ (setup.sh, templates, quickstart README), one
# tarball per architecture. A colleague unpacks one and runs ./setup.sh.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 VERSION" >&2
  exit 2
fi
version="$1"

repo_root="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
cd "$repo_root"

commit="$(git rev-parse --short HEAD)"
dist="$repo_root/dist"
mkdir -p "$dist"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

for arch in amd64 arm64; do
  name="ai-debug-gateway-share_${version}_linux_${arch}"
  stage="$work_dir/$name"
  mkdir -p "$stage"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version -X main.commit=$commit" \
    -o "$stage/gatewayd" ./cmd/gatewayd
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version -X main.commit=$commit" \
    -o "$stage/gateway" ./cmd/gateway
  cp -r share/setup.sh share/templates share/README.md "$stage/"
  tar -C "$work_dir" --sort=name --owner=0 --group=0 --numeric-owner \
    --mtime="@0" -czf "$dist/$name.tar.gz" "$name"
  (cd "$dist" && sha256sum "$name.tar.gz" > "$name.sha256")
  echo "built: dist/$name.tar.gz"
done
