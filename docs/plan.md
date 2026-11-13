# rex-tg implementation plan

Status: 2026-11-11. All product questions are answered. Work starts with the Phase 0 spikes. The design is in [design.md](design.md).

## Decisions made

| # | Question | Answer |
| --- | --- | --- |
| Q1 | Private chat with topics, or a supergroup? | Private chat with topics. |
| Q2 | What should a topic show? | A display like OpenClaw and Hermes Agent: one live progress message per turn, then the answer as a new message. |
| Q3 | What happens to a message sent while the agent works? | It waits until the agent is idle. A `⚡ Send now` button stays available as an explicit override. |
| Q4 | How much history when a topic opens? | The last 3 final answers. |
| Q5 | What is a topic bound to? | The agent session. A new session in the same pane (`/clear`, `/new`) takes over the topic. After a Rex restart, the topic follows the session. (Revised on 2026-11-11; it was "the pane" before.) |
| Q6 | Are T3 Code panes supported? | No. |
| Q7 | Which language? | Go. |
| Q8 | Are agents on other Rex hosts supported? | No. |
| Q9 | What happens to the progress message when a turn ends? | It becomes one summary line, with the tool rows in a collapsed quote. |
| Q10 | What does a progress row show for a shell command? | The agent's own description when it gives one. Otherwise the start of the command, after secret removal. |

Proposed by the design and not yet confirmed by you, but none of them blocks the start:

- D15: in a topic, `/` text goes to the agent unless it is a bot command. `//name` sends `/name` to the agent.
- A waiting message expires after 30 minutes (config setting).
- Pairing with a one-time code through `rex-tg login`.

## Requirement traceability

| Requirement | Design | Plan | Verification | Status |
| --- | --- | --- | --- | --- |
| The main chat lists the active coding agents | 4.1, 6, 8.7 | Phase 1, task 10 | Phase 1 check: a pane that runs pi can be linked from `/agents` | Covered |
| Picking an agent creates a new two-way topic | 4.1, 7 | Phase 1, task 10; Phase 2 | Phase 1 and 2 checks | Covered by design. Spike S1 is pending, so private-chat topics are not yet checked live. |
| Messages come from each agent's session JSONL when a new reply line appears | 4.2, 5, 6 | Phases 1, 3, 4 | Adapter fixture tests; Phase 1 and 4 checks | Covered; spike S4 confirms when lines are written |
| Replies go straight into the agent's pane | 4.3, 9.5 | Phase 2 | Phase 2 checks | Covered; spike S2 confirms paste and Enter |
| SQLite stops duplicate messages | 9.1, 10 | Phase 1, tasks 3 and 8 | Restart and `kill -9` checks; crash tests; sender and daemon tests of the unknown-send check | Covered as "at most once". An unknown send is checked against the chat's message numbers: sent again when it never arrived, else it needs `/resend`. |
| pi, Claude Code, and Codex are supported | 5, 6 | Phases 1–4 | Phase 4 checks | Covered; spike S2 showed that all three agents report state to Rex |
| The project lives in `~/work/personal/rex-tg` | — | — | — | Done |
| Q1 private chat with topics | D1 | Spike S1 | S1 result | Decided; S1 is pending |
| Q2 OpenClaw/Hermes display | D10, 8 | Phase 3 | Phase 3 checks | Covered; answers arrive whole, not word by word |
| Q3 wait until idle | D11, 4.4, 9.5 | Phase 2, task 3 | Phase 2 checks | Covered; depends on the state sources in D3 |
| Q4 last 3 final answers | D12, 6, 7 | Phase 1, task 10 | Phase 1 check | Covered |
| Q5 topic follows the session | D5, 7 | Phase 4, task 5; Phase 5, task 4 | Phase 4 and 5 checks | Covered. Spike S5: block IDs survived one restart. An agent that resumes in a restored pane is not tested. |
| Q6 no T3 Code panes | 1, 4.4 | Phase 1, task 5 | Unit test: a `tr3x` pane is not listed | Covered |
| Q7 Go | D8, 12 | Phase 1, task 1 | `make check` | Covered |
| Q8 no other hosts | 1 | — | — | Covered |
| Q9 summary line | D13, 8.1 | Phase 3, task 3 | Golden-file test, Phase 3 check | Covered |
| Q10 row labels | D14, 8.1 | Phase 3, task 4 | Golden-file test | Covered |
| `/new` asks for the agent (cux too, when installed) and the folder, makes the Rex session `rex-tg` when it is missing, and opens a new tab named `<folder> · <agent>` with the agent; the tab then follows its topic's name | 7, 8.7, 9.5, 11 | After Phase 6 | Unit tests (folder paths, tab names, the agent list with and without cux, questions); daemon tests (new session with buttons and the tab rename after the topic rename; a path in a linked topic, a new tab, an agent that exits; buttons after a lost send answer; the trust question answered with a button through cux, or on the Mac; no tab rename outside the session); registry test of a cux pane; unit tests of the startup question reader and its keys; end-to-end tests `TestNewAgent` (with a real `rex window rename`) and `TestNewAgentAsksFirst` with real Rex | Covered |

## Order of work

```
Phase 0  Spikes S1–S5                                   answers the open technical questions
Phase 1  pi → Telegram: answers, backfill, dividers     proves the pipeline and "no duplicates"
Phase 2  Telegram → pi: waiting, typing, receipts       the riskiest part comes early
Phase 3  Live progress display for pi                   the OpenClaw/Hermes display
Phase 4  Claude Code and Codex, hooks, installer        adds the other two agents
Phase 5  Prompts, questions, restarts                   permission buttons, Rex restarts
Phase 6  Optional extras
```

- pi comes first because it is the agent that runs in Rex panes on this Mac today.
- Each phase starts only when the checks of the phase before it pass.
- A spike result that breaks a design assumption goes back into [design.md](design.md) before the phase that depends on it starts.

## Phase 0: spikes

Each spike is a throwaway program, not product code. Each one writes its result to `docs/spikes/<spike>.md`.

### S1. Telegram topics in the private chat

Status: partly done. See [spikes/s1-telegram.md](spikes/s1-telegram.md). Finding: with Threaded Mode, a private chat has no main chat; every message goes into a topic.

**Status:** Pending. It needs your bot token and your phone.

1. Create a test bot with @BotFather and turn on Threaded Mode.
2. Call `getMe` and check `has_topics_enabled` and `allows_users_to_create_topics`.
3. Call `createForumTopic` in the private chat. Send with `message_thread_id`. Rename with `editForumTopic`.
4. Try `closeForumTopic` and `reopenForumTopic` in the private chat.
5. Write in the topic and in the main chat from the phone. Check `message_thread_id` and `is_topic_message`, and how the main chat looks when Threaded Mode is on.
6. Create a topic from the phone, then delete one. Note the updates and errors that follow.
7. Open the topic link `https://t.me/<bot_username>/<message_thread_id>`.
8. Check `editMessageText`, `deleteMessage`, and `disable_notification` in a topic.
9. Check how an expandable quote (`<blockquote expandable>`) looks on the phone.
10. Check which reaction emoji `setMessageReaction` accepts. Try 👀, 👍, 👎, and ✅.
11. Register commands with `setMyCommands` and check the menu inside a topic.

Done when: the private-chat design is confirmed, or the fallback (a private supergroup with topics) is chosen and written into the design.

### S2. Agents in Rex panes: input, state, and process

**Status:** Done, with open steps. Results: [Claude Code](spikes/s2-s4-claude.md), [Codex](spikes/s2-s4-codex.md), and [pi](spikes/s2-s4-pi.md). Open: step 6 (question prompts with a free-text choice), step 7 for pi (interrupt keys), and step 8 (a delay below 300 ms).

For pi, Claude Code, and Codex each:

1. Record Rex's `process` output for the pane while the agent is idle and while a tool runs.
2. Record whether the agent sends `program_status_changed` events to Rex, with which states.
3. Send a one-line text with a paste event, then Enter, while the agent is idle.
4. Do the same with multi-line text. Check that it arrives as one prompt.
5. Do the same while the agent is working. Record whether it queues, steers, or breaks. This defines `⚡ Send now`.
6. Open a permission prompt and a question with a free-text choice. Record the keys for each answer.
7. Record the keys that stop the current turn.
8. Find the shortest safe delay between the paste and Enter.

Done when: each adapter's detection rules and input profile are filled in, and the state source for each agent is known (D3).

### S3. Hooks

**Status:** Partly done. Results: [Claude Code](spikes/s2-s4-claude.md) and [pi](spikes/s2-s4-pi.md). Open: the Codex hook steps (2, 3, and `REX_BLOCK` in step 5), pi's `/new`, `/resume`, and forks (step 4), and pi's resume and extension loading (steps 4 and 6).

1. Claude Code: log the stdin of `SessionStart` (each `source`), `UserPromptSubmit`, `Stop`, `PermissionRequest`, `Notification`, and `SessionEnd`.
2. Codex: log the stdin of `SessionStart`, `UserPromptSubmit`, `Stop`, and `PermissionRequest`. Look for the session ID and the transcript path.
3. Codex: append a test hook at the end of an event's list. Check that Codex asks to trust it once, and that the other hooks stay trusted.
4. pi: log which extension events fire on start, `/new`, `/resume`, a fork, agent start and end, and exit. Log what `getSessionFile()` returns after each. Check that pi loads a new file in `~/.pi/agent/extensions/` without other steps.
5. All three: check that hooks see `REX_SESSION` and `REX_BLOCK` when the agent runs in a Rex pane.
6. All three: check what resume does to the transcript file. Record one of three outcomes: the same file and session ID; a new file with the old lines copied in; or a new file without the old lines. Design section 6 starts a resumed file at its end in all three cases.

Done when: the resolver, the take-over rules, and the cursor rules in design sections 6 and 7 are confirmed or changed.

### S4. When each agent writes its lines

**Status:** Done. Results: [Claude Code](spikes/s2-s4-claude.md), [Codex](spikes/s2-s4-codex.md), and [pi](spikes/s2-s4-pi.md). The synthetic fixtures are in each adapter's `testdata` folder.

1. Run one short task per agent with several tool calls. Watch the transcript with timestamps while the agent works.
2. Record when each line appears: prompt, tool call, tool result, commentary, and final answer.
3. Confirm that tool call lines are written before the tool runs. The live progress display depends on this.
4. Check whether pi creates the file before the first reply.
5. Record a Codex CLI session, and confirm the duplicate rules in [agent-transcripts.md](agent-transcripts.md#duplicates).
6. Check whether each agent keeps its transcript open (`lsof -p <pid>`). If it does, the PID can find the file without a hook.

Done when: the parser rules in [agent-transcripts.md](agent-transcripts.md) are confirmed, and synthetic fixtures for each runtime exist.

### S5. Rex events and restarts

**Status:** Done, with one open point. Results: [s5-rex.md](spikes/s5-rex.md). Open: an agent that resumes in a restored pane was not tested.

1. Record `rex events --all --json` while you open and close panes and sessions. Note the event names.
2. Restart the Rex server and let it restore the panes. Check whether block IDs change.

Done when: the registry knows which events to follow, and the restart rule in design section 7 is confirmed.

## Phase 1: pi to Telegram, basics

Tasks:

1. Go module: `cmd/rex-tg`, `internal/…`, Go version pinned in `.tool-versions`, and `make check` (`go vet`, `staticcheck`, `go test -race ./...`).
2. Config loading, the instance lock, and JSON logs with rotation.
3. SQLite schema (embedded `schema.sql`), migrations, and the backup before each migration.
4. `rex-tg login`: store the token in the Keychain and pair the owner. `rex-tg doctor`.
5. Rex gateway: CLI wrapper with JSON parsing and timeouts, and the event stream with restart and backoff. Pane registry with Rex scans, pi detection, and the guess resolver. `tr3x` panes are never listed.
6. pi adapter as a pure parser, with synthetic fixtures in `testdata/pi/`.
7. Tailer: byte offsets, half lines, replaced files, and a 1-second size check as a backup for file watching. Cursor rules from design section 6.
8. Outbox and sender: claims, parts with numbers such as `(1/3)`, Markdown to Telegram HTML, UTF-16 length counting, secret removal, the chat's token bucket, and the error table in design section 9.1.
9. Housekeeping: spool cleanup, row cleanup, and retention.
10. Poller and router: `/agents`, the link button, `createForumTopic`, the intro card, the last 3 final answers, dividers, and the answers in design section 8.8.
11. pi hook: `rex-tg hook pi`, the pi extension, and `rex-tg install --only pi`, which writes the extension and the launchd agent. Without the extension, rex-tg cannot see `/new` inside a running pi process.
12. Take-over for pi's `/new` (design section 7), so that the new session keeps the topic.

This phase uses display modes `final` and `block` only.

Done when:

- The daemon, started by launchd, reads the token from the Keychain without a dialog.
- A pane that runs pi can be linked from `/agents`, and the topic opens with the last 3 final answers.
- Each new pi answer appears once in its topic, in order.
- `/new` in pi posts a divider in the same topic.
- After a daemon restart, no answer is missing and none is repeated.
- After `kill -9` during a send, you see at most one "may not have arrived" notice, and never a duplicate.
- A second `rex-tg daemon` exits at once.

## Phase 2: Telegram to pi

Tasks:

1. `inbound` table, with the poll offset saved after the insert.
2. Safety check (design section 9.5), run right before typing.
3. Waiting until idle, one message per turn, the busy time after typing, expiry, and the `⚡ Send now` and `✖ Cancel` buttons.
4. Joining split messages (300 ms, or 1500 ms for long texts).
5. Paste and Enter through the Rex gateway, with the crash rule for `typing` rows (design section 9.5).
6. Reactions 👀, 👍, and 👎, and echo matching with its fallback.
7. Command routing (D15) and the topic commands: `/info`, `/screen`, `/esc`, `/interrupt`, `/key`, `/force`, `/mode`, `/resend`, `/unlink`, `/help`.

Done when:

- A message in the topic arrives in pi as one prompt, also when it has several lines.
- While pi works, the message waits and arrives once pi is idle. Two waiting messages arrive as two turns, in order.
- `/compact` in the topic reaches pi as `/compact`. `//help` reaches pi as `/help`.
- When the pane runs a plain shell, the message is refused with a reason, and nothing is typed. This also holds for `/key` and `/force`.
- Messages from any other Telegram account are ignored.
- Your own message is not posted back to the topic.
- If the daemon is killed between the paste and Enter, the message is never typed again, and you get a notice.

## Phase 3: live progress display for pi

Tasks:

1. Turn tracker: start, tool calls and results, commentary, the 5-second wait for the answer, and the end of a turn.
2. Progress message: 2-second delay, silent send, edits at most every 2 seconds, kept at the bottom, rows as in design section 8.1.
3. Summary line at the end of a turn (D13), with at most 30 rows in the collapsed quote.
4. Row labels (D14), with secret removal.
5. "typing…" in the topic you used last.
6. `/verbose on|full|off` and `/reasoning on|off`.
7. Recovery of progress messages after a daemon restart.

Done when:

- A pi turn with 3 or more tool calls shows one progress message that updates as the tools run.
- The answer arrives as a new message with sound. The progress message changed silently and ends as a summary line.
- No topic gets more than one edit every 2 seconds. The fake Bot API logs each call with its time, and the test checks the gaps.
- A daemon restart in the middle of a turn leaves no stale progress message: in the fake Bot API, every progress message ends as a summary line.

## Phase 4: Claude Code and Codex

Tasks:

1. Claude Code adapter, fixtures, and tests, including tool call and result pairing.
2. Codex adapter, fixtures, and tests, including the duplicate rules.
3. `rex-tg hook claude` and `rex-tg hook codex`, which write spool files and print nothing.
4. `rex-tg install` and `uninstall` for Claude Code and Codex, following the rules in Rollout. The entries are exactly the events in design section 6.
5. Take-over and resume for all three agents (design section 7): `/clear`, `/new`, `/resume`, resume while the topic is busy, and a different agent in the same pane.
6. State from hooks for agents that do not report to Rex (D3), as found in spike S2.

Done when:

- The checks of Phases 1 to 3 pass for Claude Code and Codex panes.
- `/clear` in Claude Code keeps the same topic and posts a divider.
- Resuming an older session brings its own topic back, without repeated messages.
- No Codex reply appears twice.
- `rex-tg uninstall` leaves the other tools' hooks as they were, and Codex still trusts them.

## Phase 5: prompts, questions, and restarts

Tasks:

1. Permission alerts with a screen snapshot and answer buttons. `/approve` and `/deny`.
2. Questions with answer buttons and `✍ Other…`.
3. Alerts bound to their prompt: the `alert` table, the screen hash check before each key, and buttons removed when the prompt ends (design section 8.6).
4. `offline` and `live` changes, and topics that follow their session after a Rex restart.
5. Status emoji in topic names, changed only on state changes.

Done when:

- A Claude Code permission prompt shows up in the topic within 3 seconds, and tapping an answer answers it in the pane. The time runs from the Rex event or the hook's spool file (both are logged) to Telegram's reply to the send.
- A button pressed after its prompt is gone is refused, and nothing is typed.
- After a Rex server restart, a topic works again once its session resumes in the restored pane.

## Phase 6: optional

- Photos and files from Telegram: download (up to 20 MB) and paste the local path.
- Voice notes with local speech-to-text, as Hermes does with faster-whisper.
- Telegram rich messages (Bot API 10.3), which OpenClaw and Hermes offer as an opt-in.
- A `/pause` command that stops all typing into panes until `/unpause`. It is a quick safety switch if the phone is lost.
- `/mute` per topic, which sends answers without sound.

## Rollout and rollback

Install, in this order:

1. `make install` copies the binary to `~/.local/bin/rex-tg`.
2. `rex-tg login` stores the token and pairs your Telegram account.
3. `rex-tg install` does the rest:
   - adds the hook entries to `~/.claude/settings.json`;
   - appends the hook entries to the end of each event's list in `~/.codex/hooks.json`;
   - writes `~/.pi/agent/extensions/rex-tg.ts`;
   - writes and loads the launchd agent.
4. Start Codex once and trust the new hook entries.
5. `rex-tg doctor` checks everything.

The pi extension and the launchd agent arrive in Phase 1 (`rex-tg install --only pi`). The Claude Code and Codex hook entries arrive in Phase 4. Each phase updates the binary with `make install` and a restart of the launchd agent.

Rules for editing shared files (`settings.json`, `hooks.json`):

- **Own entries only.** rex-tg finds its entries by their exact command string, such as `/Users/<you>/.local/bin/rex-tg hook claude`. It never changes another entry.
- **Keep the rest.** It changes values with `sjson`, so the rest of the file keeps its content, layout, and key order.
- **No lost writes.** It reads the file, makes the change, and reads the file again right before it writes. If the file changed in between, it starts over. It writes to a temporary file in the same folder and renames it over the original.
- **Backups.** It copies each file to `<file>.rex-tg-bak-<time>` before a change and keeps the 5 newest copies.
- **Codex trust on uninstall.** Removing an entry shifts the positions of the entries after it, and Codex then no longer trusts them. So if other entries follow a rex-tg entry in a Codex list, `uninstall` refuses. It names the entries that would lose trust. `uninstall --force` removes the entry anyway, and you trust the shifted entries again in Codex.

Stop or roll back:

- **Pause quickly:** `launchctl bootout gui/$(id -u)/local.rex-tg`. Hooks keep writing spool files, which are deleted after 24 hours.
- **Remove:** `rex-tg uninstall` removes exactly the entries it added, the pi extension, and the launchd agent. It keeps the database and the backups.
- **Bad migration:** the daemon stops and keeps `rex-tg.db.bak-<version>`. Copy it back and install the previous binary.

## Tests

| Level | What | How |
| --- | --- | --- |
| Unit | Adapters | Table tests on synthetic JSONL in `testdata/<runtime>/`. Never real transcripts. |
| Unit | Parts, formatting, secret removal | Table tests, including code blocks across a split, UTF-16 length, and characters that need escaping. |
| Unit | Progress message | Golden files for the rendered text at each step of a turn, and for the summary line. |
| Unit | Outbox state machine | Each state change, including `unknown` after a timeout, and claims that race. |
| Unit | Registry | Take-over, resume, resume while the topic is busy, nested-agent reports, PID reuse, and `tr3x` panes. The rule that a topic's current session belongs to that topic. |
| Unit | Agent state | The newest signal wins, a prompt overrides a stale `done`, and silence never counts as idle. |
| Unit | Command routing | Bot commands, other `/` text, and `//name`. |
| Unit | Safety checks | The action × checks table in design section 9.5, and stale alert buttons. |
| Unit | Vim steps | pi-vim and Claude Code vim against an in-memory pane: every start mode, an editor that does not react, a missing mode label, and the safety check before each key. |
| Integration | Telegram | A fake Bot API on `httptest.Server` that returns each error in design section 9.1, and timeouts. |
| Integration | Rex | A fake `rex` binary (`rextest/fakerex`, built once per test run) that returns recorded JSON and logs the input it gets. |
| Integration | Daemon | The whole daemon with the fake Bot API and the fake `rex`: one test per core flow and per piece of wiring (pairing, topics, typing, answers and progress, restarts, stop notices). The daemon's fixed waits are short in these tests (`daemon.Timing`), and the tests run in parallel. |
| Integration | Installer | Hook files from fixtures: install, install again, and uninstall. The rest of each file must stay byte for byte the same. `uninstall` refuses when other entries follow a rex-tg entry in a Codex list. A file changed during an install is not overwritten. |
| End to end | The whole daemon, with real Rex | A dummy agent in a real Rex pane that writes JSONL in each runtime's format and reads its input. It spends no model tokens. Build tag `e2e`. |
| Crash | Delivery rules | Build the binary, kill it in each outbox, progress, and `typing` state, start it again, and count the messages and the typed inputs. |
| Manual | Real agents | A checklist per runtime, in real Rex panes. |

## Risks

| Risk | Effect | Mitigation |
| --- | --- | --- |
| Word-by-word streaming only for pi | Claude Code and Codex write only complete blocks, so their draft changes block by block. pi's extension sees each token. | Drafts (design 8.1a) show pi's text as it grows, and the latest block of the others. The live progress message shows the tools. |
| Claude Code or Codex do not report state to Rex | If Rex did not report, "wait until idle" would have no Rex signal for them. | Closed by spike S2: all three agents report state to Rex. Hooks and transcript lines are the backup (D3). |
| Hooks may not see `REX_BLOCK` | Panes cannot be matched to sessions exactly. | Claude Code and pi hooks saw `REX_BLOCK` and `REX_SESSION` (spikes S2 and S3). Codex hooks did not run in the spikes, so that check is still open (S3). The scan-and-guess resolver still works. |
| Transcript formats change | Replies go missing or look wrong after an agent update. | Ignore unknown types, keep fixtures per version, and let `rex-tg doctor` report unknown types. |
| Topics in private chats are new | A core design choice could fail. | Spike S1 is still pending. It needs your bot token and your phone. A private supergroup is the fallback. |
| Typing into terminal apps is fragile | Text arrives broken or too early, or lands in an open menu. | Spike S2 used a 300 ms wait between the paste and Enter, and it worked for all three agents. Shorter waits are not tested. A slash menu can take the first Enter (Codex, S2). Also use the safety check and `/screen` to look first. For pi, injection through the extension API is a fallback. |
| One rate budget for all topics | Busy agents hit Telegram limits, and edits count too. | Merge progress changes, edit at most every 2 seconds, and give answers priority. |
| Remote typing is powerful | A stolen Telegram account could control the terminals. | Pairing and owner allowlist, the safety check, the token in the Keychain, and Telegram two-step verification. `/pause` in Phase 6. |
| Replies can contain secrets | Secrets reach Telegram's servers. | Best-effort secret removal, rows without output, and `final` mode. The README states the limit. |
| Hook files are shared with other tools | An install could break other tools' hooks. | Edits that keep the rest of the file, entries appended at the end, backups, and installer tests. |
