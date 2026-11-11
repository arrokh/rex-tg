# AGENTS.md

Guidance for coding agents (Claude Code, Codex, pi, and others) that work in this repository.

## Git

- Do not add AI co-author trailers to commit messages. No `Co-Authored-By: Claude …` line, and no such line for any other agent or tool.
- Commit only when you are asked to. Never force-push `main`.

## Build and check

- The Go version is in `.tool-versions` (mise).
- `make check` runs gofmt, `go vet`, staticcheck, and `go test -race ./...`.
- `go vet -tags e2e ./...` also checks the end-to-end tests.
- `make e2e` runs the end-to-end tests against a real Rex server. They use isolated sockets and scratch sessions.
- `make install` builds and installs `~/.local/bin/rex-tg`.

## Rules that keep rex-tg safe

- At most once. Never send a Telegram message again on a guess, and never type a user's message into a pane twice. A send whose outcome is unknown waits for the user (`/resend` or the `🔁 Send again` button).
- Never type into a pane unless the expected agent process is in front of it.
- The bot token lives only in the macOS Keychain. Never put it in files, argv, logs, or test data.
- Tests must not send input to, capture, or close the user's own Rex panes or sessions. Use the fakes (`internal/rex/rextest`, `internal/telegram/tgtest`) or an isolated Rex server.
- Never copy real transcripts or other user data into tests or docs.

## Docs

- Design: `docs/design.md`. Plan: `docs/plan.md`. Package contracts: `docs/implementation.md`. Findings from live tests: `docs/spikes/`.
- Write docs in plain, short sentences. Update them in the same change as the behavior they describe.
