# Contributing to rex-tg

Thanks for helping. Start with the [README](README.md) and the [architecture, feature, and command guide](docs/architecture.md). [AGENTS.md](AGENTS.md) contains repository rules for coding agents; the same safety and test rules apply to human contributions.

## Set up a development checkout

You need macOS, Git, make, and [mise](https://mise.jdx.dev/). Install Rex Beta for isolated end-to-end tests. Agent CLIs and a real Telegram bot are not needed for the fake-service unit tests.

```sh
git clone https://github.com/arrokh/rex-tg.git
cd rex-tg
mise install
mise exec -- make build
mise exec -- make core
```

Go **1.27.3 or newer** is required for this project's checks; `.tool-versions` is the pinned version. Older Go race tooling can hang forked children on macOS. The module's minimum language version is not the development-toolchain requirement. Do not change `go.mod`, `go.sum`, or `internal/agents/agents.go` without first discussing the contract or dependency change with the maintainer.

`make build` writes `bin/rex-tg`. It does not install integrations or restart your daemon. **Do not use `make install` merely to test a change:** that replaces your real installed binary.

## Pick the right tests

| Change | Check |
| --- | --- |
| One package | `mise exec -- go test ./internal/<package>/` |
| Core daemon flows | `mise exec -- make core` |
| Any contribution before commit | `mise exec -- make check` |
| Daemon, Rex, or agent behavior | `mise exec -- go vet -tags e2e ./...` and `mise exec -- make e2e` |
| Docs | Check links and anchors, compare commands with handlers, and render Mermaid diagrams in addition to `make check`. |

`make check` runs gofmt verification, vet, staticcheck, and race tests. End-to-end tests create isolated Rex services and scratch sessions. Never point tests at your active Rex server, bot, config, or state folder. Check the actual exit code; a pipe to `tail` can hide failure. Repeat tests only when investigating a flaky test, not as a routine substitute for diagnosis.

## Safety rules for code and examples

- Never resend an uncertain Telegram message on a guess. Never type an input twice after an uncertain paste or Enter.
- Before input, verify the expected agent process and live topic/conversation binding. Keep pause and prompt guards intact.
- Never commit a bot token, credential, transcript, real screenshot, or private user data. Use synthetic fixtures and temporary folders.
- Fake secrets must also be split into pieces in source; scanners inspect raw text.
- Test parsers and decisions with fixed inputs. Use daemon integration tests for wiring, not one daemon per parsing case.
- Use injected timing rather than waiting for production delays. Follow the existing package's testing pattern.
- Keep original third-party notices. Do not describe all dependencies as MIT; see [third-party notices](THIRD_PARTY_NOTICES.md).

## Submit a pull request

1. Open an issue for a large feature, contract change, dependency change, or broad refactor. For a focused fix, a PR with a clear reproduction is enough.
2. Work on a task branch. Follow the existing `fix:`, `feat:`, `docs:`, or `build:` commit style. Do not add AI co-author trailers.
3. Add a regression test and update user docs in the same change as the behavior.
4. Explain what changed, why, exact checks run, and any risk or limitation. Do not claim checks that you did not run.
5. Keep generated output, credentials, local state, and unrelated edits out of the PR.

For bugs, include macOS version, `rex-tg version`, agent/runtime version, expected and actual behavior, and a minimal synthetic reproduction. Redact paths, account names, message text, and keys before sharing logs or `doctor` output. For vulnerabilities, follow [SECURITY.md](SECURITY.md), not a public bug report.

## License and attribution

Contributions to this repository are submitted under its [MIT License](LICENSE). Keep third-party notices with copied or vendored code, and identify its source and version. Do not contribute code or data you cannot legally share. There is no separate CLA specified by this repository.

## Maintainer checklist before a public launch

Adding a license does **not** change repository visibility. Before making a restricted repository public:

- Audit the **full Git history**, not just the current files, for credentials, transcripts, logs, screenshots, private infrastructure, and access-restricted links. Rotate any exposed credentials; deleting a file from a later commit is not enough.
- Confirm ownership and permission to distribute the original work, bundled assets, and linked dependencies. Preserve required third-party notices in source and release distributions.
- Publish a release that includes `LICENSE`, then update the Homebrew tag/revision and add `license "MIT"` metadata. The currently pinned v0.5.1 source predates this license file.
- Check that install URLs, release tags, and the Homebrew tap are accessible to unauthenticated users if that is the intended distribution model.
- Review the security-reporting channel and enable GitHub private vulnerability reporting if appropriate. Do not advertise a channel before it exists.
- Follow up the known reliability issues in the [hardening review](docs/reviews/implementation-hardening.md#remaining-issues-and-limits).
- Decide repository visibility, access, moderation, and support policy explicitly. This docs PR does not change account settings or certify the history safe for public release.
