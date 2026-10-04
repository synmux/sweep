// Package sweep finds and removes empty directories below a root.
//
// A directory is empty when it holds nothing at all, or nothing but junk files
// (such as .DS_Store) and directories that are themselves empty. The root is
// never treated as a candidate for removal.
package sweep

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// junkFiles names the regular files that do not stop a directory counting as
// empty. They are deleted along with the directory.
var junkFiles = map[string]struct{}{
	".DS_Store": {},
}

// IsJunk reports whether a regular file with this name is ignorable junk.
func IsJunk(name string) bool {
	_, found := junkFiles[name]
	return found
}

// Candidate is a directory that Scan judged empty.
type Candidate struct {
	// Path is the absolute path of the directory.
	Path string
	// JunkFiles lists the junk file names inside it, deleted before the directory.
	JunkFiles []string
}

// Result is the outcome of a scan.
type Result struct {
	// Root is the absolute, cleaned root directory.
	Root string
	// Candidates are ordered so every directory follows all of its descendants,
	// which is the order they must be removed in.
	Candidates []Candidate
	// DirsScanned counts every directory visited, including the root.
	DirsScanned int
	// Errors holds per-directory read failures. Unreadable directories count
	// as non-empty, so they and their ancestors are kept.
	Errors []error
}

// JunkFileCount returns the number of junk files inside all candidates.
func (result Result) JunkFileCount() int {
	total := 0
	for _, candidate := range result.Candidates {
		total += len(candidate.JunkFiles)
	}
	return total
}

// Progress is a snapshot of a running scan.
type Progress struct {
	// DirsScanned counts directories entered so far, including the current one.
	DirsScanned int
	// CandidatesFound counts removable directories found so far.
	CandidatesFound int
	// CurrentDir is the directory being entered.
	CurrentDir string
}

// ProgressFunc is called once per directory as the scan enters it.
type ProgressFunc func(progress Progress)

// Scan walks root depth-first and returns the directories that can be removed.
// Symbolic links are never followed and count as content. It returns an error
// only when root is not a usable directory or ctx is cancelled.
func Scan(ctx context.Context, root string, progress ProgressFunc) (Result, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolving %q: %w", root, err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("%s is not a directory", absoluteRoot)
	}

	walker := &scanner{
		ctx:      ctx,
		progress: progress,
		result:   Result{Root: absoluteRoot},
	}
	if _, err := walker.classify(absoluteRoot); err != nil {
		return walker.result, err
	}
	return walker.result, nil
}

type scanner struct {
	ctx      context.Context
	progress ProgressFunc
	result   Result
}

// classify reports whether dir is removable, recording candidates post-order.
// The only error it returns is context cancellation.
func (walker *scanner) classify(dir string) (bool, error) {
	if err := walker.ctx.Err(); err != nil {
		return false, err
	}
	walker.result.DirsScanned++
	if walker.progress != nil {
		walker.progress(Progress{
			DirsScanned:     walker.result.DirsScanned,
			CandidatesFound: len(walker.result.Candidates),
			CurrentDir:      dir,
		})
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		walker.result.Errors = append(walker.result.Errors, err)
		return false, nil
	}

	removable := true
	var junkNames []string
	// Every child directory is visited even once dir is known to be kept, so
	// that empty directories nested anywhere below are still found.
	for _, entry := range entries {
		switch {
		case entry.Type().IsRegular() && IsJunk(entry.Name()):
			junkNames = append(junkNames, entry.Name())
		case entry.IsDir():
			childRemovable, err := walker.classify(filepath.Join(dir, entry.Name()))
			if err != nil {
				return false, err
			}
			if !childRemovable {
				removable = false
			}
		default:
			removable = false
		}
	}

	if removable && dir != walker.result.Root {
		walker.result.Candidates = append(walker.result.Candidates, Candidate{Path: dir, JunkFiles: junkNames})
	}
	return removable, nil
}
