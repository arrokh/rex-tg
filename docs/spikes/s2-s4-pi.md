# Spikes S2, S3 (partly), S4: pi 1.5.2

Date: 2026-11-11. Clock times are UTC, from the spike's event log. Offsets such as "+6.1 s" count from the Enter key that started the run.

This file answers the pi questions only. The other agents are in [s2-s4-claude.md](s2-s4-claude.md) and [s2-s4-codex.md](s2-s4-codex.md).

Each result has one of these labels:

- **Observed**: seen in the spike logs or in pi's session file on this Mac.
- **From source**: read in pi 1.5.2's own files. The behavior was not run.
- **Not tested**: still open.

## Setup

- pi 1.5.2 (`@earendil-works/pi-coding-agent`), run in a Rex pane.
- Started with `--no-extensions -e <spike extension>`. Only the spike extension loaded.
- The spike extension wrote one JSON line for each event it watched. Each line has `REX_BLOCK` and `REX_SESSION`.
- Rex status came from the Rex event stream and from the `process` call.
- One run: a two-line prompt that asked for a 6-second shell command. A second message was sent while that command ran.

## Findings

### 1. Process and status (S2.1, S2.2)

Observed:

- `process` for the pane: `name` is `node`, and `argv0` is `pi`. The Rex event log shows the same PID first as `sh`, then as `node`.
- `ancestors` is empty.
- `program_status` is `idle` at start.
- `bracketed_paste` is set, and the screen is the alternate screen. (Spike notes. The raw output was not kept.)
- Rex status during the run went `idle`, then `working`, then `done`. The `app` value is `pi`.

From source (not run):

- In the pi package, `dist/modes/interactive/program-status-reporter.js` writes the state to the terminal with OSC 7501.
- It reports `working` during agent runs and during compaction. It reports `blocked` while a dialog waits. After that, it reports `done`, `error`, or `idle`.
- An extension dialog reports `blocked` with kind `question`. Login reports kind `auth`. No kind is `permission` by default.
- The status text holds the session name, dialog titles, and the first line of an error. Prompts and assistant output are never sent.

### 2. Extension events (S3.4)

Observed, in order:

| Time (UTC) | Event | Detail |
| --- | --- | --- |
| 03:12:54.486 | `session_start` | reason `startup` |
| 03:13:44.809 | `input` | source `interactive`, the first prompt |
| 03:13:44.810 | `agent_start` | |
| 03:13:44.811 | `turn_start` | turn 0 |
| 03:13:50.407 | `tool_execution_start` | tool `bash` |
| 03:13:50.888 | `input` | source `interactive`, the second message |
| 03:13:56.417 | `tool_execution_end` | tool `bash`, no error |
| 03:13:56.418 | `turn_end` | turn 0 |
| 03:13:56.419 | `turn_start` | turn 1 |
| 03:14:11.330 | `turn_end` | turn 1 |
| 03:14:11.330 | `agent_end` | |
| 03:14:33.100 | `session_shutdown` | reason `quit` |

Observed:

- Every event had `REX_BLOCK` and `REX_SESSION` set.
- Every event named the same session file.
- Not seen: `/new`, `/resume`, forks, and reloads.

From source (pi 1.5.2 types, not run):

- `session_start` reasons: `startup`, `reload`, `new`, `resume`, and `fork`. `previousSessionFile` is set for `new`, `resume`, and `fork`.
- `session_shutdown` reasons: `quit`, `reload`, `new`, `resume`, and `fork`. `targetSessionFile` is set when a session is replaced.
- Also defined: `session_before_switch`, `session_before_fork`, `ui_prompt_start`, `ui_prompt_end`, and `agent_settled`. The spike did not log them.

### 3. Paste, Enter, and a message during a tool run (S2.3 to S2.5)

Observed:

- A two-line prompt was pasted. After a 300 ms wait, Enter was sent. pi recorded one user message, and its text has a line break inside.
- Delays below 300 ms were not tested.
- A second message was pasted, and Enter was sent, while the `bash` tool ran. The screen showed it as `Steering: …`, with the hint `Option+Up to edit all queued messages`.
- pi wrote the second message as a `user` line after the tool result. It is part of the same run: one `agent_start`, and two turns.
- The final answer, with `stopReason` `stop`, answered both prompts.

### 4. Session file and line timing (S4)

Observed:

- The session file did not exist before the first prompt. Its birth time is 03:13:44 UTC, which is the first prompt.
- At the first Enter (+0.37 s), pi wrote five lines in one batch: the header (`session`), `model_change`, `thinking_level_change`, a `system` message, and the `user` message.
- The file name and the header carry the launch time, 03:12:54 UTC. The first three lines also keep their launch-time timestamps. So the file name is not the time of the first prompt, and a line's timestamp can be older than the time it was written.
- The assistant message with `thinking` and `toolCall` (`stopReason` `toolUse`) was written at +6.08 s. The tool started at the same time. So the call line came before the tool ran.
- The `toolResult` line was written at +12.05 s, when the 6-second command ended.
- The steered `user` line was written at +12.05 s, after the tool result. It was not written when it was pasted (+6.44 s).
- The final assistant message, with `stopReason` `stop`, was written at +26.95 s.

### 5. Recommended values

InputProfile for pi:

```
PasteEnterDelay: 300ms     (a two-line paste worked; shorter values not tested)
SubmitKey:       enter
InterruptKeys:   [escape]  (pi's default key for app.interrupt; from source, not run)
WhileWorking:    steer     (a message sent during a tool run joined the same run)
```

State source: Rex status, from pi's own reporter. The extension events `agent_start` and `agent_end` mark the start and end of a run.

Detection by process: `argv0` is `pi`. The name is `node`, so the name alone does not identify pi.

The rex-tg extension template, `extensions/pi/rex-tg.ts`, subscribes to `session_start`, `session_shutdown`, `agent_start`, and `agent_end`. It sends nothing when `REX_BLOCK` is not set. (From the repository code.)

### 6. Not tested yet

- A paste with Enter after a delay below 300 ms.
- `/new`, `/resume`, forks, and reloads: which events fire, and what the session file becomes.
- Resume: whether pi writes to the same session file or a new one, and whether it copies old lines.
- Whether pi loads every file in `~/.pi/agent/extensions/`. This run used `--no-extensions`.
- The interrupt key. The default is `escape`, from source.
- The Rex `process` output during a tool run. The Rex event log has no process change after `node` appeared. The `process` call during a tool run was not recorded.
- Dialogs with free-text answers. The source sets a `question` state for extension dialogs, but no dialog was run.
