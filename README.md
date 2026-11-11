# rex-tg

rex-tg connects coding agents that run in [Rex](https://www.superlogical.com/rex/docs) panes to Telegram.

- In the main chat, `/agents` lists the panes that run pi, Claude Code, or Codex.
- When you pick one, the bot opens a Telegram topic for that agent session. A new session in the same pane (`/clear`, `/new`) keeps the same topic, and so does a Rex restart.
- While the agent works, one live progress message shows its tool calls, in the style of OpenClaw and Hermes Agent. The answer then arrives as a normal message.
- All of this comes from the agent's own session JSONL file.
- What you write in the topic waits until the agent is idle, and is then typed into the pane.
- SQLite makes sure no reply is posted twice.

**Status:** design phase. No code yet. The implementation will be in Go.

| Document | Contents |
| --- | --- |
| [docs/design.md](docs/design.md) | Architecture, flows, Telegram display, SQLite schema, delivery rules, security |
| [docs/plan.md](docs/plan.md) | Decisions, open questions, spikes, phases, tests, risks |
| [docs/agent-transcripts.md](docs/agent-transcripts.md) | JSONL formats and hooks of pi, Claude Code, and Codex |

## Privacy

Telegram chats with bots are not end-to-end encrypted. Agent replies pass through Telegram's servers. rex-tg removes common secret formats before it sends text, but it cannot catch every secret.
