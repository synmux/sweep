package report

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/synmux/sweep/internal/sweep"
	"github.com/synmux/sweep/internal/theme"
)

// Judgements renders Jev's decision for each directory it judged: whether the
// directory counts as empty, and each file's junk probability.
func Judgements(root string, judgements []sweep.Judgement, threshold float64, styles theme.Styles) string {
	heading := styles.Prompt.Render("🤖 Jev decisions")
	if threshold > 0 {
		heading += styles.Faint.Render(fmt.Sprintf(" · junk at ≥ %.2f · * marks judged junk in the tree", threshold))
	}
	blocks := []string{heading}
	for _, judgement := range judgements {
		blocks = append(blocks, judgementBlock(root, judgement, styles))
	}
	return strings.Join(blocks, "\n")
}

func judgementBlock(root string, judgement sweep.Judgement, styles theme.Styles) string {
	label := styles.Path.Render(relativeLabel(root, judgement.Path)) + " "
	switch {
	case judgement.Err != nil:
		label += styles.Warning.Render("error") + styles.Faint.Render(" · kept · "+judgement.Err.Error())
	case judgement.Skipped != "":
		label += styles.Kept.Render("not asked") + styles.Faint.Render(" · kept · "+judgement.Skipped)
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
