package report

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/synmux/sweep/internal/sweep"
	"github.com/synmux/sweep/internal/theme"
)

// Judgements renders Jev's decision for each directory it judged: whether the
// directory counts as empty, and each file's junk probability. Directories
// Jev was not asked about are left out; the heading counts them.
func Judgements(root string, judgements []sweep.Judgement, threshold float64, styles theme.Styles) string {
	heading := styles.Prompt.Render("🤖 Jev decisions")
	var notes []string
	if threshold > 0 {
		notes = append(notes, fmt.Sprintf("junk at ≥ %.2f", threshold))
	}
	if notAsked := len(judgements) - judgedCount(judgements); notAsked > 0 {
		notes = append(notes, fmt.Sprintf("%d not asked (too many files)", notAsked))
	}
	if len(notes) > 0 {
		heading += styles.Faint.Render(" · " + strings.Join(notes, " · "))
	}
	blocks := []string{heading}
	for _, judgement := range judgements {
		if judgement.Skipped == "" {
			blocks = append(blocks, judgementBlock(root, judgement, styles))
		}
	}
	return strings.Join(blocks, "\n")
}

// judgedCount counts judgements where Jev was asked (answered or failed).
func judgedCount(judgements []sweep.Judgement) int {
	total := 0
	for _, judgement := range judgements {
		if judgement.Skipped == "" {
			total++
		}
	}
	return total
}

func judgementBlock(root string, judgement sweep.Judgement, styles theme.Styles) string {
	label := styles.Path.Render(relativeLabel(root, judgement.Path)) + " "
	switch {
	case judgement.Err != nil:
		label += styles.Warning.Render("error") + styles.Faint.Render(" · kept · "+judgement.Err.Error())
	case judgement.Empty:
		label += styles.Removed.Render("✗ empty")
	default:
		label += styles.Success.Render("● kept")
	}
	if judgement.Cached {
		label += styles.Faint.Render(" · cached")
	}

	block := newStyledTree(label, styles)
	for _, file := range judgement.Files {
		block.Child(fileLine(file, styles))
	}
	return block.String()
}

// fileLine shows one file's probability, coloured by the verdict.
func fileLine(file sweep.FileVerdict, styles theme.Styles) string {
	probability := fmt.Sprintf("%.2f", file.JunkProbability)
	name := printableName(file.Name)
	if file.Junk {
		return styles.Removed.Render(probability+" junk ") + name
	}
	return styles.Success.Render(probability+" keep ") + name
}

// relativeLabel names a directory relative to the root, starting with the
// root's own name, as the tree does.
func relativeLabel(root, path string) string {
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.Join(filepath.Base(root), relativePath) + string(filepath.Separator)
}
