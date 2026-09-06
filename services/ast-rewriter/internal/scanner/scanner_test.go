package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestScanDirectory_MissingPathReturnsError is the regression test for a
// real bug found by adversarial review: filepath.WalkDir's own callback
// returns nil on ANY error, including the root-level error WalkDir passes
// when the root itself doesn't exist -- so before this fix, ScanDirectory
// on a nonexistent/misconfigured repo path returned (nil sites, nil error),
// indistinguishable from a genuine empty-result scan. A caller relying on
// "zero call sites" as a verified safety signal (services/intelligence's
// stale-flag ARCHIVE gate, INT-6) must never receive that misleading zero
// for a directory that was never actually scanned.
func TestScanDirectory_MissingPathReturnsError(t *testing.T) {
	sites, err := ScanDirectory("/this/path/does/not/exist/at/all", "some-flag")
	if err == nil {
		t.Fatal("ScanDirectory on a nonexistent path returned a nil error -- a misconfigured repo_path would silently look like a verified-zero scan")
	}
	if sites != nil {
		t.Errorf("sites = %v, want nil alongside a real error", sites)
	}
}

// TestScanDirectory_PathIsAFileNotADirectoryReturnsError covers the other
// "not actually a valid directory" case os.Stat alone wouldn't catch --
// dir exists but is a regular file (e.g. a config-drift scenario mounting
// the wrong path).
func TestScanDirectory_PathIsAFileNotADirectoryReturnsError(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "not-a-dir")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer func() { _ = tmpFile.Close() }()

	_, err = ScanDirectory(tmpFile.Name(), "some-flag")
	if err == nil {
		t.Fatal("ScanDirectory on a path that is a file, not a directory, returned a nil error")
	}
}

// TestScanDirectory_RealDirectoryWithNoMatchesReturnsGenuineZero proves the
// fix didn't break the real, legitimate "scanned successfully, found
// nothing" case -- a genuine verified zero must still be zero sites, zero
// error, not accidentally start erroring on every empty result.
func TestScanDirectory_RealDirectoryWithNoMatchesReturnsGenuineZero(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte("console.log('no flags here')"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	sites, err := ScanDirectory(dir, "some-flag")
	if err != nil {
		t.Fatalf("ScanDirectory on a real, readable, empty-result directory returned an error: %v", err)
	}
	if len(sites) != 0 {
		t.Errorf("sites = %v, want empty", sites)
	}
}

// TestScanDirectory_FindsRealCallSite is a basic sanity check that the
// scanner's core matching behavior (unchanged by this fix) still works.
func TestScanDirectory_FindsRealCallSite(t *testing.T) {
	dir := t.TempDir()
	src := `if (client.isEnabled("checkout-v2")) { doThing(); }`
	if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte(src), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	sites, err := ScanDirectory(dir, "checkout-v2")
	if err != nil {
		t.Fatalf("ScanDirectory returned an error: %v", err)
	}
	if len(sites) != 1 {
		t.Fatalf("sites = %v, want exactly 1 match", sites)
	}
	if sites[0].FlagKey != "checkout-v2" || sites[0].Language != "typescript" {
		t.Errorf("unexpected call site: %+v", sites[0])
	}
}

// TestScanDirectory_ErrorIsWrappedNotSwallowed guards against a future
// regression reintroducing the exact swallow pattern this fix removed --
// errors.Is must be able to see through the wrapping to the real os error.
func TestScanDirectory_ErrorIsWrappedNotSwallowed(t *testing.T) {
	_, err := ScanDirectory(filepath.Join(t.TempDir(), "definitely-missing"), "")
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want it to wrap os.ErrNotExist", err)
	}
}
