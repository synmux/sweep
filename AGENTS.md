# AGENTS.md

`sweep` is a Go CLI that removes empty directories below a root. A directory holding only `.DS_Store` files, or only empty directories, counts as empty. With `--jev`, TypeSafe's Jev model can also judge other files junk. The root itself is never removed. README.md covers usage.

## Commands

`./build.sh --check` runs everything below and then builds. Run it, or the four commands, before committing. All must pass.

```plaintext
gofmt -l .                 # must print nothing
go vet ./...
golangci-lint run ./...    # config in .golangci.yml (v2 format)
go test -race ./...         # includes one live Jev call if an API key is set; -short skips it
```

## Layout and boundaries

- `internal/sweep`: `Scan` walks with `os.ReadDir` into a tree of nodes, then (with a `Judge`) resolves directories holding other files level by level, deepest first, and finally collects candidates post-order. `Remove` relies on that children-first order. No UI imports.
- `internal/judgecache`: SQLite (`modernc.org/sqlite`, pure Go, so `CGO_ENABLED=0` builds keep working) cache that wraps any `sweep.Judge` with a `Fingerprint()`. Keys hash the request's absolute path, relative directory, file names, sizes and mtimes, empty subdirectories and the judge fingerprint. Stores probabilities, not verdicts. Entries expire after 30 days.
- `internal/jevjudge`: the `sweep.Judge` backed by `github.com/mattn/go-jev`. One Noul per file, state is the directory listing (names and sizes only). The criteria text was tuned on live answers; re-check with `go test -run TestLiveJev -v ./internal/jevjudge/` after changing it.
- `internal/report`: rendering only (tree, summary, errors, `Header`, `DisplayPath`, `TruncateLeft`). The terminal UI and plain mode both use it. Jev's per-file decisions render only with `Summary.ShowJudgements` (`--jev-results`), and always last, after the summary and errors; otherwise a one-line hint stands in.
- `internal/theme`: every colour and style. Add new styles here, not inline.
- `internal/ui`: the Bubble Tea v2 model, with phases scanning, review, removing and done. The done view is empty on purpose: `internal/cli` prints the final report after the program exits, so long lists land in scrollback.
- `internal/cli`: the cobra command, terminal detection, plain mode and `ExitCode`.

## Invariants

- Never use `os.RemoveAll`. `os.Remove` failing with ENOTEMPTY is the safety net against files that appear after the scan.
- Never follow symlinks. The walk relies on `DirEntry.IsDir()` being false for symlinks.
- Always-junk names live in `junkFiles` in `internal/sweep/scan.go`. Add new ones there only. Anything else is junk only when a `Judge` says so.
- Judged files carry size and modification time; `Remove` refuses to delete them if either changed. Never drop that check.
- A `Judge` error wrapping `sweep.ErrJudgeUnavailable` (401, 403, 422 from Jev) stops the scan. Other errors keep just that directory.
- Never send file contents to Jev. Names, sizes and the relative directory only. `JudgeRequest.Path` and `FileInfo.ModTime` exist for cache keys and must never reach the API (`TestRequestNeverSendsCacheOnlyFields`).
- Changing the Jev prompt's state layout means bumping `promptVersion` in `internal/jevjudge`; the instructions template and criteria are hashed into the fingerprint automatically. Changing the cache key layout means bumping `keyVersion` in `internal/judgecache`.
- API key lookup order is `$TYPESAFE_API_KEY`, `$JEV_API_KEY`, `$__TYPESAFE_API_KEY` (`jevjudge.APIKeyVariables`).
- Worker goroutines in `internal/ui` must always send their final `...DoneMsg`, even after cancellation, because the model waits for it before quitting.
- Keep the inline frame shorter than the terminal (`chromeLines` in `internal/ui/model.go`). A frame that fills the terminal scrolls it and leaves stale frames behind.

## Dependency pin

`charm.land/lipgloss/v2` is pinned to v2.0.5 on purpose. v2.0.6 pulls a newer `github.com/charmbracelet/ultraviolet` (20260811), and with that, bubbletea v2.0.10 does not erase its inline frame on exit. Re-test before bumping: run `sweep` in tmux on a directory with a few empties, press `n`, and check that `tmux capture-pane -p` shows only the header and "Nothing removed.".

## Tests

- `internal/ui` tests use `github.com/charmbracelet/x/exp/teatest/v2`.
- Judge tests use small table-driven fakes; `internal/jevjudge` tests use `httptest` with `jev.WithURL`.
- Filesystem tests build fixtures under `t.TempDir()`. Permission tests skip when running as root.
- Charm v2 modules live under `charm.land/...`, not `github.com/charmbracelet/...`.

## Commits

Use Conventional Commits with a gitmoji title, for example `✨ feat(ui): ...`. Commit after each feature, fix or refactor.
