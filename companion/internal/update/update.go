// Package update implements the Beta-phase update check: fetch a tiny
// version manifest over the SSRF-guarded channel, compare versions, and
// report whether a newer release exists. It never downloads or installs
// anything, and the page users are sent to is a built-in constant — a
// tampered manifest can cause at most one false "update available" notice.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DownloadPage is the official release page shown with an update notice.
// Deliberately not a manifest field. Empty until the public domain exists;
// consumers hide the link while it is empty.
const DownloadPage = ""

// Interval is how often the resident Core re-checks.
const Interval = 24 * time.Hour

// manifest is the hosted document: {"version": "x.y.z"}.
type manifest struct {
	Version string `json:"version"`
}

// Status is the latest check outcome.
type Status struct {
	Latest    string    `json:"latest_version,omitempty"`
	Available bool      `json:"update_available"`
	CheckedAt time.Time `json:"-"`
}

// Checker polls ManifestURL and remembers the last outcome.
type Checker struct {
	// ManifestURL is the https manifest location; empty disables checking.
	ManifestURL string
	// Current is the running product version (shared/buildinfo).
	Current string
	// Fetch retrieves the manifest bytes. Production wires the SSRF-guarded
	// urlfetch; tests inject.
	Fetch func(ctx context.Context, url string) ([]byte, error)

	mu   sync.Mutex
	last Status
}

// Last returns the most recent check outcome (zero before any check).
func (c *Checker) Last() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// Check fetches and compares once, storing and returning the outcome.
func (c *Checker) Check(ctx context.Context) (Status, error) {
	raw, err := c.Fetch(ctx, c.ManifestURL)
	if err != nil {
		return Status{}, fmt.Errorf("fetching update manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Status{}, fmt.Errorf("decoding update manifest: %w", err)
	}
	latest, err := parseVersion(m.Version)
	if err != nil {
		return Status{}, fmt.Errorf("update manifest version: %w", err)
	}
	current, err := parseVersion(c.Current)
	if err != nil {
		return Status{}, fmt.Errorf("current version: %w", err)
	}
	s := Status{Latest: m.Version, Available: latest.newerThan(current), CheckedAt: time.Now()}
	c.mu.Lock()
	c.last = s
	c.mu.Unlock()
	return s, nil
}

// version is a parsed semver-lite: major.minor.patch with an optional
// pre-release tag; a pre-release sorts before its own release.
type version struct {
	nums [3]int
	pre  string
}

var versionRe = regexp.MustCompile(`^(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:-([0-9A-Za-z.-]{1,32}))?$`)

func parseVersion(s string) (version, error) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return version{}, fmt.Errorf("invalid version %q", s)
	}
	var v version
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return version{}, err
		}
		v.nums[i] = n
	}
	v.pre = m[4]
	return v, nil
}

func (v version) newerThan(o version) bool {
	for i := 0; i < 3; i++ {
		if v.nums[i] != o.nums[i] {
			return v.nums[i] > o.nums[i]
		}
	}
	// Same core: a release is newer than any pre-release of it. Two
	// different pre-releases compare lexically (good enough for -dev).
	if (v.pre == "") != (o.pre == "") {
		return v.pre == ""
	}
	return v.pre > o.pre
}
