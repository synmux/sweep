package report

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/synmux/sweep/internal/sweep"
	"github.com/synmux/sweep/internal/theme"
)

const testRoot = "/data"

var testStyles = theme.New(true)

func candidate(relativePath string, junkFiles ...string) sweep.Candidate {
	return sweep.Candidate{Path: filepath.Join(testRoot, relativePath), JunkFiles: junkFiles}
}

func TestTreeLayout(t *testing.T) {
	candidates := []sweep.Candidate{
		candidate("photos/2019/raw/empty"),
		candidate("zeta"),
		candidate("alpha/inner"),
		candidate("alpha", ".DS_Store"),
		candidate("mixed/one"),
		candidate("mixed/two"),
	}

	got := ansi.Strip(Tree(testRoot, candidates, testStyles))
	want := strings.Join([]string{
		"/data",
		"├── ✗ alpha/ · .DS_Store · +1 empty inside",
		"├── mixed/",
		"│   ├── ✗ one/",
		"│   ╰── ✗ two/",
		"├── photos/2019/raw/",
		"│   ╰── ✗ empty/",
		"╰── ✗ zeta/",
	}, "\n")

	if normalise(got) != normalise(want) {
		t.Errorf("tree mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// normalise trims trailing spaces that lipgloss may pad lines with.
func normalise(text string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

func TestTreeIgnoresPathsOutsideRoot(t *testing.T) {
	candidates := []sweep.Candidate{{Path: "/elsewhere/x"}, {Path: testRoot}, candidate("inside")}
	got := ansi.Strip(Tree(testRoot, candidates, testStyles))
	if strings.Contains(got, "elsewhere") || strings.Count(got, "✗") != 1 {
		t.Errorf("unexpected tree:\n%s", got)
	}
}

func TestRenderNothingToDo(t *testing.T) {
	got := ansi.Strip(Render(Summary{Root: testRoot, DirsScanned: 3}, testStyles))
	for _, fragment := range []string{"sweep", testRoot, "Nothing to sweep", "Scanned", "3 directories"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, got)
		}
	}
	if strings.Contains(got, "DRY RUN") {
		t.Error("real run must not be tagged DRY RUN")
	}
}

func TestRenderDryRun(t *testing.T) {
	summary := Summary{
		Root:        testRoot,
		DryRun:      true,
		DirsScanned: 1234,
		Candidates:  []sweep.Candidate{candidate("a", ".DS_Store"), candidate("b")},
		Elapsed:     1500 * time.Millisecond,
	}
	got := ansi.Strip(Render(summary, testStyles))
	for _, fragment := range []string{"DRY RUN", "Would remove", "2 directories", ".DS_Store", "1 file", "1,234 directories", "1.5s", "✗ a/", "✗ b/"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, got)
		}
	}
	if strings.Contains(got, "Removed") {
		t.Error("dry run must not claim anything was removed")
	}
}

func TestRenderRealRunWithProblems(t *testing.T) {
	summary := Summary{
		Root:        testRoot,
		DirsScanned: 10,
		Candidates:  []sweep.Candidate{candidate("a"), candidate("b"), candidate("c")},
		ScanErrors:  []error{errors.New("open /data/locked: permission denied")},
		Removal: &sweep.RemoveResult{
			DirsRemoved: 1,
			JunkRemoved: 2,
			Skipped:     1,
			Errors:      []error{errors.New("remove /data/a: directory not empty")},
		},
	}
	got := ansi.Strip(Render(summary, testStyles))
	for _, fragment := range []string{"Removed", "1 directory", "2 files", "Skipped", "no longer empty", "Errors", "Problems", "permission denied", "directory not empty"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, got)
		}
	}
}

func TestErrorBoxTruncates(t *testing.T) {
	var errs []error
	for index := range maxListedErrors + 3 {
		errs = append(errs, fmt.Errorf("problem %d", index))
	}
	got := ansi.Strip(ErrorBox(errs, testStyles))
	if !strings.Contains(got, "problem 9") || strings.Contains(got, "problem 10") {
		t.Errorf("expected the first %d errors only:\n%s", maxListedErrors, got)
	}
	if !strings.Contains(got, "…and 3 more") {
		t.Errorf("missing truncation note:\n%s", got)
	}
}

func TestFormatDuration(t *testing.T) {
	testCases := map[time.Duration]string{
		1234567 * time.Nanosecond:             "1ms",
		1234 * time.Millisecond:               "1.23s",
		90*time.Second + 400*time.Millisecond: "1m30s",
	}
	for input, want := range testCases {
		if got := FormatDuration(input); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", input, got, want)
		}
	}
}

func TestHeader(t *testing.T) {
	if got := ansi.Strip(Header("/x", true, testStyles)); !strings.Contains(got, "/x") || !strings.Contains(got, "DRY RUN") {
		t.Errorf("Header = %q", got)
	}
}
