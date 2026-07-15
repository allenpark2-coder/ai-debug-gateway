#!/usr/bin/env bash
# build-release.sh VERSION
#
# Produces reproducible, checksummed Linux release archives for
# ai-debug-gateway under dist/. Refuses to run against a dirty working
# tree so every archive can be traced back to an exact commit.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 VERSION" >&2
  exit 2
fi
version="$1"

repo_root="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
cd "$repo_root"

if [ -n "$(git status --porcelain)" ]; then
  echo "build-release: working tree is not clean; commit or stash changes first" >&2
  git status --short >&2
  exit 1
fi

commit="$(git rev-parse --short HEAD)"
dist="$repo_root/dist"
rm -rf "$dist"
mkdir -p "$dist"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

ldflags="-X main.version=$version -X main.commit=$commit -s -w"

for arch in amd64 arm64; do
  name="ai-debug-gateway_${version}_linux_${arch}"
  build_dir="$work_dir/$arch"
  mkdir -p "$build_dir/docs"

  echo "build-release: building $name"
  for cmd in gateway gatewayd; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "$ldflags" \
      -o "$build_dir/$cmd" "./cmd/$cmd"
  done

  cp README.md "$build_dir/"
  cp docs/uart-operator-guide.md docs/ssh-operator-guide.md "$build_dir/docs/"

  archive="${name}.tar.gz"
  tar -czf "$dist/$archive" -C "$build_dir" gateway gatewayd README.md docs
  ( cd "$dist" && sha256sum "$archive" > "${name}.sha256" )
done

echo "build-release: done"
ls -la "$dist"
