package crashlog

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupArmsAndRotates(t *testing.T) {
	t.Cleanup(Release)
	dataDir := t.TempDir()

	latest, err := Setup(dataDir, "0.0.1-test")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(latest); err != nil || info.Size() != 0 {
		t.Fatalf("armed crash file: %v, %v", info, err)
	}

	// Simulate a crash from the previous run, then restart.
	if err := os.WriteFile(latest, []byte("fatal error: boom\n\ngoroutine 1 ...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(dataDir, "0.0.1-test"); err != nil {
		t.Fatal(err)
	}

	rotated, _ := filepath.Glob(filepath.Join(dataDir, dirName, "crash-2*.log"))
	if len(rotated) != 1 {
		t.Fatalf("rotated crash logs = %v", rotated)
	}
	body, _ := os.ReadFile(rotated[0])
	if !strings.Contains(string(body), "boom") {
		t.Fatalf("rotated content = %q", body)
	}
	metaPath := strings.TrimSuffix(rotated[0], ".log") + ".meta"
	meta, err := os.ReadFile(metaPath)
	if err != nil || !strings.Contains(string(meta), "0.0.1-test") {
		t.Fatalf("rotated meta = %q, %v", meta, err)
	}
	if info, err := os.Stat(latest); err != nil || info.Size() != 0 {
		t.Fatalf("new capture not re-armed empty: %v, %v", info, err)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	t.Cleanup(Release)
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < keep+3; i++ {
		stamp := base.Add(time.Duration(i) * time.Hour).Format("20060102-150405")
		os.WriteFile(filepath.Join(dir, "crash-"+stamp+".log"), []byte("x"), 0o600)
		os.WriteFile(filepath.Join(dir, "crash-"+stamp+".meta"), []byte("m"), 0o600)
	}

	if _, err := Setup(dataDir, "v"); err != nil {
		t.Fatal(err)
	}
	logs, _ := filepath.Glob(filepath.Join(dir, "crash-2*.log"))
	if len(logs) != keep {
		t.Fatalf("kept %d rotated logs, want %d", len(logs), keep)
	}
	// The oldest ones are the pruned ones.
	for _, l := range logs {
		if strings.Contains(l, "20260101-000000") || strings.Contains(l, "20260101-010000") {
			t.Fatalf("oldest log survived pruning: %s", l)
		}
	}
}

func TestDiagnosticsExcludesSecrets(t *testing.T) {
	t.Cleanup(Release)
	dataDir := t.TempDir()
	if _, err := Setup(dataDir, "0.0.1-test"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir, dirName)
	os.WriteFile(filepath.Join(dir, "crash-20260101-000000.log"), []byte("fatal error: kaput\n"), 0o600)
	// Secrets living in the data directory must never enter the bundle.
	os.WriteFile(filepath.Join(dataDir, "control.json"), []byte(`{"token":"SECRET"}`), 0o600)
	os.WriteFile(filepath.Join(dataDir, "fylane.db"), []byte("sqlite"), 0o600)

	out := filepath.Join(t.TempDir(), "diag.zip")
	if err := Diagnostics(dataDir, "0.0.1-test", out); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if strings.Contains(f.Name, "control") || strings.Contains(f.Name, ".db") {
			t.Fatalf("secret file in bundle: %s", f.Name)
		}
	}
	for _, want := range []string{"info.txt", "crash-20260101-000000.log", "crash-latest.meta"} {
		if !names[want] {
			t.Fatalf("bundle missing %s (has %v)", want, names)
		}
	}

	// Refuses to clobber an existing bundle.
	if err := Diagnostics(dataDir, "0.0.1-test", out); err == nil {
		t.Fatal("overwrote an existing bundle")
	}
}
