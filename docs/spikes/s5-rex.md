# Spike S5: Rex events and restarts

Date: 2026-11-11, 10:11 to 10:12 local (+07:00), from the spike logs.

Results are marked **observed** (seen in the spike logs or notes) or **not tested**, as in [s2-s4-pi.md](s2-s4-pi.md).

## Setup

- An isolated Rex server, with its own socket and its own state folder. The spike notes say the user's own server was not touched.
- The socket path was absolute, in a cache folder, and its name ended in `rextg-s5.sock`.
- One server run, then `server stop`, then a restart with `--restore`. Both runs used the same state folder.
- The installed Rex Beta app is version 0.1.0 (bundle build 1925). The saved state records build `0.1.0-preview.14`.

## Findings

### 1. Listener rules (observed)

- A relative socket path, `unix:./rex.sock`, was refused. The error said the endpoint is "a TCP endpoint whose callers Rex cannot identify". (Spike log `try.log`.)
- The same error offered `--allow-anonymous` to serve it anyway. rex-tg does not use this flag.
- The same error named two identified options for remote access: `--extra-listen tailnet://…` and `--tailscale`. rex-tg does not use them, because it works on one Mac only.
- Unix socket paths must be absolute and at most 104 bytes. This comes from the spike notes. No log line for the length rule was kept.

### 2. Events while panes and sessions change (observed)

The event log has 35 events, plus one error line from `server stop`. The table shows how many of each event were logged, and how rex-tg handles it (`internal/rex/events.go`).

| Event | Count | rex-tg |
| --- | --- | --- |
| `session_created` | 1 | `SessionCreated` |
| `session_event` / `block_created` | 2 | `BlockCreated` |
| `session_event` / `block_closed` | 1 | `BlockClosed` |
| `session_event` / `block_event` / `process_changed` | 5 | `ProcessChanged` |
| `session_event` / `block_event` / `pwd_changed` | 1 | `PwdChanged` |
| `session_event` / `block_event` / `title_changed` | 1 | `TitleChanged` |
| `session_event` / `block_owner_changed` | 4 | dropped |
| `session_event` / `session_view_changed` | 5 | dropped |
| `session_event` / `client_connected` | 3 | dropped |
| `session_event` / `client_disconnected` | 3 | dropped |
| `client_joined` | 5 | dropped |
| `client_left` | 4 | dropped |

One block was created, and later closed. Both events carried the same block ID.

### 3. Restart with the same state folder (observed)

1. `rex server stop`. The shutdown was clean (`clean_shutdown=true` in the restore log).
2. `rex server run --restore`, with the same state folder.

The restore log shows:

- One saved session was restored.
- Two blocks were restored, and no fresh blocks were created.
- The creation options were not replayed. The `command` option was dropped.

The block IDs and the session ID were the same before and after the restart (spike notes). The saved snapshot holds the same two block IDs and the same session ID. The block that was closed before the stop was not restored.

### 4. JSON shapes (observed)

- `rex ls --json` returns `{"sessions": [...]}`.
- `rex block ls --json` returns `{"blocks": [...]}`.

The Rex client in `internal/rex/rex.go` reads these keys.

## What this means for the design

- The block IDs did not change after one restart on this build. Design section 7 still allows a new block ID as a fallback. Keep that case until more restarts are tested.
- A restored pane does not run its creation command again. So the agent process has to be resumed in the pane. This was not tested with an agent.
- rex-tg's event reader drops the client, owner, and view events, and `progress_report`. It keeps the other events. It maps unknown events to `OtherEvent`.

## Not tested

- An agent that resumes in a restored pane. This is the rest of the restart rule in design section 7.
- More than one restart, and restarts with more than two panes.
- The 104-byte limit. No log line was kept.
