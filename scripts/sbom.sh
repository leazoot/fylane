#!/usr/bin/env sh
# Generates CycloneDX SBOMs for every distributable component into tmp/sbom/.
# Tool versions are pinned so the output is reproducible for a given commit.
set -eu
cd "$(dirname "$0")/.."
out=tmp/sbom
mkdir -p "$out"

gomod="github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.10.0"

go run "$gomod" mod -json -licenses -output "$out/companion-relay.cdx.json" .
go run "$gomod" mod -json -licenses -output "$out/desktop.cdx.json" ./desktop

# JS component: only production dependencies ship in the bundle, so the
# SBOM lists the prod closure from the lockfile-resolved tree.
(cd desktop/frontend && npm ls --omit=dev --all --json) | jq '
  def deps: (.dependencies // {}) | to_entries[]
    | {name: .key, version: .value.version}, (.value | deps);
  [deps] | unique
  | {bomFormat: "CycloneDX", specVersion: "1.5", version: 1,
     metadata: {component: {type: "application", name: "fylane-desktop-frontend"}},
     components: map({type: "library", name, version,
                      purl: "pkg:npm/\(.name)@\(.version)"})}
' > "$out/desktop-frontend.cdx.json"

# cloudflared ships inside the desktop bundle but is neither a Go module
# nor an npm package, so nothing above sees it. The version is read from the
# fetch script so the pin has one home, not two.
cfver=$(sed -n 's/^VERSION=//p' scripts/fetch-cloudflared.sh)
[ -n "$cfver" ] || { echo "error: no cloudflared pin in scripts/fetch-cloudflared.sh" >&2; exit 1; }
# The bundled typefaces are the same case as cloudflared: vendored files that
# ship inside the desktop bundle and belong to no package manager, so neither
# generator above can see them.
#
# They are listed by what is actually verifiable — the SHA-256 of the file that
# ships, and the licence text that travels beside it. Upstream release numbers
# are deliberately not invented: nothing in this repository records one, and a
# wrong version in an SBOM is worse than no version, because it makes an
# advisory match on a release that was never shipped and miss the one that was.
fonts=desktop/frontend/src/assets/fonts
font_component() { # name, licence-file-stem, woff2
  [ -f "$fonts/$2-LICENSE.txt" ] || {
    echo "error: $1 ships in the desktop bundle with no licence file beside it" >&2
    exit 1
  }
  [ -f "$fonts/$3" ] || {
    echo "error: $1 is listed in the SBOM but $fonts/$3 is not there to ship" >&2
    exit 1
  }
  jq -n --arg n "$1" --arg f "$3" --arg h "$(shasum -a 256 "$fonts/$3" | cut -d" " -f1)" \
    '{type: "file", name: $n, licenses: [{license: {id: "OFL-1.1"}}],
      hashes: [{alg: "SHA-256", content: $h}],
      properties: [{name: "fylane:bundled-as", value: $f}]}'
}

jq -n --arg v "$cfver" \
  --argjson geist "$(font_component 'Geist Mono' GEIST GeistMono-Regular.woff2)" \
  --argjson geistmed "$(font_component 'Geist Mono' GEIST GeistMono-Medium.woff2)" \
  --argjson news "$(font_component Newsreader NEWSREADER Newsreader-Variable.woff2)" \
  '{bomFormat: "CycloneDX", specVersion: "1.5", version: 1,
  metadata: {component: {type: "application", name: "fylane-desktop-bundled"}},
  components: [{type: "application", name: "cloudflared", version: $v,
                licenses: [{license: {id: "Apache-2.0"}}],
                purl: "pkg:github/cloudflare/cloudflared@\($v)"},
               $geist, $geistmed, $news]}' \
  > "$out/desktop-bundled.cdx.json"

ls -l "$out"
