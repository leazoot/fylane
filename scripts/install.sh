#!/bin/sh
# Installs the Fylane companion from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/leazoot/fylane/main/scripts/install.sh | sh
#
# What it does, in order: work out the platform, download the release
# archive and the checksum manifest, refuse anything whose SHA-256 does not
# match, unpack, and put `fylane-companion` on PATH. The archive is kept
# whole because the tunnel program travels beside the binary and the
# Companion looks for it there; only a symlink goes into the bin directory.
#
#   FYLANE_VERSION=v0.0.3   install that tag instead of the latest
#   FYLANE_HOME=~/.fylane   where the archive is unpacked
#   FYLANE_BIN=~/.local/bin where the symlink goes
set -eu

repo=leazoot/fylane
home=${FYLANE_HOME:-$HOME/.fylane}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$os" in
  linux|darwin) ;;
  *) echo "fylane: no command-line build for $os; download the desktop app from https://github.com/$repo/releases" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "fylane: no build for $arch" >&2; exit 1 ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  echo "fylane: need curl or wget" >&2; exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  digest() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  digest() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  echo "fylane: need sha256sum or shasum to verify the download" >&2; exit 1
fi

if [ -n "${FYLANE_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/$FYLANE_VERSION"
else
  base="https://github.com/$repo/releases/latest/download"
fi
name="fylane-companion-$os-$arch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading $name"
fetch "$base/$name.tar.gz" "$tmp/$name.tar.gz"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"

want=$(grep " $name.tar.gz\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
got=$(digest "$tmp/$name.tar.gz")
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "fylane: checksum mismatch for $name.tar.gz; not installing" >&2
  exit 1
fi

mkdir -p "$home"
rm -rf "$home/$name"
tar -xzf "$tmp/$name.tar.gz" -C "$home"
chmod +x "$home/$name/fylane-companion"

# The bin directory: the system one when it can be written to, the user's
# otherwise. Neither needs sudo to be asked for by a script.
bin=${FYLANE_BIN:-}
if [ -z "$bin" ]; then
  if [ -w /usr/local/bin ]; then bin=/usr/local/bin; else bin="$HOME/.local/bin"; fi
fi
mkdir -p "$bin"
ln -sf "$home/$name/fylane-companion" "$bin/fylane-companion"

version=$("$home/$name/fylane-companion" version 2>/dev/null || echo "fylane-companion")
echo "installed $version to $bin"
case ":$PATH:" in
  *":$bin:"*) ;;
  *) echo "add it to PATH first:  export PATH=\"$bin:\$PATH\"" ;;
esac
echo
echo "next: cd into a project and run  fylane-companion share"
