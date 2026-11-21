# Quick start

[README](../README.md) · [User reference](reference.md) · [Troubleshooting](reference.md#troubleshooting)

Set up one agent and send your first prompt from Telegram. You do not need Tailscale for this guide.

## 1. Check the prerequisites

- **A Mac with [Rex Beta](https://www.superlogical.com/rex/docs/installation).** You need access to Rex separately. Open Rex and run `rex whoami` in a Rex pane to confirm the CLI can reach it. If your usual shell cannot find `rex`, see [Rex CLI setup](https://www.superlogical.com/rex/docs/automate/cli) or set `[rex] cli` in the [rex-tg config](reference.md#configuration).
- **One working coding agent.** Install and sign in to pi, Claude Code, or Codex separately. Confirm it can answer a prompt on the Mac. cux is also supported as a launcher for Claude Code.
- **A Telegram account and [Homebrew](https://brew.sh/).** If you do not use Homebrew, [build from source](reference.md#build-from-source).

> Pair only a Telegram account you trust to control your terminals. Bot chats are **not end-to-end encrypted**, and linking a session can post recent answers to Telegram. Start in a scratch folder with no sensitive data. See [Security](../SECURITY.md).

## 2. Install the CLI

On the Mac:

```sh
brew tap arrokh/rex-tg https://github.com/arrokh/rex-tg
# If Homebrew requires trust for the tap's formula:
# brew trust --formula arrokh/rex-tg/rex-tg
brew install arrokh/rex-tg/rex-tg
rex-tg version
```

## 3. Create your bot

In Telegram, open [@BotFather](https://t.me/BotFather), run `/newbot`, and follow the prompts. For that bot, enable **Bot Settings → Threaded Mode**. Keep the bot token private.

## 4. Start the bridge and pair your account

```sh
rex-tg login
rex-tg install
```

Paste the token only into the local prompt from `login`. It stores the token in the macOS Keychain and prints a one-time code. `install` adds the [agent integrations and skills](reference.md#agent-integrations-skills-and-daemon-rex-tg-install) and starts the background service with launchd. You do not need to keep that terminal open.

Send `/start <code>` to **the bot you created**, not to BotFather. The code expires after 10 minutes. If it expires, run `rex-tg pair` for a new one. Then run `rex-tg doctor` on your Mac. Fix any `✗` checks relevant to your setup before moving on.

## 5. Get your first reply

1. Start a **new agent session** in a Rex pane in a scratch folder. Existing sessions need to restart to load the integrations. For Codex, review and trust the new rex-tg hook entries when prompted.
2. In your bot chat, send `/agents`. Pick the agent and open its linked topic. If it says **waiting for session**, send a prompt on the Mac and try `/agents` again.
3. In the **linked topic**, send `Reply with: rex-tg is connected. Do not run tools.` You should see a reaction when rex-tg types the prompt and the agent's reply in the same topic. Text in an unlinked topic does not go to an agent.

## Daily use

| I want to… | How |
| --- | --- |
| Find or reconnect to an agent | `/agents` in Telegram. To link a session to the current unlinked topic, use `/attach`. |
| Start an agent remotely | `/new` in Telegram. Pick a runtime and a folder on your Mac. |
| Start an agent locally | From its project folder, run `rex-tg new pi`, `rex-tg new claude`, or `rex-tg new codex`. Then open it with `/agents`. |
| See what an agent is doing | `/info` for session details, `/screen` for the pane, `/interrupt` to interrupt its current turn. |
| Stop remote typing for a while | `/pause`; use `/unpause` to resume. Outgoing replies continue. |
| Send an agent's slash command | Send `//help` to pass `/help` to the agent instead of opening the bot's help. |
| Schedule a prompt | `/at 30m check the build` in a linked topic. |

In a linked topic, 👀 means typed, 👍 means echoed or submitted as an agent command, and 👎 means refused or uncertain. Read the accompanying notice. If something **may not have arrived**, check Telegram or the pane before trying again. See [waiting, receipts, and recovery](reference.md#waiting-and-sending) and the [full command list](architecture.md#telegram-commands).

## Next steps

- [Configure the bridge](reference.md#configuration), or [restart, update, or remove it](reference.md#run-stop-and-update).
- Add the optional [Mini App](reference.md#mini-app) or [local voice transcription](reference.md#voice-notes).
- Read [what rex-tg stores and sends](reference.md#privacy) before linking an active project. By default, opening a topic can post the last three final answers. To disable this, set `backfill = 0` under `[display]` in the config and restart the daemon before linking.
