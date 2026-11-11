# Spikes S2, S3 (partly), S4: Codex CLI 0.167.0

Date: 2026-11-11, 10:25 to 10:29 (+07:00). All times below are local.

## Setup

- Codex CLI 0.167.0 (`/opt/homebrew/bin/codex`), model shown as "GPT-6-Luna max".
- Own Rex session `rextg-spike-codex`, one pane per run, folder `…/spikes/agents/codex-work` (empty, no git).
- Command: `codex --disable hooks -C "$SPK" -a on-request -s read-only`. `--disable hooks` is the right name (`codex features list` shows `hooks stable true`). No hook ran, so **hook payloads were not tested here**. Only the Rex status and transcript questions are answered.
- A background script polled the rollout file every 50 ms and logged the time of each new line. A second recorder logged `rex events`. Both were stopped, and the session was killed.
- Startup dialogs: no folder-trust dialog appeared. On the second start (`resume`) an "Update available" dialog appeared (`1 Update now`, `2 Skip`, `3 Skip until next version`). The key `2` skipped it. A weekly-limit banner is always on screen; it does not change input.
- Caveat: the interrupt test (step 7) hit while Codex was still thinking, before the `sleep` tool call existed. The tool-running interrupt was not tested.

## Findings

### 1. Idle pane: Rex process, status, modes

`rex block call … process` keys: `child`, `foreground`, `home`, `last_exit`. There is **no `ancestors` key** in this Rex build. The `foreground` object has `pid`, `name`, `path`, `invoked_path`, `argv0`, `cwd`, `user`, `user_id`.

| Field | Value |
| --- | --- |
| name | `codex` |
| argv0 | `codex` |
| path | `/opt/homebrew/Caskroom/codex/0.167.0/bin/codex` |
| invoked_path | `/opt/homebrew/bin/codex` |
| child and foreground | the same pid |

`program_status` record: `{state, kind?, app:"codex", effective_app:"codex", updated_at, owner:{pid,name:"codex"}}`.

Mode `bracketed_paste` (2004) is **set**. The screen is the alternate screen (1049 set). Mouse modes 1000, 1002, 1003, 1006 and focus events 1004 are also set.

**Codex does report program status to Rex.** The events arrive as `block_event` with `name=program_status_changed`, payload `{id:"", record:{…}, reason:"report"}`. No `progress_report` event was seen (0 in 874 events). Observed states:

| Time | State | Cause |
| --- | --- | --- |
| 10:25:38.50 to 10:25:39.79 | `idle`, `working`, `idle` | Codex starting up |
| 10:26:05.540 | `working` | Enter pressed (10:26:05.54) |
| 10:26:13.521 | `blocked`, `kind:"permission"` | Approval prompt shown |
| 10:26:20.269 | `working` | `y` pressed at 10:26:20.247 |
| 10:26:26.989 | `done` | Turn ended |
| 10:26:41.857 | `idle` | About 15 s after `done`, with no input |
| 10:27:31.050 | `idle` (not `done`) | Escape pressed at 10:27:31.021 |
| 10:28:05 | `program_status_removed` (reason `clear`) | Codex exited |

The `title_changed` events also show a spinner (a braille character before the title) while it works. They are not needed.

### 2. Multi-line paste then Enter

I pasted two lines with a `paste` event at 10:26:05.15. The screen showed both lines in the input box after 300 ms. Enter was sent at 10:26:05.52 (about 340 ms after the paste). Codex took it as **one prompt**. The rollout has one `UserMessage` item whose text is `line1\nline2`. The Rex state turned `working` within about 25 ms of Enter.

I did not test a shorter delay. Every paste in this spike used 300 ms and every one worked (5 of 5).

### 3. Approval prompt (read-only sandbox, on-request)

State at the time: Rex `blocked`, `kind:"permission"`, set at 10:26:13.521. The rollout `custom_tool_call` line appeared at 10:26:13.419, 100 ms earlier.

Last lines of the screen (structure only):

```
  Would you like to run the following command?
  Environment: local
  Reason: <model's reason text>
  $ <the command>

› 1. Yes, proceed (y)
  2. Yes, and don't ask again for commands that start with `<command>` (p)
  3. No, and tell Codex what to do differently (esc)

  Press enter to confirm or esc to cancel
```

| Label | Keys |
| --- | --- |
| 1. Yes, proceed | `y` (also `1`/Enter on the highlighted row, not tested) |
| 2. Yes, and don't ask again for this command prefix | `p` |
| 3. No, and tell Codex what to do differently | `Escape` |

I approved with `send-key y` at 10:26:20.247. No Enter was needed. The command ran at once, and Rex turned `working` at 10:26:20.269. The shortcut letters are written in the option text, so the adapter can read them from the screen. Option 3 stops the command and then lets you tell Codex what to do. I did not test what Esc leaves behind afterward.

### 4. Message pasted while Codex works

I sent "Run `sleep 25`…", then at 10:27:02.98 pasted a second line and pressed Enter at about 10:27:03.3. Result: **steer**.

- The text showed on the screen at once as a normal prompt line in the same turn.
- In the rollout it appeared at 10:27:03.379, as `response_item/message role=user` plus `item_completed UserMessage`, with the **same `turn_id`** and **no new `task_started`**.
- The same instant also flushed the pending `custom_tool_call_output` of the running tool.
- The final answer covered both asks ("SLEPT" and "HELLO2"). Only one `task_complete` came.
- No Tab was needed. It was not ignored and not queued for later.

So `⚡ Send now` into a working Codex steers the current turn.

### 5. Transcript

File: `~/.codex/sessions/2026/11/11/rollout-<start time>-<session id>.jsonl`.

- `session_meta.payload` keys: `base_instructions, cli_version, context_window, creator_account_id, creator_user_id, cwd, history_mode, id, model_provider, originator, runtime_workspace_roots, session_id, source, thread_source, timestamp`.
- **`originator` = `codex-tui`, `source` = `cli`.** Use these to tell the CLI from T3 Code or Codex Desktop.
- **The file is created at the first Enter**, not at launch. At 10:25:45 (idle) no file existed. It appeared at 10:26:05.6. Its name carries the **process start time** (10:25:38), not the time of the first prompt. So the tailer must wait for the file.
- First write batch (10:26:05.6 to 10:26:06.3), in order: `session_meta`, `task_started`, `additional_tools`, 4 developer messages, one `response_item/message role=user` holding `<environment_context>` (injected), `world_state`, `turn_context`, `thread_settings_applied`, `response_item/message role=user` (the real prompt), `item_completed UserMessage`.
- Later turns write: `thread_settings_applied`, `task_started`, `turn_context`, then the user lines.

Timeline of turn 1 (times when the line was first seen):

| Time | Line |
| --- | --- |
| 10:26:11.200 | `item_completed Reasoning`, then `response_item/reasoning` |
| 10:26:11.259 | `item_completed AgentMessage` (phase `commentary`), then `response_item/message role=assistant phase=commentary` |
| 10:26:13.419 | `response_item/custom_tool_call` (`name:"exec"`, `status:"completed"`, has `call_id`), `token_usage_record` |
| 10:26:20.363 | `item_completed CommandExecution`, then `custom_tool_call_output` (same `call_id`), `token_count` |
| 10:26:27.019 | `item_completed AgentMessage` (phase `final_answer`), `response_item/message … final_answer`, `token_usage_record`, `token_count`, `task_complete` |

Answers:

- **Tool call line before the command runs: yes.** The `custom_tool_call` line was written at 10:26:13.4 while the approval prompt was open. The command started at 10:26:20.25 and its output line came at 10:26:20.363. A second case: the `sleep 25` call line was written at 10:26:47.6, and its output only at 10:27:03.4. The tool name is `exec` (a custom tool with JavaScript text as `input`). Another call used `function_call` with `name:"wait"`. Both kinds must be handled for the live progress display.
- **Duplicate rule: confirmed.** Each reply is written twice: `event_msg/item_completed` with `item.type=AgentMessage` **first**, then `response_item/message role=assistant`, in the same flush (same millisecond). The `item.id` of the AgentMessage equals the `payload.id` of the response message (`msg_…`), so the key `codex:<session id>:<id>` dedupes either one. Read replies from `response_item` only, as planned. `task_complete.last_agent_message` repeats the final answer, so ignore its text.
- **User prompt: rule confirmed.** `response_item role=user` appears for the real prompt too, and **also** for injected context (`<environment_context>` at the start of turn 1). `item_completed UserMessage` holds only the real prompt (`content[0]` = `{type:"text", text}`; the text keeps `\n`). The two lines use different ids, so match prompts on `item_completed UserMessage`. In the same flush, the response_item comes first and the UserMessage right after.
- **`task_started` keys:** `collaboration_mode_kind, model_context_window, root_turn_id, started_at, turn_attribution, turn_id, type`.
- **`task_complete` keys:** `completed_at, duration_ms, last_agent_message, root_turn_id, started_at, time_to_first_token_ms, turn_id, type`.
- **Interrupt writes `event_msg/turn_aborted`** (keys `completed_at, duration_ms, reason, root_turn_id, started_at, turn_id, type`, `reason:"interrupted"`) plus a developer-role `response_item/message`. There is **no `task_complete`** on an interrupted turn. The tailer must treat `turn_aborted` as a turn end.
- `item_completed` payload keys: `completed_at_ms, item, started_at_ms, thread_id, turn_id, type`.
- Codex keeps the rollout open: `lsof -p <pid>` showed it on fd 52 (`u`, read-write) after resume. So the PID can find the file. I did not check this before the first prompt, and the file does not exist then.

### 6. Rex process while a command runs

While `sleep 25` ran (10:26:58 and 10:27:02), Rex `process` still reported `foreground` = `codex` (the same pid as `child`). The `sleep` was in its own session and process group (pgid = its pid, `tpgid` 0), so it never became the foreground. Codex's own children at that time: a `node` MCP launcher, two more MCP helper processes, `node_repl`, and `codex-code-mode-host`. Rex gives no ancestors list in this build. For check 3 of design 9.5, "foreground is codex" is enough for Codex.

### 7. Interrupt

Prompt "Run `sleep 30`…". I sent `send-key escape` at 10:27:31.021 (4.4 s after the prompt, while Codex was still reasoning).

- Screen: `■ Conversation interrupted - use /feedback if something went wrong`, and the input box was empty again.
- Rex: `working` to `idle` at 10:27:31.050 (not `done`).
- Rollout: `item_completed Reasoning` 10:27:31.007, then at 10:27:31.062 `token_count`, a developer message, and `turn_aborted reason=interrupted`.
- One Escape was enough. The screen footer while working says "esc to interrupt".

### 8. /new

I pasted `/new` and pressed Enter at 10:27:39.6. The pane cleared and the **same Codex process (same pid)** continued. **No file was created at `/new`.** At the first prompt after it (10:27:47.7) a **new rollout file** appeared with a **new session id**, a new `session_meta` as line 1, and no copied history. The file name carries the time of `/new` (10:27:39). The old file was left unchanged. When the slash menu is open, the first Enter may be taken by the menu (the screen showed `/new  start a new chat…`); a single Enter worked here.

### 9. Resume

I typed `/quit` (paste + Enter). Codex exited with exit code 0, Rex `program_status` cleared, and the block kept its screen with the text "To continue this session, run: codex resume <id>". Then `codex … resume --last` in a new block (pid 20105) opened the **last session of the folder** (the `/new` one): the screen showed the old history.

- **Same file, same session id.** No new file. No new `session_meta` line (the file still has exactly 1). The first prompt after resume appended `thread_settings_applied`, `task_started`, … to the end of the same file, from byte offset = old size.
- No history was copied anywhere.
- Nothing was written to the file at resume time, before the first prompt. Rex status after resume was fresh (`idle`, then `working`, `done`).

## Recommended values for the rex-tg adapter

**InputProfile for Codex**

| Field | Value |
| --- | --- |
| PasteEnterDelay | 300 ms (tested, 5 of 5). Lower values were not tested. |
| SubmitKey | `enter` |
| InterruptKeys | `escape` (one press) |
| WhileWorking | `steer` |
| Paste | bracketed paste is set, multi-line works as one prompt |
| Foreground check | `foreground.name == "codex"`; it stays `codex` while tools run |

**Approval answers** (label to keys; read the letter from the screen's `(y)`, `(p)`, `(esc)` tags in case a version changes them)

| Label | Keys |
| --- | --- |
| Yes | `y` |
| Always (this command prefix) | `p` |
| No | `escape` |

Rex shows `blocked` with `kind:"permission"` for this prompt, so the alert needs no screen scan to detect it. The screen capture is still needed for the text.

**State source for Codex: use Rex status** (`program_status_changed`) as the main source. It reports `idle`, `working`, `blocked/permission`, `done` and `idle` after Escape, with a delay of 0 to 30 ms against the transcript. Add transcript lines as the second source: `task_started` (working), `task_complete` and `turn_aborted` (turn end). Treat Rex `done` followed by `idle` after about 15 s as normal. Hooks are an extra for the exact session id and path (not tested here, S3). Codex hooks are not needed for state.

**Duplicate rule: confirmed.** Replies from `response_item/message role=assistant` only (key on `payload.id`, phase `final_answer` or `commentary`). Ignore `item_completed AgentMessage` and the text in `task_complete`. Prompts from `item_completed UserMessage` only. Skip `response_item role=user` (it carries injected context).

**Other parser rules:** end of turn on `task_complete` or `turn_aborted`; tool calls come as `custom_tool_call` and `function_call`, written before the command runs; a CLI session has `originator=codex-tui` and `source=cli`; the rollout file does not exist until the first prompt, and its name time is the start time of the process (or of `/new`).

**Resume rule:** resume keeps the **same file and session id** and appends to it. rex-tg must start such a tailer at the saved cursor, or at the end of the file with the last 3 answers (design section 6). `/new` makes a new file at the next prompt in the **same process**: when the tailer sees a new file in the same folder with a new `session_meta`, a take-over from byte 0 is right. The take-over must not wait for a hook, because the new file appears only at the first prompt after `/new`. A fresh start (new pid) also writes its file only at the first prompt.
