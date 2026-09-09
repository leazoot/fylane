#!/usr/bin/env sh
# Fetches the pinned cloudflared build for one target into
# third_party/cloudflared/bin/<goos>-<goarch>/, verified against the pinned
# checksum. The desktop bundle ships this binary next to the Core so a fresh
# install can publish a tunnel without the user installing anything first
# . This runs at build time on a developer or CI
# machine; the runtime path for a package that carried no binary is package
# tunnelget, which asks the user first and checks the same digest.
#
#   ./scripts/fetch-cloudflared.sh            # host target
#   ./scripts/fetch-cloudflared.sh darwin arm64
#
# The version and the checksums are NOT in this file. They live in
# companion/internal/tunnelget/pins.txt, which the Companion also reads at
# runtime. Two copies of a checksum is two chances
# to raise one of them and forget the other, and the copy that gets forgotten
# is the one nobody runs until the day it matters.
#
# Raising the pin: edit pins.txt, refresh third_party/cloudflared/LICENSE if
# upstream changed it, and re-run this for every target.
set -eu
cd "$(dirname "$0")/.."

PINS=companion/internal/tunnelget/pins.txt

goos=${1:-$(go env GOOS)}
goarch=${2:-$(go env GOARCH)}

pin() { awk -v k="$1" -v b="$2" -v t="${3:-}" '$1==k && $2==b && (k!="asset" || $3==t)' "$PINS"; }

BASE=$(pin base cloudflared | awk '{print $3}')
VERSION=$(pin version cloudflared | awk '{print $3}')
if [ -z "$BASE" ] || [ -z "$VERSION" ]; then
  echo "error: no base url or version for cloudflared in $PINS" >&2
  exit 1
fi

# The checksum is of the published asset, taken from the release metadata for
# this exact tag. A mismatch aborts the build; it is never a warning.
line=$(pin asset cloudflared "$goos/$goarch")
if [ -z "$line" ]; then
  echo "error: no pinned cloudflared for $goos/$goarch in $PINS" >&2
  exit 1
fi
asset=$(echo "$line" | awk '{print $4}')
form=$(echo "$line" | awk '{print $5}')
sum=$(echo "$line" | awk '{print $6}')

# Not vendor/: a top-level vendor directory makes the Go toolchain treat the
# repo as vendored and fail every build.
dest="third_party/cloudflared/bin/$goos-$goarch"
bin="cloudflared"
[ "$goos" = windows ] && bin="cloudflared.exe"

# The stamp records which pin produced the binary that is already there. The
# extracted binary cannot be checked against the asset checksum (the darwin
# assets are archives), so the stamp is what makes re-runs cheap.
if [ -f "$dest/.version" ] && [ -f "$dest/$bin" ] &&
  [ "$(cat "$dest/.version")" = "$VERSION" ]; then
  echo "cloudflared $VERSION already vendored for $goos/$goarch"
  exit 0
fi

sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    sha256sum "$1" | cut -d' ' -f1
  fi
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "fetching cloudflared $VERSION for $goos/$goarch"
curl -fsSL --proto '=https' --tlsv1.2 -o "$tmp/$asset" "$BASE/$VERSION/$asset"

got=$(sha256 "$tmp/$asset")
if [ "$got" != "$sum" ]; then
  echo "error: $asset checksum mismatch" >&2
  echo "  want $sum" >&2
  echo "  got  $got" >&2
  exit 1
fi

case "$form" in
tgz) tar -xzf "$tmp/$asset" -C "$tmp" cloudflared ;;
*) mv "$tmp/$asset" "$tmp/$bin" ;;
esac

mkdir -p "$dest"
mv "$tmp/$bin" "$dest/$bin"
chmod +x "$dest/$bin"
echo "$VERSION" >"$dest/.version"
echo "vendored $dest/$bin"
