// Package crashlog captures fatal panics to local files. Nothing is
// ever reported automatically: crash data leaves the machine only when the
// user runs `fylane-companion diagnostics` and shares the bundle themselves.
package crashlog

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
)

const (
	dirName    = "crash"
	latestName = "crash-latest.log"
	metaName   = "crash-latest.meta"
	// keep bounds how many rotated crash files survive pruning.
	keep = 5
)

// Setup arms process-level crash capture into <dataDir>/crash/. A non-empty
// capture from a previous run is rotated aside first (its .meta sidecar,
// written every start, records which version crashed). Returns the armed
// file's path. The file handle intentionally stays open for process life.
func Setup(dataDir, version string) (string, error) {
	dir := filepath.Join(dataDir, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating crash directory: %w", err)
	}

	latest := filepath.Join(dir, latestName)
	meta := filepath.Join(dir, metaName)
	// Let go of any capture armed earlier in this process before touching the
	// file. The runtime holds its own handle, and on Windows an open file can
	// be neither renamed into the archive nor removed with its directory.
	if err := debug.SetCrashOutput(nil, debug.CrashOptions{}); err != nil {
		return "", fmt.Errorf("releasing crash output: %w", err)
	}
	if info, err := os.Stat(latest); err == nil && info.Size() > 0 {
		stamp := info.ModTime().UTC().Format("20060102-150405")
		os.Rename(latest, filepath.Join(dir, "crash-"+stamp+".log"))
		os.Rename(meta, filepath.Join(dir, "crash-"+stamp+".meta"))
	}
	prune(dir)

	// The sidecar carries context the runtime dump lacks. No paths, no
	// workspace data — version and platform only.
	metaBody := fmt.Sprintf("fylane-companion %s %s/%s go%s\n",
		version, runtime.GOOS, runtime.GOARCH, strings.TrimPrefix(runtime.Version(), "go"))
	if err := os.WriteFile(meta, []byte(metaBody), 0o600); err != nil {
		return "", fmt.Errorf("writing crash meta: %w", err)
	}

	f, err := os.OpenFile(latest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("opening crash file: %w", err)
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
		return "", fmt.Errorf("arming crash output: %w", err)
	}
	// The runtime duplicated the descriptor; this copy is now just a leak.
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing crash file: %w", err)
	}
	return latest, nil
}

// prune keeps only the newest `keep` rotated crash logs (plus sidecars).
func prune(dir string) {
	entries, err := filepath.Glob(filepath.Join(dir, "crash-*.log"))
	if err != nil {
		return
	}
	var rotated []string
	for _, e := range entries {
		if filepath.Base(e) != latestName {
			rotated = append(rotated, e)
		}
	}
	sort.Strings(rotated) // timestamp names sort chronologically
	for len(rotated) > keep {
		os.Remove(rotated[0])
		os.Remove(strings.TrimSuffix(rotated[0], ".log") + ".meta")
		rotated = rotated[1:]
	}
}

// Diagnostics writes a shareable zip of the crash directory plus a build
// info line. It contains nothing else from the data directory — no
// control.json, no database, no workspace content.
func Diagnostics(dataDir, version, outPath string) error {
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("creating diagnostics bundle: %w", err)
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	info, err := zw.Create("info.txt")
	if err != nil {
		return err
	}
	fmt.Fprintf(info, "fylane-companion %s %s/%s go%s\n",
		version, runtime.GOOS, runtime.GOARCH, strings.TrimPrefix(runtime.Version(), "go"))

	dir := filepath.Join(dataDir, dirName)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "crash-") {
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return out.Close()
}
