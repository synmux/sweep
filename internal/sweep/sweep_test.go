package sweep

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// buildTree creates entries under root. A path ending in "/" is a directory;
// anything else is a file holding its own name.
func buildTree(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, relativePath := range paths {
		fullPath := filepath.Join(root, relativePath)
		if strings.HasSuffix(relativePath, "/") {
			if err := os.MkdirAll(fullPath, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(relativePath), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// candidatePaths returns candidate paths relative to root, in result order.
func candidatePaths(t *testing.T, result Result) []string {
	t.Helper()
	relativePaths := make([]string, 0, len(result.Candidates))
	for _, candidate := range result.Candidates {
		relativePath, err := filepath.Rel(result.Root, candidate.Path)
		if err != nil {
			t.Fatal(err)
		}
		relativePaths = append(relativePaths, filepath.ToSlash(relativePath))
	}
	return relativePaths
}

// snapshot lists every path under root with its type, for before/after checks.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		entries = append(entries, path+" "+entry.Type().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func mustScan(t *testing.T, root string) Result {
	t.Helper()
	result, err := Scan(context.Background(), root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return result
}

func TestIsJunk(t *testing.T) {
	if !IsJunk(".DS_Store") {
		t.Error(".DS_Store should be junk")
	}
	for _, name := range []string{"DS_Store", ".ds_store", "file.txt", ""} {
		if IsJunk(name) {
			t.Errorf("%q should not be junk", name)
		}
	}
}

func TestScanClassification(t *testing.T) {
	testCases := []struct {
		name  string
		tree  []string
		wants []string
	}{
		{name: "empty root yields nothing", tree: nil, wants: nil},
		{name: "truly empty dir", tree: []string{"empty/"}, wants: []string{"empty"}},
		{name: "dir with only .DS_Store", tree: []string{"junk/.DS_Store"}, wants: []string{"junk"}},
		{name: "dir with a real file is kept", tree: []string{"kept/file.txt"}, wants: nil},
		{name: ".DS_Store plus a real file is kept", tree: []string{"kept/.DS_Store", "kept/file.txt"}, wants: nil},
		{name: ".DS_Store directory is not junk", tree: []string{"odd/.DS_Store/file.txt"}, wants: nil},
		{name: "empty .DS_Store directory is just an empty dir", tree: []string{"odd/.DS_Store/"}, wants: []string{"odd/.DS_Store", "odd"}},
		{
			name:  "nested cascade is removed children first",
			tree:  []string{"a/b/c/", "a/b/.DS_Store", "a/.DS_Store"},
			wants: []string{"a/b/c", "a/b", "a"},
		},
		{
			name:  "empty dirs inside a kept dir are still found",
			tree:  []string{"kept/file.txt", "kept/empty/", "kept/deeper/emptier/"},
			wants: []string{"kept/deeper/emptier", "kept/deeper", "kept/empty"},
		},
		{
			name:  "kept sibling keeps the parent",
			tree:  []string{"parent/empty/", "parent/full/file.txt"},
			wants: []string{"parent/empty"},
		},
		{name: "files in root do not matter", tree: []string{"root-file.txt", "empty/"}, wants: []string{"empty"}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			buildTree(t, root, testCase.tree...)
			result := mustScan(t, root)
			if got := candidatePaths(t, result); !slices.Equal(got, testCase.wants) {
				t.Errorf("candidates = %q, want %q", got, testCase.wants)
			}
			if len(result.Errors) != 0 {
				t.Errorf("unexpected errors: %v", result.Errors)
			}
		})
	}
}

func TestScanRecordsJunkFilesAndCounts(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/.DS_Store", "a/b/", "c/file.txt")
	result := mustScan(t, root)

	if result.DirsScanned != 4 {
		t.Errorf("DirsScanned = %d, want 4 (root, a, a/b, c)", result.DirsScanned)
	}
	if result.JunkFileCount() != 1 {
		t.Errorf("JunkFileCount = %d, want 1", result.JunkFileCount())
	}
	for _, candidate := range result.Candidates {
		wantJunk := filepath.Base(candidate.Path) == "a"
		if hasJunk := len(candidate.JunkFiles) > 0; hasJunk != wantJunk {
			t.Errorf("%s JunkFiles = %v", candidate.Path, candidate.JunkFiles)
		}
	}
}

func TestScanNeverOffersRoot(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, ".DS_Store", "empty/")
	result := mustScan(t, root)
	for _, candidate := range result.Candidates {
		if candidate.Path == result.Root {
			t.Fatal("root must never be a candidate")
		}
	}
}

func TestScanDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	buildTree(t, outside, "target-empty/")
	buildTree(t, root, "holder/")
	if err := os.Symlink(filepath.Join(outside, "target-empty"), filepath.Join(root, "holder", "link")); err != nil {
		t.Fatal(err)
	}

	result := mustScan(t, root)
	if got := candidatePaths(t, result); len(got) != 0 {
		t.Errorf("symlink should count as content, got candidates %q", got)
	}
	if result.DirsScanned != 2 {
		t.Errorf("DirsScanned = %d, want 2: the symlinked dir must not be entered", result.DirsScanned)
	}
}

func TestScanUnreadableDirIsKeptAndReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	root := t.TempDir()
	buildTree(t, root, "parent/locked/")
	locked := filepath.Join(root, "parent", "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	result := mustScan(t, root)
	if got := candidatePaths(t, result); len(got) != 0 {
		t.Errorf("unreadable dir and its parent must be kept, got %q", got)
	}
	if len(result.Errors) != 1 || !errors.Is(result.Errors[0], fs.ErrPermission) {
		t.Errorf("Errors = %v, want one permission error", result.Errors)
	}
}

func TestScanRejectsBadRoots(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "file.txt")
	if _, err := Scan(context.Background(), filepath.Join(root, "file.txt"), Options{}); err == nil {
		t.Error("scanning a file should fail")
	}
	if _, err := Scan(context.Background(), filepath.Join(root, "missing"), Options{}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("scanning a missing path: err = %v, want ErrNotExist", err)
	}
}

func TestScanReportsProgress(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/b/", "c/")
	var snapshots []Progress
	_, err := Scan(context.Background(), root, Options{Progress: func(progress Progress) {
		if progress.DirsScanned != len(snapshots)+1 {
			t.Errorf("DirsScanned = %d, want %d", progress.DirsScanned, len(snapshots)+1)
		}
		snapshots = append(snapshots, progress)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 4 {
		t.Fatalf("visited %d dirs, want 4: %+v", len(snapshots), snapshots)
	}
	// Order is root, a, a/b, c. By the time c is entered, a/b and a are found.
	if last := snapshots[3]; filepath.Base(last.CurrentDir) != "c" || last.CandidatesFound != 2 {
		t.Errorf("last snapshot = %+v, want c with 2 candidates found", last)
	}
}

func TestScanHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/", "b/")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, root, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestScanLeavesTreeUntouched(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/b/.DS_Store", "c/", "d/file.txt")
	before := snapshot(t, root)
	mustScan(t, root)
	if after := snapshot(t, root); !slices.Equal(before, after) {
		t.Errorf("scan modified the tree:\nbefore %q\nafter  %q", before, after)
	}
}

func TestRemoveDeletesCandidatesAndJunk(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/b/c/", "a/b/.DS_Store", "a/.DS_Store", "kept/file.txt", "kept/.DS_Store", "kept/empty/")
	result := mustScan(t, root)

	var processed []int
	removal, err := Remove(context.Background(), result.Candidates, func(count int, _ Candidate, err error) {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		processed = append(processed, count)
	})
	if err != nil {
		t.Fatal(err)
	}

	if removal.DirsRemoved != 4 || removal.JunkRemoved != 2 || removal.Skipped != 0 || len(removal.Errors) != 0 {
		t.Errorf("RemoveResult = %+v", removal)
	}
	if !slices.Equal(processed, []int{1, 2, 3, 4}) {
		t.Errorf("progress counts = %v", processed)
	}
	for _, gone := range []string{"a", "kept/empty"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s should be gone, stat err = %v", gone, err)
		}
	}
	// A .DS_Store in a kept directory is left alone.
	for _, kept := range []string{"kept/file.txt", "kept/.DS_Store"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("%s should survive: %v", kept, err)
		}
	}
	if rescan := mustScan(t, root); len(rescan.Candidates) != 0 {
		t.Errorf("rescan still finds %q", candidatePaths(t, rescan))
	}
}

func TestRemoveFailsSafelyWhenContentAppears(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "outer/middle/inner/", "outer/.DS_Store", "other/")
	result := mustScan(t, root)

	// Something writes into a candidate between the scan and the removal.
	buildTree(t, root, "outer/middle/inner/new-file.txt")

	removal, err := Remove(context.Background(), result.Candidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(removal.Errors) != 1 || !errors.Is(removal.Errors[0], syscall.ENOTEMPTY) {
		t.Errorf("Errors = %v, want a single ENOTEMPTY", removal.Errors)
	}
	if removal.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 (middle, outer)", removal.Skipped)
	}
	if removal.DirsRemoved != 1 {
		t.Errorf("DirsRemoved = %d, want 1 (other)", removal.DirsRemoved)
	}
	for _, kept := range []string{"outer/middle/inner/new-file.txt", "outer/.DS_Store"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("%s should survive: %v", kept, err)
		}
	}
}

func TestRemoveToleratesVanishedJunk(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/.DS_Store")
	result := mustScan(t, root)
	if err := os.Remove(filepath.Join(root, "a", ".DS_Store")); err != nil {
		t.Fatal(err)
	}
	removal, err := Remove(context.Background(), result.Candidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	if removal.DirsRemoved != 1 || removal.JunkRemoved != 0 || len(removal.Errors) != 0 {
		t.Errorf("RemoveResult = %+v", removal)
	}
}

func TestRemoveHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/", "b/")
	result := mustScan(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	removal, err := Remove(ctx, result.Candidates, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if removal.DirsRemoved != 0 {
		t.Errorf("removed %d dirs after cancellation", removal.DirsRemoved)
	}
}
