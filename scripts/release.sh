#!/usr/bin/env sh
# Builds every distributable artifact for the version in the repo-root
# VERSION file into dist/<version>/, with a SHA256SUMS manifest.
#
#   ./scripts/release.sh                 companion+relay (5 os/arch)
#   FYLANE_BUILD_DESKTOP=1 ...           also build the desktop shell (host OS only;
#                                        needs the wails CLI and native toolchain)
#
# Two shapes, because the two components have different needs:
#
#   relay      a bare binary. It runs on a server, never opens a tunnel, and
#              has no neighbour to arrive with.
#   companion  an archive. tunnelproc looks for the tunnel program in the
#              directory holding the *Companion* executable, so the two have to
#              land in the same place or the carry buys nothing.
#              The same rule is why the desktop packages carry the Core: a
#              shell with no Core beside it has no directory for cloudflared to
#              be found in.
#
# Signing hooks (artifacts stay unsigned until the credentials exist):
#   FYLANE_MAC_SIGN_ID=<identity>        codesign darwin binaries (hardened runtime)
#   FYLANE_WIN_SIGN_CMD=<cmd>            run per .exe: $FYLANE_WIN_SIGN_CMD <file>
# Only Fylane's own binaries are signed. cloudflared keeps the signature
# Cloudflare published it with, which says more than an ad-hoc one of ours; the
# .app is the exception, where a bundle has to be signed as a unit.
# Notarization of the desktop .app bundle happens in the desktop step once an
# Apple Developer account exists (xcrun notarytool; not scripted yet).
set -eu
cd "$(dirname "$0")/.."
repo=$(pwd)

VERSION=$(cat VERSION)
out="dist/$VERSION"
rm -rf "$out"
mkdir -p "$out"
ldflags="-s -w -X github.com/leazoot/fylane/shared/buildinfo.Version=$VERSION"

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

for osarch in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  goos=${osarch%/*}
  goarch=${osarch#*/}
  ext=""
  [ "$goos" = windows ] && ext=".exe"

  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -ldflags "$ldflags" -o "$out/fylane-relay-$goos-$goarch$ext" ./relay/cmd/relay

  pkg="$stage/fylane-companion-$goos-$goarch"
  mkdir -p "$pkg"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -ldflags "$ldflags" -o "$pkg/fylane-companion$ext" ./companion/cmd/companion

  if [ "$goos" = darwin ] && [ -n "${FYLANE_MAC_SIGN_ID:-}" ]; then
    codesign --force --options runtime --timestamp -s "$FYLANE_MAC_SIGN_ID" \
      "$out/fylane-relay-$goos-$goarch" "$pkg/fylane-companion"
  fi
  if [ "$goos" = windows ] && [ -n "${FYLANE_WIN_SIGN_CMD:-}" ]; then
    $FYLANE_WIN_SIGN_CMD "$out/fylane-relay-$goos-$goarch$ext"
    $FYLANE_WIN_SIGN_CMD "$pkg/fylane-companion$ext"
  fi

  # The tunnel program travels with the Companion so a fresh install can
  # publish a tunnel with nothing installed on the machine. Downloading it at
  # runtime (package tunnelget) stays as the fallback for a Companion that
  # arrived without one, not as the normal path. Apache-2.0 requires the
  # licence to travel with the binary.
  ./scripts/fetch-cloudflared.sh "$goos" "$goarch"
  cp "third_party/cloudflared/bin/$goos-$goarch/cloudflared$ext" "$pkg/cloudflared$ext"
  chmod +x "$pkg/cloudflared$ext"
  cp third_party/cloudflared/LICENSE "$pkg/CLOUDFLARED-LICENSE.txt"

  if [ "$goos" = windows ]; then
    # No -y: these archives hold no symlinks, and the zip that Chocolatey
    # ships for the Windows runner rejects the flag outright (exit 16).
    (cd "$stage" && zip -qr "$repo/$out/fylane-companion-$goos-$goarch.zip" "fylane-companion-$goos-$goarch")
  else
    tar -czf "$out/fylane-companion-$goos-$goarch.tar.gz" -C "$stage" "fylane-companion-$goos-$goarch"
  fi
done
if [ -z "${FYLANE_MAC_SIGN_ID:-}" ]; then
  echo "note: darwin binaries unsigned (set FYLANE_MAC_SIGN_ID)" >&2
fi
if [ -z "${FYLANE_WIN_SIGN_CMD:-}" ]; then
  echo "note: windows binaries unsigned (set FYLANE_WIN_SIGN_CMD)" >&2
fi

if [ "${FYLANE_BUILD_DESKTOP:-}" = "1" ]; then
  command -v wails >/dev/null || { echo "error: wails CLI not installed" >&2; exit 1; }
  wailsver=$(node -e "console.log(require('./desktop/wails.json').info.productVersion)")
  if [ "$wailsver" != "$VERSION" ]; then
    echo "error: desktop/wails.json productVersion $wailsver != $VERSION" >&2
    exit 1
  fi
  # wails builds for the host only, so the pieces the package needs are the
  # host's — and they exist only if the host is one of the targets above.
  hostos=$(go env GOOS)
  hostarch=$(go env GOARCH)
  hostext=""
  [ "$hostos" = windows ] && hostext=".exe"
  src="$stage/fylane-companion-$hostos-$hostarch"
  if [ ! -d "$src" ]; then
    echo "error: no companion build for host $hostos/$hostarch; add it to the target list" >&2
    exit 1
  fi
  (cd desktop && wails build -clean -trimpath -ldflags "$ldflags")

  if [ "$hostos" = darwin ]; then
    # Wails names the bundle after the project; users install "Fylane
    # Companion.app". The bundle directory name is display-only.
    if [ -d desktop/build/bin/desktop.app ]; then
      rm -rf "desktop/build/bin/Fylane Companion.app"
      mv desktop/build/bin/desktop.app "desktop/build/bin/Fylane Companion.app"
    fi
    app="desktop/build/bin/Fylane Companion.app"
    [ -d "$app" ] || { echo "error: wails produced no .app in desktop/build/bin" >&2; exit 1; }
    # Self-contained install: the Core ships inside the bundle, where the
    # shell's auto-start looks first (next to its own executable), and
    # cloudflared beside the Core, where tunnelproc looks first. Signing must
    # happen after this mutation, and covers cloudflared too because a bundle
    # is signed as a unit — the one place we re-sign someone else's binary.
    cp "$src/fylane-companion" "$app/Contents/MacOS/fylane-companion"
    cp "$src/cloudflared" "$app/Contents/MacOS/cloudflared"
    chmod +x "$app/Contents/MacOS/fylane-companion" "$app/Contents/MacOS/cloudflared"
    cp third_party/cloudflared/LICENSE "$app/Contents/Resources/CLOUDFLARED-LICENSE.txt"
    if [ -n "${FYLANE_MAC_SIGN_ID:-}" ]; then
      codesign --force --options runtime --timestamp -s "$FYLANE_MAC_SIGN_ID" "$app/Contents/MacOS/fylane-companion"
      codesign --force --options runtime --timestamp -s "$FYLANE_MAC_SIGN_ID" "$app/Contents/MacOS/cloudflared"
      codesign --force --options runtime --timestamp -s "$FYLANE_MAC_SIGN_ID" "$app"
    else
      codesign --force -s - "$app/Contents/MacOS/fylane-companion" "$app/Contents/MacOS/cloudflared" "$app" 2>/dev/null || true
    fi
    # Archive the bundle so the checksum manifest only ever lists flat files.
    (cd desktop/build/bin && zip -qry "../../../$out/fylane-desktop-macos.zip" "Fylane Companion.app")
    # Drag-install DMG (unsigned until credentials exist; notarization is
    # wired here once an Apple Developer account is available).
    staging=$(mktemp -d)
    cp -R "$app" "$staging/"
    ln -s /Applications "$staging/Applications"
    hdiutil create -quiet -volname "Fylane Companion" -srcfolder "$staging" -ov -format UDZO "$out/fylane-desktop-macos.dmg"
    rm -rf "$staging"
  else
    # Windows and Linux have no bundle to hide the pieces in, so the package is
    # a directory: shell, Core, tunnel program, licence. The three executables
    # sit together because that is the only arrangement in which the shell
    # finds the Core and the Core finds cloudflared.
    shell=""
    for f in desktop/build/bin/*; do
      [ -f "$f" ] || continue
      [ -n "$shell" ] && { echo "error: more than one file in desktop/build/bin; cannot tell which is the shell" >&2; exit 1; }
      shell=$f
    done
    [ -n "$shell" ] || { echo "error: wails produced no shell binary in desktop/build/bin" >&2; exit 1; }
    name="fylane-desktop-$hostos-$hostarch"
    pkgd="$stage/$name"
    mkdir -p "$pkgd"
    if [ "$hostos" = windows ]; then
      cp "$shell" "$pkgd/Fylane Companion.exe"
    else
      cp "$shell" "$pkgd/fylane-desktop"
    fi
    cp "$src/fylane-companion$hostext" "$src/cloudflared$hostext" "$pkgd/"
    cp third_party/cloudflared/LICENSE "$pkgd/CLOUDFLARED-LICENSE.txt"
    if [ "$hostos" = windows ]; then
      (cd "$stage" && zip -qr "$repo/$out/$name.zip" "$name")
    else
      tar -czf "$out/$name.tar.gz" -C "$stage" "$name"
    fi
  fi
fi

(cd "$out" && shasum -a 256 -- * > SHA256SUMS)
echo "release $VERSION:"
ls -l "$out"
