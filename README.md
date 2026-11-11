# rex-tg

rex-tg connects coding agents that run in Rex panes to Telegram.

- In the main chat, `/agents` lists the agents that run in Rex panes.
- Tap an agent. rex-tg opens a topic for its session.
- `/new` starts an agent in a new Rex tab, in a folder you pick, and opens its topic.
- A new session in the same pane, such as after the agent's `/clear` or `/new`, takes over the topic.
- After a Rex restart, the topic follows the session when the agent resumes in its pane.
- While the agent works, one progress message shows its tool calls. The answer then arrives as a normal message.
- Text you write in a topic waits until the agent is idle. Then rex-tg types it into the pane.
- Replies come from each agent's session file. Rex and the agent's hooks tell rex-tg whether the agent is working.
- SQLite makes sure no reply is posted twice.

Supported agents: pi, Claude Code, and Codex, on this Mac. Not supported: T3 Code panes, and Rex servers on other computers.

> **Status: version 1 is built.** Unit tests, daemon integration tests (with a fake Telegram and a fake Rex), and an end-to-end test (with a real, isolated Rex server and dummy agents) pass. Spike S1 is still open: nobody has tried rex-tg with a real Telegram client yet, because that needs your bot. Items marked **(unverified)** depend on that check.

## Requirements

- **macOS.** rex-tg uses the macOS Keychain, the `security` tool, and launchd.
- **Rex**, the Rex Beta app, with its `rex` command. The spikes used Rex Beta 0.1.0 (build 1925).
- **pi, Claude Code, or Codex.** Each agent runs in a Rex pane.
- **Go 1.26.1, to build.** `.tool-versions` sets the version. mise installs it.
- **A Telegram bot** from @BotFather, with Threaded Mode turned on.
- **For voice notes only:** ffmpeg and whisper.cpp.

## Setup

Do the steps in order. Step 5 comes after step 4, because the daemon must run to read the pairing code.

### 1. Create the bot

1. Open @BotFather in Telegram. Send `/newbot`, and follow the steps. Keep the token. Treat it like an SSH key.
2. Turn on Threaded Mode. In @BotFather, choose your bot. Open Bot Settings > Threaded Mode, and turn it on.

Threaded Mode gives your private chat with the bot topics. rex-tg uses one topic for each agent session.

### 2. Build and install the binary

```sh
mise install   # installs Go 1.26.1 from .tool-versions
make install   # builds the binary and copies it to ~/.local/bin/rex-tg
```

If the shell cannot find `rex-tg`, add `~/.local/bin` to your `PATH`. The hooks and the launchd agent use the full path, so they do not need `PATH`.

### 3. Store the token and get a pairing code

```sh
rex-tg login
```

1. macOS `security` asks for the token. Paste the token from @BotFather. It is not shown.
2. rex-tg checks the token with Telegram, and stores it in the Keychain.
3. It tells you if Threaded Mode is off.
4. It prints a pairing code. Keep it for step 5. The code works for 10 minutes.

`rex-tg login --token-stdin` reads the token from one line of standard input instead. Use it in scripts.

### 4. Install the hooks and the launchd agent

```sh
rex-tg install
```

The command changes these files:

- **Claude Code:** adds six rex-tg hook entries to `~/.claude/settings.json`. The events are `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, `Notification`, and `SessionEnd`.
- **Codex:** adds four rex-tg hook entries to the end of each event list in `~/.codex/hooks.json`. The events are `SessionStart`, `UserPromptSubmit`, `Stop`, and `PermissionRequest`.
- **pi:** writes the extension `~/.pi/agent/extensions/rex-tg.ts`. The extension sends four pi events to rex-tg: `session_start`, `session_shutdown`, `agent_start`, and `agent_end`.
- **launchd:** writes `~/Library/LaunchAgents/local.rex-tg.plist` and starts the daemon. launchd starts the daemon at login, and restarts it if it stops.

Before it changes `settings.json` or `hooks.json`, rex-tg saves a backup of that file next to it. It keeps the 5 newest backups. Running the command again changes nothing.

To change one agent only, add `--only pi`, `--only claude`, or `--only codex`. The launchd agent is still installed.

Then start Codex once. Codex asks you to trust the new hook entries. Trust them. The trust prompt is not tested yet (spike S3).

pi loads extensions from its folder. Whether pi loads this extension with no other step is not tested yet (spike S3).

### 5. Pair your Telegram account

Send the code from step 3 to your bot:

```
/start <code>
```

Send it after step 4. The daemon reads the code only while it runs.

- The first valid code makes your account the owner.
- The bot answers no one until pairing is done.
- A code works once. It expires after 10 minutes. After it expires, run `rex-tg pair` for a new code.
- After 5 wrong codes from one Telegram account, that account cannot pair with this code. Others still can, so a stranger cannot lock you out. Run `rex-tg pair` for a new code; it clears all counts.

### 6. Check the setup

```sh
rex-tg doctor
```

Doctor prints one line for each check. `✓` means the check passed. `!` is a warning. `✗` is a failure. The checks cover:

- the Rex CLI, and `rex whoami`
- the bot token, `getMe`, and Threaded Mode
- the database, and whether an owner is paired
- the Claude Code and Codex hooks, and the pi extension
- the launchd agent, and the folder modes
- whether a daemon is running

## Daily use

### Main chat

- `/agents` lists the agents in Rex panes. Tap one to open its topic. If the session already has a topic, you get a link to it. If you deleted that topic, a new one is made for the same session.
- `/new` starts a coding agent in a new Rex tab. Tap the agent, then tap a recent folder or send a path such as `~/work/app`. The tab goes into the Rex session named after the folder (only the name counts, so two folders with the same name share a session). If there is no such session, rex-tg makes it. The agent starts as if you typed its name in a terminal, so your `~/.zshrc` applies. When the agent is ready, its topic opens. `/new pi ~/work/app` does it in one step.
- A topic is named after its Rex pane when you gave the pane a name, else after the agent's session name. The name follows changes of both.
- When an agent exits or its pane closes, its topic says so (🔴), a running turn ends, and the topic's name gets a 🔴 until the agent runs again.
- `/topics` lists the topics and their states.
- `/status` shows the health of the daemon.
- `/pause` and `/unpause` stop and allow all typing into panes. Use `/pause` if you lose your phone. (optional)
- `/help` lists the commands.

An agent marked `(guess)` has no hook report yet. rex-tg took the newest transcript in the agent's folder that changed after the agent started. An agent marked `(waiting for session)` has no known session file yet. It resolves at the agent's next prompt or turn end.

### In a topic

Text you write in a topic goes to the agent. The commands below are for rex-tg. Any other text that starts with `/` goes to the agent.

| Command | What it does |
| --- | --- |
| `/info` | Shows the agent's state, and when rex-tg last heard a signal from it. |
| `/screen [lines]` | Shows the pane's screen, with secrets removed. |
| `/approve`, `/deny` | Answers the open permission prompt. |
| `/esc` | Sends Escape to the pane. |
| `/interrupt` | Stops the current turn with the agent's interrupt keys. |
| `/key <keys>` | Presses the keys you name in the pane. |
| `/force <text>` | Types text even when a prompt is open. It skips the prompt check. |
| `/mode progress`, `block`, or `final` | Sets how replies appear in this topic. |
| `/verbose on`, `full`, or `off` | Adds one message per tool call (`on`), or also the tool output (`full`). |
| `/reasoning on` or `off` | Shows the agent's thinking text, when the agent stores it. |
| `/resend [id]` | Sends again the parts of a reply that may not have arrived. Without an id: the newest such reply. The `🔁 Send again` button on the notice does the same. |
| `/unlink` | Closes the topic in rex-tg. The topic stays in Telegram. |
| `/mute` and `/unmute` | Sends answers without sound, or with sound again. |
| `/help` | Lists the commands. |

Other `/` text goes to the agent unchanged. Examples are `/compact`, `/model`, and `/clear`. To send an agent command that has the same name as a bot command, add a second slash. For example, `//help` sends `/help` to the agent, and `//new` sends `/new`. In a topic, `/new` itself starts another agent (see Main chat).

When you open a topic, the bot posts an intro card. The card shows the session, the pane, the folder, and the mode. Then the bot posts the last 3 final answers. The number is the `backfill` setting.

When a new session takes over a topic, the bot posts a divider. For example: `── claude · new session (clear) ──`.

When the agent stops, the topic shows `🔴 agent stopped`. Messages you send there are refused, with a reason.

### Waiting and sending

- If the agent is working, your message waits. The bot replies `⏳ Waiting until the agent is idle.` It shows two buttons: `⚡ Send now` and `✖ Cancel`.
- Waiting messages go in the order you sent them. Each one is its own turn.
- A waiting message expires after 30 minutes, and the bot tells you.
- `⚡ Send now` types the message at once. All three agents steer. The message joins the current run.
- Telegram splits long messages into parts. rex-tg joins parts that arrive close together into one prompt. The window is 300 ms, or 1500 ms for long messages.

### Reactions, progress, and answers

- 👀 on your message: rex-tg typed it into the pane.
- 👍 on your message: the agent's transcript recorded it.
- 👎 on your message: it was refused or failed. A reply says why.
- (unverified) Spike S1 has not yet checked on a real Telegram client that Telegram accepts these reactions.
- `typing…` shows while the agent works. It appears in the topic you used last.
- One progress message updates while the agent works. It appears after 2 seconds without an answer. It shows the latest commentary and the recent tool calls.
- When the turn ends, the progress message becomes one summary line. The tool rows stay in a collapsed quote.
- The answer arrives as a new message. Long answers are split into numbered parts, such as `(1/3)`.

### Permission prompts

When an agent asks for permission, the bot posts the bottom of the pane. The post has one button for each answer. Tap a button to send its key to the pane. The buttons go away when the prompt ends. A button for an old prompt is refused.

`/approve` and `/deny` answer the open prompt too.

Questions that allow free text get a `✍ Other…` button. Your next message becomes the answer. This is not tested yet (spike S2).

### Files, photos, and voice notes

- Photos and files are downloaded up to the `max_upload_mb` limit, which is 20 MB by default. Their local path is typed into the pane, with your caption.
- Voice notes are described below.
- Stickers, videos, and other kinds get the reply `Only text is supported for now.`
- Edited messages are not sent to the agent. The bot says so.
- A reply to a bot message sends only your new text.

## Configuration

rex-tg reads `~/.config/rex-tg/config.toml`. The file is optional. A missing file means every setting has its default.

- You only need the lines you change. A partial file works.
- An unknown key is an error. rex-tg names the key.
- A leading `~` is expanded in `rex.cli`, `state.dir`, `voice.whisper_cli`, and `agents.claude_exec_glob`. Use an absolute path for `voice.model`, because `~` is not expanded there.

The example lists every key with its default. The `[voice]` keys and `rich_messages` turn on optional features.

```toml
# ~/.config/rex-tg/config.toml
#
# Every key is shown with its default value. A missing file, or a missing
# key, uses the default. An unknown key is an error.

[telegram]
api_base = "https://api.telegram.org"  # Telegram Bot API address. Keep the default.
rate_per_second = 1                     # Messages per second for the whole chat.
rate_burst = 5                          # Messages that may be sent at once above that rate.

[rex]
cli = "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"  # Path to the rex CLI. A leading ~ is expanded.
scan_interval = "30s"                   # How often rex-tg scans the Rex panes, as a backup to Rex events.

[agents]
pi_argv0 = ["pi"]                       # Process names (argv0) that mean a pi pane.
claude_argv0 = ["claude"]               # Process names (argv0) that mean a Claude Code pane.
claude_exec_glob = ["~/.local/share/claude/versions/*"]  # Claude Code program paths. A leading ~ is expanded.
codex_names = ["codex"]                 # Process names that mean a Codex pane.

[agents.pi]
launch = ["pi"]                         # What /new runs, one word per item, as if typed in a terminal (~/.zshrc applies).
paste_enter_delay = "300ms"             # Wait between the paste and the Enter key.
interrupt_keys = ["escape"]             # Keys that stop the current turn (/interrupt).
while_working = "steer"                 # What Enter does while the agent works: steer, queue, or unknown.
answers = []                            # Fallback permission answers. pi has none.

[agents.claude]
launch = ["claude"]
paste_enter_delay = "300ms"
interrupt_keys = ["escape"]
while_working = "steer"

[[agents.claude.answers]]               # Fallback permission answers, used when the screen shows no numbered options.
label = "1 Yes"
keys = ["1"]

[[agents.claude.answers]]
label = "2 Always"
keys = ["2"]

[[agents.claude.answers]]
label = "4 No"
keys = ["4"]

[[agents.claude.answers]]
label = "Esc"
keys = ["escape"]

[agents.codex]
launch = ["codex"]
paste_enter_delay = "300ms"
interrupt_keys = ["escape"]
while_working = "steer"

[[agents.codex.answers]]
label = "Yes"
keys = ["y"]

[[agents.codex.answers]]
label = "Always"
keys = ["p"]

[[agents.codex.answers]]
label = "No"
keys = ["escape"]

[display]
mode = "progress"                       # progress, block, or final.
backfill = 3                            # Final answers to post when a topic opens.
show_mac_prompts = true                 # Post prompts that you type in the pane on the Mac.
progress_delay = "2s"                   # Wait before the progress message appears.
progress_edit_interval = "2s"           # Least time between two edits of the progress message.
rich_messages = false                   # Send final answers as Telegram rich messages.

[input]
queue_expiry = "30m"                    # A waiting message expires after this time.
join_window = "300ms"                   # Messages that arrive this close together become one prompt.
join_window_long = "1500ms"             # Join window when a message is 4000 characters or longer.
busy_after_typing = "15s"               # After a message is typed, the pane counts as busy for up to this long.
max_upload_mb = 20                      # Largest photo or file to download from Telegram.

[voice]
whisper_cli = ""                        # Path to whisper-cli. Empty turns voice notes off.
model = ""                              # Path to a ggml model file. Use an absolute path.
language = ""                           # Language code for whisper.cpp, such as "en".

[state]
dir = "~/.local/state/rex-tg"           # Database, spool, logs, and uploads. A leading ~ is expanded.
```

## Voice notes (optional)

Voice notes need ffmpeg and whisper.cpp.

1. If you do not have ffmpeg, install it: `brew install ffmpeg`.
2. Install whisper.cpp: `brew install whisper-cpp`. Homebrew names the formula `whisper.cpp`, and `whisper-cpp` is an alias for it. The install adds the `whisper-cli` command.
3. Download a ggml model file from the whisper.cpp project. For example, `ggml-base.en.bin`.
4. Find the command path: `which whisper-cli`.
5. Set the three keys in `[voice]`:

```toml
[voice]
whisper_cli = "/path/from/which/whisper-cli"
model = "/Users/you/models/ggml-base.en.bin"   # use an absolute path; ~ is not expanded here
language = "en"                                # a language code; the default is empty
```

When voice is on, rex-tg converts a voice note to 16 kHz WAV with ffmpeg. Then whisper-cli transcribes it. The bot replies with 🎤 and the text. The text then goes to the agent, like any text you write.

When voice is off, the bot replies: `Voice notes need whisper.cpp; see the README.`

## Rich messages (optional)

Final answers can use Telegram's rich message format. To turn it on, set:

```toml
[display]
rich_messages = true
```

The default is `false`. If Telegram rejects a rich message, rex-tg sends the text as plain text. If the text is longer than 4000 characters, rex-tg sends a `.md` file instead.

## Status, logs, and stopping

- `rex-tg status` shows the topics, the queued messages, and any open alerts. It only reads the database.
- The daemon log is `~/.local/state/rex-tg/logs/rex-tg.log`. It holds one JSON object per line. The file rotates at 10 MB and keeps 3 old copies.
- launchd writes `launchd.out.log` and `launchd.err.log` to the same folder.
- To watch the log, run `tail -f ~/.local/state/rex-tg/logs/rex-tg.log`.
- To stop the daemon, run `launchctl bootout gui/$(id -u)/local.rex-tg`. To start it again, run `rex-tg install`.

## Uninstall

```sh
rex-tg uninstall
```

The command removes:

- the hook entries that rex-tg added to Claude Code and Codex
- the pi extension
- the launchd agent

It keeps the database, the file backups, and the bot token in the Keychain.

Options:

- `--only pi`, `--only claude`, or `--only codex` removes one agent's parts. The launchd agent stays.
- `--keep-launchd` keeps the launchd agent.
- `--force` removes Codex entries even if that moves the trust of other Codex hooks.

**Codex trust rule.** Codex trusts each hook by its position in the list. If another Codex hook comes after a rex-tg entry, removing the rex-tg entry moves that hook. Codex then stops trusting it. So uninstall does not remove the Codex entries. It names the entries that would lose trust. To remove them anyway, run:

```sh
rex-tg uninstall --only codex --force
```

Then start Codex and trust the moved entries again.

To delete the token from the Keychain, run `security delete-generic-password -s rex-tg -a bot-token`. To delete the database and the logs, remove `~/.local/state/rex-tg`.

## Troubleshooting

Start with `rex-tg doctor`.

| Problem | What to do |
| --- | --- |
| Threaded Mode is off | `login` and `doctor` say so. In @BotFather, choose your bot, then turn on Bot Settings > Threaded Mode. Check again with `rex-tg doctor`. |
| Codex does not trust the hooks | Start Codex once, and trust the new rex-tg entries. Codex trusts each entry by its position. The prompt is not tested yet (spike S3). |
| Rex is not running | All topics show as offline. rex-tg retries every 10 seconds. It never starts Rex. Start Rex Beta. If `doctor` cannot find the Rex CLI, set `[rex] cli`. |
| No owner is paired | `doctor` reports it. Run `rex-tg pair`, then send `/start <code>` to the bot. |
| "waiting for session" | rex-tg does not know the session file yet. It learns the file at the agent's next prompt or turn end. |
| "message thread not found" | You deleted the topic in Telegram, so rex-tg closed it. Open the agent again with `/agents`. |
| "This topic is not linked" | The topic is closed, or rex-tg did not create it. Open the agent again with `/agents`. |
| "may not have arrived" | rex-tg never resends on its own. Send the `/resend` command from the notice, if you want the missing parts. |
| "may be in the input box without Enter" | The daemon stopped between the paste and Enter. Check the pane with `/screen`. rex-tg does not type the text again. |
| `/new`: "exited with code 127" | The shell did not find the agent. Set its full path in `[agents.<name>] launch`, for example `launch = ["/opt/homebrew/bin/codex"]`. |
| `/new`: "has not reported its session yet" | The agent may wait for an answer on its screen, such as a folder trust question. Answer it on the Mac, then open the agent with `/agents`. |
| Telegram returns 409 Conflict | Another program uses the same bot token. Stop it. Only one daemon may poll Telegram. |
| Telegram says the bot was blocked | Unblock the bot in Telegram. |
| `doctor` reports unknown line types | The agent changed its transcript format. rex-tg ignores those lines. The adapter may need an update. |
| "Not sent: the pane now runs ..." | The agent is no longer running in the pane. Start it again in the pane, or open another agent with `/agents`. |

## Privacy

- Telegram chats with bots are not end-to-end encrypted. Replies pass through Telegram's servers.
- rex-tg removes common secret formats before it sends text. These include API keys, GitHub and AWS keys, Slack tokens, private key blocks, JWTs, Telegram bot tokens, and Bearer tokens. This removal is best effort. It cannot catch every secret.
- Progress rows show tool names and short labels. They never show tool output. `/verbose full` shows tool output.
- The database keeps the text of messages. rex-tg deletes sent messages after 30 days.
- The state folder and the spool folder can be read only by you.

## Security

- The bot can type into your terminals. Treat your Telegram account and the bot token like an SSH key.
- Turn on two-step verification in Telegram.
- Only the owner can use the bot. The owner is the account that paired. rex-tg ignores other users and other chats.
- Before rex-tg types, it checks that the sender is the owner, the topic is live, the agent is running in front in the pane, and no permission prompt is open. `/force` skips the last check only.
- The bot token is in the macOS Keychain. It is not in the config file or the logs.
- rex-tg makes only outgoing connections, and it opens no port.
- rex-tg never starts a Rex server.

## Documents

| Document | Contents |
| --- | --- |
| [docs/design.md](docs/design.md) | Architecture, flows, Telegram display, SQLite schema, delivery rules, security |
| [docs/plan.md](docs/plan.md) | Decisions, traceability, spikes, phases, rollout, tests, risks |
| [docs/implementation.md](docs/implementation.md) | The contract between the packages, and the CLI commands. For developers. |
| [docs/agent-transcripts.md](docs/agent-transcripts.md) | JSONL formats and hooks of pi, Claude Code, and Codex |
| [docs/spikes/](docs/spikes/) | Spike results. Spike S1 is still pending. |

## Development

- `make build` builds `bin/rex-tg`.
- `make test` runs the tests.
- `make core` runs one daemon test per core flow, in a few seconds.
- `make check` runs gofmt, go vet, staticcheck, and the tests with the race detector.
- `make e2e` runs the end-to-end tests against a real, isolated Rex server.
- `docs/implementation.md` says how the packages fit together. Keep it in step with `docs/design.md`.
