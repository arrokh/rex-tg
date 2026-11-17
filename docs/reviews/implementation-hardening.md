# Implementation hardening review

## Scope and result

This is a historical review of the source at `d7ad809`, not a fresh audit of current `main`. Four audit lanes covered delivery/storage/Telegram, daemon input and lifecycle, Mini App security/resources, and transcript/registry/turn processing. Three isolated implementation lanes and two fresh acceptance reviewers supported the fixes. The parent integrated and verified their changes.

At review time the initial worktree was clean and the changes were uncommitted. They were later committed as `13103e8`, merged through PR [#38](https://github.com/arrokh/rex-tg/pull/38), and released as v0.5.1. The review did not change the live daemon, user panes, credentials, or installed binary; end-to-end tests used isolated Rex services. Task-owned worktrees were removed and their absence verified.

## Findings and changes

- **P1 — stale delivery targets.** `internal/daemon/typing.go` checked the process but not the prepared topic/conversation throughout delivery. It now revalidates the topic, conversation, pane, and PID after external reads and before Vim keys, paste, and submit. Unlink cancels in-flight delivery. `commands.go` also checks the binding before sending keys. Text already pasted is never automatically retried.
- **P2 — stale startup answers.** `startup.go` accepted a matching runtime even after process replacement. Questions now retain their observed PID, and every answer key must target that process.
- **P2 — Send now bypassed idle-only input.** `updates.go` exempted the clicked row from the steerability check. Controls, shell/agent commands, and prompt notes now remain queued until idle, including through the preparation safety check.
- **P2 — interrupted media work and excessive workers.** `media.go` previously advanced the poll offset before retaining recoverable worker state. File metadata now precedes download work; startup offers an explicit retry unless its text row already exists. Voice work has a durable marker and one interruption notice. Downloads/transcriptions share four slots; excess work starts no waiting worker. Permanent streaming-size rejection clears metadata with its notice atomically.
- **P2 — transcript recovery gaps.** `transcripts.go` previously saved backfill cursors before outputs, mutated trackers before failed commits, and saved snapshots after cursor commits. Outputs, cursor, and tracker snapshot now commit together. Failed candidates are discarded. Missing-cursor startup retries configured backfill.
- **P2 — replay side effects.** Acceptance review found replay could recapture a turn-start tree or forget an already acknowledged prompt's Telegram origin. Prompt-keyed trees now survive until the answer transaction commits; stale tree workers are ignored. Exact acknowledged event identities remain recognizable across replay/restart, without matching another event with the same text.
- **P2 — delayed dropped-event recovery.** `lifecycle.go` consumed stream errors only after events closed. It now drains both channels together and requests a scan when a drop is reported.
- **P2 — unknown-send notices could disappear on restart.** `sender/outbox.go` and `store/outbox.go` now commit terminal notices with the checked flag. After converting a check note, checked state and removal of its resume key commit together. Unknown original sends are still not retried without proof or owner action.
- **P2 — emoji-rich fallback could exceed Telegram's limit.** Rich fallback now uses UTF-16 length and sends a document when one formatted text part is insufficient.
- **P2 — quadratic backlog selection.** `NextParts` now ranks pending parts once per topic instead of repeatedly scanning the backlog. Ordering, closed-topic exclusion, and sending-part blocking are covered by tests.
- **P2 — filesystem work was not bounded by response limits.** Mini App listings now inspect at most 4,096 raw names plus a truncation probe, sort only that subset, and warn about possible omissions. Eight distinct outstanding filesystem reads share a budget. Timed-out work keeps its slot until it actually finishes; duplicate requests still join.
- **P2 — concurrent backup overwrite.** Installer backup publication now uses an atomic hard link rather than a check followed by rename. Concurrent creators cannot overwrite the same backup.

## Performance evidence

Same synthetic seed: 2,000 topics, one pending part each. Apple M4 Max, Go 1.27.3, three benchmark iterations:

| Query | Time per selection |
| --- | ---: |
| Original correlated query | 857.29 ms |
| Ranked query | 10.86 ms |

This is about 79 times faster for that scheduling query. Allocations were essentially unchanged. It is not a measurement of end-to-end Telegram latency or a live workload.

## Verification

All passed on the integrated code:

- `GOMAXPROCS=4 GOFLAGS='-p=1' make check`
- `GOFLAGS='-p=1' go vet -tags e2e ./...`
- `GOMAXPROCS=4 GOFLAGS='-p=1' make e2e`
- `GOMAXPROCS=4 GOFLAGS='-p=1' go test ./internal/store -run '^$' -bench '^BenchmarkNextPartsManyTopics' -benchtime=3x -benchmem`
- `git diff --check`

Regression tests cover failed commits, replay/restart, stale bindings, process replacement, interrupted media, resource limits, oversized downloads, emoji fallback, terminal-notice recovery, and concurrent backup creation. Some concurrent worker-wide test runs encountered timing-sensitive failures; targeted reruns and the final parent checks passed.

## Remaining issues and limits

1. **P2 — asynchronous control/prompt-note preparation still has a crash-loss window.** `controls.go` and alert-note workers can stop after the source update is recorded but before the queued-text event becomes durable. A further change should durably represent preparation intent/result, preserving at-most-once prompt answers. The media fixes do not solve this separate handoff.
2. **P2 — very long tool-heavy turns retain all tool-call history.** `internal/turn/turn.go` keeps the full call list; snapshots therefore grow with the turn. A bounded representation must preserve totals, active calls, result matching, display history, and snapshot compatibility. That model change was not attempted in this bounded pass.
3. There is no new aggregate upload-storage quota. Existing per-file limits and seven-day cleanup remain. Choosing a total disk quota and eviction policy needs a separate decision.

**Recommendation: NOT READY for an unconditional whole-system reliability sign-off.** The fixes above pass verification, but the two remaining P2 issues need follow-up. No claim is made that the codebase is defect-free.
