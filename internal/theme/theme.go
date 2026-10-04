// Package theme holds the colours and lipgloss styles shared by the TUI and the
// printed report, built on Charm's charmtone palette.
package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
)

// Styles is the full set of styles, resolved for a light or dark background.
type Styles struct {
	// Badge is the "sweep" pill at the start of the header.
	Badge lipgloss.Style
	// Path renders the root directory in the header.
	Path lipgloss.Style
	// DryRunTag marks dry runs in the header.
	DryRunTag lipgloss.Style

	// TreeRoot, Kept, Removed and Note style the directory tree.
	TreeRoot   lipgloss.Style
	Kept       lipgloss.Style
	Removed    lipgloss.Style
	Note       lipgloss.Style
	Enumerator lipgloss.Style

	// Label and Value style summary rows; Success and Warning colour outcomes.
	Label   lipgloss.Style
	Value   lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
	Faint   lipgloss.Style

	// SummaryBox and ErrorBox frame the summary and error listings.
	SummaryBox lipgloss.Style
	ErrorBox   lipgloss.Style

	// Prompt styles the confirmation question; Key highlights key hints.
	Prompt lipgloss.Style
	Key    lipgloss.Style

	// Spinner colours the scanning spinner.
	Spinner lipgloss.Style
	// ProgressBlend is the gradient for the removal progress bar.
	ProgressBlend []color.Color
}

// New returns the styles for a dark (isDark) or light terminal background.
func New(isDark bool) Styles {
	pick := lipgloss.LightDark(isDark)

	text := pick(charmtone.Pepper, charmtone.Salt)
	accent := charmtone.Charple
	removed := pick(charmtone.Sriracha, charmtone.Coral)
	dim := pick(charmtone.Squid, charmtone.Squid)
	faint := pick(charmtone.Smoke, charmtone.Oyster)
	success := pick(charmtone.Zinc, charmtone.Guac)
	warning := pick(charmtone.Paprika, charmtone.Tang)

	return Styles{
		Badge:     lipgloss.NewStyle().Bold(true).Foreground(charmtone.Butter).Background(accent).Padding(0, 1),
		Path:      lipgloss.NewStyle().Bold(true).Foreground(text),
		DryRunTag: lipgloss.NewStyle().Bold(true).Foreground(charmtone.Pepper).Background(charmtone.Mustard).Padding(0, 1),

		TreeRoot:   lipgloss.NewStyle().Bold(true).Foreground(pick(charmtone.Charple, charmtone.Hazy)),
		Kept:       lipgloss.NewStyle().Foreground(dim),
		Removed:    lipgloss.NewStyle().Foreground(removed),
		Note:       lipgloss.NewStyle().Foreground(faint).Italic(true),
		Enumerator: lipgloss.NewStyle().Foreground(faint).PaddingRight(1),

		Label:   lipgloss.NewStyle().Foreground(dim).Width(14),
		Value:   lipgloss.NewStyle().Bold(true).Foreground(text),
		Success: lipgloss.NewStyle().Bold(true).Foreground(success),
		Warning: lipgloss.NewStyle().Bold(true).Foreground(warning),
		Faint:   lipgloss.NewStyle().Foreground(faint),

		SummaryBox: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 2),
		ErrorBox:   lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(warning).Padding(0, 1),

		Prompt: lipgloss.NewStyle().Bold(true).Foreground(text),
		Key:    lipgloss.NewStyle().Bold(true).Foreground(pick(charmtone.Charple, charmtone.Dolly)),

		Spinner:       lipgloss.NewStyle().Foreground(charmtone.Dolly),
		ProgressBlend: []color.Color{charmtone.Charple, charmtone.Dolly},
	}
}
