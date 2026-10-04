// Package ui is the Bubble Tea front end for sweep. It scans with a spinner,
// shows the tree of empty directories for review, asks for confirmation, and
// removes them behind a progress bar. The final report is printed by the
// caller after the program exits, so long lists end up in scrollback.
package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/dustin/go-humanize"
	"github.com/dustin/go-humanize/english"

	"github.com/synmux/sweep/internal/report"
	"github.com/synmux/sweep/internal/sweep"
	"github.com/synmux/sweep/internal/theme"
)

const (
	// defaultWidth is used until the terminal reports its size.
	defaultWidth = 80
	// defaultHeight is used until the terminal reports its size.
	defaultHeight = 24
	// maxProgressWidth caps the progress bar so it stays readable on wide terminals.
	maxProgressWidth = 60
	// chromeLines is the room the review screen needs around the tree: six
	// lines of header, hint, prompt and help, plus two spare so the frame never
	// fills the terminal (that scrolls it and leaves stale frames behind).
	chromeLines = 8
	// minTreeLines keeps the review viewport usable on tiny terminals.
	minTreeLines = 3
)

// Options configure a run.
type Options struct {
	// Root is the directory to sweep.
	Root string
	// DryRun scans and reports without removing anything.
	DryRun bool
	// AssumeYes skips the confirmation prompt.
	AssumeYes bool
	// Judge, when set, decides whether files other than .DS_Store are junk.
	Judge sweep.Judge
	// JunkThreshold is passed to sweep.Options; zero means the default.
	JunkThreshold float64
	// ShowJudgements adds Jev's per-file decisions to the review screen and
	// the final report (--jev-results).
	ShowJudgements bool
}

// Outcome is what the program produced, read once it has exited.
type Outcome struct {
	Summary report.Summary
	// IsDark reports the terminal background, so the caller can print the
	// report with matching styles.
	IsDark bool
	// Width is the terminal width, so the caller can fit the report to it.
	Width int
	// Cancelled is true when the user interrupted the run with ctrl+c.
	Cancelled bool
	// Declined is true when the user answered no at the confirmation prompt.
	Declined bool
	// Err is a fatal error, such as the root not being a directory.
	Err error
}

type phase int

const (
	phaseScanning phase = iota
	phaseReview
	phaseRemoving
	phaseDone
)

type (
	scanProgressMsg sweep.Progress
	scanDoneMsg     struct {
		result  sweep.Result
		err     error
		elapsed time.Duration
	}
	removeProgressMsg struct {
		processed int
		path      string
	}
	removeDoneMsg struct {
		result sweep.RemoveResult
		err    error
	}
)

// Model is the Bubble Tea model. Create it with New.
type Model struct {
	options Options
	ctx     context.Context
	cancel  context.CancelFunc
	// events carries messages from the scan and removal goroutines.
	events chan tea.Msg

	styles   theme.Styles
	isDark   bool
	keys     keyMap
	help     help.Model
	spinner  spinner.Model
	progress progress.Model
	viewport viewport.Model
	width    int
	height   int

	phase      phase
	cancelling bool

	scanProgress sweep.Progress
	scanResult   sweep.Result

	removedSoFar int
	lastRemoved  string

	outcome Outcome
}

// New builds a model for the given options. The caller should cancel parent
// to abort; the model also cancels its own context when the user quits.
func New(parent context.Context, options Options) *Model {
	ctx, cancel := context.WithCancel(parent)
	model := &Model{
		options: options,
		ctx:     ctx,
		cancel:  cancel,
		events:  make(chan tea.Msg, 1),
		keys:    newKeyMap(),
		help:    help.New(),
		width:   defaultWidth,
		height:  defaultHeight,
	}
	model.applyTheme(true)
	model.viewport = viewport.New()
	model.outcome.Summary = report.Summary{Root: options.Root, DryRun: options.DryRun}
	return model
}

// Outcome returns the result of the run. Call it after the program exits.
func (model *Model) Outcome() Outcome {
	model.outcome.IsDark = model.isDark
	model.outcome.Width = model.width
	return model.outcome
}

func (model *Model) applyTheme(isDark bool) {
	model.isDark = isDark
	model.styles = theme.New(isDark)
	model.help.Styles = help.DefaultStyles(isDark)
	model.spinner = spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(model.styles.Spinner))
	model.progress = progress.New(
		progress.WithColors(model.styles.ProgressBlend...),
		progress.WithWidth(model.progressWidth()),
	)
}

func (model *Model) progressWidth() int {
	return max(10, min(maxProgressWidth, model.width-20))
}

// Init starts the scan and the spinner, and asks for the background colour.
func (model *Model) Init() tea.Cmd {
	return tea.Batch(
		tea.RequestBackgroundColor,
		model.spinner.Tick,
		model.startScan(),
		model.waitForEvent(),
	)
}

// startScan runs the scan in a goroutine. Progress is sent without blocking,
// so a busy renderer drops snapshots instead of slowing the walk.
func (model *Model) startScan() tea.Cmd {
	ctx, root, events := model.ctx, model.options.Root, model.events
	scanOptions := sweep.Options{
		Judge:         model.options.Judge,
		JunkThreshold: model.options.JunkThreshold,
		Progress: func(snapshot sweep.Progress) {
			select {
			case events <- scanProgressMsg(snapshot):
			default:
			}
		},
	}
	return func() tea.Msg {
		go func() {
			started := time.Now()
			result, err := sweep.Scan(ctx, root, scanOptions)
			// The final message is always delivered, even after cancellation,
			// because the model waits for it before quitting.
			events <- scanDoneMsg{result: result, err: err, elapsed: time.Since(started)}
		}()
		return nil
	}
}

func (model *Model) startRemoval() tea.Cmd {
	ctx, candidates, events := model.ctx, model.scanResult.Candidates, model.events
	return func() tea.Msg {
		go func() {
			result, err := sweep.Remove(ctx, candidates, func(processed int, candidate sweep.Candidate, _ error) {
				select {
				case events <- removeProgressMsg{processed: processed, path: candidate.Path}:
				default:
				}
			})
			events <- removeDoneMsg{result: result, err: err}
		}()
		return nil
	}
}

func (model *Model) waitForEvent() tea.Cmd {
	events := model.events
	return func() tea.Msg { return <-events }
}

// Update handles a message and returns the next command.
func (model *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		model.applyTheme(msg.IsDark())
		model.refreshTree()
		return model, model.spinner.Tick

	case tea.WindowSizeMsg:
		model.width, model.height = msg.Width, msg.Height
		model.help.SetWidth(msg.Width)
		model.progress.SetWidth(model.progressWidth())
		model.refreshTree()
		return model, nil

	case tea.KeyPressMsg:
		return model.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		model.spinner, cmd = model.spinner.Update(msg)
		return model, cmd

	case progress.FrameMsg:
		var cmd tea.Cmd
		model.progress, cmd = model.progress.Update(msg)
		return model, cmd

	case scanProgressMsg:
		model.scanProgress = sweep.Progress(msg)
		return model, model.waitForEvent()

	case scanDoneMsg:
		return model.handleScanDone(msg)

	case removeProgressMsg:
		model.removedSoFar, model.lastRemoved = msg.processed, msg.path
		total := len(model.scanResult.Candidates)
		return model, tea.Batch(model.progress.SetPercent(float64(msg.processed)/float64(total)), model.waitForEvent())

	case removeDoneMsg:
		removal := msg.result
		model.outcome.Summary.Removal = &removal
		if errors.Is(msg.err, context.Canceled) {
			model.outcome.Cancelled = true
		}
		return model.finish()
	}

	if model.phase == phaseReview {
		var cmd tea.Cmd
		model.viewport, cmd = model.viewport.Update(msg)
		return model, cmd
	}
	return model, nil
}

func (model *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, model.keys.quit) {
		return model.abort()
	}

	if model.phase != phaseReview {
		return model, nil
	}
	switch {
	case key.Matches(msg, model.keys.confirm):
		return model.beginRemoval()
	case key.Matches(msg, model.keys.decline):
		model.outcome.Declined = true
		return model.finish()
	}
	var cmd tea.Cmd
	model.viewport, cmd = model.viewport.Update(msg)
	return model, cmd
}

// abort handles ctrl+c. With a worker running it cancels and waits for the
// worker's final message, so partial results are reported accurately.
func (model *Model) abort() (tea.Model, tea.Cmd) {
	model.outcome.Cancelled = true
	model.cancel()
	if model.phase == phaseScanning || model.phase == phaseRemoving {
		model.cancelling = true
		return model, nil
	}
	return model.finish()
}

func (model *Model) handleScanDone(msg scanDoneMsg) (tea.Model, tea.Cmd) {
	model.scanResult = msg.result
	if msg.result.Root != "" {
		model.outcome.Summary = report.FromScan(msg.result, model.options.DryRun, msg.elapsed)
		model.outcome.Summary.JunkThreshold = model.junkThreshold()
		model.outcome.Summary.ShowJudgements = model.options.ShowJudgements
	}

	switch {
	case errors.Is(msg.err, context.Canceled):
		model.outcome.Cancelled = true
		return model.finish()
	case msg.err != nil:
		model.outcome.Err = msg.err
		return model.finish()
	case len(msg.result.Candidates) == 0, model.options.DryRun:
		return model.finish()
	case model.options.AssumeYes:
		return model.beginRemoval()
	}

	model.phase = phaseReview
	model.refreshTree()
	return model, nil
}

func (model *Model) beginRemoval() (tea.Model, tea.Cmd) {
	model.phase = phaseRemoving
	return model, tea.Batch(model.startRemoval(), model.waitForEvent())
}

func (model *Model) finish() (tea.Model, tea.Cmd) {
	model.phase = phaseDone
	model.cancel()
	return model, tea.Quit
}

// refreshTree sizes the review viewport to the tree and the terminal.
func (model *Model) refreshTree() {
	if model.phase != phaseReview {
		return
	}
	content := report.Tree(model.outcome.Summary.Root, model.scanResult.Candidates, model.styles)
	if judgements := model.scanResult.Judgements; len(judgements) > 0 && model.options.ShowJudgements {
		content += "\n\n" + report.Judgements(model.outcome.Summary.Root, judgements, model.junkThreshold(), model.styles)
	}
	treeLines := strings.Count(content, "\n") + 1
	model.viewport.SetWidth(model.width)
	model.viewport.SetHeight(min(treeLines, max(minTreeLines, model.height-chromeLines)))
	model.viewport.SetContent(content)
}

// View renders the current phase. The done phase renders nothing, because
// the caller prints the full report once the program has exited.
func (model *Model) View() tea.View {
	if model.phase == phaseDone {
		return tea.NewView("")
	}

	header := report.Header(model.outcome.Summary.Root, model.options.DryRun, model.styles, model.width)
	var body string
	switch model.phase {
	case phaseScanning:
		body = model.scanningView()
	case phaseReview:
		body = model.reviewView()
	case phaseRemoving:
		body = model.removingView()
	}
	if model.cancelling {
		body += "\n" + model.styles.Warning.Render("Cancelling…")
	}
	return tea.NewView(header + "\n\n" + body + "\n")
}

// junkThreshold is the threshold in effect, or zero without a Judge.
func (model *Model) junkThreshold() float64 {
	switch {
	case model.options.Judge == nil:
		return 0
	case model.options.JunkThreshold > 0:
		return model.options.JunkThreshold
	default:
		return sweep.DefaultJunkThreshold
	}
}

func (model *Model) scanningView() string {
	styles := model.styles
	if model.scanProgress.Phase == sweep.PhaseJudging {
		status := fmt.Sprintf("%s %s  %s %s",
			model.spinner.View(),
			styles.Prompt.Render("Asking Jev"),
			styles.Value.Render(fmt.Sprintf("%s/%s",
				humanize.Comma(int64(model.scanProgress.DirsJudged)), humanize.Comma(int64(model.scanProgress.DirsToJudge)))),
			styles.Faint.Render("directories at most"),
		)
		return status + "\n" + styles.Faint.Render("  "+report.TruncateLeft(report.DisplayPath(model.scanProgress.CurrentDir), model.width-2))
	}
	status := fmt.Sprintf("%s %s  %s %s",
		model.spinner.View(),
		styles.Prompt.Render("Scanning"),
		styles.Value.Render(humanize.Comma(int64(model.scanProgress.DirsScanned))),
		styles.Faint.Render(english.PluralWord(model.scanProgress.DirsScanned, "directory", "directories")),
	)
	if found := model.scanProgress.CandidatesFound; found > 0 {
		status += styles.Faint.Render(" · ") + styles.Removed.Render(humanize.Comma(int64(found))+" empty")
	}
	return status + "\n" + styles.Faint.Render("  "+report.TruncateLeft(report.DisplayPath(model.scanProgress.CurrentDir), model.width-2))
}

func (model *Model) reviewView() string {
	styles := model.styles
	candidates := len(model.scanResult.Candidates)
	junk := model.scanResult.JunkFileCount()

	lines := []string{model.viewport.View()}
	if model.viewport.TotalLineCount() > model.viewport.VisibleLineCount() {
		lines = append(lines, styles.Faint.Render(fmt.Sprintf("  %d%% · scroll for more", int(model.viewport.ScrollPercent()*100))))
	}

	question := "Remove " + english.Plural(candidates, "empty directory", "empty directories")
	if junk > 0 {
		question += " and " + english.Plural(junk, "junk file", "junk files")
		if judged := model.scanResult.JudgedFileCount(); judged > 0 {
			question += fmt.Sprintf(" (%d judged by Jev)", judged)
		}
	}
	lines = append(lines, "",
		styles.Prompt.Render(question+"?")+" "+styles.Key.Render("(y/N)"),
		model.help.ShortHelpView([]key.Binding{model.keys.confirm, model.keys.decline, model.keys.scroll}),
	)
	return strings.Join(lines, "\n")
}

func (model *Model) removingView() string {
	styles := model.styles
	total := len(model.scanResult.Candidates)
	status := fmt.Sprintf("%s %s %s",
		styles.Prompt.Render("Removing"),
		model.progress.View(),
		styles.Faint.Render(fmt.Sprintf("%s/%s", humanize.Comma(int64(model.removedSoFar)), humanize.Comma(int64(total)))),
	)
	return status + "\n" + styles.Removed.Render("  ✗ ") + styles.Faint.Render(report.TruncateLeft(report.DisplayPath(model.lastRemoved), model.width-4))
}
