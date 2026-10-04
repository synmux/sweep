package sweep

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveResult summarises a removal pass.
type RemoveResult struct {
	DirsRemoved int
	JunkRemoved int
	// Skipped counts candidates left alone because a directory inside them
	// could not be removed, so they cannot be empty.
	Skipped int
	Errors  []error
}

// RemoveFunc is called after each candidate is processed. err is nil on
// success, and also nil when the candidate was skipped.
type RemoveFunc func(processed int, candidate Candidate, err error)

// Remove deletes candidates in the order given, which must be the order Scan
// produced. Directories are removed with os.Remove, never os.RemoveAll, so
// anything that appeared since the scan makes removal fail instead of being
// lost. It carries on past failures and only stops early if ctx is cancelled.
func Remove(ctx context.Context, candidates []Candidate, onEach RemoveFunc) (RemoveResult, error) {
	var result RemoveResult
	// blockedParents holds the parents of directories that could not be
	// removed. Those parents still have content, so they are skipped.
	blockedParents := make(map[string]struct{})

	for index, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		err := removeOne(candidate, blockedParents, &result)
		if err != nil {
			result.Errors = append(result.Errors, err)
			blockedParents[filepath.Dir(candidate.Path)] = struct{}{}
		}
		if onEach != nil {
			onEach(index+1, candidate, err)
		}
	}
	return result, nil
}

// removeOne removes a single candidate. A candidate holding a directory that
// could not be removed is skipped without producing a new error, and in turn
// blocks its own parent.
func removeOne(candidate Candidate, blockedParents map[string]struct{}, result *RemoveResult) error {
	if _, isBlocked := blockedParents[candidate.Path]; isBlocked {
		blockedParents[filepath.Dir(candidate.Path)] = struct{}{}
		result.Skipped++
		return nil
	}

	// Check every judged file before deleting anything, so a directory is
	// either fully cleared or left exactly as it was.
	for _, junkFile := range candidate.JunkFiles {
		if junkFile.Judged {
			if err := checkUnchanged(candidate.Path, junkFile); err != nil {
				return err
			}
		}
	}

	for _, junkFile := range candidate.JunkFiles {
		err := os.Remove(filepath.Join(candidate.Path, junkFile.Name))
		switch {
		case err == nil:
			result.JunkRemoved++
		case errors.Is(err, fs.ErrNotExist):
			// Already gone; nothing to do.
		default:
			return err
		}
	}

	if err := os.Remove(candidate.Path); err != nil {
		return err
	}
	result.DirsRemoved++
	return nil
}

// checkUnchanged confirms a judged file is still the regular file, with the
// same size and modification time, that the Judge saw.
func checkUnchanged(dir string, junkFile JunkFile) error {
	path := filepath.Join(dir, junkFile.Name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != junkFile.Size || !info.ModTime().Equal(junkFile.ModTime) {
		return fmt.Errorf("%s changed since it was judged junk, so it was kept", path)
	}
	return nil
}
