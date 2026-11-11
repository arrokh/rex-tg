# Agent transcripts and hooks

This file records what the three agents write and which hooks they offer, as observed on this Mac on 2026-11-11. It is the reference for the adapters in [design.md](design.md#5-agent-adapters). The spike results are in [s2-s4-claude.md](spikes/s2-s4-claude.md), [s2-s4-codex.md](spikes/s2-s4-codex.md), [s2-s4-pi.md](spikes/s2-s4-pi.md), and [s5-rex.md](spikes/s5-rex.md).

The examples below are synthetic. They copy the structure of real lines, not their content. Do not commit real transcripts as test fixtures, because they can contain private data and secrets.

| Runtime | Version seen | Transcript folder |
| --- | --- | --- |
| pi | 1.5.2 (`@earendil-works/pi-coding-agent`) | `~/.pi/agent/sessions/` |
| Claude Code | 2.1.322 | `~/.claude/projects/` |
| Codex CLI | 0.167.0 | `~/.codex/sessions/` |

Formats change between versions. Each adapter must ignore line types it does not know.

## pi

### Location

```
~/.pi/agent/sessions/--<cwd, "/" replaced by "-">--/<ISO time>_<session id>.jsonl
```

Example: `~/.pi/agent/sessions/--Users-me-work-personal-dotfiles--/2026-11-10T05-32-43-796Z_01a1c3f1-….jsonl`.

The file is created at the first prompt, not at launch. Its name and its header carry the launch time, so the name is not the time of the first prompt (observed in S4).

Skip subagent files in the same folder tree:

- `<session folder>/<run id>/run-0/session.jsonl`
- `subagent-artifacts/*_worker_transcript.jsonl`

### Lines

The first line is a header. Every other line is an entry with an 8-character hex `id` and a `parentId`. The entries form a tree, because pi supports branches.

At the first prompt, pi writes the header, `model_change`, `thinking_level_change`, a `system` message, and the first `user` message in one batch. The first entries keep their launch time as their timestamp (observed in S4).

Line types seen in one session: `message` (1158), `custom` (15), `custom_message` (13), `thinking_level_change` (10), `compaction` (5), `model_change` (4), `context_edit` (4), `session_info` (1), `session` (1).

```jsonl
{"type":"session","version":3,"id":"01a1c40d-0000-7000-0000-000000000000","timestamp":"2026-11-10T06:03:11.852Z","cwd":"/Users/me/work/project"}
{"type":"session_info","id":"54728860","parentId":"8d87ccf6","timestamp":"2026-11-10T06:03:12.778Z","name":"[infra][chore] Update tooling"}
{"type":"message","id":"c78eb422","parentId":"c7eae892","timestamp":"…","message":{"role":"user","content":[{"type":"text","text":"Fix the failing test"}]}}
{"type":"message","id":"8503fcd2","parentId":"c78eb422","timestamp":"…","message":{"role":"assistant","content":[{"type":"thinking","thinking":"…"},{"type":"text","text":"I will run the tests first."},{"type":"toolCall","name":"bash","arguments":{}}],"stopReason":"toolUse","model":"…","provider":"…"}}
{"type":"message","id":"2e4f1fed","parentId":"4d247d8d","timestamp":"…","message":{"role":"assistant","content":[{"type":"text","text":"The test passes now."}],"stopReason":"stop"}}
```

Assistant message keys: `api`, `content`, `durationMs`, `model`, `provider`, `rawStopReason`, `responseId`, `role`, `stopReason`, `thinkingLevel`, `timestamp`, `usage`.

Roles seen: `assistant`, `user`, `toolResult`, `system`.

### Adapter rules

| Event | Source |
| --- | --- |
| Session ID | header `id` |
| Title | `session_info.name` |
| Reply | `type=message`, `message.role=assistant`, the `text` blocks joined |
| Final or commentary | `stopReason`: `stop` is final, `toolUse` is commentary, `error` is an error |
| Prompt typed on the Mac | `type=message`, `message.role=user`, the `text` blocks joined |
| Message sent during a tool run | `type=message`, `message.role=user`. It is written after the `toolResult` line, in the same run (observed in S4). |
| Tool call (verbose mode) | `toolCall` blocks in assistant messages |
| Divider | `compaction` |
| Deduplication key | `pi:<session id>:<entry id>` |
| Skip | `toolResult`, `system`, `custom`, `custom_message`, `model_change`, `thinking_level_change`, `context_edit`, `thinking` blocks |

`custom_message` entries come from extensions. Some have `display: true`. They stay skipped until there is a reason to show them.

### Hook surface

pi loads TypeScript extensions from `~/.pi/agent/extensions/`. The `rexume` extension on this Mac uses:

- `pi.on("session_start", (event, ctx) => …)`
- `ctx.sessionManager.getSessionFile()` for the transcript path
- `pi.on("session_shutdown", …)`
- `pi.sendMessage({ customType, content, display: true }, { triggerTurn: true, deliverAs: "followUp" })` to inject a message

The last call is a possible fallback for incoming text, if typing into the pane proves unreliable for pi.

The spike extension saw these events in one run (observed in S3): `session_start` (reason `startup`), `input`, `agent_start`, `turn_start`, `tool_execution_start`, `tool_execution_end`, `turn_end`, `agent_end`, and `session_shutdown` (reason `quit`). Each one had `REX_BLOCK` and `REX_SESSION` set. The rex-tg extension (`extensions/pi/rex-tg.ts`) subscribes to `session_start`, `session_shutdown`, `agent_start`, and `agent_end`.

pi's types also define the reasons `reload`, `new`, `resume`, and `fork` for both `session_start` and `session_shutdown`. No run has shown them yet. So `/new`, `/resume`, and forks are still open (S3).

pi also reports its own state to Rex, through OSC 7501 (see [s2-s4-pi.md](spikes/s2-s4-pi.md)).

## Claude Code

### Location

```
~/.claude/projects/<cwd, each "/" and "." replaced by "-">/<session id>.jsonl
```

Example: `/Users/me/work/personal` becomes `~/.claude/projects/-Users-me-work-personal/<session id>.jsonl`.

The folder name uses the real path. A cwd under `/tmp` gives a folder that starts with `-private-tmp-`. Use the `transcript_path` from the `SessionStart` hook, and do not compute the path (observed in S2).

Skip `~/.claude/projects/<project>/<session id>/subagents/*.jsonl`.

### Lines

Line types seen in one session: `assistant` (144), `attachment` (121), `user` (85), `api-request` (70), `api-request-blob` (39), `session-owner` (36), `custom-title` (36), `atis-latch` (36), `last-prompt` (35), `tools-used` (5), `queue-operation` (2), `api-request-shape` (2), `system` (1).

Each `assistant` line holds one content block. Several lines share the same `message.id`. In the sample, blocks were `tool_use` (84), `thinking` (48), and `text` (12).

The spike session also had these line types, which the adapter skips: `mode`, `permission-mode`, `file-history-snapshot`, `ai-title`, and `cost-state` (observed in S4). Many of these lines have no `timestamp` and no `uuid`. A rule that reads the first line's time must use the first line that has a timestamp (observed in S4).

```jsonl
{"type":"user","uuid":"u-1","parentUuid":null,"sessionId":"S","isSidechain":false,"entrypoint":"cli","cwd":"/Users/me/work/project","timestamp":"…","message":{"role":"user","content":"Fix the failing test"}}
{"type":"assistant","uuid":"a-1","parentUuid":"u-1","sessionId":"S","isSidechain":false,"timestamp":"…","message":{"id":"msg_1","role":"assistant","model":"…","stop_reason":"tool_use","content":[{"type":"text","text":"I will run the tests first."}]}}
{"type":"assistant","uuid":"a-2","parentUuid":"a-1","sessionId":"S","isSidechain":false,"timestamp":"…","message":{"id":"msg_1","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash","input":{}}]}}
{"type":"user","uuid":"u-2","parentUuid":"a-2","sessionId":"S","timestamp":"…","message":{"role":"user","content":[{"type":"tool_result","content":"…"}]}}
{"type":"assistant","uuid":"a-3","parentUuid":"u-2","sessionId":"S","timestamp":"…","message":{"id":"msg_2","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"The test passes now."}]}}
{"type":"custom-title","customTitle":"tests: fix parser","sessionId":"S"}
```

Top-level keys of an assistant line: `advisorModel`, `apiBlockIndex`, `cwd`, `effort`, `entrypoint`, `gitBranch`, `isSidechain`, `message`, `parentUuid`, `perTurnEffort`, `promptCacheRequested`, `requestId`, `requestRef`, `requestedModel`, `sessionId`, `thinkingDisplay`, `thinkingDurationMs`, `timestamp`, `type`, `userType`, `uuid`, `version`.

`entrypoint` tells where the session runs. It is `cli` for the Claude Code CLI and `t3-code` for T3 Code. In a sample of 40 Claude session files changed in the two days before 2026-11-11, 36 came from T3 Code and 4 from the CLI.

### Adapter rules

| Event | Source |
| --- | --- |
| Session ID | `sessionId`, or the file name |
| Title | `custom-title.customTitle` |
| Reply | `type=assistant`, `text` blocks |
| Final or commentary | `message.stop_reason`: `end_turn` is final. The final line is complete when it is written, with no earlier stub line (observed in S4). `tool_use` is commentary. |
| Prompt typed on the Mac | `type=user` with string content or `text` blocks, without `isMeta`, and not command wrappers such as `<command-name>`, `<local-command-stdout>`, or `<local-command-caveat>`. A multi-line paste is one string with `\n` (observed in S2). |
| Message sent during a tool run | `queue-operation` with `operation=enqueue` (`content` is the text), when it is sent. `queue-operation` with `operation=remove` and `reason=absorbed_mid_turn`, when Claude reads it. An `attachment` line with `attachment.type=queued_command` (`attachment.prompt` is the text, `humanTurn=true`). The line is written when the text is absorbed, but its timestamp is the time of sending. There is no `user` line. Match the echo on the `queued_command` attachment (observed in S2). |
| Interrupt | A `user` line with a `text` block that starts with `[Request interrupted`. It ends the turn and is not a prompt (observed in S2). |
| Tool call (verbose mode) | `tool_use` blocks |
| Divider | `system` lines with a compaction subtype, and `SessionStart` with source `clear` |
| Deduplication key | `claude:<sessionId>:<uuid>` |
| Skip | `isSidechain: true`, `thinking` blocks, `tool_result`, `attachment` except `queued_command`, `api-request*`, `session-owner`, `atis-latch`, `last-prompt`, `tools-used`, `queue-operation`, `mode`, `permission-mode`, `file-history-snapshot`, `ai-title`, `cost-state`, other `system` lines |

### Process

`~/.local/bin/claude` is a link to `~/.local/share/claude/versions/<version>`, for example `2.1.322`. So the process name can be the version string, not `claude`. Detection uses `argv0`, the invoked path, and the executable path instead.

Claude Code also runs helper processes, such as `claude daemon run` and `claude bg-pty-host … --bg-spare`. They are not agents in a pane, and the registry ignores them.

Rex `process` shows `name` as the version string, for example `2.1.322`. It shows `argv0` as `claude` and `invoked_path` as `~/.local/bin/claude`. The Rex status `app` is `claude-code`, not `claude` (observed in S2). The foreground stays Claude while a Bash tool runs (observed in S2). Claude Code does not keep the transcript open, so `lsof` cannot find the file. Use the hook instead (observed in S2).

### Hook surface

Hooks are configured in `~/.claude/settings.json`. Each hook gets JSON on stdin with `session_id`, `transcript_path`, `cwd`, and `hook_event_name`. `SessionStart` also gets `source`: `startup`, `resume`, `clear`, or `compact`.

What the spike showed (S2 and S3):

- `SessionStart` with `clear` gives a new session ID and a new file in the same folder. With `resume`, the session ID and the file stay the same, and no old lines are copied.
- `Notification` has `notification_type`. The value `permission_prompt` is a permission request. The idle value, `idle_prompt`, comes from the Claude Code docs. No run showed it.
- `PermissionRequest` carries `tool_name`, `tool_input`, `permission_suggestions`, and `prompt_id`. The hook log also has `session_id`, `transcript_path`, and `cwd` for this hook.
- `Stop` does not fire after Esc or after a denied permission. Rex status and the transcript show the end of the turn instead.
- `UserPromptSubmit` also fires for a message sent during a tool run. That message does not start a new turn.
- `/clear` fires `SessionEnd`, then `SessionStart` with `source` `clear`. `/exit` fires `SessionEnd` with `reason` `prompt_input_exit`.
- Hooks saw `REX_SESSION` and `REX_BLOCK`, including after a resume into a new pane.

Hooks already installed on this Mac include `moshi-hook`, `cux`, `rexume report claude`, a `herdr` script, `paseo`, and `rex __internal session-start --agent claude`. rex-tg adds its own entries for the events listed in [design.md section 6](design.md#6-panes-sessions-and-transcripts), and leaves the others alone.

Hooks also run in T3 Code sessions. There, `REX_BLOCK` is not set, so `rex-tg hook` does nothing.

## Codex

### Location

```
~/.codex/sessions/YYYY/MM/DD/rollout-<local time>-<session id>.jsonl
```

There is no folder per project. The `cwd` is in the first line, `session_meta`.

The file is created at the first Enter, not at launch. Its name carries the process start time, or the time of `/new`. It does not carry the time of the first prompt (observed in S4). `/new` makes a new file at the next prompt, in the same process. The old file stays unchanged. A resume appends to the same file (observed in S4).

### Lines

Each line is `{"timestamp", "type", "payload"}`. Counts seen in one session (`type / payload.type`):

| Count | Line |
| --- | --- |
| 155 | `event_msg / item_completed` |
| 91 | `token_usage_record` |
| 91 | `event_msg / token_count` |
| 70 | `response_item / custom_tool_call`, and 70 `custom_tool_call_output` |
| 55 | `response_item / message` |
| 39 | `response_item / reasoning` |
| 17 | `response_item / function_call`, and 17 `function_call_output` |
| 3 | `turn_context`, `event_msg / task_started`, `event_msg / task_complete` |
| 1 | `session_meta` |

`session_meta.payload` keys: `base_instructions`, `cli_version`, `context_window`, `creator_account_id`, `creator_user_id`, `cwd`, `git`, `history_mode`, `id`, `model_provider`, `originator`, `runtime_workspace_roots`, `session_id`, `source`, `timestamp`.

`originator` tells where the session runs, for example `T3 Code` or `Codex Desktop`. The Codex CLI writes `originator` `codex-tui` and `source` `cli` (observed in S4).

```jsonl
{"timestamp":"…","type":"session_meta","payload":{"id":"01a1c0eb-…","cwd":"/Users/me/work/project","originator":"…","cli_version":"0.167.0","source":"…"}}
{"timestamp":"…","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions …"}]}}
{"timestamp":"…","type":"event_msg","payload":{"type":"item_completed","thread_id":"…","turn_id":"…","item":{"type":"UserMessage","id":"…","content":[{"type":"text","text":"Fix the failing test"}]}}}
{"timestamp":"…","type":"response_item","payload":{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","status":"completed","content":[{"type":"output_text","text":"I will run the tests first."}]}}
{"timestamp":"…","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"…","phase":"commentary","content":[…]}}}
{"timestamp":"…","type":"response_item","payload":{"type":"message","id":"msg_2","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"The test passes now."}]}}
{"timestamp":"…","type":"event_msg","payload":{"type":"task_complete","turn_id":"…","last_agent_message":"The test passes now."}}
```

### Duplicates

Codex records each reply more than once:

- `response_item / message` with `role=assistant` (55 messages in the sample, 30 of them from the assistant)
- `event_msg / item_completed` with `item.type=AgentMessage` (30 in the sample)
- `event_msg / task_complete` with `last_agent_message`

The adapter reads replies from `response_item` only. It treats `task_complete` as the end of a turn and ignores its text.

User messages have the same issue. `response_item` with `role=user` also holds text that Codex adds itself, such as the `AGENTS.md` instructions. `item_completed` with `UserMessage` held only the real prompts in the sample. The adapter reads prompts from `item_completed`.

Spike S4 confirmed both choices on a CLI rollout. In each flush, the `item_completed` `AgentMessage` line comes first, then the `response_item` message with the same ID. The `task_complete` text repeats the final answer. `response_item` with `role=user` also holds the `<environment_context>` text that Codex adds at the start of a session (observed in S4).

### Adapter rules

| Event | Source |
| --- | --- |
| Session ID | `session_meta.payload.id` |
| Reply | `response_item`, `payload.type=message`, `payload.role=assistant`, the `output_text` blocks joined |
| Final or commentary | `payload.phase`: `final_answer` or `commentary` |
| Prompt typed on the Mac | `event_msg / item_completed` with `item.type=UserMessage` (confirmed in S4) |
| Turn end | `event_msg / task_complete`. An interrupted turn has `turn_aborted` (`reason` `interrupted`) and no `task_complete` (observed in S2 and S4). |
| Message sent during a turn | The same `turn_id`, and no new `task_started`. It is written as a `response_item` `user` line and an `item_completed` `UserMessage` (observed in S2). |
| Tool call (verbose mode) | `response_item / function_call` and `custom_tool_call` |
| Deduplication key | `codex:<session id>:<payload.id>` |
| Skip | `reasoning`, tool outputs, `token_count`, `token_usage_record`, `turn_context`, `world_state`, `item_completed` except `UserMessage`, `response_item` with `role=user` (it also holds injected context) |

### Hook surface

`~/.codex/config.toml` has `[features] hooks = true`. Hooks live in `~/.codex/hooks.json`, in the same shape as Claude Code:

```json
{ "hooks": { "SessionStart": [ { "matcher": "", "hooks": [ { "type": "command", "command": "rex-tg hook codex" } ] } ] } }
```

Events in use on this Mac: `SessionStart`, `UserPromptSubmit`, `Stop`, `PreToolUse`, `PostToolUse`, and `PermissionRequest`.

The spike ran Codex with hooks off (`--disable hooks`). So no hook payload was checked at runtime, and trust was not tested (S3). The names `hook_event_name`, `transcript_path`, `session_id`, `permission_mode`, and `tool_input` occur in the Codex 0.167.0 binary. This is a string search only.

Codex trusts each hook entry by a hash. `config.toml` stores one table per trusted entry:

```toml
[hooks.state."/Users/me/.codex/hooks.json:session_start:0:0"]
trusted_hash = "sha256:…"
```

The key holds the entry's position in its list. So the installer appends new entries at the end of a list, and existing entries keep their positions and their trust. Spike S3 checks that Codex asks once to trust a new entry.

`config.toml` also has a `notify` program for `turn-ended`. That slot is already used by another program, so rex-tg uses hooks instead.

Still open (S3): whether the hook payload includes the session ID and the transcript path.

### Process

`/opt/homebrew/bin/codex` is a link to the Codex binary in the Homebrew Caskroom (`/opt/homebrew/Caskroom/codex/0.167.0/bin/codex`).

Codex keeps the rollout file open. After a resume, `lsof` showed it on an open file descriptor (observed in S4). Its foreground process stays `codex` while a command runs (observed in S2). Its Rex `name` and `argv0` are both `codex`.

## Pairing tool calls with their results

A live progress view needs to know when each tool call ends and whether it failed. Each runtime links a result to its call by an ID:

| Runtime | Call | Result | Error flag |
| --- | --- | --- | --- |
| pi | `toolCall` block in an assistant message: `id`, `name`, `arguments` | `message.role=toolResult`: `toolCallId`, `toolName`, `durationMs`, `content`, `details` | `isError` |
| Claude Code | `tool_use` block: `id`, `name`, `input`, `caller` | `tool_result` block in a `user` line: `tool_use_id`, `content` | `is_error` (sometimes missing; missing means no error) |
| Codex | `response_item` `function_call` or `custom_tool_call`: `call_id`, `name`, `arguments` or `input`, `status` | `function_call_output` or `custom_tool_call_output`: `call_id`, `output` | none seen; read the exit code from `output` when present |

pi results can also carry `nestedCalls`, for tools that run other tools.

Each call line is written before the tool runs, and each result line after it ends. So the tailer sees "started" and "finished" as two separate lines. Spike S4 confirmed this timing for all three agents.

## Rex

Facts from Rex `0.0.1793981048-g01d6c4`, API revision 11. The S2 to S5 spikes ran on the installed Rex Beta app, version 0.1.0 (bundle build 1925). See [s5-rex.md](spikes/s5-rex.md).

- `rex events --all --json` prints one JSON object per line. Agent status arrives as `{"type":"session_event","payload":{"session_id":…,"event":{"type":"block_event","payload":{"block_id":…,"name":"program_status_changed","payload":{"record":{"state":…,"kind":…,"app":…,"msg":…}}}}}}`.
- The `app` values seen are `pi`, `claude-code`, and `codex` (observed in S2).
- Most block events are `progress_report`. The registry ignores them.
- `rex block call -s S -b B process` returns `foreground` with `pid`, `name`, `path`, `argv0`, and `cwd`, plus `ancestors`, `child`, and `last_exit`. Claude Code and Codex showed no `ancestors` key. pi showed an empty `ancestors` list (observed in S2).
- `rex block call -s S -b B program_status` returns `records` with `state`, `kind`, `app`, `effective_app`, `msg`, `updated_at`, and `owner` (`pid`, `name`).
- `block.event` accepts `{"type":"paste","payload":{"text":"…"}}`, as the example in `rex api describe block.event` shows. Spike S2 tested it with all three agents. A paste, then Enter after a 300 ms wait, gave one prompt in each (observed).
- `rex send` types text. `rex send-key` presses keys. `rex capture --trim` prints the screen and scrollback.
- `rex events` keeps running until it is stopped. A Perl `alarm` wrapper did not stop it.
