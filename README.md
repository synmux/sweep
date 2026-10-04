# sweep

`sweep` removes empty directories below a directory you give it.

A directory counts as empty if it holds nothing at all, only a `.DS_Store` file, or only directories that are themselves empty. So a tree of nested empty folders, each with a stray `.DS_Store`, goes in one pass. The directory you point it at is never removed, even if it ends up empty.

## Usage

```plaintext
sweep [DIRECTORY] [--dry-run] [--yes] [--jev]
```

| Flag | Effect |
|---|---|
| `-n`, `--dry-run` | Show what would be removed. Nothing is touched. |
| `-y`, `--yes` | Remove without the confirmation prompt. |
| `-J`, `--jev` | Ask TypeSafe's Jev model whether other files are junk. See [Jev](#judging-other-files-with-jev). |
| `--jev-threshold` | Probability at or above which Jev's answer makes a file junk. Default `0.8`. |
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

## Judging other files with Jev

On its own, `sweep` only treats `.DS_Store` as junk. With `--jev`, a directory also counts as empty when every other file in it is OS junk or a useless leftover, such as `Thumbs.db`, `desktop.ini`, Office lock files or crash dumps. The rule still cascades: a parent holding only such directories goes too.

[Jev](https://docs.typesafe.ai) is TypeSafe's decision model: instead of writing text, it returns the probability that a yes/no question's answer is yes. `sweep` asks it one question per file, whether the file is junk that can be deleted without losing anything a person would want. It sends the directory's path below the root, each file's name and size, and the names of its empty subdirectories. `sweep` never sends file contents. A file is junk when Jev's probability reaches `--jev-threshold`.

To keep calls down, `sweep` only asks about a directory when nothing else stops it being empty. It asks the deepest directories first, several at once, and never asks about a parent once a child has been kept. Directories with more than 32 files are kept without asking.

`--dry-run` lists every decision: the probability for each file and whether its directory counts as empty. A real run shows the same list before asking for confirmation, and files Jev picked are marked with `*` in the tree.

```plaintext
sweep --jev --dry-run ~/Downloads
sweep --jev --jev-threshold 0.9 ~/Downloads
```

The API key is read from the first of these that is set: `$TYPESAFE_API_KEY`, `$JEV_API_KEY`, `$__TYPESAFE_API_KEY`. A rejected key stops the run before anything is removed. Answers near the threshold can change from run to run, so check the dry run before a real one.

## Safety

- Directories are deleted with `os.Remove`, never a recursive delete. If a file appears in a directory between the scan and the removal, that directory and its parents are kept and the failure is reported.
- Symbolic links are never followed. A symlink counts as content, so the directory holding it stays.
- An unreadable directory is treated as having content. It is kept, along with its parents, and the error is reported.
- `.DS_Store` is deleted only from directories that are being removed.
- A file Jev judged junk is deleted only if its size and modification time haven't changed since the scan. Otherwise that directory and its parents are kept.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Finished, nothing to do, or you answered no |
| 1 | Bad arguments, or some directories could not be read or removed |
| 130 | Interrupted with ctrl+c |

## Building

```plaintext
./build.sh                 # build ./sweep
./build.sh --check         # run formatting, vet, lint and tests first
./build.sh --install       # install into ~/.local/bin ($SWEEP_INSTALL_DIR overrides)
./build.sh --output PATH   # write the binary somewhere else
```

`build.sh` checks your Go version against `go.mod`, downloads and verifies modules, and stamps the version from `git describe`. It needs Go 1.27.1 or later; with an older Go, the Go toolchain downloads the right version unless `GOTOOLCHAIN=local` is set. `golangci-lint` is used by `--check` when installed.

To install without cloning the repository:

```plaintext
go install github.com/synmux/sweep@latest
```

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
| `internal/sweep` | Scanning, judging and removal. No UI code. |
| `internal/jevjudge` | The Jev-backed judge and API key lookup |
| `internal/report` | Tree, summary and error boxes |
| `internal/theme` | Light and dark styles from Charm's charmtone palette |
| `internal/ui` | Bubble Tea model |

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Bubbles](https://github.com/charmbracelet/bubbles), [Lip Gloss](https://github.com/charmbracelet/lipgloss) and [Fang](https://github.com/charmbracelet/fang).
