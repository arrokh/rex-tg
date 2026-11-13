# rex-tg

rex-tg lets you use your coding agents from Telegram.

The agents run in Rex panes on your Mac. Each agent session gets its own Telegram topic:

- You write in the topic. rex-tg types your text into the agent's pane.
- The agent works. One progress message shows its tool calls.
- The agent answers. The answer arrives in the topic as a normal message.

Supported agents: pi, Claude Code (also through [cux](https://github.com/inulute/cux)), and Codex, on this Mac.
Not supported: T3 Code panes, and Rex servers on other computers.

**Status:** version 1 works. It is tested with a real bot and phone ([spike S1](docs/spikes/s1-telegram.md)). A few small checks are still open.

## What you need

- macOS (rex-tg uses the Keychain and launchd)
- Rex Beta, with its `rex` command
- pi, Claude Code, or Codex, running in a Rex pane
- Go, to build (the version is in `.tool-versions`; `mise install` gets it)
- A Telegram bot from @BotFather, with Threaded Mode on
- For voice notes only: ffmpeg and whisper.cpp

## Setup

1. **Make the bot.** In @BotFather, send `/newbot` and keep the token. Treat it like an SSH key.
   Then open Bot Settings > Threaded Mode and turn it on.
2. **Install.** With Homebrew:
   ```sh
   brew tap arrokh/rex-tg https://github.com/arrokh/rex-tg
   brew trust --formula arrokh/rex-tg/rex-tg   # Homebrew asks for this for a third-party tap
   brew install rex-tg
   ```
   Or build from source:
   ```sh
   mise install
   make install      # puts the binary in ~/.local/bin/rex-tg
   ```
3. **Store the token.** Run `rex-tg login` and paste the token. It goes into the Keychain.
   rex-tg prints a pairing code. The code works for 10 minutes.
4. **Install the hooks and the daemon.** Run `rex-tg install`.
   This adds rex-tg hooks to Claude Code and Codex, a pi extension, and a launchd job.
   It backs up each file first. Then start Codex once and trust the new hooks.
5. **Pair.** Send `/start <code>` to your bot. Your account becomes the owner.
   The bot answers no one else. If the code expired, run `rex-tg pair`.
6. **Check.** Run `rex-tg doctor`. `✓` is good, `!` is a warning, `✗` is a failure.

Details of each step: [docs/reference.md](docs/reference.md#setup-details).

## Daily use

With Threaded Mode, every message is in a topic. A topic that is not linked to an agent works as the main chat.

### Main chat

| Command | What it does |
| --- | --- |
| `/agents` | Lists the agents in Rex panes, the ones that wait for you first. The list updates by itself; pin it. Tap an agent to open its topic. |
| `/new` | Starts an agent in a new Rex tab. Pick the agent and a folder. `/new pi ~/work/app` does it in one step. |
| `/topics` | Lists the topics and their states. |
| `/status` | Shows the daemon's health. |
| `/pause`, `/unpause` | Stops or allows all typing into panes. Use `/pause` if you lose your phone. |
| `/help` | Lists the commands. |

### In a topic

Your text goes to the agent. Text that starts with `/` also goes to the agent (for example `/compact`), except these rex-tg commands:

| Command | What it does |
| --- | --- |
| `/info` | Shows the agent's state. |
| `/screen [lines]` | Shows the pane's screen, with secrets removed. |
| `/approve`, `/deny` | Answers the open permission prompt. |
| `/esc`, `/interrupt`, `/key <keys>` | Sends Escape, stops the turn, or presses keys. |
| `/force <text>` | Types text even when a prompt is open. |
| `/model [name]` | Shows the agent's model with buttons, or changes it. |
| `/effort [level]` | Shows the thinking effort with buttons, or changes it. |
| `/mode progress\|block\|final` | Sets how replies appear. |
| `/verbose on\|full\|off` | Shows each tool call (`on`), and also its output (`full`). |
| `/reasoning on\|off` | Shows the agent's thinking, when the agent stores it. |
| `/resend [id]` | Sends again a reply that may not have arrived. |
| `/mute`, `/unmute` | Answers arrive without or with sound. |
| `/unlink` | Closes the topic in rex-tg. |

To send an agent a command that has a bot command's name, use two slashes: `//help` sends `/help` to the agent, and `//model` sends the agent's own `/model`.

### What you see

- **⏳ Waiting:** the agent is busy, so your message waits. It is typed when the agent is idle. `⚡ Send now` types it at once. `✖ Cancel` drops it. A waiting message expires after 30 minutes.
- **Reactions on your message:** 👀 means typed into the pane. 👍 means the agent got it. 👎 means refused; a reply says why.
- **Progress:** one message updates while the agent works. When the turn ends, it becomes one summary line.
- **Answer:** a new message. A long answer comes as a few chat-sized messages, split at paragraphs, without numbers.
- **Live text (optional):** with `stream = true`, the text shows as a draft at the bottom of the topic while the agent writes (word by word for pi, block by block for Claude Code and Codex). Off by default.
- **Files:** when an answer names an image on your Mac, rex-tg sends the picture too. A PDF, CSV, HTML, or Office file gets a 📎 button: tap it to get the file.
- **Permission prompts:** the bot shows the bottom of the pane, with one button per answer.
- **⏰ Still waiting for your answer:** a prompt has waited 10 minutes. Tap the reminder to jump to the prompt.
- **💻 In use on the Mac:** you typed in the pane on the Mac. The replies stay on the Mac, and this one notice shows the latest prompt. `📄 Last answer` shows the newest answer. Write in the topic to go on in Telegram.
- **🔴:** the agent stopped, or its pane closed.

### Photos, files, and voice

- Photos and files (up to 20 MB) are saved on the Mac. rex-tg types their path and your caption into the pane.
- Voice notes are turned into text, when voice is set up ([how](docs/reference.md#voice-notes)).
- Stickers and videos are not supported. Edited messages are not sent again.

## Settings

Settings go in `~/.config/rex-tg/config.toml`. The file is optional. Write only the keys you change.
All keys and their defaults: [docs/reference.md](docs/reference.md#configuration).

Settings that people change most:

```toml
[telegram]
proxy = "socks5h://127.0.0.1:40000"  # when your network blocks or drops Telegram

[display]
mode = "progress"         # progress, block, or final
mac_notice = true         # one notice while you work on the Mac
send_images = true        # send images that an answer names
stream = false            # true: show the text while the agent writes
```

## Logs, stop, and uninstall

- `rex-tg status` shows topics, queued messages, and alerts.
- The log is `~/.local/state/rex-tg/logs/rex-tg.log`.
- Update: `brew upgrade rex-tg` (or `git pull && make install`). The daemon restarts itself within a minute, when no message is being typed. Topics stay linked to their sessions.
- Restart the daemon: `launchctl kickstart -k gui/$(id -u)/local.rex-tg`.
- Stop the daemon: `launchctl bootout gui/$(id -u)/local.rex-tg`. Start it again with `rex-tg install`.
- Remove rex-tg: `rex-tg uninstall`. It keeps the database and the token. See [Codex notes](docs/reference.md#uninstall).

## Problems

Start with `rex-tg doctor`. Common cases:

| Problem | What to do |
| --- | --- |
| Replies are slow, or come in bursts | Your network may drop Telegram. Set `[telegram] proxy` ([more](docs/reference.md#slow-or-unreliable-network)). |
| "may not have arrived" | Tap `🔁 Send again`, or send `/resend`, if the reply is missing. |
| "This topic is not linked" | Open the agent again with `/agents`. |
| Telegram returns 409 Conflict | Another program uses the same bot token. Stop it. |

More cases: [docs/reference.md](docs/reference.md#troubleshooting).

## Safety

- The bot can type into your terminals. Protect your Telegram account (turn on two-step verification) and the bot token.
- Only the owner can use the bot. rex-tg types only when the agent is in front in its pane and no prompt is open.
- The token is only in the macOS Keychain. rex-tg opens no port.
- Telegram bot chats are not end-to-end encrypted. rex-tg removes common secrets (API keys, tokens, private keys) before it sends text, but it cannot catch every secret.
- Sent messages are deleted from the database after 30 days.

## Documents

| Document | Contents |
| --- | --- |
| [docs/reference.md](docs/reference.md) | Setup details, all settings, voice notes, uninstall, troubleshooting |
| [docs/design.md](docs/design.md) | How rex-tg works: flows, display, database, delivery rules, security |
| [docs/plan.md](docs/plan.md) | Decisions, spikes, phases, tests, risks |
| [docs/implementation.md](docs/implementation.md) | Package contracts and CLI commands, for developers |
| [docs/agent-transcripts.md](docs/agent-transcripts.md) | Transcript formats and hooks of pi, Claude Code, and Codex |
| [docs/spikes/](docs/spikes/) | Results of the live tests |

## Development

See [AGENTS.md](AGENTS.md). In short: `make check` before a commit, and `make e2e` when the daemon, Rex, or an agent changes.
