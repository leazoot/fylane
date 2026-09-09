package tunnelget

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed pins.txt
var pinsFile string

// A Pin is one publisher's release of one tunnel binary for one target: the
// exact bytes this build of Fylane is willing to run, named by digest rather
// than by version string. Nothing outside this file decides what may be
// downloaded.
type Pin struct {
	// Binary is the program name, as the tunnel provider knows it.
	Binary string
	// Version is the vendor's release tag.
	Version string
	// Asset is the published file name.
	Asset string
	// Archive reports that Asset is a gzipped tar holding the binary rather
	// than the binary itself.
	Archive bool
	// SHA256 is the digest of the published asset, lowercase hex.
	SHA256 string
	// URL is the full download address.
	URL string
	// Base is the publisher's release address without the version or the
	// asset name. It is what the consent screen shows as the source: the
	// version and file name are already on their own lines there, and the
	// digest is the control that makes the exact path uninteresting.
	Base string
}

// hex64 is what a SHA-256 looks like written down. A pin whose digest is
// mistyped must fail here, at parse time, and not at the comparison — a
// truncated digest that never matches is indistinguishable from a tampered
// download, and the two need different answers from whoever reads the error.
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PinFor returns the pin for one binary on one target, or an error naming the
// target when this build has no pin for it. There is deliberately no fallback
// to "some other version": an unpinned target means we do not know what we
// would be running.
func PinFor(binary, goos, goarch string) (Pin, error) {
	pins, err := parsePins(pinsFile)
	if err != nil {
		return Pin{}, err
	}
	p, ok := pins[key(binary, goos, goarch)]
	if !ok {
		return Pin{}, fmt.Errorf("no pinned %s build for %s/%s", binary, goos, goarch)
	}
	return p, nil
}

// Pinned reports whether this build could download binary for the target.
func Pinned(binary, goos, goarch string) bool {
	_, err := PinFor(binary, goos, goarch)
	return err == nil
}

func key(binary, goos, goarch string) string {
	return binary + " " + goos + "/" + goarch
}

func parsePins(text string) (map[string]Pin, error) {
	bases := map[string]string{}
	versions := map[string]string{}
	assets := map[string]Pin{}

	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		lineno := n + 1
		switch {
		case f[0] == "base" && len(f) == 3:
			bases[f[1]] = f[2]
		case f[0] == "version" && len(f) == 3:
			versions[f[1]] = f[2]
		case f[0] == "asset" && len(f) == 6:
			form := f[4]
			if form != "tgz" && form != "raw" {
				return nil, fmt.Errorf("pins.txt:%d: unknown asset form %q", lineno, form)
			}
			if !hex64.MatchString(f[5]) {
				return nil, fmt.Errorf("pins.txt:%d: %s is not a sha-256 digest", lineno, f[5])
			}
			assets[f[1]+" "+f[2]] = Pin{
				Binary:  f[1],
				Asset:   f[3],
				Archive: form == "tgz",
				SHA256:  f[5],
			}
		default:
			return nil, fmt.Errorf("pins.txt:%d: cannot read %q", lineno, line)
		}
	}

	out := make(map[string]Pin, len(assets))
	for k, p := range assets {
		base, ok := bases[p.Binary]
		if !ok {
			return nil, fmt.Errorf("pins.txt: no base url for %s", p.Binary)
		}
		version, ok := versions[p.Binary]
		if !ok {
			return nil, fmt.Errorf("pins.txt: no version for %s", p.Binary)
		}
		p.Version = version
		p.Base = strings.TrimSuffix(base, "/")
		p.URL = p.Base + "/" + version + "/" + p.Asset
		out[k] = p
	}
	return out, nil
}
