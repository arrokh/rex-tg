# AGENTS.md

Guidance for coding agents (Claude Code, Codex, pi, and others) that work in this repository.

## Git

- Do not add AI co-author trailers to commit messages. No `Co-Authored-By: Claude …` line, and no such line for any other agent or tool.
- Commit only when you are asked to. Never force-push `main`.

## Build and check

- The Go version is in `.tool-versions` (mise). Use Go 1.27.3 or newer. In Go 1.26, the race detector can hang a forked child process on macOS. Such a child spins forever and keeps the test's files open, so `go test -race` fails at random.
- `make check` runs gofmt, `go vet`, staticcheck, and `go test -race ./...`.
- `go vet -tags e2e ./...` also checks the end-to-end tests.
- `make e2e` runs the end-to-end tests against a real Rex server. They use isolated sockets and scratch sessions.
- `make install` builds and installs `~/.local/bin/rex-tg`.

## Test routine

- While you work, run the tests of the package you change, for example `go test ./internal/daemon/` (about 10 seconds). `make core` runs one daemon test per core flow in a few seconds. Its list of tests is in the `Makefile`.
- Before a commit, run `make check` once. Also run `make e2e` when the change touches the daemon, Rex, or an agent.
- Check the real exit status of a test run. A pipe such as `go test … | tail` hides a failure.
- Run tests many times (`-count=N`) only to hunt a flaky test, not as a routine check.

## Writing tests

- Test logic with unit tests: call the parser, the state machine, or the decision with fixed input. For example, `internal/daemon/vim_settle_test.go` drives the vim steps against an in-memory pane.
- Daemon tests start the whole daemon with the fake Bot API and the fake `rex`. Use them for the core flows and for wiring, one test per flow, not one per case.
- Never wait for a production delay in a test. The daemon's fixed waits are in `daemon.Timing`, and the daemon tests use the short values in `testTiming`.
- Daemon tests run in parallel (`newEnv` calls `t.Parallel`). A test that changes the environment (`t.Setenv`) uses `newEnvSerial`.
- Do not write a new executable file (a script or a binary) for each test. macOS checks every new executable the first time it runs, and with many tests at once each check takes seconds. Build a program once and link to it, as `rextest.New` does.

## Rules that keep rex-tg safe

- At most once. Never send a Telegram message again on a guess, and never type a user's message into a pane twice. A send whose outcome is unknown is sent again only when the chat's message numbers prove that it never arrived (design 9.1); otherwise it waits for the user (`/resend` or the `🔁 Send again` button).
- Never type into a pane unless the expected agent process is in front of it.
- The bot token lives only in the macOS Keychain. Never put it in files, argv, logs, or test data.
- Tests must not send input to, capture, or close the user's own Rex panes or sessions. Use the fakes (`internal/rex/rextest`, `internal/telegram/tgtest`) or an isolated Rex server.
- Never copy real transcripts or other user data into tests or docs.
- Fake secrets in tests (tokens, API keys, JWTs, private keys) must not appear whole in any file: build them from pieces, such as `"ghp_" + "abc…"` or a split `BEGIN … PRIVATE KEY` header, and use obviously fake key bodies. Secret scanners (GitGuardian) read the raw text and flag whole fake values too.

## Docs

- Design: `docs/design.md`. Plan: `docs/plan.md`. Package contracts: `docs/implementation.md`. Findings from live tests: `docs/spikes/`.
- Write docs in plain, short sentences. Update them in the same change as the behavior they describe.
