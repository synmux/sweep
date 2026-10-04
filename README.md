# sweep

`sweep` removes empty directories below a directory you give it.

A directory counts as empty if it holds nothing at all, only a `.DS_Store` file, or only directories that are themselves empty. So a tree of nested empty folders, each with a stray `.DS_Store`, goes in one pass. The directory you point it at is never removed, even if it ends up empty.

## Usage

```plaintext
sweep [DIRECTORY] [--dry-run] [--yes]
```

| Flag | Effect |
|---|---|
| `-n`, `--dry-run` | Show what would be removed. Nothing is touched. |
| `-y`, `--yes` | Remove without the confirmation prompt. |
| `-v`, `--version` | Print the version. |
| `-h`, `--help` | Print help. |

`DIRECTORY` defaults to the current directory.

```plaintext
sweep --dry-run ~/Downloads
sweep ~/Projects
sweep --yes /Volumes/Backup | tee sweep.log
```

## What you see

In a terminal, `sweep`:

1. Scans with a spinner and a live count of directories and empties found.
2. Shows a tree of the directories it is about to remove. A removed directory lists its `.DS_Store` and how many empty directories sit inside it. You can scroll the tree.
3. Asks `Remove N empty directories? (y/N)`. Anything but `y` removes nothing.
4. Removes them behind a progress bar, then prints the tree and a summary.

Without a terminal on both stdin and stdout (in a pipe, cron or CI), there is no prompt. A real run then needs `--yes`, and output comes without colour.

## Safety

- Directories are deleted with `os.Remove`, never a recursive delete. If a file appears in a directory between the scan and the removal, that directory and its parents are kept and the failure is reported.
- Symbolic links are never followed. A symlink counts as content, so the directory holding it stays.
- An unreadable directory is treated as having content. It is kept, along with its parents, and the error is reported.
- `.DS_Store` is only deleted from directories that are being removed.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Finished, nothing to do, or you answered no |
| 1 | Bad arguments, or some directories could not be read or removed |
| 130 | Interrupted with ctrl+c |

## Building

```plaintext
go build -o sweep .
go install github.com/synmux/sweep@latest
```

Requires Go 1.27 or later.

## Development

```plaintext
gofmt -l .
go vet ./...
golangci-lint run ./...
go test -race ./...
```

Layout:

| Path | Contents |
|---|---|
| `main.go` | Entry point. Hands the cobra command to fang. |
| `internal/cli` | Flags, terminal detection, plain mode, exit codes |
| `internal/sweep` | Scanning and removal. No UI code. |
| `internal/report` | Tree, summary and error boxes |
| `internal/theme` | Light and dark styles from Charm's charmtone palette |
| `internal/ui` | Bubble Tea model |

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Bubbles](https://github.com/charmbracelet/bubbles), [Lip Gloss](https://github.com/charmbracelet/lipgloss) and [Fang](https://github.com/charmbracelet/fang).
