// Package report renders the results of a sweep: a header, a tree of the
// directories that were (or would be) removed, and a summary. The TUI and the
// plain, non-interactive mode share it, so both print the same output.
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustin/go-humanize"
	"github.com/dustin/go-humanize/english"

	"github.com/synmux/sweep/internal/sweep"
	"github.com/synmux/sweep/internal/theme"
)

// maxListedErrors caps how many individual errors the report prints.
const maxListedErrors = 10

// Summary is everything the final report needs.
type Summary struct {
	Root        string
	DryRun      bool
	DirsScanned int
	Candidates  []sweep.Candidate
	ScanErrors  []error
	// Removal is nil when nothing was removed (dry runs, or nothing to do).
	Removal *sweep.RemoveResult
	Elapsed time.Duration
	// Judgements are the Judge's decisions; empty without --jev.
	Judgements []sweep.Judgement
	// JunkThreshold is the probability a judged file needed to count as junk.
	// Zero when no Judge was used.
	JunkThreshold float64
}

// FromScan builds a Summary from a finished scan. Removal is left nil.
func FromScan(result sweep.Result, dryRun bool, elapsed time.Duration) Summary {
	return Summary{
		Root:        result.Root,
		DryRun:      dryRun,
		DirsScanned: result.DirsScanned,
		Candidates:  result.Candidates,
		ScanErrors:  result.Errors,
		Elapsed:     elapsed,
		Judgements:  result.Judgements,
	}
}

// Header renders the one-line title shown above everything else. When
// maxWidth is positive, the path is shortened from the left to fit.
func Header(root string, dryRun bool, styles theme.Styles, maxWidth int) string {
	badge := styles.Badge.Render("🧹 sweep")
	tag := ""
	if dryRun {
		tag = " " + styles.DryRunTag.Render("DRY RUN")
	}
	path := DisplayPath(root)
	if maxWidth > 0 {
		path = TruncateLeft(path, maxWidth-lipgloss.Width(badge)-lipgloss.Width(tag)-1)
	}
	return badge + " " + styles.Path.Render(path) + tag
}

// DisplayPath abbreviates the user's home directory to "~".
func DisplayPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, isUnderHome := strings.CutPrefix(path, home+string(filepath.Separator)); isUnderHome {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// TruncateLeft keeps the end of text, the informative part of a path, and
// prefixes an ellipsis when text does not fit in width cells. A width below
// two leaves text unchanged, since nothing useful fits.
func TruncateLeft(text string, width int) string {
	overflow := ansi.StringWidth(text) - width
	if width < 2 || overflow <= 0 {
		return text
	}
	return ansi.TruncateLeft(text, overflow+1, "…")
}

// Render builds the complete report: header, tree, summary and errors. When
// maxWidth is positive the header is fitted to it.
func Render(summary Summary, styles theme.Styles, maxWidth int) string {
	sections := []string{Header(summary.Root, summary.DryRun, styles, maxWidth), ""}
	if len(summary.Candidates) == 0 {
		sections = append(sections, styles.Success.Render("✨ Nothing to sweep."), "")
	} else {
		sections = append(sections, Tree(summary.Root, summary.Candidates, styles), "")
	}
	if len(summary.Judgements) > 0 {
		sections = append(sections, Judgements(summary.Root, summary.Judgements, summary.JunkThreshold, styles), "")
	}
	sections = append(sections, SummaryBox(summary, styles))

	allErrors := append(append([]error{}, summary.ScanErrors...), removalErrors(summary.Removal)...)
	if len(allErrors) > 0 {
		sections = append(sections, ErrorBox(allErrors, styles))
	}
	return strings.Join(sections, "\n")
}

// SummaryBox renders the totals in a bordered box.
func SummaryBox(summary Summary, styles theme.Styles) string {
	var rows []string
	addRow := func(label, value string) {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, styles.Label.Render(label), value))
	}

	junkCount, judgedCount := 0, 0
	for _, candidate := range summary.Candidates {
		junkCount += len(candidate.JunkFiles)
		judgedCount += candidate.JudgedFileCount()
	}
	judgedNote := ""
	if judgedCount > 0 {
		judgedNote = styles.Faint.Render(fmt.Sprintf(" · %s judged by Jev", humanize.Comma(int64(judgedCount))))
	}

	switch {
	case summary.Removal != nil:
		addRow("Removed", styles.Success.Render(countOf(summary.Removal.DirsRemoved, "directory", "directories")))
		if summary.Removal.JunkRemoved > 0 {
			addRow("Junk files", styles.Value.Render(countOf(summary.Removal.JunkRemoved, "file", "files"))+judgedNote)
		}
		if summary.Removal.Skipped > 0 {
			addRow("Skipped", styles.Warning.Render(countOf(summary.Removal.Skipped, "directory", "directories"))+
				styles.Faint.Render(" · no longer empty"))
		}
	case summary.DryRun:
		addRow("Would remove", styles.Value.Render(countOf(len(summary.Candidates), "directory", "directories")))
		if junkCount > 0 {
			addRow("Junk files", styles.Value.Render(countOf(junkCount, "file", "files"))+judgedNote)
		}
	default:
		addRow("Removed", styles.Value.Render(countOf(0, "directory", "directories")))
	}

	addRow("Scanned", styles.Value.Render(countOf(summary.DirsScanned, "directory", "directories"))+
		styles.Faint.Render(" in "+FormatDuration(summary.Elapsed)))

	if len(summary.Judgements) > 0 {
		askedCount, emptyCount, notAskedCount, cachedCount := 0, 0, 0, 0
		for _, judgement := range summary.Judgements {
			if judgement.Cached {
				cachedCount++
			}
			switch {
			case judgement.Skipped != "":
				notAskedCount++
			case judgement.Empty:
				askedCount++
				emptyCount++
			default:
				askedCount++
			}
		}
		note := fmt.Sprintf(" judged · %s empty", humanize.Comma(int64(emptyCount)))
		if notAskedCount > 0 {
			note += fmt.Sprintf(" · %s not asked", humanize.Comma(int64(notAskedCount)))
		}
		if cachedCount > 0 {
			note += fmt.Sprintf(" · %s from cache", humanize.Comma(int64(cachedCount)))
		}
		addRow("Jev", styles.Value.Render(countOf(askedCount, "directory", "directories"))+styles.Faint.Render(note))
	}

	if errorCount := len(summary.ScanErrors) + len(removalErrors(summary.Removal)); errorCount > 0 {
		addRow("Errors", styles.Warning.Render(humanize.Comma(int64(errorCount))))
	}
	return styles.SummaryBox.Render(strings.Join(rows, "\n"))
}

// ErrorBox lists errors in a bordered box, truncated after maxListedErrors.
func ErrorBox(errs []error, styles theme.Styles) string {
	lines := []string{styles.Warning.Render("⚠ Problems")}
	for index, err := range errs {
		if index == maxListedErrors {
			lines = append(lines, styles.Faint.Render(fmt.Sprintf("…and %s more", humanize.Comma(int64(len(errs)-maxListedErrors)))))
			break
		}
		lines = append(lines, styles.Faint.Render("• ")+err.Error())
	}
	return styles.ErrorBox.Render(strings.Join(lines, "\n"))
}

// FormatDuration renders a duration for humans: milliseconds below a second,
// then hundredths of a second, then whole seconds past a minute.
func FormatDuration(duration time.Duration) string {
	switch {
	case duration < time.Second:
		return duration.Round(time.Millisecond).String()
	case duration < time.Minute:
		return duration.Round(10 * time.Millisecond).String()
	default:
		return duration.Round(time.Second).String()
	}
}

func countOf(quantity int, singular, plural string) string {
	return humanize.Comma(int64(quantity)) + " " + english.PluralWord(quantity, singular, plural)
}

func removalErrors(removal *sweep.RemoveResult) []error {
	if removal == nil {
		return nil
	}
	return removal.Errors
}

// node is one directory in the trie built from candidate paths.
type node struct {
	name      string
	children  map[string]*node
	candidate bool
	junkFiles []sweep.JunkFile
}

func newNode(name string) *node {
	return &node{name: name, children: make(map[string]*node)}
}

// buildTrie arranges candidate paths, relative to root, into a trie. Paths
// outside root are ignored, which never happens with Scan's output.
func buildTrie(root string, candidates []sweep.Candidate) *node {
	trieRoot := newNode(root)
	for _, candidate := range candidates {
		relativePath, err := filepath.Rel(root, candidate.Path)
		if err != nil || relativePath == "." || strings.HasPrefix(relativePath, "..") {
			continue
		}
		current := trieRoot
		for _, component := range strings.Split(filepath.ToSlash(relativePath), "/") {
			child, exists := current.children[component]
			if !exists {
				child = newNode(component)
				current.children[component] = child
			}
			current = child
		}
		current.candidate = true
		current.junkFiles = candidate.JunkFiles
	}
	return trieRoot
}

// sortedChildren returns the children ordered by name.
func (current *node) sortedChildren() []*node {
	children := make([]*node, 0, len(current.children))
	for _, child := range current.children {
		children = append(children, child)
	}
	sort.Slice(children, func(left, right int) bool { return children[left].name < children[right].name })
	return children
}

// candidateDescendants counts the candidates strictly below this node.
func (current *node) candidateDescendants() int {
	total := 0
	for _, child := range current.children {
		if child.candidate {
			total++
		}
		total += child.candidateDescendants()
	}
	return total
}

// Tree renders the candidates as a directory tree under the root's name (the
// header carries the full path). A removed directory hides
// the empty directories beneath it behind a count, and chains of kept
// directories with a single child are joined into one line ("a/b/c/").
func Tree(root string, candidates []sweep.Candidate, styles theme.Styles) string {
	trieRoot := buildTrie(root, candidates)
	rootLabel := filepath.Base(root)
	if rootLabel != string(filepath.Separator) {
		rootLabel += string(filepath.Separator)
	}
	rendered := newStyledTree(styles.TreeRoot.Render(rootLabel), styles)
	for _, child := range trieRoot.sortedChildren() {
		rendered.Child(renderNode(child, styles))
	}
	return rendered.String()
}

func newStyledTree(label string, styles theme.Styles) *tree.Tree {
	return tree.Root(label).
		Enumerator(tree.RoundedEnumerator).
		EnumeratorStyle(styles.Enumerator).
		IndenterStyle(styles.Enumerator)
}

// renderNode returns a plain label for leaves and a subtree otherwise.
func renderNode(current *node, styles theme.Styles) any {
	if current.candidate {
		return candidateLabel(current, styles)
	}

	name := current.name
	for len(current.children) == 1 {
		onlyChild := current.sortedChildren()[0]
		if onlyChild.candidate {
			break
		}
		name += "/" + onlyChild.name
		current = onlyChild
	}

	subtree := newStyledTree(styles.Kept.Render(name+"/"), styles)
	for _, child := range current.sortedChildren() {
		subtree.Child(renderNode(child, styles))
	}
	return subtree
}

func candidateLabel(current *node, styles theme.Styles) string {
	label := styles.Removed.Render("✗ " + current.name + "/")
	var notes []string
	if len(current.junkFiles) > 0 {
		notes = append(notes, junkNames(current.junkFiles))
	}
	if hidden := current.candidateDescendants(); hidden > 0 {
		notes = append(notes, fmt.Sprintf("+%s empty inside", humanize.Comma(int64(hidden))))
	}
	if len(notes) > 0 {
		label += styles.Note.Render(" · " + strings.Join(notes, " · "))
	}
	return label
}

// maxNamedJunkFiles caps how many junk file names a tree line lists.
const maxNamedJunkFiles = 3

// junkNames lists junk file names for a tree line, marking judged ones and
// summarising the rest once there are too many.
func junkNames(junkFiles []sweep.JunkFile) string {
	var names []string
	for index, junkFile := range junkFiles {
		if index == maxNamedJunkFiles {
			names = append(names, fmt.Sprintf("+%d more", len(junkFiles)-maxNamedJunkFiles))
			break
		}
		name := printableName(junkFile.Name)
		if junkFile.Judged {
			name += "*"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// printableName escapes control characters in a file name, such as the
// carriage return in macOS's "Icon\r", so they cannot garble the terminal.
func printableName(name string) string {
	quoted := strconv.Quote(name)
	return quoted[1 : len(quoted)-1]
}
