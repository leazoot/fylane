// Package buildinfo carries the product version stamped at build time.
package buildinfo

// Version is the single product version, set by the release pipeline via
// -ldflags "-X github.com/leazoot/fylane/shared/buildinfo.Version=<v>" from the VERSION file at
// the repo root. Plain source builds report the -dev default so a stamped
// release is always distinguishable from a developer build.
var Version = "0.0.1-dev"
