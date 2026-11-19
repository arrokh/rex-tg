---
name: rex-tg
description: Use when the user asks about rex-tg's Telegram bridge, wants to start or supervise an agent in Rex, or this agent is connected to a linked topic.
---
<!-- rex-tg:managed-skill -->

# Use rex-tg

rex-tg connects coding agents in Rex terminal panes to topics in the owner's Telegram bot chat. A linked topic carries prompts to the agent and mirrors the agent's replies. The normal response in this session is already delivered there; do not create a second copy through another bot or API.

## Choose the right control surface

- Do the requested coding work in the current session. rex-tg manages the connection; it does not replace the agent's tools, permissions, or approval checks.
- To inspect local topic queues and connection state, run `rex-tg status`; add `--all` to include closed topics. Use `rex-tg doctor` for setup diagnostics when the user asks to diagnose the bridge.
- To start another agent, run `rex-tg new pi`, `rex-tg new claude`, `rex-tg new codex`, or `rex-tg new cux` from the requested project folder. This opens a new Rex tab. Do this only when the user asks to start another agent. `cux` runs Claude Code and must already be installed. If creation is unconfirmed, inspect Rex before trying again; it may have started already.
- The user opens sessions from Telegram with `/agents`, then taps an agent. `/new` starts an agent through Telegram. There is no local `rex-tg` command that opens a Telegram topic.

## Telegram topic commands

In a linked topic, the bot handles its documented commands such as `/info`, `/screen`, `/interrupt`, `/model`, and `/effort`. To send a slash command to the coding agent, the user prefixes it with two slashes: `//compact` sends `/compact`. Do not mistake a bot command for an agent command.

## Keep delivery safe

Treat each requested message or input as at-most-once. If delivery is uncertain, stop and check the Telegram topic; inspect the pane through available Rex tools or ask the user. Never repeat a message or pane input on a guess; retry only when evidence proves the first attempt did not arrive. The user can use `/resend` after checking the topic.

The bot token stays in the macOS Keychain. Never ask the user to paste it into chat, read it from a file, or put it in arguments, logs, tests, or this skill. Do not use Telegram credentials or APIs to send a duplicate response.
