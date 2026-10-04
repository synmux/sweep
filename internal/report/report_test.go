package report

import (
	"errors"
	"fmt"
	"os"
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

func candidate(relativePath string, junkNames ...string) sweep.Candidate {
	var junkFiles []sweep.JunkFile
	for _, name := range junkNames {
		junkFiles = append(junkFiles, sweep.JunkFile{Name: name})
	}
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
		"data/",
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
	got := ansi.Strip(Render(Summary{Root: testRoot, DirsScanned: 3}, testStyles, 0))
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
	got := ansi.Strip(Render(summary, testStyles, 0))
	for _, fragment := range []string{"DRY RUN", "Would remove", "2 directories", "Junk files", "1 file", "1,234 directories", "1.5s", "✗ a/", "✗ b/"} {
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
	got := ansi.Strip(Render(summary, testStyles, 0))
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
	if got := ansi.Strip(Header("/x", true, testStyles, 0)); !strings.Contains(got, "/x") || !strings.Contains(got, "DRY RUN") {
		t.Errorf("Header = %q", got)
	}
	longRoot := "/a/very/long/path/that/will/not/fit/in/a/narrow/terminal/at/all/target"
	got := ansi.Strip(Header(longRoot, true, testStyles, 40))
	if width := ansi.StringWidth(got); width > 40 {
		t.Errorf("Header width = %d, want <= 40: %q", width, got)
	}
	if !strings.Contains(got, "…") || !strings.Contains(got, "/target") || !strings.Contains(got, "DRY RUN") {
		t.Errorf("Header should keep the end of the path and the tag: %q", got)
	}
}

func TestDisplayPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	testCases := map[string]string{
		home:                             "~",
		filepath.Join(home, "Downloads"): filepath.Join("~", "Downloads"),
		home + "-other":                  home + "-other",
		"/elsewhere":                     "/elsewhere",
	}
	for input, want := range testCases {
		if got := DisplayPath(input); got != want {
			t.Errorf("DisplayPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTruncateLeft(t *testing.T) {
	testCases := []struct {
		text  string
		width int
		want  string
	}{
		{text: "/short", width: 20, want: "/short"},
		{text: "/a/very/long/path", width: 10, want: "…long/path"},
		{text: "/exact", width: 6, want: "/exact"},
		{text: "/anything", width: 1, want: "/anything"},
	}
	for _, testCase := range testCases {
		if got := TruncateLeft(testCase.text, testCase.width); got != testCase.want {
			t.Errorf("TruncateLeft(%q, %d) = %q, want %q", testCase.text, testCase.width, got, testCase.want)
		}
	}
}

func TestTreeRootLabelForFilesystemRoot(t *testing.T) {
	got := ansi.Strip(Tree("/", []sweep.Candidate{{Path: "/empty"}}, testStyles))
	if !strings.HasPrefix(got, "/\n") {
		t.Errorf("tree for / should be labelled /:\n%s", got)
	}
}

func TestFromScan(t *testing.T) {
	scanError := errors.New("boom")
	result := sweep.Result{Root: testRoot, DirsScanned: 7, Candidates: []sweep.Candidate{candidate("a")}, Errors: []error{scanError}}
	summary := FromScan(result, true, time.Second)
	if summary.Root != testRoot || !summary.DryRun || summary.DirsScanned != 7 || len(summary.Candidates) != 1 ||
		len(summary.ScanErrors) != 1 || summary.Elapsed != time.Second || summary.Removal != nil {
		t.Errorf("FromScan = %+v", summary)
	}
}

func TestJunkNamesInTree(t *testing.T) {
	judgedCandidate := sweep.Candidate{
		Path: filepath.Join(testRoot, "a"),
		JunkFiles: []sweep.JunkFile{
			{Name: ".DS_Store"}, {Name: "Thumbs.db", Judged: true}, {Name: "Icon\r", Judged: true},
			{Name: "desktop.ini", Judged: true}, {Name: "x.tmp", Judged: true},
		},
	}
	got := ansi.Strip(Tree(testRoot, []sweep.Candidate{judgedCandidate}, testStyles))
	if !strings.Contains(got, `.DS_Store, Thumbs.db*, Icon\r*, +2 more`) {
		t.Errorf("junk names not summarised as expected:\n%s", got)
	}
}

func TestJudgementsRendering(t *testing.T) {
	judgements := []sweep.Judgement{
		{Path: filepath.Join(testRoot, "empty"), Empty: true, Files: []sweep.FileVerdict{{Name: "Thumbs.db", JunkProbability: 0.96, Junk: true}}},
		{Path: filepath.Join(testRoot, "kept"), Files: []sweep.FileVerdict{
			{Name: "Thumbs.db", JunkProbability: 0.95, Junk: true},
			{Name: "notes.txt", JunkProbability: 0.13},
		}},
		{Path: filepath.Join(testRoot, "crowded"), Skipped: "40 files is more than the 32 sweep asks about"},
		{Path: filepath.Join(testRoot, "broken"), Err: errors.New("jev: HTTP 500")},
	}
	got := ansi.Strip(Judgements(testRoot, judgements, 0.8, testStyles))
	for _, fragment := range []string{
		"Jev decisions", "junk at ≥ 0.80",
		"data/empty/ ✗ empty", "0.96 junk Thumbs.db",
		"data/kept/ ● kept", "0.13 keep notes.txt",
		"1 not asked (too many files)",
		"data/broken/ error · kept · jev: HTTP 500",
	} {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, got)
		}
	}
	if strings.Contains(got, "data/crowded/") {
		t.Errorf("directories Jev was not asked about are left out:\n%s", got)
	}
}

func TestRenderIncludesJudgementsAndJevRow(t *testing.T) {
	summary := Summary{
		Root:       testRoot,
		DryRun:     true,
		Candidates: []sweep.Candidate{{Path: filepath.Join(testRoot, "a"), JunkFiles: []sweep.JunkFile{{Name: "Thumbs.db", Judged: true}}}},
		Judgements: []sweep.Judgement{
			{Path: filepath.Join(testRoot, "a"), Empty: true, Cached: true},
			{Path: filepath.Join(testRoot, "b")},
			{Path: filepath.Join(testRoot, "c"), Skipped: "too many files"},
		},
		JunkThreshold: 0.8,
	}
	got := ansi.Strip(Render(summary, testStyles, 0))
	for _, fragment := range []string{"1 judged by Jev", "2 directories judged · 1 empty · 1 not asked · 1 from cache", "with --jev-results"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, got)
		}
	}
	if strings.Contains(got, "Jev decisions") {
		t.Errorf("decisions are only printed with --jev-results:\n%s", got)
	}

	summary.ShowJudgements = true
	got = ansi.Strip(Render(summary, testStyles, 0))
	decisions, scanned := strings.Index(got, "Jev decisions"), strings.Index(got, "Scanned")
	if decisions < 0 || decisions < scanned {
		t.Errorf("with --jev-results, decisions must follow the summary:\n%s", got)
	}
	if !strings.Contains(got, "data/a/ ✗ empty · cached") || strings.Contains(got, "--jev-results") {
		t.Errorf("unexpected output:\n%s", got)
	}
	plain := ansi.Strip(Render(Summary{Root: testRoot, DryRun: true, Candidates: []sweep.Candidate{candidate("a")}}, testStyles, 0))
	if strings.Contains(plain, "Jev") {
		t.Errorf("no Jev output without judgements:\n%s", plain)
	}
}

func TestPrintableName(t *testing.T) {
	if got := printableName("Icon\r"); got != `Icon\r` {
		t.Errorf("printableName = %q", got)
	}
	if got := printableName("café 写真.jpg"); got != "café 写真.jpg" {
		t.Errorf("printable characters must pass through, got %q", got)
	}
}

func TestDecisionsFollowErrors(t *testing.T) {
	summary := Summary{
		Root:           testRoot,
		DryRun:         true,
		ScanErrors:     []error{errors.New("open /data/locked: permission denied")},
		Judgements:     []sweep.Judgement{{Path: filepath.Join(testRoot, "a"), Empty: true}},
		ShowJudgements: true,
	}
	got := ansi.Strip(Render(summary, testStyles, 0))
	if strings.Index(got, "Jev decisions") < strings.Index(got, "Problems") {
		t.Errorf("decisions must come after the error box:\n%s", got)
	}
}

func TestNoHintWhenNothingWasJudged(t *testing.T) {
	summary := Summary{Root: testRoot, Judgements: []sweep.Judgement{{Path: filepath.Join(testRoot, "x"), Skipped: "too many"}}}
	if got := ansi.Strip(Render(summary, testStyles, 0)); strings.Contains(got, "--jev-results") {
		t.Errorf("no hint when Jev was never asked:\n%s", got)
	}
}
