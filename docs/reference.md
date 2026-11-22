# rex-tg reference

New here? Follow the [quick start](quickstart.md) to install and send a test prompt. This guide covers setup options, daily use, and service operations. For every CLI command, Telegram command, and button, see the [command catalog](architecture.md#feature-and-command-catalog). [Back to README](../README.md).

## On this page

[Setup](#setup-details) · [Using rex-tg](#using-rex-tg) · [All settings](#configuration) · [Mini App](#mini-app) · [Voice](#voice-notes) · [Run, stop, and update](#run-stop-and-update) · [Logs](#logs) · [Uninstall](#uninstall) · [Troubleshooting](#troubleshooting) · [Privacy](#privacy)

## Setup details

### Install with Homebrew

```sh
brew tap arrokh/rex-tg https://github.com/arrokh/rex-tg
# If Homebrew requires trust for the tap's formula:
# brew trust --formula arrokh/rex-tg/rex-tg
brew install arrokh/rex-tg/rex-tg           # released version pinned by the formula
# Or build the latest main instead:
# brew install --HEAD arrokh/rex-tg/rex-tg
```

Homebrew builds from source, so it needs Go (it installs it as a build tool).

Homebrew clones the pinned Git tag from the public repository. No GitHub token is needed to install from this tap.

`rex-tg install` writes the stable path `<prefix>/opt/rex-tg/bin/rex-tg`, not the versioned Cellar path. That path stays valid after an upgrade.

For upgrades, see [Run, stop, and update](#run-stop-and-update). Add `--fetch-HEAD` to `brew upgrade` if you installed HEAD.

If you installed with `make install` before, move to the brew binary in two steps:

1. Run `"$(brew --prefix)/bin/rex-tg" install`. The hooks, pi extension, and launchd then name the brew binary; the skills are installed alongside them.
2. Replace the old copy with a link: `ln -sf "$(brew --prefix)/opt/rex-tg/bin/rex-tg" ~/.local/bin/rex-tg`. Do not just delete it: agents that run already read their hook settings when they started, and keep calling the old path until they restart.

`rex-tg doctor` warns while a second copy is left.

To remove: run `rex-tg uninstall` first, then `brew uninstall rex-tg`.

### Build from source

Install [mise](https://mise.jdx.dev/) and make, then run:

```sh
git clone https://github.com/arrokh/rex-tg.git
cd rex-tg
mise install
mise exec -- make install
```

This builds `main` and places the binary in `~/.local/bin`. Add that directory to `PATH` if needed. This step does not install integrations or start the daemon. Continue with [Create your bot](quickstart.md#3-create-your-bot) in the quick start. Do not run `make install` over a symlink that points to a Homebrew binary.

### Token and pairing code (`rex-tg login`)

1. macOS `security` asks for the token. Paste it. It is not shown.
2. rex-tg checks the token with Telegram, and stores it in the Keychain.
3. It tells you if Threaded Mode is off.
4. It prints a pairing code. The code works for 10 minutes.

`rex-tg login --token-stdin` reads the token from one line of standard input. Use it in scripts.

If the shell cannot find `rex-tg`, add `~/.local/bin` to your `PATH`. The hooks and the launchd job use the full path, so they do not need `PATH`.

### Agent integrations, skills, and daemon (`rex-tg install`)

The command changes these files:

- **Claude Code:** adds six hook entries to `~/.claude/settings.json`: `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, `Notification`, and `SessionEnd`.
- **Codex:** adds four hook entries to the end of each event list in `~/.codex/hooks.json`: `SessionStart`, `UserPromptSubmit`, `Stop`, and `PermissionRequest`.
- **pi:** writes the extension `~/.pi/agent/extensions/rex-tg.ts`. It sends `session_start`, `session_shutdown`, `agent_start`, and `agent_end` to rex-tg.
- **Claude Code:** installs the personal skill `~/.claude/skills/rex-tg/SKILL.md`.
- **Codex and Pi:** install one shared skill at `~/.agents/skills/rex-tg/SKILL.md`.
- **launchd:** writes `~/Library/LaunchAgents/local.rex-tg.plist` and starts the daemon. launchd starts it at login and restarts it on failure.

Before it changes `settings.json` or `hooks.json`, rex-tg saves a backup next to the file. It keeps the 5 newest backups. Skill files are marked as rex-tg-owned; replacing one or removing it saves a backup too. If a skill file already exists at an install path and is not marked as rex-tg's, install leaves it alone and reports a conflict. Move that file before running install again.

`--only claude` installs Claude Code's hooks and skill. `--only codex` or `--only pi` installs that agent's integration and the shared Codex/Pi skill. The shared skill stays installed while either Codex hooks or the Pi extension remains. The launchd job is still installed.

After the install, restart agent sessions so they discover the skill and integration. Start Codex once and trust the new hook entries.

### Pairing (`/start <code>`)

Send the code after `rex-tg install`. The daemon reads the code only while it runs.

- The first valid code makes your account the owner.
- The bot answers no one until pairing is done.
- A code works once, for 10 minutes. Run `rex-tg pair` for a new code.
- After 5 wrong codes from one Telegram account, that account cannot pair with this code. After 200 wrong attempts across all accounts, pairing locks for everyone. Run `rex-tg pair` to create a new code and clear both counts.

### Doctor (`rex-tg doctor`)

Doctor checks:

- the Rex CLI, and `rex whoami`
- the bot token, `getMe`, and Threaded Mode
- the database, and whether an owner is paired
- the Claude Code and Codex hooks, the pi extension, and both skills
- the launchd job, and the folder modes
- whether a daemon is running
- the Mini App, when `[miniapp] url` is set: safe settings (`listen` on `127.0.0.1` with `tailnet_only`), that `tailscale serve` sends the address to rex-tg with Funnel off, and that the page answers. It only reads Tailscale's setup.

## Using rex-tg

### Start from a terminal

In a project folder, run `rex-tg new pi`, `rex-tg new claude`, `rex-tg new codex`, or `rex-tg new cux`. The command starts the agent in the current folder, in a tab under `[rex] new_session` (default `rex-tg`). `cux` must be installed. Arguments after the agent name are passed to it unchanged; for example, `rex-tg new cux --resume <session>` passes `--resume <session>` to cux. The daemon discovers the agent through its hook; use `/agents` in Telegram to open its topic. `rex-tg new --config FILE <agent> [agent args...]` uses another config file. If Rex does not confirm that it made the tab, check the session before running the command again; Rex may have started it anyway.

### Commands in a topic

See the [active Telegram command catalog](architecture.md#telegram-commands) for the full list, including commands not shown in Telegram's slash menu. In a linked topic, unrecognized slash commands go to the agent. Use `//name` to send `/name` directly to the agent when the bot would otherwise handle it. Agent commands wait until the agent is idle; 👍 confirms typing, not successful execution.

Common commands and settings:

| Command | What it does |
| --- | --- |
| `/info` | Shows the session, the pane, the state, and the settings. |
| `/screen [lines]`, `/screen text [lines]` | Sends the pane's screen as a picture, or as text. See [`/screen`](#screen). |
| `/files`, `/changes` | Opens the read-only file browser or available git comparisons in the Mini App. |
| `/approve`, `/deny` | Selects a recognized answer to an open permission prompt. |
| `/esc`, `/interrupt`, `/key <keys>` | Presses Escape, stops the turn, or presses keys. |
| `/force <text>` | Bypasses readiness and prompt waiting; owner, live-target, process, and pause checks remain. |
| `/model [name]`, `/effort [level]` | Shows or changes the model and the thinking effort. See [`/model` and `/effort`](#model-and-effort). |
| `/mode progress\|block\|final` | Sets how replies appear. |
| `/verbose on\|full\|off` | Shows each tool call (`on`), and also its output (`full`). |
| `/reasoning on\|off` | Shows the agent's thinking, when the agent stores it. |
| `/resend [id]` | Sends again a reply that may not have arrived. |
| `/mute`, `/unmute` | Answers arrive without or with sound. |
| `/at <time or delay> <text>`, `/at`, `/at cancel <n>` | Sends text to the agent later, lists the schedules, or removes one. See [`/at`](#at). |
| `/rename <name>`, `/rename auto` | Renames the topic, its Rex pane, and its Rex tab. `/rename auto` names them after the session again. |
| `/unlink` | Closes the topic in rex-tg. |
| `/attach` | Lists resolved Rex agents with no open topic. Choose one to attach it to this existing Telegram topic. A live topic must be unlinked first. |
| `/annotate` | Says that ✍️ Annotate opens from its button under the message box. |
| `/rekey` | Makes a new key for the buttons under the message box; the old buttons stop working. |
| `/help` | Lists the commands and brings the buttons back. |

### Buttons under the message box

rex-tg adds a keyboard under the message box: 🎛 Panel, 🖥 Screen, 📁 Project, ⚙️ Config, ✍️ Annotate, 🧩 Templates, and 📥 Queue. Telegram shows it in place of your normal keyboard; tap ⊞ in the message box to switch between the two.

- With the Mini App on, the page buttons open at once: the control panel, live screen, Project, Config, [Annotate](#annotate), or [Templates](#templates). Project groups the file browser and Git changes; `/files` and `/changes` still open their individual views. Config applies only to the session in the linked topic where you open it.
- Telegram gives a page that a keyboard button opens no signed login data. So each button's address carries a key that rex-tg made (64 random characters, kept in rex-tg's store). The key opens pages and authorizes validated actions in the control panel, including rename, unlink, and starting an agent; you do not need to open `/rt` first. Annotate sends also use the key, but only from the page's bound topic/session and with your verified Tailscale login. Actions are checked and claimed once. The key cannot pause or unpause typing. A signed panel opened with Telegram init data still needs data from the last hour to act. A page opened with an old key says "This button is old"; `/rekey` revokes the old key.
- `/rekey` makes a new key at once. The old buttons stop working, and the main chat and every open topic get new buttons in a short silent note. Use it when a button's address may have leaked.
- Without the Mini App, the buttons send their labels as text. Project and Config explain which existing commands to use. The other buttons act like `/rt`, `/screen`, `/annotate`, or `/templates`. A button never goes to the agent as a prompt.
- Only a new message can carry the keyboard. rex-tg sends it with the pairing answer, with the first message of each new topic, and with every `/help` answer.
- On the first start after an update that changes the buttons, after a change of `[miniapp] url`, and after `/rekey`, rex-tg posts one short silent note to the main chat and to every open topic. The note carries the keyboard, so a topic that existed before shows the new buttons without `/help`. A closed topic gets no note. Each topic gets each note at most once, also after a restart.
- It is not yet known whether each Telegram app keeps one keyboard for the whole chat or one per topic. So every open topic gets its own note, with its own topic in the buttons' addresses. If a button opens the wrong topic, send `/help` in the topic you are in.

### Templates

🧩 Templates stores up to 50 named message templates in this device's Mini App storage. They do not sync across devices. Create, edit, or delete a template; sending uses its exact message text, not its name. Sending is tied to the topic and agent session shown by the page, uses the normal queued-message checks, waits until the agent is ready, and follows `/pause`. The topic gets a copy after rex-tg queues the message. Keyboard launches require your verified Tailscale login; signed launches require fresh Telegram data. An uncertain send keeps the same action ID for retry, so a retry does not queue the message twice. If the result stays unknown, check the topic before confirming a new send. A confirmed send closes the Mini App.

### Annotate

✍️ Annotate opens the answers of the agent's current session in the topic, newest first. Scroll down for older ones. Answers from other topics, and from the topic's earlier sessions, never show. The page's title names the topic and the session, unless it repeats the opening words of the first answer. Saving a note keeps your reading position.

- Tap a paragraph, a list item, a heading, a table row, or a line of code to add a note to it. Or select part of the text and tap `✍️ Note on selection`. Notes are marked with numbers; tap one to change or delete it.
- The note editor has quick-fill buttons for `Yes`, `No`, and an existing comment. The existing-comment button restores the saved comment on this note, or reuses the latest saved comment when adding a new one. These buttons only fill the editor; tap `Save`, then use `Review` to send.
- `Review` lists the notes. You can add a message to the agent. Then choose how to send:
  - `Send to agent`: rex-tg sends the notes to the agent as one queued message. It waits until the agent is idle and follows `/pause`; the topic gets a copy (`✍️ Notes sent to the agent`). Keyboard launches require your verified Tailscale login; signed launches require fresh Telegram data. The action stays on the page's topic and session. If the agent started a new session meanwhile, or the page belongs to another topic, the notes are refused. After a successful send, the notes disappear from this page and the Mini App closes. An uncertain send keeps the notes and action ID, and leaves the Mini App open; retries use the same ID. After ten minutes, the page asks you to check the topic before starting another send.
  - `Copy`: copies the notes. Paste them in the topic yourself.
  - `Put in message box (beta)`: opens the bot's chat with the notes in the message box. Telegram may open the main chat instead of the topic.
- The message quotes each part you noted, with your note under it.
- `📄 Whole answer` pages work the same way: tap or select parts of the long answer, add notes, and send them. There, `Send to agent` goes straight from the page, signed by Telegram (open the page again after an hour). It sends once, also if you tap twice. A whole answer from before this version does not know its topic; use `Copy` there.
- Notes that you have not sent stay on the phone, also when you close the page. Once sent, they are removed from this page, the Mini App closes, and no restore copy is kept. If the result is uncertain, rex-tg keeps the notes and same action ID. After ten minutes, it asks you to check the topic before starting another send.

### Agents and topics

- `/agents` lists the agents: the ones that wait for you first, then working, then idle, each with how long it has been in that state, such as `waiting for you · 12m`. `💻 on the Mac` marks a session in use on the Mac.
- The newest `/agents` list updates by itself when an agent's state changes. Pin it to see all agents at a glance. Older lists do not change.
- Tap an agent in the list: if the session already has a topic, you get a link to it. If you deleted that topic, a new one is made for the same session.
- In an unlinked, offline, or locally closed Telegram topic, `/attach` lists resolved Rex sessions that have no open topic. Tap one to link it to this same Telegram topic; rex-tg posts a link card and backfills recent answers. It does not create a new Telegram topic or type into the agent. A live topic must be unlinked first. An agent already serving another open topic is not offered. A closed topic's unsent old-session output is discarded; if a message is already in flight, wait for it to finish and try again. The link card carries the reply keyboard, and the topic uses the configured `[display] mode`.
- An agent marked `(guess)` has no hook report yet. rex-tg took the newest transcript in the agent's folder that changed after the agent started.
- An agent marked `(waiting for session)` has no known session file yet. rex-tg learns it at the agent's next prompt or turn end.
- A topic is named after the name you gave its Rex tab (see below), else its Rex pane when you named the pane, else the agent's session name. The name follows changes of all three.
- `/rename <name>` in a topic renames three things: the topic, the agent's Rex pane (`rex block rename`), and its Rex tab. A tab that also holds another agent's pane keeps its name. The tab of an agent that `/new` started follows the topic's name, with ` #2` when another tab of that session has the name. The reply says what was renamed, and what was not and why. `/rename` always sends the name to Telegram, even when rex-tg thinks the topic has it, and the reply says what Telegram answered (renamed, already had that name, refused, or no answer). So sending `/rename` again fixes a topic whose name did not change. In this private chat, Telegram keeps a new topic name but sends the Telegram apps no notice of it (a Telegram bug, [tdlib/telegram-bot-api#915](https://github.com/tdlib/telegram-bot-api/issues/915)). So an app that already shows the topic may keep the old name until it loads its topics again, and Telegram for Android keeps it until its local data is cleared. The reply says so; no bot call fixes it. The name sticks; `/rename auto` names the topic after the agent's session again (so a topic cannot be named "auto"): the pane gets Rex's default name `shell` back, and the tab takes the topic's new name. Both forms replace a pane or tab name that you set yourself in Rex. `//rename` sends `/rename` to the agent, such as Claude Code's own command. An agent cannot rename its topic: asked to, it may rename its Rex session instead, and rex-tg names the session for `/new` back to `rex-tg`.
- A name starts with a letter or digit, because Telegram draws a topic's icon from its first character. Legacy stopped-topic names (🔴) get the same cleanup. pi's `[folder][kind]` tags before a session name go: the folder tag is dropped, and other tags move to the end. Markdown marks (`**`, `` ` ``) and leading symbols go too.
- A new session in the same pane, such as after the agent's `/clear`, takes over the topic. The bot posts a divider, such as `── claude · new session (clear) ──`.
- After a Rex restart, the topic follows the session when the agent resumes in its pane, if the topic is still open.
- When an agent exits or its pane closes, rex-tg posts `🔴 agent stopped`, finishes any running turn and in-flight paste, then tries to close the Telegram topic after queued answers and notices. The topic and its history stay in Telegram. rex-tg detaches the local link; it does not stop or close the Rex pane. If the session resumes, `/agents` lists it without a topic. Open it there to create a new topic with recent answers backfilled. A Telegram close error is logged; rex-tg still detaches the ended session. Retryable or uncertain close failures are retried on the next daemon start. A permanent Bad Request is not retried because Telegram will reject the same close again.
- When you open a topic, the bot posts an intro card (session, pane, folder, mode) and the last 3 final answers (`backfill`).

### `/new`

- Tap the agent, then tap a folder or send a path such as `~/work/app`. cux is one of the agents when it is installed.
- The folder buttons, at most 6: the folders in `[rex] new_folders` first, in their order, then the folder of this topic's agent, then the folders where agents ran recently, newest first. A folder that no longer exists is left out, and so is a recent folder that you removed in the control panel ([`/rt`](#rt-control-panel)), until an agent starts a new session there.
- Every tab started by `/new` or `rex-tg new` goes into one Rex session, `rex-tg` (`[rex] new_session`). rex-tg makes the session when it is missing. If an agent in it renames the session, rex-tg tries to name it back at the next 30-second name check, no more than once every 10 minutes.
- When the agent reports its session, rex-tg links its topic and queues the intro card before it posts the “Opened” link. The new topic is ready when you open it.
- A tab is named after its folder and agent, such as `app · pi`. The tab follows its topic name: at link time, and at the 30-second name check when its label differs. When you (or an agent, with `rex window rename`) rename the tab, rex-tg keeps that name, and the topic takes it at the next name check. Rename the tab back to its `<folder> · <agent>` form, such as `app · pi`, and rex-tg names the tab and the topic again. When the topic has only the folder name, the tab keeps the `<folder> · <agent>` form. Two tabs with one name get ` #2`, ` #3`. Tabs in other Rex sessions are never renamed.
- The agent starts as if you typed its name in a terminal, so your `~/.zshrc` applies.
- If the agent asks something before it starts, such as "trust this folder?", the question comes to the chat with a button per answer. A button moves the cursor there and presses Enter. rex-tg checks the screen after each key and verifies that the same agent process still owns the question.
- In a topic, `/new` also starts another agent. `//new` sends `/new` to the agent.

### Waiting and sending

- A message you send while the agent works joins the current run at once, as typing on the Mac does. Claude Code reads it after its current tool. Set `[input] while_busy = "wait"` to wait until the agent is idle instead.
- A message still waits while a prompt is open, or for a moment right after another message was typed. Agent commands (`//compact`, `/model` buttons), shell commands (`!ls`), and the note of a prompt answer always wait until the agent is idle.
- When you answer a prompt, on the Mac or in Telegram, the waiting messages go in as soon as rex-tg sees the prompt gone. Claude Code and Codex send no signal for an answer, so rex-tg reads the screen: when the agent's transcript goes on, and every 3 seconds while the pane waits (every 15 seconds after the first minute). Key hints such as `Esc to cancel` at the bottom of the screen mean a prompt is still there.
- While the agent works, rex-tg reads the screen right before it types. If a prompt is on the screen, it waits instead of typing into it.
- Tap **📥 Queue** below the message box (or send `/queue`) in a linked topic to see what is waiting. You do not need to scroll back to the earlier notice.
- Messages that wait share one notice, on the oldest one, after it waited 3 seconds. It says what they wait for and how many wait, such as `⏳ 2 messages wait until the prompt is answered.`, with a cancel action. Send is offered only for the eligible messages at the front of the queue. The button says **Send all now** when all are eligible, or shows a smaller send action when only the first messages are eligible. `⚡ Send all now` sends your steerable messages, starting at the front of the queue and stopping at the first agent command, shell command, or prompt note: that one still waits until the agent is idle, and so does what you sent after it, so the order stays. The notice goes away when the message is typed (👀, then 👍) or refused (👎).
- Waiting messages go in the order you sent them. Each one is its own turn.
- `⚡ Send now` types waiting text at once. Commands and prompt notes still wait for idle. All three agents steer: the message joins the current run.
- Telegram splits long messages into parts. rex-tg joins parts that arrive close together into one prompt. The window is 300 ms, or 1500 ms for long messages.
- A reply tells the agent what you reply to. Your text comes first, then `In reply to:` and the replied message as a quote (`> …`), cut at 600 characters. If you quote part of a message in Telegram, only that part is sent. A rich-message answer reaches rex-tg without its text in a reply, so rex-tg quotes the text it sent. A reply to an answer that came as a `.md` file, or to a code file, quotes its start too; a reply to an image quotes nothing.
- This works for replies to the agent's messages (answers, tool lines, prompts typed on the Mac) and to your own messages. A reply to one of rex-tg's own messages (notices, alerts, cards) sends only your text. So do an agent command such as `//compact` and a shell command such as `!ls`.

### `/model` and `/effort`

In a linked topic, **⚙️ Config** opens a Mini App for changing the model and thinking effort of that session. Choose a model from a searchable list: Pi reports available models from its in-session registry, Codex uses its model cache, and Claude Code (including CUX) uses Claude’s model aliases. Config rejects models outside the list. A model list set under `[agents.<name>.choices]` overrides discovery. If no list is available, Config leaves model selection unavailable. For Pi, rerun `rex-tg install --only pi` after upgrading and restart the Pi session to load the updated extension. Config uses the same queue as the commands below. It does not change defaults for new sessions. If the topic switches to another session, reopen Config before making a change.

- `/model` and `/effort` with no value show the current value and buttons. `/model opus` or `/effort high` set a value at once. Use `/model <name>` for a model that has no button.
- rex-tg types the agent's own command, such as `/model opus`, like a message. It waits until the agent is idle, and every safety check applies. A button press gets no 👀 on your message, but the bot says "⚙️ Sending …". The agent writes no prompt for a slash command, so the 👍 comes as soon as it is typed. This holds for every slash command you send to the agent, such as `/restart`.
- The commands by agent:

| Agent | `/model <v>` types | `/effort <v>` types | Values of `/effort` |
| --- | --- | --- | --- |
| Claude Code | `/model <v>` | `/effort <v>` | `low`, `medium`, `high`, `xhigh`, `max` |
| Codex | `/model <v> --reasoning=<effort>` | `/model <model> --reasoning=<v>` | `low`, `medium`, `high`, `xhigh`, `max` |
| pi | `/model <v>` | `/thinking <v>` | `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |

- A model name may use letters, digits, and `. _ : / [ ] -` only, up to 80 characters. Anything else is refused, because the text goes into a terminal.
- rex-tg learns the current values from the transcript, so they show after the agent's first reply. Codex needs both values to change one. Before its first turn, rex-tg answers "send a message first".
- Buttons for the model: Claude Code has `opus`, `sonnet`, `haiku`, and `fable`. Codex has the models in `~/.codex/models_cache.json`. pi has none, because it knows hundreds of models. Set your own with `[agents.<name>.choices]` (see Configuration).
- `//model` still sends `/model` to the agent unchanged.

### `/at`

- `/at 09:00 run the tests and report` sends that text to the topic's agent at the next 09:00, by the clock of this Mac. If 09:00 has passed today, it is tomorrow's.
- `/at 30m <text>` and `/at 2h <text>` send it after a delay. `1h30m` and `45s` work too. The longest delay is 30 days.
- `/at` with no text lists the topic's schedules, numbered, with their times. `/at cancel <n>` removes one.
- A topic holds at most 20 schedules. The text is at most 4000 characters.
- When the time comes, rex-tg posts a silent note, `⏰ Sending the scheduled message: …`, and queues the text as if you had typed it in the topic. All the usual checks apply: the agent must be in front, a prompt that is open makes it wait, `input.while_busy` decides whether it steers a running turn, and you get the waiting notice. Text that starts with `/` or `!` waits until the agent is idle.
- Schedules are kept across a restart. A schedule that came due while rex-tg was not running is sent at once, with a note, when it is less than 1 hour late. When it is later, it is not sent, and the topic says `⏰ Missed: … (rex-tg was not running at 09:00)`.
- A schedule is removed when it is sent, cancelled, or its topic closes. A schedule is sent once, also across a restart.

### Progress and answers

- Only two kinds of message make a sound: the agent's answer (its first message) and a prompt that waits for you (an alert or its reminder). Progress, notices, the waiting notice, and replies to commands come silently. `/mute` makes a topic's answers and reminders silent too. An alert still makes a sound.
- Quick replies are buttons under each answer that send their text to the agent with one tap, as if you typed it. They are off by default. Set them with `[display] quick_replies`, such as `["Continue", "Yes", "Run tests", "Commit"]`: at most 8, each 1 to 64 characters. They sit two to a row, and the 📄 and 📝 buttons one to a row, so a phone shows their labels whole. A quick reply waits like any typed message, for example while a prompt is open. An answer that comes as a `.md` file has no buttons.
- `typing…` shows while the agent works, in the topic you used last.
- The progress message appears after 2 seconds without an answer. It shows the latest commentary and the recent tool calls. It never shows tool output, unless `/verbose full` is on.
- The progress message stays the last message of a running turn. When an alert, a notice, or your own message comes below it, it moves down. While a prompt waits for your answer, the alert stays last.
- At the end of a turn, the progress message becomes a `✅ Done` summary only when the turn took 2 minutes or more (`[display] summary_after`). A shorter turn that ends with an answer leaves just the answer, so many short turns do not fill the topic. An error, a stop, or an interrupted turn always keeps its summary.
- Its first icon is an hourglass (⏳ ⌛). By default it turns only when the message changes. Set `[display] progress_animation` (for example `"5s"`) to turn it on a fixed pace while the agent works. The progress edits share one budget (below), so a fast animation on many topics slows down instead.
- The progress edits of the chat share one budget of 20 edits per minute (burst 3). The final summary edit is not counted. An edit over the budget waits, and newer edits of its topic merge into it. After a 429 from Telegram (a flood wait), the budget runs at half its rate for one hour.
- With the animation off (the default), its timer moves on while the agent thinks or a long tool runs: every 10 seconds at first, then less often, at most once a minute later in a long turn. It stops while rex-tg waits for your answer.
- At the end of the turn, the tool rows stay in a collapsed quote under the summary line.
- A long answer comes as several messages of about 1500 characters, split at paragraphs and headings like a chat, never inside a code block, and without `(1/2)` numbers. Only the first one makes a sound. `message_length` sets the size (300 to 4000; 4000 splits only when Telegram's limit needs it). An answer that would take more than 6 messages comes in fuller ones, up to 4000 characters each. In rex-tg's own format, an answer longer than 16000 characters comes as a `.md` file.
- While the agent writes, the text shows at the bottom of the topic as a draft, Telegram's preview of a message being written. pi's text grows word by word. Claude Code and Codex show each finished block, because they write nothing earlier. A draft never becomes a message: the answer replaces it. Final mode shows no drafts. Drafts are off by default; set `stream = true` to turn them on. `stream_interval` sets how often the draft changes: `1s` is the default and the fastest, because drafts share Telegram's budget of about one message a second in a chat; a larger value such as `"3s"` leaves more of it to answers.
- Images named in a final answer come by themselves, as photos, up to 10 per answer. Set `send_images = false` to turn this off.
- When the answer names no image file, the images that the agent looked at during the turn come after it instead: the pictures in its tool results, such as a screenshot that it read with its `read` tool. This works for pi and Claude Code. The last 4 come (`turn_images`, 0 to 10; 0 turns this off), each image once. A photo that you sent from Telegram is never sent back. The pictures are saved for 3 days in the `shown` folder of the state folder, readable only by you.
- Files go to Telegram only from the agent's folder, your home folder, or a temporary folder, never from a hidden place such as `~/.ssh` or `.env`, or from `~/Library`. A tap checks the place again before the file goes.
- Other files named in a final answer (`pdf`, `csv`, `tsv`, `html`, `htm`, `xlsx`, `docx`, `pptx`) come only when you ask: a silent `📎 Files in this answer` message lists them with a button each. When Mini App Files is enabled, files inside the agent’s working folder can open directly in the Files viewer. **Attach** sends the file as a Telegram document instead. The attachment button also works without the Mini App. Change the types with `send_files` (`[]` turns the offers off).
- Work on the Mac: when you type in the pane on the Mac, the topic gets one silent notice, `💻 In use on the Mac`, instead of the prompts and replies. The notice shows the latest prompt and whether the agent is working or done. `📄 Last answer` posts the newest answer. Write in the topic to go on in Telegram: from then on, replies come as usual. Permission prompts still come.
- Set `mac_notice = false` to show Mac turns instead. `💻 You (on Mac)` then shows the prompts typed on the Mac. Prompts that no person typed, such as Claude Code's task notifications, are not shown. Set `show_mac_prompts = false` to hide all of them.

### Permission prompts

- Tap a button to send its key to the pane. The buttons go away when the prompt ends. A button of an old prompt is refused.
- If Rex reports a blocked state without a prompt kind, rex-tg shows answer buttons only when the screen parses as a question or an exact `Do you want to proceed?` prompt with both Yes and No choices. This works across runtimes, including CUX/Claude. Unknown screens, including typed permission screens, still get a redacted alert with no buttons by default; rex-tg does not guess what to press. An explicit `[[agents.pi.answers]]`, `[[agents.claude.answers]]`, or `[[agents.codex.answers]]` list can supply fallback choices only for a typed permission screen when `allow_unknown_permission_answers = true`. Answer an unrecognized prompt on the Mac.
- `/approve` and `/deny` answer the open prompt too. `/approve` presses only a clearly labeled, one-time Yes/Allow choice; if there is no clear choice, tap the button you want. It never chooses a lasting permission by default. `/deny` presses Esc, which both Claude Code and Codex read as "no, wait for me"; it never presses an answer that allows, makes a lasting rule, or starts something else.
- Reminders: when a prompt waits for 10 minutes, the topic gets `⏰ Still waiting for your answer`, as a reply to the prompt. More reminders come 10 minutes after the one before, at most 3, and each one replaces the one before. They go away when the prompt ends; a reminder that Telegram did not confirm, or that could not be deleted, stays, and its `✓ Dismiss` button removes it. Set `[alerts] remind_after` and `reminders` to change this, and `remind_after = "0s"` to turn them off.
- Questions that allow free text get a `✍ Other…` button. Your next message becomes the answer. This is not tested yet (spike S2).
- Time limits: a Claude Code safety check blocks the request by itself after a while (`automatically block this request in 2:00`). The alert then says `⏳ Answer within 2:00 (by 07:00:57)`, one reminder comes 30 seconds before, and if the time runs out the topic says `⌛ Blocked`.
- Reply to a permission prompt with text to answer No with a note: rex-tg refuses it with Esc, then types your text as your next message, when the agent waits for it. A reply that starts with yes, ok, ya, iya, lanjut, or gas presses nothing: tap Yes for that. It works like Claude Code's "Tab to amend" for No, and Codex's "No, and tell Codex what to do differently". For Yes with a note, tap Yes and send the note as a message.
- Codex prints the key of each answer, such as `(y)` or `(esc)`; its buttons press that key.
- Alerts read like the prompt: who asks, what it wants to do (the command in monospace), warnings, and the question in bold; the answers are the buttons. Once answered, an alert shrinks to one line with the question and your answer, so old prompts do not fill the topic.
- A Claude question offers `Chat about this`: tap it to talk the question over instead of answering. On a question with previews, the button is `💬 Chat about this`; rex-tg moves the cursor there and presses Enter.
- Claude Code questions: tap an answer, or reply with text to give your own answer ("Type something"). For answers to tick, the `☐` buttons tick them and `Next ➡` goes on. With several questions, each next one, and the "Submit answers" page, comes as its own alert.
- pi asks nothing before it acts, but an extension can open a dialog (`ctx.ui.select` or `confirm`, such as `/goal-clear`). pi reports it as blocked, and the alert is a question with one button per answer. A tap moves the cursor to that answer, one key at a time with the screen read after each, and presses Enter. The alert stays while the dialog is on screen, and closes when it goes. A reply with text answers nothing: tap an answer, or `Esc`. A dialog with a text field (`input`, `editor`) is not read: answer it on the Mac.
- The review page of several questions (`Review your answers`) lists each question and its answer, as `• question` and `→ answer`. Its buttons are `1 Submit answers`, `2 Cancel`, and `Esc`. Tap `1 Submit answers` to send the answers.

| Agent | Buttons | Reply with text |
| --- | --- | --- |
| Claude Code | `1 Yes`, `2 Yes, and don't ask again …`, `3 No`, `Esc` | No (Esc) + your note |
| Claude Code question | one per answer (☐ to tick, `Next ➡`), `Esc` | your own answer ("Type something") |
| Codex | `Yes, proceed` (y), `don't ask again` (p), `No …` (Esc) | No (Esc) + your note |
| pi extension dialog (a select, or a confirm) | one per answer, `Esc` | — (tap an answer) |

### `/screen`

- `/screen [lines]` in a topic sends the pane's screen as a picture, without sound. Light text on a dark background is easy to read on a phone. The caption has the topic name and the time.
- `[lines]` is how many of the bottom lines to show: 30 by default, at most 80. A picture holds at most 60 lines and 200 columns; rex-tg keeps the bottom lines.
- The text is redacted first, as for every screen, so no secret is drawn.
- The font is Menlo (macOS). For characters that Menlo lacks, it falls back to Apple Symbols, Arial Unicode, and Hiragino Sans GB. Emoji use Apple Color Emoji. A character that no font has shows as an empty box. Without Menlo, a small built-in font is used.
- `/screen text [lines]` sends monospace text of up to 80 lines; longer text keeps its last 3800 characters. `text` and the number can come in either order. rex-tg also sends text when it cannot make the picture or Telegram refuses it.
- If the send fails and the outcome is unknown, rex-tg does not send it again. Ask `/screen` again.
- With the Mini App set up, the picture has a `🖥 Live screen` button. It opens the pane's screen in the Mini App and reads it again every 2 seconds while the page is open (see [Mini App](#mini-app)).

### `/files`

- `/files` in a topic answers with a `📂 Browse files` button. It opens the agent's folder in the Mini App as a folder tree: the top folder of its git repository, or its working folder when that is not in a repository.
- It needs the Mini App. Set `[miniapp] files = false` to turn it off.

### `/changes`

- `/changes` in a topic says what changed in the agent's git repository, in three ways:
  - **Not committed**: the files that differ from the last commit, staged or not, and new files that git does not ignore.
  - **This branch**: everything since the branch left the main branch (`origin/main`, or `main` or `master`), committed or not. This shows work that an agent already committed.
  - **Last turn**: what the agent's last turn changed, even when that turn committed it. A turn that changed nothing shows nothing. Git may clean up a turn's snapshot after about two weeks; then that turn is not offered. It needs `[display] show_changes`, which takes a snapshot when a turn starts and ends.
- When any of them has changes, a `📝 View changes` button opens them in the Mini App. There you pick one of the three, see the changed files as a folder tree, and tap a file to see its diff.
- It needs the Mini App. Set `[miniapp] changes = false` to turn it off.

### `/rt` (control panel)

- `/rt` answers with a `🎛 Open control panel` button. It works in the main chat and in a topic. `//rt` in a topic sends `/rt` to the agent.
- The panel has three tabs, `Agents`, `Topics`, and `New agent`. It remembers the last one on the phone.
- `Agents` lists the agents in Rex panes, the ones that wait for you first, like `/agents`. Each agent is one short row: its state, its name, and how long it has been in that state. Tap a row to see its details and actions; `Expand all` opens every row. The chips at the top count the agents by state (`1 waiting · 2 working · 2 idle`); tap them to show only those. The search box finds an agent by name, folder, agent, session title, or Rex session. You can group the list by state, folder, or Rex session. The list is read again every 5 seconds while the panel is open; open rows stay open.
- The details of an agent: the agent and its folder, the session's title, where it runs in Rex (`Rex: session › tab › pane`), `💻 on the Mac`, and `topic offline`. Its actions:
  - `↗ Open / reconnect` asks rex-tg to validate the topic before opening it. If Telegram no longer has it, rex-tg creates a replacement. An agent without a topic has `＋ Connect to Telegram`; it makes a new topic for that existing session. Both actions close the panel after opening the topic. A row also has a shortcut with the same validation.
  - `🖥 Screen`, `📂 Files`, and `📝 Changes` open the topic's live screen, its files, and its changes in the Mini App, as `/screen`, `/files`, and `/changes` do. `‹ Panel` or Telegram's back button goes back to the panel. They need a topic; Files and Changes also need `[miniapp] files` and `[miniapp] changes`.
  - `✏️ Rename` renames the topic, the Rex pane, and its tab, like `/rename`. `Auto name` names them after the session again. An agent without a topic gets its pane and tab renamed.
  - `🔌 Unlink` stops mirroring the session, like `/unlink`. The topic stays as history. The panel asks first.
- `Topics` lists every topic that rex-tg mirrors, live or stopped, with where its agent runs in Rex: `session › tab › pane`, or `agent stopped`. Tap a topic for its actions: `↗ Open topic`, `✏️ Rename` (the topic, the pane, and the tab, as `/rename`; it needs a running agent), and `🔌 Unlink`. Filter it by live or stopped, or search it. `Clean up stopped` unlinks every topic whose agent stopped, after a confirm, as `/unlink` would in each; the topics stay in Telegram as history.
- `New agent` starts an agent in a new Rex tab, like `/new`. Pick the agent, then a folder: one from the list of folders, or one that you find with the folder explorer. The list has the folders in `[rex] new_folders` (📌) and the folders where agents ran recently. `✕` removes a recent folder from the list, and `Clean up` removes them all, also the ones further down; pinned folders stay. A removed folder also leaves `/new`'s buttons, and comes back when an agent starts a new session there. An agent that keeps running there does not bring it back. The explorer starts in your home folder and never leaves it. Tap a part of the path at the top to go back to it, or type in the filter to narrow the folders. A `GIT` badge marks a git repository. macOS asks on the Mac before rex-tg reads Desktop, Documents, or Downloads; until you click Allow there, such a folder does not open (see [Troubleshooting](#troubleshooting)). It shows folders only, not files, and a link to a folder only when the folder is inside your home folder too. `Hidden` also shows names that start with a dot. The panel's own bar at the bottom starts the agent, such as `Start Claude Code in ~/work/app`. It does not use Telegram's main button, because on iOS Telegram's bar is see-through and the page shows through it. The panel starts an agent only in a folder inside your home folder, or in one that `/new` offers; use `/new <agent> <folder>` for any other folder. The progress and the new topic come to the main chat. The panel goes to `Agents` and shows `Starting …` until the new agent is in the list; then it opens and marks that agent.
- While typing is paused, the panel says so. `/pause` and `/unpause` stay chat commands, so init data that leaked cannot undo a `/pause`. The panel starts no agent while typing is paused.
- Each tap acts at most once. The page sends a random id with each action, and rex-tg keeps the ids for a day, so a request that the network repeats does nothing a second time. When rex-tg cannot say yet how an action ended (it was busy, or the action still runs), the panel says so. Tap again to ask: the page sends the same id, so the action does not run twice.
- Signed panel reads accept owner init data up to 24 hours old; signed actions need data from the last hour. Close and reopen with `/rt` if it expires. A keyboard-key launch uses the current key and allowed tailnet login instead; it can run validated panel actions too. `/rekey` revokes the key.
- It needs the Mini App.

### Files and photos

- Photos and files are downloaded up to `max_upload_mb` (20 MB). They are saved under the state folder, and their path is typed into the pane with your caption. If a download fails, or a restart interrupts it, tap **↻ Retry download** on the error message. A failed retry offers the button again; a successful retry queues the file once.
- At most four uploads are processed at once, including voice transcription and retry downloads. When busy, files keep a retry button; voice notes ask you to send them again shortly. If a restart interrupts transcription before it is queued, rex-tg posts one notice asking you to resend the voice note. It does not type it automatically.
- Claude Code turns the path of an image into an attached image.
- Stickers, videos, and other kinds get the reply `Only text is supported for now.`
- Edited messages are not sent to the agent. The bot says so.

## Configuration

rex-tg reads `~/.config/rex-tg/config.toml`. The file is optional.

- You only need the lines you change.
- An unknown key is an error. rex-tg names the key.
- A leading `~` is expanded in `rex.cli`, `rex.new_folders`, `state.dir`, `voice.whisper_cli`, and `agents.claude_exec_glob`. Use an absolute path for `voice.model`.
- Restart the daemon after a change: `launchctl kickstart -k gui/$(id -u)/local.rex-tg`.

Every key, with its default:

```toml
[telegram]
api_base = "https://api.telegram.org"  # Telegram Bot API address. Keep the default.
proxy = ""                              # Proxy for Telegram: http, https, socks5, or socks5h URL. Empty uses HTTPS_PROXY or ALL_PROXY.
fallback = true                         # Used only with proxy: switch to the direct route when the proxy fails, and back.
rate_per_second = 1                     # Messages per second for the whole chat.
rate_burst = 5                          # Messages that may be sent at once above that rate.

[rex]
cli = "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"  # Path to the rex CLI.
scan_interval = "30s"                   # How often rex-tg scans the Rex panes, as a backup to Rex events.
new_session = "rex-tg"                  # The Rex session where /new and `rex-tg new` open tabs.
new_folders = []                        # Folders that /new always offers first, such as ["~/work/app"].

[agents]
pi_argv0 = ["pi"]                       # Process names (argv0) that mean a pi pane.
claude_argv0 = ["claude", "cux"]        # Process names that mean a Claude Code pane. cux runs claude as its child.
claude_exec_glob = ["~/.local/share/claude/versions/*"]  # Claude Code program paths.
codex_names = ["codex"]                 # Process names that mean a Codex pane.

[agents.pi]
launch = ["pi"]                         # What /new runs, one word per item, as if typed in a terminal.
paste_enter_delay = "300ms"             # Wait between the paste and the Enter key.
interrupt_keys = ["escape"]             # Keys that stop the current turn (/interrupt).
while_working = "steer"                 # What Enter does while the agent works: steer, queue, or unknown.
allow_unknown_permission_answers = false # Keep unknown permission screens no-action by default.
answers = []                            # Empty by default; pi has no fallback choices.

[agents.pi.choices]                     # Optional. Buttons of /model and /effort, by command name. Replaces the agent's own list.
model = ["anthropic/claude-sonnet-5-5", "openai/gpt-6-sol"]   # Same for [agents.claude.choices] and [agents.codex.choices].

[agents.claude]
launch = ["claude"]
paste_enter_delay = "300ms"
interrupt_keys = ["escape"]
while_working = "steer"
allow_unknown_permission_answers = false  # Keep unknown permission screens no-action; opt in only for known keys.

# Fallback choices are empty by default. Add [[agents.claude.answers]] only
# for a permission prompt whose keys and effects you know, and set the opt-in above.

[agents.codex]
launch = ["codex"]
paste_enter_delay = "300ms"
interrupt_keys = ["escape"]
while_working = "steer"
allow_unknown_permission_answers = false  # Keep unknown permission screens no-action; opt in only for known keys.

# Fallback choices are empty by default. Add [[agents.codex.answers]] only
# for a permission prompt whose keys and effects you know, and set the opt-in above.

[display]
mode = "progress"                       # progress, block, or final.
backfill = 3                            # Final answers to post when a topic opens.
show_mac_prompts = true                 # Post prompts that you type in the pane on the Mac (when mac_notice is off).
mac_notice = true                       # Hold turns typed on the Mac behind one notice, until you write in Telegram.
progress_delay = "2s"                   # Wait before the progress message appears.
progress_edit_interval = "5s"           # Least time between two edits of the progress message.
progress_animation = "0s"              # How often the progress message turns its hourglass and moves its timer. "0s" (default) turns it off; at least 2s.
summary_after = "2m"                    # Keep the "Done" summary only for turns this long or longer; shorter ones leave just the answer. "0s" keeps every summary.
rich_messages = true                    # Send final answers as Telegram rich messages: Telegram renders tables and code itself.
send_images = true                      # Send the image files that a final answer names.
turn_images = 4                         # When it names none, send the last N images that the agent looked at in the turn (0 to 10; 0 is off).
send_files = ["pdf", "csv", "tsv", "html", "htm", "xlsx", "docx", "pptx"]  # Other file types that a final answer offers, with a button to send each.
quick_replies = []                      # Buttons under each answer that send their text to the agent, such as ["Continue", "Yes"]: at most 8, 1 to 64 characters each. Off by default.
show_changes = true                     # A button under an answer that shows what the turn changed (needs [miniapp] url).
message_length = 1500                   # About how long one message of a long answer is (300 to 4000).
stream = false                          # Show the agent's text while it writes, as a Telegram draft.
stream_interval = "1s"                  # Least time between two drafts of a topic. 1s is the fastest: Telegram allows about one message a second in a chat.

[alerts]
remind_after = "10m"                    # Remind of an unanswered prompt after this, and again each time it passes. "0s" turns reminders off.
reminders = 3                           # The most reminders for one prompt.

[input]
queue_expiry = "30m"                    # A waiting message expires after this time.
join_window = "300ms"                   # Messages that arrive this close together become one prompt.
join_window_long = "1500ms"             # Join window when a message is 4000 characters or longer.
busy_after_typing = "15s"               # After a message is typed, the pane counts as busy for up to this long.
while_busy = "steer"                    # A message sent while the agent works: "steer" joins the current run, "wait" waits until idle.
max_upload_mb = 20                      # Largest photo or file to download from Telegram.

[miniapp]
url = ""                                # The Mini App's public https:// address (see Mini App). Empty turns it off.
listen = "127.0.0.1:8770"               # Its local address.
answer_preview = 3000                   # Long-answer preview threshold; split-prone answers use a page too. At least 2000.
files = true                            # /files opens a read-only browser of the agent's folder in the Mini App.
changes = true                          # /changes shows what changed in the agent's git repository in the Mini App.

[voice]
whisper_cli = ""                        # Path to whisper-cli. Empty turns voice notes off.
model = ""                              # Path to a ggml model file. Use an absolute path.
language = ""                           # Language code for whisper.cpp, such as "en".

[state]
dir = "~/.local/state/rex-tg"           # Database, spool, logs, and uploads.
```

Unknown prompt layouts have no answer buttons by default, even if an older config still contains fallback answers. A typed permission screen can use `[[agents.pi.answers]]`, `[[agents.claude.answers]]`, or `[[agents.codex.answers]]` only when `allow_unknown_permission_answers = true`; set it only when you know what those keys do. Fallback choices do not apply to questions or untyped prompts.

### Mini App

The Mini App shows what does not fit a chat message. Its pages open from buttons, and only for you:

- `📄 Whole answer`: an answer longer than `[miniapp] answer_preview` (3000 characters by default) shows its start in the chat. The whole answer, with its tables and code, is in the Mini App, where you can add notes to it and send them to the agent ([Annotate](#annotate)). Such an answer is not sent as one rich message. If rich messages are off, an answer that would split into multiple chat messages also opens as a Whole answer page, even when it is shorter than this setting.
- `📝 2 files · +40 −12`: what the turn changed in the agent's git repository, file by file. rex-tg takes a snapshot of the working files (tracked and new, not ignored) when a turn starts and when it ends with its answer. The snapshots use a copy of git's index, so your own index stays as it is. They write objects into the repository, which git cleans up later. Other agents that work in the same repository at the same time show up in it too. Secrets are removed from the diff. A folder that is not a git repository gets no button. Set `[display] show_changes = false` to turn it off. A repository with more than 300 new files that are not ignored, or more than 20 MB of them, gets no button: the snapshot would be slow.
- `🖥 Live screen`, under a `/screen` picture: the pane's screen with its colors, drawn as a terminal (xterm.js), read again every 2 seconds while the page is open, and not while it is hidden. It fills the phone's height and keeps the bottom line in view; scroll up to read back, and `↓` goes to the bottom again. `Pause` stops the refresh while you read. `Fit width` is on by default. It scales the terminal to the phone and prevents sideways scrolling. Turn it off to pan, or tap `A+` to zoom and turn it off. Secrets are removed, as on every screen; a line that held one shows without colors. The page only shows the screen; it types nothing. A topic has one live screen page, so all its buttons open the same page.
- `📂 Browse files`, from `/files`: the agent's folder as a tree, read-only. Tap a folder to open or close it in place, and a file to read it; `‹ Files` or Telegram's back button goes back to the tree as you left it. `Collapse all` closes every folder, and `↻` loads the tree again. Markdown files are rendered, other text is shown as it is, with secrets removed, and pictures (PNG, JPEG, GIF, WebP up to 4 MB) are shown. Of a long text file, the first 512 KB are shown; a binary file shows only its size. The browser never leaves the folder: a path with `..` stays inside it, and a link that points out of it does not open. It hides `.git`, files that git ignores (such as `.env` and `node_modules`), and files with names of keys and credentials (`.env*`, `*.pem`, `*.key`, `id_rsa*`, `credentials.json`, and the like, in any case; `.env.example` is shown). In a folder that is not a git repository, it also hides every name that starts with a dot. A link to a hidden file does not open either. A folder shows at most 2000 entries. Listings inspect at most 4096 raw names; larger folders show a bounded subset with a warning that entries may be omitted.
- `📝 View changes`, from `/changes`: the changes of the agent's git repository. Three tabs pick what to compare: `Uncommitted` (since the last commit), `This branch` (since the branch left the main branch), and `Last turn`. A tab without anything to compare is dimmed, and tapping it says why. The view opens on the first tab that has changes. The changed files show as a tree, with a letter for each: `M` changed, `A` added, `D` deleted, `U` new and not tracked, `T` turned into a link or back, `!` in conflict. Tap a file for its diff, with the old and the new line numbers. `‹` and `›` go to the previous or next file, and `Wrap` wraps long lines. The view reads git each time it opens, so it shows the changes as they are now. It hides what the file browser hides: ignored files are not listed, and a file with the name of a key or credential is listed with 🔒 but its changes are not shown. Secrets are removed from every diff. A list shows at most 2000 files, and a diff at most 1 MB. Pictures that changed are shown as they are now.

- `✍️ Annotate`, from the button under the message box: the agent's answers, to add notes to and send back ([Annotate](#annotate)).
- `🧩 Templates`, from the button under the message box: create reusable messages on this device, then send one to the agent in that topic. See [Templates](#templates).
- `🎛 Open control panel`, from `/rt`: the agents and their topics, with actions on them, and a folder explorer to start a new agent ([`/rt`](#rt-control-panel)). The control panel acts: it opens and unlinks topics, renames agents (topic, pane, and tab), and starts agents. It never types into a pane. There is one control panel page.

A table on a page scrolls sideways in its own box, and the page stays still. A column is as wide as its text, up to about 18 em; longer text wraps at words.

Every page has a bar at the top. `A−` and `A+` make the text smaller or bigger, and the percentage between them sets it back to 100%. ☀/🌙 switches between light and dark; tapping back to Telegram's own scheme follows Telegram again. The page keeps your choices on the phone. Fixed footers and sheets respect Telegram's safe area, including the home-indicator area. Annotate's Review bar and bottom sheets leave extra room when Telegram reports no inset.

The Mini App needs an HTTPS address that reaches this Mac. rex-tg answers only inside your tailnet, so the phone must run Tailscale too. It is off until `url` is set. With Tailscale:

1. Serve rex-tg's port to your tailnet: `tailscale serve --bg --https=8443 http://127.0.0.1:8770`. `--bg` keeps it running after the command ends. HTTPS certificates must be on for your tailnet (admin console). `tailscale serve status` shows its address, marked `(tailnet only)`. Do not use `tailscale funnel`: it makes the address public, and rex-tg refuses those requests.
2. Put the address in the config. Its port must be the port of `serve`, and the target of `serve` must be the `listen` address:

```toml
[miniapp]
url = "https://<your-mac>.<tailnet>.ts.net:8443"   # https:// only. Empty turns the Mini App off.
listen = "127.0.0.1:8770"                           # the local target of tailscale serve; keep it on 127.0.0.1
tailnet_only = true                                 # answer only requests from your tailnet
# tailnet_users = ["you@example.com"]             # who may use the buttons' key; empty: rex-tg learns your login
answer_preview = 3000                               # longer answers open in the Mini App; split-prone answers use a page too
files = true                                        # /files: browse the agent's folder
changes = true                                      # /changes: what changed in the agent's repository
```

Restart rex-tg with `launchctl kickstart -k gui/$(id -u)/local.rex-tg`. Then check the log: `mini app: serving` means the server runs. Send a long answer and tap `📄 Whole answer` to test it.

File and folder reads share a limit of eight outstanding reads. A read waiting for macOS permission keeps its slot, even after the page times out. Further reads ask you to try again shortly.

Who may open a page:

- **Only your tailnet.** With `tailnet_only = true` (the default), rex-tg answers a request only when `tailscale serve` brought it from a device of your tailnet. Tailscale marks such a request with the `Tailscale-User-Login` header, which it sets itself and removes when a client sends it. A request through Funnel has no such header and is refused, the page shell too. A tagged device has no user, so it cannot open the Mini App.
- **Only you.** A page request carries the Mini App's init data, which Telegram signs for the account that opened it. rex-tg checks the signature with a key made from the bot token. It refuses a signature that is more than 24 hours old, or one that is not the owner's.
- **Or the buttons' key, from your own Tailscale account.** A page opened from a button under the message box has no init data; it carries the buttons' key instead ([Buttons](#buttons-under-the-message-box)). The key works only from your own Tailscale logins, so in a shared tailnet another person's device cannot use it. rex-tg learns your login from a page that you open from a chat button (Telegram signs those), and, before it knows any, from the key's first use. `/rekey` forgets the learned logins. To name them yourself, set `tailnet_users = ["you@example.com"]` (the login that `tailscale status` shows, in any case); then only those work. In a tailnet that you share with other people, set `tailnet_users` before you turn on the Mini App or upgrade to 0.4.1: until rex-tg knows a login, the first device that uses the key is trusted. The key can read everything the pages show: the agents and topics, the live screens, the agents' folders and files, the changes, the answers, and the folders of your home folder in the folder explorer. It can also authorize validated control-panel actions; each action is claimed once, and `/rekey` revokes the key. It cannot pause or unpause typing. Annotate sends are allowed only from the page-bound topic/session and require your verified Tailscale login. Telegram sees the key, because it is in the buttons' addresses. Run `/rekey` if you think the key leaked.
- **Only through Tailscale on this Mac.** With `tailnet_only`, `listen` must be `127.0.0.1` or `[::1]`: another address would let devices on your network reach the server directly and fake Tailscale's header. rex-tg then keeps the Mini App off, and the log and `rex-tg doctor` say why.
- A refused request is logged as `mini app: refused requests`, with the reason, at most once a minute. A phone outside the tailnet gets a page that says "Turn on Tailscale".

`rex-tg doctor` checks the Mini App from end to end: the config, that `tailscale serve` sends the address to rex-tg and that Funnel is off for it, and a live request to the page. It only reads Tailscale's setup; it never changes it.

To turn the Mini App off, set `url = ""`, restart rex-tg, and run `tailscale serve --https=8443 off`.

Upgrading from a setup with `tailscale funnel`: since the buttons carry a key, rex-tg refuses Funnel requests by default. Move the port to `serve`: `tailscale funnel --https=8443 off`, then `tailscale serve --bg --https=8443 http://127.0.0.1:8770`. The address stays the same. Turn on Tailscale on the phone. At the start, the log says `mini app: answers only requests from your tailnet`. Disabling `tailnet_only` removes the tailnet restriction. Anyone who obtains a current keyboard key can then read pages and run validated control-panel actions from outside the tailnet. Do not use that setting as a public-sharing feature.

Answers are kept for 30 days, changes for 14, live screens, file browsers, changes views and the control panel for 7 days after the last `/screen`, `/files`, `/changes` or `/rt` that sent their button, and at most 500 pages in all. While the Mini App's server is not running, answers come whole in the chat.

### Tables and rich messages

A code block with a line longer than 80 characters does not fit a phone. It stays in the message as a short preview (3 lines), and the whole block comes right after as a file, such as `code-1.go` (named by the block's language, else `.txt`). Tap it to open or copy it.

A table that is wider than a phone (more than 40 characters) becomes a list: one block per row, with the first cell in bold and `Column: value` for the others. A narrower table stays a monospace grid.

Final answers go out in Telegram's rich message format (`[display] rich_messages`, on by default), so Telegram renders tables, code, and headings itself. An answer of up to 32768 characters goes as one rich message. If Telegram rejects one with a 400 error, rex-tg sends the answer again in its own format (Telegram HTML, with the tables as above): as one message when it fits one formatted part and at most 4000 UTF-16 units, else as a `.md` file. A rate limit (429) does not change the format: the answer waits, and it goes out as a rich message. Set `rich_messages = false` to always use rex-tg's own format. There, an answer longer than 16000 characters comes as a `.md` file. With the Mini App set up, an answer longer than `answer_preview` shows its start instead; an answer that would split into multiple messages also uses a Whole answer page instead of chat chunks (see [Mini App](#mini-app)).

## Voice notes

Telegram voice notes and audio files use the same local transcription setup: ffmpeg and whisper.cpp.

1. Install ffmpeg: `brew install ffmpeg`.
2. Install whisper.cpp: `brew install whisper-cpp`. This adds the `whisper-cli` command.
3. Download a ggml model file from the whisper.cpp project, such as `ggml-base.en.bin`.
4. Find the command path: `which whisper-cli`.
5. Set the three keys:

```toml
[voice]
whisper_cli = "/path/from/which/whisper-cli"
model = "/Users/you/models/ggml-base.en.bin"   # absolute path; ~ is not expanded here
language = "en"
```

rex-tg converts a voice note or audio file to 16 kHz WAV with ffmpeg, and whisper-cli transcribes it. The bot replies with 🎤 and the text. The text then goes to the agent like any message.

When voice is off, the bot replies `Voice notes need whisper.cpp; see the README.`

## Run, stop, and update

`rex-tg install` starts a per-user launchd service. It starts at login; you do not need to keep a terminal open. Keep the Mac awake and Rex running to use the bridge. rex-tg cannot start Rex or wake the Mac.

| Task | Command on the Mac |
| --- | --- |
| Restart after changing config | `launchctl kickstart -k gui/$(id -u)/local.rex-tg` |
| Stop the background service | `launchctl bootout gui/$(id -u)/local.rex-tg` |
| Load the background service again | `rex-tg install` |
| Check topic state | `rex-tg status` (add `--all` for closed topics) |
| Remove the integrations | See [Uninstall](#uninstall). |

Restarting interrupts any in-flight work in the bridge. If a prompt may have been pasted, check the pane rather than sending it again.

**Foreground mode.** On a fresh setup, run `rex-tg install --no-load` to install the integrations without loading the launchd job. If the job is already running, stop it first. Run `rex-tg daemon --log-stderr` and keep that terminal open. Press `Ctrl+C` to stop it. Do not run two pollers for the same bot token.

**Updates.** For Homebrew, run `brew update && brew upgrade rex-tg`. For source builds, run `git pull --ff-only && mise exec -- make install` from the checkout. The launchd service notices a binary replacement and restarts when safe, usually within about a minute. Restart a foreground daemon yourself. If the [release notes](https://github.com/arrokh/rex-tg/releases) mention integration or skill changes, run `rex-tg install` again and restart your agents. Use `--no-load` if you want to keep using foreground mode.

## Logs

- `rex-tg status` reads only the database. It counts live, offline, and closed topics, but hides closed-topic details by default. Use `rex-tg status --all` to show them.
- The daemon log `~/.local/state/rex-tg/logs/rex-tg.log` has one JSON object per line. It rotates at 10 MB and keeps 3 old copies.
- launchd writes `launchd.out.log` and `launchd.err.log` to the same folder.
- Watch the log: `tail -f ~/.local/state/rex-tg/logs/rex-tg.log`.

## Uninstall

`rex-tg uninstall` removes the Claude Code and Codex hook entries, the pi extension, the agent skills, and the launchd job. It keeps the database, the file backups, and the token. The Claude skill is removed when its hooks are gone. The shared Codex/Pi skill stays while either integration remains.

- `--only pi`, `--only claude`, or `--only codex` removes that agent's parts. The shared skill stays if another Codex/Pi integration remains. The launchd job stays.
- `--keep-launchd` keeps the launchd job.
- `--force` removes Codex entries even when that moves the trust of other Codex hooks.

**Codex trust rule.** Codex trusts each hook by its position in the list. If another Codex hook comes after a rex-tg entry, removing the rex-tg entry moves that hook, and Codex stops trusting it. So uninstall does not remove such Codex entries. It names them instead. To remove them anyway:

```sh
rex-tg uninstall --only codex --force
```

Then start Codex and trust the moved entries again.

To delete the token: `security delete-generic-password -s rex-tg -a bot-token`. To delete the database and logs: remove `~/.local/state/rex-tg`.

## Troubleshooting

| Problem | What to do |
| --- | --- |
| Threaded Mode is off | In @BotFather, choose your bot, then turn on Bot Settings > Threaded Mode. Check with `rex-tg doctor`. |
| Codex does not trust the hooks | Start Codex once, and trust the new rex-tg entries. |
| Rex is not running | rex-tg cannot read or type into panes. It retries every 10 seconds and never starts Rex. Start Rex Beta. If `doctor` cannot find the Rex CLI, set `[rex] cli`. |
| No owner is paired | Run `rex-tg pair`, then send `/start <code>` to the bot. |
| "waiting for session" | rex-tg learns the session file at the agent's next prompt or turn end. |
| "message thread not found" | You deleted the topic in Telegram, so rex-tg detached it without stopping the agent. Open the agent again with `/agents` to make a replacement topic. |
| "This topic is not linked" | Use `/attach` in this topic to select an unattached Rex session, or `/agents` to open it in a new topic. |
| "Checking whether it arrived" | Telegram did not confirm a reply. rex-tg checks the chat's message numbers. A reply that never arrived is sent again by itself. |
| "may not have arrived" | The message may already be in the chat. Check before using `🔁 Send again` or `/resend`. A single message has no part number; a split message shows the affected part. An uncertain bot notification, such as a keyboard refresh, does not produce a resend warning. Use `/help` if you need to restore the keyboard. |
| "may be in the input box without Enter" | The daemon stopped between the paste and Enter. Check the pane with `/screen`. rex-tg does not type the text again. |
| `/new`: "exited with code 127" | The shell did not find the agent. Set its full path, for example `[agents.codex] launch = ["/opt/homebrew/bin/codex"]`. |
| `/new`: "has not reported its session yet" | The agent may wait for something on its screen, such as a login. The message shows the screen. Deal with it on the Mac, then open the agent with `/agents`. |
| `/new`: "did not react to a key" | The startup question did not move its cursor. Answer it on the Mac. |
| Telegram returns 409 Conflict | Another program uses the same bot token. Only one daemon may poll Telegram. |
| Mini App page does not open | Turn on Tailscale on the phone: the Mini App answers only inside your tailnet ("The Mini App opens only inside your tailnet"). Check that `[miniapp] url` is the `https://` address of `tailscale serve`, with its port, and that it runs (`tailscale serve status`). `mini app: serving` in the log means the server runs. See [Mini App](#mini-app). |
| A button's page says "This button is old" | The buttons' key changed (`/rekey`). Send `/help` in the topic for new buttons. |
| A button's page says "only on your own Tailscale account" | The key works only from your Tailscale logins. Open it on your own phone. If you changed Tailscale accounts, send `/rekey`, then tap a button: rex-tg learns the new login. Or set `[miniapp] tailnet_users`. |
| "✍️ Not sent: … another session" or "… came from this topic" | Annotate sends notes only to the session it showed, from its own topic. Open ✍️ Annotate again in the topic you are in. If a button opens another topic, send `/help` there for that topic's buttons. |
| The log says `mini app: off` | The Mini App's settings are not safe (for example `listen` is not `127.0.0.1` while `tailnet_only` is on). `rex-tg doctor` shows the reason and the fix. |
| The folder explorer or `📂 Browse files` says "The Mac did not let rex-tg read this folder in time" | macOS asks before a program reads Desktop, Documents, Downloads, iCloud Drive, or a network volume. The question shows on the Mac, as "rex-tg would like to access files in your Documents folder". Click Allow, then tap Try again (`↻` in the file browser). Until someone answers, the read waits, so the page stops waiting after 8 seconds and says so. Don't Allow keeps the folder closed to rex-tg. Change it later in System Settings > Privacy & Security > Files and Folders. rex-tg is not signed with a fixed identity, so macOS may ask again after an upgrade. |
| The log says `mini app: listen` | The `[miniapp] listen` address is in use or not valid. The buttons still show, but their pages do not open. Set `listen`, then restart rex-tg. |
| Telegram says the bot was blocked | Unblock the bot in Telegram. |
| Your messages reach the agent, but no replies come | Telegram may have asked rex-tg to wait (429 Too Many Requests). The log says `Telegram rate limit` with the method and the wait. A wait of 5 minutes or more holds the whole chat. `/status` shows it as `⏸ Telegram asked rex-tg to wait until HH:MM`. When the wait ends, the main chat gets a note with its start and end, and the held messages go out. A restart does not end the wait. A shorter wait pauses all sending. Nothing is lost. |
| `doctor` reports unknown line types | The agent changed its transcript format. rex-tg ignores those lines. The adapter may need an update. Once a rex-tg update knows a type, doctor stops reporting it. |
| "Not sent: the pane now runs ..." | The agent no longer runs in the pane. Start it again, or open another agent with `/agents`. |

### Slow or unreliable network

Some networks drop the route to Telegram for a minute or two, again and again. Replies then come late, in bursts.

- rex-tg notices a dead connection in about 15 seconds. It retries every 0.5 to 2 seconds, and sends queued replies within seconds after the route returns.
- The log has one warning when a break starts, and one "Telegram reachable again" line when it ends.
- If the breaks keep coming, send Telegram through a proxy or VPN: set `[telegram] proxy`, such as `"socks5h://127.0.0.1:40000"`. It takes an `http`, `https`, `socks5`, or `socks5h` URL. The log shows only the scheme and host, never a password.
- With `proxy` set, rex-tg also uses the direct route when the proxy stops working. This is `[telegram] fallback`, on by default. Three requests in a row that never left the machine (a dial error, a TLS handshake timeout, a refused connection) make rex-tg use the other route for the next requests. It switches back the same way. Such a request is also tried once at once on the other route, because nothing was posted. A request with an unknown outcome is never sent again on the other route. The log has one line `Telegram route: direct` or `Telegram route: proxy` for each switch. Set `fallback = false` to keep the proxy only.
- When `proxy` is empty, rex-tg uses `HTTPS_PROXY` or `ALL_PROXY` from the daemon's environment. There is no fallback then.

## Privacy

- Replies pass through Telegram's servers. Bot chats are not end-to-end encrypted.
- rex-tg removes common secret formats before it sends text: API keys, GitHub and AWS keys, Slack tokens, private key blocks, JWTs, Telegram bot tokens, and Bearer tokens. This is best effort.
- The database keeps message text. Housekeeping removes sent/skipped outbox rows older than 30 days, inbound/alert rows older than 30 days, and outbox rows for topics closed more than 30 days ago. Pending, failed, and unknown outbox rows of an open topic can remain longer. Upload files older than seven days are cleaned separately. None of this deletes Telegram's copies.
- Only you can read the state folder and the spool folder.
- Before ordinary typing, rex-tg checks the owner, live topic/session/pane binding, expected process, pause, readiness, and prompt state. `/force` bypasses readiness and prompt waiting, not owner, target, process, or pause checks.
- rex-tg makes outgoing connections to Telegram and to Rex, and never starts a Rex server. Its one listening port is the Mini App's. It exists only when `[miniapp] url` is set (see [Mini App](#mini-app)).
