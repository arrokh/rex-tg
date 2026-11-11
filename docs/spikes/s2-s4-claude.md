# Spikes S2, S3, S4: Claude Code

Date: 2026-11-11. Claude Code 2.1.322 (model Opus 5.5), run in a Rex pane.
This file answers the Claude Code questions only. Codex and pi are in other files.

## Setup

- Own Rex session `rextg-spike-claude`, one tab. The session is closed now. `rex ls` shows only the two user sessions.
- Start command: `claude --setting-sources project,local --settings <claude-settings.json> --permission-mode default`. The cwd was an empty folder.
- The settings file registers one logging hook for `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, `Notification`, and `SessionEnd`. The hook prints nothing.
- An events recorder (`rex events -s S --json`) and a transcript watcher (polls the file every 50 ms and notes when each line first appears) ran in the background. Both are stopped.
- Times below are UTC (`03:27:31` is `10:27:31` in Rex's +07:00 stamps).
- First-run dialogs:
  - Folder trust. The cursor starts on "No, exit". The key `2` did nothing. `down` then `enter` chose "Yes, I trust this folder".
  - "Claude in Chrome extension detected". The cursor starts on "No, keep browser tools off". `enter` accepted that.
- Note: `sleep 5; echo rex-tg-spike` alone ran with no permission prompt. Claude treats it as a safe command. Prompts needed `touch spike-marker.txt; …`.
- Housekeeping slip: at the start I emptied the shared `logs/hooks.jsonl`. Other spikes may have lost earlier lines. All Claude lines are tagged `"agent":"claude"`.

## Findings

### 1. Idle pane: Rex data (S2.1, S2.2)

`process` while idle (03:25:55 UTC; the same shape at every later check):

- Keys: `child`, `foreground`, `home`, `last_exit`. There is no `ancestors` key at all.
- `child` and `foreground` are the same process.
- `name` = `2.1.322` (the version string, not `claude`).
- `argv0` = `claude`.
- `path` = `~/.local/share/claude/versions/2.1.322`.
- `invoked_path` = `~/.local/bin/claude`.
- After `/exit`: `foreground` is null and `last_exit` = `{"exit_code":0}`.

`program_status`: Claude Code does report to Rex.

- Record fields: `state`, `kind` (only when blocked), `app` = `claude-code`, `effective_app` = `claude-code`, `msg` (optional), `updated_at`, `owner` = `{pid, name}`.
- It uses `block_event` name `program_status_changed` with `reason: "report"`. I saw no `progress_report` events.
- States seen: `idle`, `working`, `blocked` (kind `permission`), `done`.
- At start it is `idle`. The `app` value is `claude-code`, not `claude`.
- `msg` is a short label while working, for example "Waiting 5 seconds then printing marker", and `approve Bash: <command>` while blocked.
- `title_changed` events also come from Claude (title `Claude Code`, then a task title).

Modes (`block call modes`): `bracketed_paste` (2004) is **set**. Also set: mouse modes 1000/1002/1003/1006, focus_event 1004, report_color_scheme 2031. The screen is `alternate`.

### 2. Two-line paste (S2.4)

- Paste at 03:26:57.3, a 300 ms wait, then `enter` at 03:26:58.3.
- After the paste, the input box showed both lines, with no submit yet.
- Result: one prompt. The transcript has one `user` line whose `message.content` is a single string with `\n` inside. The `UserPromptSubmit` hook fired once, with `prompt` holding both lines.
- The same worked in every later round. A 300 ms delay always worked. I did not test shorter delays.

### 3. Permission prompt (S2.6, S3.1)

Round 2. Enter at 03:27:28. The prompt was on screen by 03:27:31.

Last lines of the screen (user text removed):

```
 Bash command
 Create marker file, wait, print marker
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 touch spike-marker.txt; sleep 5; echo rex-tg-spike
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and always allow access to <dir> from this project
   3. Yes, and switch to auto mode · auto mode handles these prompts for you
   4. No

 Esc to cancel · Tab to amend
```

Options and keys:

| Label | Key | Evidence |
| --- | --- | --- |
| 1. Yes | `1` (no Enter needed) | Pressed `1` at 03:27:36.7. The prompt closed and the tool ran. |
| 2. Yes, always allow | `2` (by the same rule; not pressed) | Not tested. |
| 3. Yes, switch to auto mode | `3` (not pressed) | Not tested. Do not expose this one. |
| 4. No | `4` | Pressed at 03:32:01. The tool did not run. See section 7. |
| Cancel | `escape` | Footer says "Esc to cancel". Esc during a running tool is tested in section 7. |

The number of options changes with the tool and the folder. The option list is part of the screen. The adapter must read labels from the screen and map each label to its number.

State at that moment:

- Rex: `state=blocked`, `kind=permission`, `msg="approve Bash: <command>"`, `updated_at` 03:27:31.39.
- Rex `working` returned at 03:27:36.7, about 40 ms after the key press.

Hooks:

- `PermissionRequest` fired at 03:27:31. Fields: `session_id`, `prompt_id`, `permission_mode`, `effort`, `hook_event_name`, `tool_name` (`Bash`), `tool_input` (`command`, `description`), `permission_suggestions`. It has no `transcript_path` and no `cwd`.
- `Notification` fired about 6 s after the prompt appeared (03:31:12 for a prompt shown at about 03:31:06). Round 2 ended too quickly to show it. Fields: `session_id`, `transcript_path`, `cwd`, `prompt_id`, `hook_event_name`, `message` = `"Claude needs your permission"`, `notification_type` = `"permission_prompt"`.
- The field that separates the kinds is `notification_type`. The permission kind is `permission_prompt`. The idle kind should be `idle_prompt`. I never saw an idle notification (section 10), so that value comes from the Claude Code docs and not from this run.

### 4. Second message while a tool runs (S2.5)

Round 3. Tool `sleep 25`, approved at 03:28:12.0. Second message pasted at 03:28:14.1, Enter at 03:28:14.4.

- Claude accepted it at once. The screen showed the text under the tool, the hint `ctrl+enter to send now`, and the footer line `Press up to edit queued messages`.
- It did not interrupt the tool. The tool ended at 03:28:37.
- The message was **absorbed mid-turn**: Claude read it right after the tool result, in the same turn. The final answer answered both ("SPIKE DONE" and "PING"). There was only one `Stop`.
- `UserPromptSubmit` fired for it at once (03:28:14), with its own `prompt_id`. It did not mean a new turn.
- Transcript:
  - 03:28:14.48: line `type=queue-operation`, `operation=enqueue`, `content` = the text. This is the first trace.
  - 03:28:37.05: `queue-operation` with `operation=remove` and `reason=absorbed_mid_turn`.
  - 03:28:37.05: the tool result line.
  - A line `type=attachment` with `attachment.type=queued_command`, `attachment.prompt` = the text, `humanTurn=true`. Its `timestamp` (03:28:14.456) is the time of typing, but the line was written at 03:28:37.
  - There is **no** `type=user` line with the text of the second message.
- Consequence: an echo matcher that waits for a `user` prompt line would never see this message. Match `queue-operation/enqueue` (by `content`) or the `queued_command` attachment.

### 5. Transcript timeline (S4)

Path: `~/.claude/projects/<cwd with / and . → ->/<session_id>.jsonl`. Here the cwd was `/private/tmp/...`, so the folder name starts with `-private-tmp-claude-501-` (the `/private` form, not `/tmp`). The `transcript_path` of the `SessionStart` hook is exact. Use it.

Round 2 (one tool call), times when the line first appeared:

| Time | `type` | Content / stop_reason | Note |
| --- | --- | --- | --- |
| 03:27:28.11 | `user` | string content | the prompt, `promptSource=typed`, `origin.kind=human`. Written at the time of Enter. |
| 03:27:28.14 | `api-request` | | skip |
| 03:27:31.37 | `assistant` | `[tool_use]`, `stop_reason=tool_use`, id `msg_…` | written when the model finished, 5 s **before** the approval (03:27:36.7) and before the tool ran |
| 03:27:41.74 | `user` | `[tool_result]` | after the tool ended (approval + 5 s) |
| 03:27:43.98 | `assistant` | `[text]`, `stop_reason=end_turn`, new `msg_…` | final answer |
| 03:27:44.02 | `system` ×2 | `subtype` = `stop_hook_summary` and `turn_duration` | skip |

Answers:

- **Each block its own line?** Yes. Each `assistant` line holds one content block. In this small task each assistant message had exactly one block, so I could not see several lines with one `message.id`. The earlier survey in agent-transcripts.md shows that they exist. No `thinking` blocks were written in this run (`thinking_tokens: 0`).
- **tool_use written before the tool runs?** Yes. The line appeared at 03:27:31.5, 5 s before the tool started (needs approval) and 10 s before the result. In round 3 the line came at 03:28:11.8 and the result at 03:28:37.1. The live progress display can rely on it.
- **Final text line `stop_reason`?** `end_turn` **at write time**. It was never `null` first. No stub line was written and later updated. The line is final when it appears.
- **Prompt line:** `type=user`, `message.content` a string, no `isMeta`, with `promptId`, `origin:{kind:human}`, `promptSource:typed`, `turnOrigin:human`, `turnPosition:{promptIndex,turnIndex}`. A multi-line paste is one string with `\n`.
- **Tool result line:** `type=user`, `message.content` = `[{type:tool_result, tool_use_id, content, is_error}]`, plus `toolUseResult`, `sourceToolAssistantUUID`, and `permissionDecision`. The `parentUuid` is the `tool_use` line's `uuid`.
- **Other types seen** (skip them): `mode`, `permission-mode`, `session-owner`, `atis-latch`, `file-history-snapshot`, `attachment`, `api-request`, `api-request-blob`, `api-request-shape`, `last-prompt`, `ai-title`, `tools-used`, `cost-state`, `queue-operation`, `system`.
- Many non-chat lines (`mode`, `session-owner`, …) have no `timestamp` and no `uuid`. Any "first line is older than the process" rule must use the first line **with** a timestamp, or the file's birth time.
- A new transcript file appears at session start. The `SessionStart` hook runs before the first prompt. The first lines are written before the first reply.
- Claude does not keep the file open: `lsof -p <pid>` showed no `.jsonl`. The PID cannot find the file. Use the hook.
- Delay between the final line and the signals: final line 03:28:41.83, `Stop` hook 03:28:41, Rex `done` 03:28:41.87. They arrive together. The 5-second wait for the answer line is not needed for Claude, but is harmless.

### 6. Process during a tool run (S2.1)

- Rex `process` at 03:28:14 (tool running): `foreground` and `child` were still pid 14306, name `2.1.322`, argv0 `claude`. The tool is not the foreground process.
- The Bash tool's `zsh -c …` and a `caffeinate` are children of claude (seen with `pgrep -P`), but Rex does not report them. There is no `ancestors` field.
- So for Claude, check 3 in design 9.5 is true by the foreground alone.

### 7. Interrupt (S2.7)

Round 4: `sleep 30`, approved at 03:29:13.6. `escape` sent at 03:29:19.6, 6 s into the tool.

- Key: `escape` (one key, no confirm).
- Screen: `Interrupted · What should Claude do instead?`, then the empty prompt. Claude stops at once.
- Transcript (both lines at 03:29:19.61):
  - `user` / `tool_result` with `is_error` text "The user doesn't want to proceed with this tool use…".
  - `user` / `text` block `[Request interrupted by user for tool use]`, with `interruptedMessageId`. It has `type=user` and a `text` block, so it looks like a prompt. The adapter must skip texts that start with `[Request interrupted`.
  - No assistant text line follows.
- Rex: `working` → `idle` at 03:29:19.61 (not `done`).
- Hooks: **no `Stop` hook fired.** Nothing fired. No `Notification`.
- Pressing `4` (No) on a permission prompt gives the same result: the same two lines, Rex `idle` at once, no `Stop`.
- Consequence: after an interrupt, only Rex status and the transcript tell you the turn is over. A state machine that waits for `Stop` would stay in `working` for ever.

### 8. /clear (S3.1)

`/clear` pasted and Enter at 03:32:10.

- Hooks, in order: `SessionEnd` (old id `305b0036…`), then `SessionStart` with `source=clear`, new `session_id` (`42059a8e…`), new `transcript_path` = a new file in the **same folder**. No `UserPromptSubmit` for `/clear`.
- The old file was not changed except for trailing metadata lines (`cost-state`).
- The new file starts at byte 0 with `mode`, `session-owner`, `file-history-snapshot`, then two `user` lines that wrap the command: one starts with `<local-command-caveat>`, one has `<command-name>/clear</command-name>`, then a `system` line with `subtype=local_command`. All must be skipped as prompts.
- Same pane, same process (pid unchanged). Rex status went `working` → `idle` within 30 ms.

### 9. Resume (S3.6)

`/exit` (paste + Enter), then a new block in the same Rex session with `--resume 305b0036…`.

- On exit: hook `SessionEnd` with `reason=prompt_input_exit`. Rex: status removed (`program_status_removed`, reason `clear`), `child_exited` (code 0), `foreground` null. The block stays open but is dead. The screen prints `claude --resume <id of the LAST session>` (that was the `/clear` session, not the first).
- Resume hook: `SessionStart` with `source=resume`, `session_id` = the **same** `305b0036…`, `transcript_path` = the **same file**.
- File: appended, not copied. The line count went from 87 to 88 (`tools-used`, `cost-state` metadata only). No old lines were copied.
- This is the "same file and session ID" outcome. A new process (new pid 36336) and a new Rex block: the hook gives the new `REX_BLOCK`.
- Rex status for the new process: `idle`, owner pid 36336.

### 10. Notification when idle (S3.1)

- Not observed. The pane was idle for about 104 s (03:29:19–03:31:03) and again for about 100 s after the resume (03:32:35–03:34:14). No idle `Notification` fired either time.
- The only `Notification` seen was the permission one (section 3).
- So the idle kind is unconfirmed on 2.1.322. It may need a longer time, or it may need an unfocused terminal. Treat any `Notification` whose `notification_type` is not `permission_prompt` as "not blocked".

### 11. Environment (S3.5)

Hooks see `REX_SESSION` and `REX_BLOCK`. Every Claude log line carried the right session ID and the block ID of the pane that ran Claude. After the resume in a new block, the value was the new block.

### Hook summary

| Hook | When it fired | Useful fields |
| --- | --- | --- |
| `SessionStart` | at start (`startup`), `/clear`, resume. Before any prompt. | `session_id`, `transcript_path`, `cwd`, `source`, `model` |
| `UserPromptSubmit` | on every Enter with text, also for a mid-turn message. | `prompt`, `prompt_id`, `permission_mode` |
| `PermissionRequest` | when the prompt opens | `tool_name`, `tool_input`, `permission_suggestions`. No `transcript_path`. |
| `Notification` | about 6 s after the prompt opens | `notification_type`, `message`, `transcript_path` |
| `Stop` | when a turn ends normally | `last_assistant_message`, `stop_hook_active`, `background_tasks`. Not on interrupt or deny. |
| `SessionEnd` | on `/exit` and before `/clear` | `reason` (`prompt_input_exit`) |

## Recommended values

InputProfile for Claude Code:

```
PasteEnterDelay: 300ms   (worked every time; shorter values not tested)
SubmitKey:       enter
InterruptKeys:   [escape]   (one press stops a running tool and the turn)
WhileWorking:    steer      (the message is absorbed at the next tool boundary,
                             inside the same turn; ctrl+enter sends it at once)
```

Notes for `WhileWorking`: the text is accepted whenever the model is busy, and it is read by Claude after the current tool result. If no tool call follows, it will probably become the next turn. I did not test that case. So rex-tg's own "wait for idle" rule (D11) still gives clean one-turn-per-message behavior. `⚡ Send now` is safe to type.

Permission prompt answer keys (read the labels from the screen; map label to number):

| Label | Keys |
| --- | --- |
| Yes | `1` |
| Yes, and always allow … | `2` (confirm the label text first) |
| Yes, and switch to auto mode | `3`. Do not offer this as a button. |
| No | `4` (the last number; read it from the screen) |
| Cancel | `escape` |

A digit key is enough. Do not add `enter`. Check the screen hash before sending.

Notification field rule: `Notification` with `notification_type == "permission_prompt"` means blocked (kind `permission`). Any other value means "waiting for input" and does not set `blocked`.

State source for Claude: **Rex status first.** Claude reports `idle`, `working`, `blocked` (+ `kind=permission` and `msg`), and `done` correctly and on time. It also covers the cases that hooks miss: interrupt and deny (no `Stop` hook, Rex goes `idle`). Use hooks for the session ID, transcript path, and `PermissionRequest` details (`tool_name`, `tool_input`). Use `UserPromptSubmit` and `Stop` only as a second signal. Use `Stop` carefully: it is missing after an interrupt. Transcript lines are the third source. A mid-turn message makes `UserPromptSubmit` fire but does not start a new turn.

Detection by process: `argv0 == "claude"` or `invoked_path` ends with `/.local/bin/claude`. The `name` is the version string. `program_status.app == "claude-code"` also works. There is no `ancestors` field in `process`.

Resume rule:

- `SessionStart` with `source=resume` gives the same `session_id` and the same `transcript_path`. The old lines stay and only metadata is appended. Start the tailer at the saved cursor, or at the end of the file with the last 3 final answers if there is no cursor. Do not read from byte 0.
- `source=clear` gives a new `session_id` and a new file in the same folder. Take over the topic and start at byte 0 (the first lines are `mode`, `session-owner`, and the `/clear` wrapper lines; skip them).
- `startup` with a new file: byte 0.
- After `/exit` the Rex block is dead (`child_exited`, status removed). A resume starts in a new block. The `SessionStart` hook carries the new `REX_BLOCK`. Move the conversation to that pane (a session runs in one pane at a time).

Adapter rules to add or change in agent-transcripts.md:

- The final-answer test `stop_reason == end_turn` is confirmed. The line is written complete.
- `tool_use` lines appear before the tool runs. Confirmed.
- Add to "Skip": `type=user` text blocks that start with `[Request interrupted`; `<local-command-caveat>` and `<command-name>` wrappers (already listed); `cost-state`, `mode`, `permission-mode`, `file-history-snapshot`, `ai-title`.
- Mid-turn messages: use `queue-operation` (`enqueue`) or the `queued_command` attachment for the echo. There is no `user` line.
- The project folder name uses the real path (`/private/tmp/...`, not `/tmp/...`). Never compute it; use `transcript_path`.
