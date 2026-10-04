# AGENTS.md

`sweep` is a Go CLI that removes empty directories below a root. A directory holding only `.DS_Store` files, or only empty directories, counts as empty. The root itself is never removed. README.md covers usage.

## Commands

Run all four before committing. All must pass.

```plaintext
gofmt -l .                 # must print nothing
go vet ./...
golangci-lint run ./...    # config in .golangci.yml (v2 format)
go test -race ./...
```

## Layout and boundaries

- `internal/sweep`: `Scan` (post-order walk with `os.ReadDir`) and `Remove`. No UI imports. Candidates come out children first, and `Remove` relies on that order.
- `internal/report`: rendering only (tree, summary, errors, `Header`, `DisplayPath`, `TruncateLeft`). The terminal UI and plain mode both use it.
- `internal/theme`: every colour and style. Add new styles here, not inline.
- `internal/ui`: the Bubble Tea v2 model, with phases scanning, review, removing and done. The done view is empty on purpose: `internal/cli` prints the final report after the program exits, so long lists land in scrollback.
- `internal/cli`: the cobra command, terminal detection, plain mode and `ExitCode`.

## Invariants

- Never use `os.RemoveAll`. `os.Remove` failing with ENOTEMPTY is the safety net against files that appear after the scan.
- Never follow symlinks. The walk relies on `DirEntry.IsDir()` being false for symlinks.
- Junk file names live in `junkFiles` in `internal/sweep/scan.go`. Add new ones there only.
- Worker goroutines in `internal/ui` must always send their final `...DoneMsg`, even after cancellation, because the model waits for it before quitting.
- Keep the inline frame shorter than the terminal (`chromeLines` in `internal/ui/model.go`). A frame that fills the terminal scrolls it and leaves stale frames behind.

## Dependency pin

`charm.land/lipgloss/v2` is pinned to v2.0.5 on purpose. v2.0.6 pulls a newer `github.com/charmbracelet/ultraviolet` (20260811), and with that, bubbletea v2.0.10 does not erase its inline frame on exit. Re-test before bumping: run `sweep` in tmux on a directory with a few empties, press `n`, and check that `tmux capture-pane -p` shows only the header and "Nothing removed.".

## Tests

- `internal/ui` tests use `github.com/charmbracelet/x/exp/teatest/v2`.
- Filesystem tests build fixtures under `t.TempDir()`. Permission tests skip when running as root.
- Charm v2 modules live under `charm.land/...`, not `github.com/charmbracelet/...`.

## Commits

Use Conventional Commits with a gitmoji title, for example `✨ feat(ui): ...`. Commit after each feature, fix or refactor.
