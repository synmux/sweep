// Package cli wires sweep's command line: argument parsing, choosing between
// the interactive TUI and plain output, printing the report and mapping the
// outcome to an error for the exit code.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
	"github.com/dustin/go-humanize/english"
	"github.com/spf13/cobra"

	"github.com/synmux/sweep/internal/report"
	"github.com/synmux/sweep/internal/sweep"
	"github.com/synmux/sweep/internal/theme"
	"github.com/synmux/sweep/internal/ui"
)

// ErrCancelled means the user interrupted the run.
var ErrCancelled = errors.New("cancelled")

// ErrNeedsConfirmation means a real run was asked for without a terminal to
// confirm on and without --yes.
var ErrNeedsConfirmation = errors.New("not running in a terminal, so there is nobody to confirm: pass --yes to remove, or --dry-run to preview")

// ExitCode maps an error returned by the command to a process exit code.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrCancelled):
		return 130
	default:
		return 1
	}
}

// IsInteractive reports whether both stdin and stdout are terminals.
func IsInteractive() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

type flags struct {
	dryRun    bool
	assumeYes bool
}

// NewCommand builds the root command. interactive decides between the TUI
// and plain output; pass IsInteractive outside tests.
func NewCommand(interactive func() bool) *cobra.Command {
	var options flags
	command := &cobra.Command{
		Use:   "sweep [DIRECTORY]",
		Short: "Remove empty directories, including ones holding only .DS_Store",
		Long: "sweep walks DIRECTORY (default: the current directory) and removes every empty\n" +
			"directory below it. A directory holding only a .DS_Store file counts as empty, and\n" +
			"so does one left holding only empty directories. DIRECTORY itself is never removed.\n\n" +
			"Before removing anything, sweep shows what it found and asks for confirmation.",
		Example: "  sweep --dry-run ~/Downloads\n  sweep ~/Projects\n  sweep --yes /Volumes/Backup | tee sweep.log",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			target := "."
			if len(args) == 1 {
				target = args[0]
			}
			root, err := resolveRoot(target)
			if err != nil {
				return err
			}
			if interactive() {
				return runInteractive(command.Context(), command.OutOrStdout(), root, options)
			}
			return runPlain(command.Context(), command.OutOrStdout(), root, options)
		},
	}
	command.Flags().BoolVarP(&options.dryRun, "dry-run", "n", false, "show what would be removed without removing anything")
	command.Flags().BoolVarP(&options.assumeYes, "yes", "y", false, "remove without asking for confirmation")
	return command
}

// resolveRoot returns the absolute path of target, which must be a directory.
func resolveRoot(target string) (string, error) {
	root, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", target, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		// Drop the "stat" prefix: fang capitalises errors, which reads badly.
		if pathError, isPathError := errors.AsType[*fs.PathError](err); isPathError {
			return "", fmt.Errorf("cannot open %s: %w", root, pathError.Err)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", root)
	}
	return root, nil
}

func runInteractive(ctx context.Context, output io.Writer, root string, options flags) error {
	model := ui.New(ctx, ui.Options{Root: root, DryRun: options.dryRun, AssumeYes: options.assumeYes})
	if _, err := tea.NewProgram(model, tea.WithContext(ctx)).Run(); err != nil {
		if ctx.Err() != nil {
			return ErrCancelled
		}
		return err
	}

	outcome := model.Outcome()
	if outcome.Err != nil {
		return outcome.Err
	}
	styles := theme.New(outcome.IsDark)
	if outcome.Declined {
		_, err := lipgloss.Fprintln(output, report.Header(root, options.dryRun, styles, outcome.Width)+"\n\n"+
			styles.Faint.Render("Nothing removed."))
		return err
	}
	if _, err := lipgloss.Fprintln(output, report.Render(outcome.Summary, styles, outcome.Width)); err != nil {
		return err
	}
	if outcome.Cancelled {
		return ErrCancelled
	}
	return problemsError(outcome.Summary)
}

func runPlain(ctx context.Context, output io.Writer, root string, options flags) error {
	if !options.dryRun && !options.assumeYes {
		return ErrNeedsConfirmation
	}

	started := time.Now()
	result, err := sweep.Scan(ctx, root, nil)
	if err != nil {
		return cancelledOr(err)
	}
	summary := report.FromScan(result, options.dryRun, time.Since(started))

	var removeErr error
	if !options.dryRun && len(result.Candidates) > 0 {
		removal, err := sweep.Remove(ctx, result.Candidates, nil)
		summary.Removal = &removal
		removeErr = err
	}

	// lipgloss.Fprintln drops colour when output is not a terminal.
	if _, err := lipgloss.Fprintln(output, report.Render(summary, theme.New(true), 0)); err != nil {
		return err
	}
	if removeErr != nil {
		return cancelledOr(removeErr)
	}
	return problemsError(summary)
}

func cancelledOr(err error) error {
	if errors.Is(err, context.Canceled) {
		return ErrCancelled
	}
	return err
}

// problemsError returns an error when the run hit any per-directory problems,
// so the exit code reflects them. The details are already in the report.
func problemsError(summary report.Summary) error {
	count := len(summary.ScanErrors)
	if summary.Removal != nil {
		count += len(summary.Removal.Errors)
	}
	if count == 0 {
		return nil
	}
	return fmt.Errorf("finished with %s; see the report above", english.Plural(count, "problem", "problems"))
}
