package update

import (
	"context"
	"errors"
	"testing"
)

func checker(current, body string) *Checker {
	return &Checker{
		ManifestURL: "https://updates.example/manifest.json",
		Current:     current,
		Fetch: func(context.Context, string) ([]byte, error) {
			return []byte(body), nil
		},
	}
}

func TestCheckCompares(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.0.1", `{"version":"0.0.2"}`, true},
		{"0.0.1", `{"version":"0.0.1"}`, false},
		{"0.0.2", `{"version":"0.0.1"}`, false},
		{"0.0.1", `{"version":"0.1.0"}`, true},
		{"0.0.1", `{"version":"1.0.0"}`, true},
		// A -dev build of a version is older than its release.
		{"0.0.1-dev", `{"version":"0.0.1"}`, true},
		{"0.0.2-dev", `{"version":"0.0.1"}`, false},
	}
	for _, tc := range cases {
		c := checker(tc.current, tc.latest)
		s, err := c.Check(context.Background())
		if err != nil {
			t.Fatalf("%s vs %s: %v", tc.current, tc.latest, err)
		}
		if s.Available != tc.want {
			t.Errorf("current %s manifest %s: available = %v, want %v", tc.current, tc.latest, s.Available, tc.want)
		}
		if got := c.Last(); got != s {
			t.Errorf("Last() = %+v, want %+v", got, s)
		}
	}
}

func TestCheckRejectsBadManifests(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{}`,
		`{"version":"evil string"}`,
		`{"version":"1.2"}`,
		`{"version":"1.2.3.4"}`,
	} {
		c := checker("0.0.1", body)
		if _, err := c.Check(context.Background()); err == nil {
			t.Errorf("manifest %q accepted", body)
		}
		if c.Last().Available {
			t.Errorf("failed check %q left an available status", body)
		}
	}
}

func TestCheckFetchErrorPassesThrough(t *testing.T) {
	c := &Checker{ManifestURL: "https://x", Current: "0.0.1",
		Fetch: func(context.Context, string) ([]byte, error) { return nil, errors.New("boom") }}
	if _, err := c.Check(context.Background()); err == nil {
		t.Fatal("fetch error swallowed")
	}
}
