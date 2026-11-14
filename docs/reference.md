# rex-tg reference

This file has the details that the [README](../README.md) leaves out.

## Setup details

### Install with Homebrew

```sh
brew tap arrokh/rex-tg https://github.com/arrokh/rex-tg
brew trust --formula arrokh/rex-tg/rex-tg   # Homebrew asks for this for a third-party tap
brew install rex-tg           # the newest release
brew install --HEAD rex-tg    # or: build `main`
```

Homebrew builds from source, so it needs Go (it installs it as a build tool).

The repository is private. Homebrew gets it with `git clone`, so the tap and the install work for a GitHub account that has access, through your git login. If `brew tap` asks for a password, run `gh auth setup-git` once.

`rex-tg install` writes the stable path `<prefix>/opt/rex-tg/bin/rex-tg`, not the versioned Cellar path. That path stays valid after an upgrade.

Upgrade with `brew upgrade rex-tg` (add `--fetch-HEAD` for a HEAD install). The daemon restarts itself within about a minute, and the topics stay linked. Run `rex-tg install` again only when the release notes say the hooks changed. `rex-tg doctor` checks it.

If you installed with `make install` before, move to the brew binary in two steps:

1. Run `"$(brew --prefix)/bin/rex-tg" install`. The hooks, the pi extension, and launchd then name the brew binary, and the daemon restarts with it.
2. Replace the old copy with a link: `ln -sf "$(brew --prefix)/opt/rex-tg/bin/rex-tg" ~/.local/bin/rex-tg`. Do not just delete it: agents that run already read their hook settings when they started, and keep calling the old path until they restart.

`rex-tg doctor` warns while a second copy is left.

To remove: run `rex-tg uninstall` first, then `brew uninstall rex-tg`.

**Release steps.** The maintainer does these by hand:

1. Tag and push: `git tag v0.1.0 && git push origin v0.1.0`.
2. Run `make formula-release TAG=v0.1.0`. It prints the `url` lines: the git URL, the tag, and its commit.
3. Put them into `Formula/rex-tg.rb` in place of the old ones, above `head`, then commit and push `main`. Users get the release with `brew update && brew upgrade rex-tg`.

### Token and pairing code (`rex-tg login`)

1. macOS `security` asks for the token. Paste it. It is not shown.
2. rex-tg checks the token with Telegram, and stores it in the Keychain.
3. It tells you if Threaded Mode is off.
4. It prints a pairing code. The code works for 10 minutes.

`rex-tg login --token-stdin` reads the token from one line of standard input. Use it in scripts.

If the shell cannot find `rex-tg`, add `~/.local/bin` to your `PATH`. The hooks and the launchd job use the full path, so they do not need `PATH`.

### Hooks and daemon (`rex-tg install`)

The command changes these files:

- **Claude Code:** adds six hook entries to `~/.claude/settings.json`: `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, `Notification`, and `SessionEnd`.
- **Codex:** adds four hook entries to the end of each event list in `~/.codex/hooks.json`: `SessionStart`, `UserPromptSubmit`, `Stop`, and `PermissionRequest`.
- **pi:** writes the extension `~/.pi/agent/extensions/rex-tg.ts`. It sends `session_start`, `session_shutdown`, `agent_start`, and `agent_end` to rex-tg.
- **launchd:** writes `~/Library/LaunchAgents/local.rex-tg.plist` and starts the daemon. launchd starts it at login and restarts it if it stops.

Before it changes `settings.json` or `hooks.json`, rex-tg saves a backup next to the file. It keeps the 5 newest backups. Running the command again changes nothing.

`--only pi`, `--only claude`, or `--only codex` changes one agent only. The launchd job is still installed.

After the install, start Codex once and trust the new hook entries. Two things are not tested yet (spike S3): the Codex trust prompt, and whether pi loads the extension with no other step.

### Pairing (`/start <code>`)

Send the code after `rex-tg install`. The daemon reads the code only while it runs.

- The first valid code makes your account the owner.
- The bot answers no one until pairing is done.
- A code works once, for 10 minutes. Run `rex-tg pair` for a new code.
- After 5 wrong codes from one Telegram account, that account cannot pair with this code. Other accounts still can, so a stranger cannot lock you out. `rex-tg pair` makes a new code and clears all counts.

### Doctor (`rex-tg doctor`)

Doctor checks:

- the Rex CLI, and `rex whoami`
- the bot token, `getMe`, and Threaded Mode
- the database, and whether an owner is paired
- the Claude Code and Codex hooks, and the pi extension
- the launchd job, and the folder modes
- whether a daemon is running

## Using rex-tg

### Commands in a topic

Text that starts with `/` goes to the agent, except the commands below and the main-chat commands (`/agents`, `/new`, and so on). `//name` sends `/name` to the agent. Such an agent command, like `/restart` or `/compact`, waits until the agent is idle. An agent does not record its slash commands as prompts, so the 👍 comes as soon as the command is typed.

| Command | What it does |
| --- | --- |
| `/info` | Shows the session, the pane, the state, and the settings. |
| `/screen [lines]`, `/screen text [lines]` | Sends the pane's screen as a picture, or as text. See [`/screen`](#screen). |
| `/approve`, `/deny` | Answers the open prompt. |
| `/esc`, `/interrupt`, `/key <keys>` | Presses Escape, stops the turn, or presses keys. |
| `/force <text>` | Types text even when a prompt is open. |
| `/model [name]`, `/effort [level]` | Shows or changes the model and the thinking effort. See [`/model` and `/effort`](#model-and-effort). |
| `/mode progress\|block\|final` | Sets how replies appear. |
| `/verbose on\|full\|off` | Shows each tool call (`on`), and also its output (`full`). |
| `/reasoning on\|off` | Shows the agent's thinking, when the agent stores it. |
| `/resend [id]` | Sends again a reply that may not have arrived. |
| `/mute`, `/unmute` | Answers arrive without or with sound. |
| `/at <time or delay> <text>`, `/at`, `/at cancel <n>` | Sends text to the agent later, lists the schedules, or removes one. See [`/at`](#at). |
| `/rename <name>`, `/rename auto` | Renames the topic, its Rex pane, and its Rex tab. `/rename auto` names them after the session again. |
| `/unlink` | Closes the topic in rex-tg. |
| `/help` | Lists the commands. |

### Agents and topics

- `/agents` lists the agents: the ones that wait for you first, then working, then idle, each with how long it has been in that state, such as `waiting for you · 12m`. `💻 on the Mac` marks a session in use on the Mac.
- The newest `/agents` list updates by itself when an agent's state changes. Pin it to see all agents at a glance. Older lists do not change.
- Tap an agent in the list: if the session already has a topic, you get a link to it. If you deleted that topic, a new one is made for the same session.
- An agent marked `(guess)` has no hook report yet. rex-tg took the newest transcript in the agent's folder that changed after the agent started.
- An agent marked `(waiting for session)` has no known session file yet. rex-tg learns it at the agent's next prompt or turn end.
- A topic is named after the name you gave its Rex tab (see below), else its Rex pane when you named the pane, else the agent's session name. The name follows changes of all three.
- `/rename <name>` in a topic renames three things: the topic, the agent's Rex pane (`rex block rename`), and its Rex tab. A tab that also holds another agent's pane keeps its name. The tab of an agent that `/new` started follows the topic's name, with ` #2` when another tab of that session has the name. The reply says what was renamed, and what was not and why. `/rename` always sends the name to Telegram, even when rex-tg thinks the topic has it, and the reply says what Telegram answered (renamed, already had that name, refused, or no answer). So sending `/rename` again fixes a topic whose name did not change. The name sticks; `/rename auto` names the topic after the agent's session again (so a topic cannot be named "auto"): the pane gets Rex's default name `shell` back, and the tab takes the topic's new name. Both forms replace a pane or tab name that you set yourself in Rex. `//rename` sends `/rename` to the agent, such as Claude Code's own command. An agent cannot rename its topic: asked to, it may rename its Rex session instead, and rex-tg names the session for `/new` back to `rex-tg`.
- A name starts with a letter or digit, because Telegram draws a topic's icon from its first character. Stopped topics (🔴) get the same cleanup. pi's `[folder][kind]` tags before a session name go: the folder tag is dropped, and other tags move to the end. Markdown marks (`**`, `` ` ``) and leading symbols go too.
- A new session in the same pane, such as after the agent's `/clear`, takes over the topic. The bot posts a divider, such as `── claude · new session (clear) ──`.
- After a Rex restart, the topic follows the session when the agent resumes in its pane.
- When an agent exits or its pane closes, the topic shows `🔴 agent stopped`, a running turn ends, and the topic's name gets a 🔴. A tab in the `/new` session gets the 🔴 too, until the agent runs again. Messages you send there are refused, with a reason.
- When you open a topic, the bot posts an intro card (session, pane, folder, mode) and the last 3 final answers (`backfill`).

### `/new`

- Tap the agent, then tap a folder or send a path such as `~/work/app`. cux is one of the agents when it is installed.
- The folder buttons, at most 6: the folders in `[rex] new_folders` first, in their order, then the folder of this topic's agent, then the folders where agents ran recently, newest first. A folder that no longer exists is left out, and so is a recent folder that you removed in the control panel ([`/rt`](#rt-control-panel)), until an agent starts a new session there.
- Every tab goes into one Rex session, `rex-tg` (`[rex] new_session`). rex-tg makes the session when it is missing. If an agent in it renames the session, rex-tg names it back within 30 seconds.
- A tab is named after its folder and agent, such as `app · pi`. The tab follows its topic name: at link time, and at the 30-second name check when its label differs. When you (or an agent, with `rex window rename`) rename the tab, rex-tg keeps that name, and the topic takes it at the next name check. Rename the tab back to its `<folder> · <agent>` form, such as `app · pi`, and rex-tg names the tab and the topic again. When the topic has only the folder name, the tab keeps the `<folder> · <agent>` form. Two tabs with one name get ` #2`, ` #3`. Tabs in other Rex sessions are never renamed.
- The agent starts as if you typed its name in a terminal, so your `~/.zshrc` applies.
- If the agent asks something before it starts, such as "trust this folder?", the question comes to the chat with a button per answer. A button moves the cursor there and presses Enter. rex-tg checks the screen after each key.
- In a topic, `/new` also starts another agent. `//new` sends `/new` to the agent.

### Waiting and sending

- A message you send while the agent works joins the current run at once, as typing on the Mac does. Claude Code reads it after its current tool. Set `[input] while_busy = "wait"` to wait until the agent is idle instead.
- A message still waits while a prompt is open, or for a moment right after another message was typed. Agent commands (`//compact`, `/model` buttons), shell commands (`!ls`), and the note of a prompt answer always wait until the agent is idle.
- When you answer a prompt, on the Mac or in Telegram, the waiting messages go in as soon as rex-tg sees the prompt gone. Claude Code and Codex send no signal for an answer, so rex-tg reads the screen: when the agent's transcript goes on, and every 3 seconds while the pane waits (every 15 seconds after the first minute). Key hints such as `Esc to cancel` at the bottom of the screen mean a prompt is still there.
- While the agent works, rex-tg reads the screen right before it types. If a prompt is on the screen, it waits instead of typing into it.
- Messages that wait share one notice, on the oldest one, after it waited 3 seconds. It says what they wait for and how many wait, such as `⏳ 2 messages wait until the prompt is answered.`, with `⚡ Send all now` and `✖ Cancel all` (`⚡ Send now` and `✖ Cancel` for one). `⚡ Send all now` sends the message the notice is on and your own messages behind it, up to the first agent command or shell command: that one still waits until the agent is idle, and so does what you sent after it, so the order stays. The notice goes away when the message is typed (👀, then 👍) or refused (👎).
- Waiting messages go in the order you sent them. Each one is its own turn.
- `⚡ Send now` types a waiting message at once. All three agents steer: the message joins the current run.
- Telegram splits long messages into parts. rex-tg joins parts that arrive close together into one prompt. The window is 300 ms, or 1500 ms for long messages.
- A reply tells the agent what you reply to. Your text comes first, then `In reply to:` and the replied message as a quote (`> …`), cut at 600 characters. If you quote part of a message in Telegram, only that part is sent. A rich-message answer reaches rex-tg without its text in a reply, so rex-tg quotes the text it sent. A reply to an answer that came as a `.md` file, or to a code file, quotes its start too; a reply to an image quotes nothing.
- This works for replies to the agent's messages (answers, tool lines, prompts typed on the Mac) and to your own messages. A reply to one of rex-tg's own messages (notices, alerts, cards) sends only your text. So do an agent command such as `//compact` and a shell command such as `!ls`.

### `/model` and `/effort`

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
- Its first icon is an hourglass (⏳ ⌛) that turns every 3 seconds while the agent works, and the timer moves with it, so you can see that the turn is still running. Set `[display] progress_animation` to change the pace, or to `"0s"` to stop it; then the icon turns only when the message changes anyway.
- Its timer moves on while the agent thinks or a long tool runs: every 10 seconds at first, then less often, at most once a minute later in a long turn. It stops while rex-tg waits for your answer.
- At the end of the turn, the tool rows stay in a collapsed quote under the summary line.
- A long answer comes as several messages of about 1500 characters, split at paragraphs and headings like a chat, never inside a code block, and without `(1/2)` numbers. Only the first one makes a sound. `message_length` sets the size (300 to 4000; 4000 splits only when Telegram's limit needs it). An answer that would take more than 6 messages comes in fuller ones, up to 4000 characters each. In rex-tg's own format, an answer longer than 16000 characters comes as a `.md` file.
- While the agent writes, the text shows at the bottom of the topic as a draft, Telegram's preview of a message being written. pi's text grows word by word. Claude Code and Codex show each finished block, because they write nothing earlier. A draft never becomes a message: the answer replaces it. Final mode shows no drafts. Drafts are off by default; set `stream = true` to turn them on. `stream_interval` sets how often the draft changes: `1s` is the default and the fastest, because drafts share Telegram's budget of about one message a second in a chat; a larger value such as `"3s"` leaves more of it to answers.
- Images named in a final answer come by themselves, as photos, up to 10 per answer. Set `send_images = false` to turn this off.
- When the answer names no image file, the images that the agent looked at during the turn come after it instead: the pictures in its tool results, such as a screenshot that it read with its `read` tool. This works for pi and Claude Code. The last 4 come (`turn_images`, 0 to 10; 0 turns this off), each image once. A photo that you sent from Telegram is never sent back. The pictures are saved for 3 days in the `shown` folder of the state folder, readable only by you.
- Files go to Telegram only from the agent's folder, your home folder, or a temporary folder, never from a hidden place such as `~/.ssh` or `.env`, or from `~/Library`. A tap checks the place again before the file goes.
- Other files named in a final answer (`pdf`, `csv`, `tsv`, `html`, `htm`, `xlsx`, `docx`, `pptx`) come only when you ask: a silent `📎 Files in this answer` message lists them with a button each. A tap sends that file as a document, once. Change the types with `send_files` (`[]` turns the offers off).
- Work on the Mac: when you type in the pane on the Mac, the topic gets one silent notice, `💻 In use on the Mac`, instead of the prompts and replies. The notice shows the latest prompt and whether the agent is working or done. `📄 Last answer` posts the newest answer. Write in the topic to go on in Telegram: from then on, replies come as usual. Permission prompts still come.
- Set `mac_notice = false` to show Mac turns instead. `💻 You (on Mac)` then shows the prompts typed on the Mac. Prompts that no person typed, such as Claude Code's task notifications, are not shown. Set `show_mac_prompts = false` to hide all of them.

### Permission prompts

- Tap a button to send its key to the pane. The buttons go away when the prompt ends. A button of an old prompt is refused.
- `/approve` and `/deny` answer the open prompt too. `/deny` presses Esc, which both Claude Code and Codex read as "no, wait for me"; it never presses an answer that allows, makes a lasting rule, or starts something else.
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
  - `↗ Topic` goes to its topic, and the panel closes: Telegram would otherwise open the topic behind it. An agent without a topic has `Open topic` instead, which makes one, like a tap in `/agents`. A row with a topic also has a `↗` shortcut.
  - `🖥 Screen`, `📂 Files`, and `📝 Changes` open the topic's live screen, its files, and its changes in the Mini App, as `/screen`, `/files`, and `/changes` do. `‹ Panel` or Telegram's back button goes back to the panel. They need a topic; Files and Changes also need `[miniapp] files` and `[miniapp] changes`.
  - `✏️ Rename` renames the topic, the Rex pane, and its tab, like `/rename`. `Auto name` names them after the session again. An agent without a topic gets its pane and tab renamed.
  - `🔌 Unlink` stops mirroring the session, like `/unlink`. The topic stays as history. The panel asks first.
- `Topics` lists every topic that rex-tg mirrors, live or stopped, with where its agent runs in Rex: `session › tab › pane`, or `agent stopped`. Tap a topic for its actions: `↗ Open topic`, `✏️ Rename` (the topic, the pane, and the tab, as `/rename`; it needs a running agent), and `🔌 Unlink`. Filter it by live or stopped, or search it. `Clean up stopped` unlinks every topic whose agent stopped, after a confirm, as `/unlink` would in each; the topics stay in Telegram as history.
- `New agent` starts an agent in a new Rex tab, like `/new`. Pick the agent, then a folder: one from the list of folders, or one that you find with the folder explorer. The list has the folders in `[rex] new_folders` (📌) and the folders where agents ran recently. `✕` removes a recent folder from the list, and `Clean up` removes them all, also the ones further down; pinned folders stay. A removed folder also leaves `/new`'s buttons, and comes back when an agent starts a new session there. An agent that keeps running there does not bring it back. The explorer starts in your home folder and never leaves it. Tap a part of the path at the top to go back to it, or type in the filter to narrow the folders. A `GIT` badge marks a git repository. It shows folders only, not files, and a link to a folder only when the folder is inside your home folder too. `Hidden` also shows names that start with a dot. Telegram's main button at the bottom starts the agent, such as `Start Claude Code in ~/work/app`. The panel starts an agent only in a folder inside your home folder, or in one that `/new` offers; use `/new <agent> <folder>` for any other folder. The progress and the new topic come to the main chat. The panel goes to `Agents` and shows `Starting …` until the new agent is in the list; then it opens and marks that agent.
- While typing is paused, the panel says so. `/pause` and `/unpause` stay chat commands, so init data that leaked cannot undo a `/pause`. The panel starts no agent while typing is paused.
- Each tap acts at most once. The page sends a random id with each action, and rex-tg keeps the ids for a day, so a request that the network repeats does nothing a second time. When rex-tg cannot say yet how an action ended (it was busy, or the action still runs), the panel says so. Tap again to ask: the page sends the same id, so the action does not run twice.
- The panel reads with the same signature as every page (24 hours). An action needs a signature from the last hour, so init data that leaked can change less. Close the panel and open it again with `/rt` when it says so.
- It needs the Mini App.

### Files and photos

- Photos and files are downloaded up to `max_upload_mb` (20 MB). They are saved under the state folder, and their path is typed into the pane with your caption.
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
new_session = "rex-tg"                  # The Rex session where /new opens its tabs.
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
answers = []                            # Fallback permission answers. pi has none.

[agents.pi.choices]                     # Optional. Buttons of /model and /effort, by command name. Replaces the agent's own list.
model = ["anthropic/claude-sonnet-5-5", "openai/gpt-6-sol"]   # Same for [agents.claude.choices] and [agents.codex.choices].

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
label = "No"
keys = ["escape"]

[display]
mode = "progress"                       # progress, block, or final.
backfill = 3                            # Final answers to post when a topic opens.
show_mac_prompts = true                 # Post prompts that you type in the pane on the Mac (when mac_notice is off).
mac_notice = true                       # Hold turns typed on the Mac behind one notice, until you write in Telegram.
progress_delay = "2s"                   # Wait before the progress message appears.
progress_edit_interval = "2s"           # Least time between two edits of the progress message.
progress_animation = "3s"              # How often the progress message turns its hourglass and moves its timer. "0s" turns it off; at least 2s.
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
answer_preview = 3000                   # Longer answers show their start and open whole in the Mini App. At least 2000.
files = true                            # /files opens a read-only browser of the agent's folder in the Mini App.
changes = true                          # /changes shows what changed in the agent's git repository in the Mini App.

[voice]
whisper_cli = ""                        # Path to whisper-cli. Empty turns voice notes off.
model = ""                              # Path to a ggml model file. Use an absolute path.
language = ""                           # Language code for whisper.cpp, such as "en".

[state]
dir = "~/.local/state/rex-tg"           # Database, spool, logs, and uploads.
```

### Mini App

The Mini App shows what does not fit a chat message. Its pages open from buttons, and only for you:

- `📄 Whole answer`: an answer longer than `[miniapp] answer_preview` (3000 characters by default) shows its start in the chat. The whole answer, with its tables and code, is in the Mini App. Such an answer is not sent as one rich message.
- `📝 2 files · +40 −12`: what the turn changed in the agent's git repository, file by file. rex-tg takes a snapshot of the working files (tracked and new, not ignored) when a turn starts and when it ends with its answer. The snapshots use a copy of git's index, so your own index stays as it is. They write objects into the repository, which git cleans up later. Other agents that work in the same repository at the same time show up in it too. Secrets are removed from the diff. A folder that is not a git repository gets no button. Set `[display] show_changes = false` to turn it off. A repository with more than 300 new files that are not ignored, or more than 20 MB of them, gets no button: the snapshot would be slow.
- `🖥 Live screen`, under a `/screen` picture: the pane's screen with its colors, drawn as a terminal (xterm.js), read again every 2 seconds while the page is open, and not while it is hidden. It fills the phone's height and keeps the bottom line in view; scroll up to read back, and `↓` goes to the bottom again. `Pause` stops the refresh while you read. `Fit width` makes the font smaller, so a wide screen fits the phone. Secrets are removed, as on every screen; a line that held one shows without colors. The page only shows the screen; it types nothing. A topic has one live screen page, so all its buttons open the same page.
- `📂 Browse files`, from `/files`: the agent's folder as a tree, read-only. Tap a folder to open or close it in place, and a file to read it; `‹ Files` or Telegram's back button goes back to the tree as you left it. `Collapse all` closes every folder, and `↻` loads the tree again. Markdown files are rendered, other text is shown as it is, with secrets removed, and pictures (PNG, JPEG, GIF, WebP up to 4 MB) are shown. Of a long text file, the first 512 KB are shown; a binary file shows only its size. The browser never leaves the folder: a path with `..` stays inside it, and a link that points out of it does not open. It hides `.git`, files that git ignores (such as `.env` and `node_modules`), and files with names of keys and credentials (`.env*`, `*.pem`, `*.key`, `id_rsa*`, `credentials.json`, and the like, in any case; `.env.example` is shown). In a folder that is not a git repository, it also hides every name that starts with a dot. A link to a hidden file does not open either. Folders with more than 2000 entries show the first 2000.
- `📝 View changes`, from `/changes`: the changes of the agent's git repository. Three tabs pick what to compare: `Uncommitted` (since the last commit), `This branch` (since the branch left the main branch), and `Last turn`. A tab without anything to compare is dimmed, and tapping it says why. The view opens on the first tab that has changes. The changed files show as a tree, with a letter for each: `M` changed, `A` added, `D` deleted, `U` new and not tracked, `T` turned into a link or back, `!` in conflict. Tap a file for its diff, with the old and the new line numbers. `‹` and `›` go to the previous or next file, and `Wrap` wraps long lines. The view reads git each time it opens, so it shows the changes as they are now. It hides what the file browser hides: ignored files are not listed, and a file with the name of a key or credential is listed with 🔒 but its changes are not shown. Secrets are removed from every diff. A list shows at most 2000 files, and a diff at most 1 MB. Pictures that changed are shown as they are now.

- `🎛 Open control panel`, from `/rt`: the agents and their topics, with actions on them, and a folder explorer to start a new agent ([`/rt`](#rt-control-panel)). The control panel acts: it opens and unlinks topics, renames agents (topic, pane, and tab), and starts agents. It never types into a pane. There is one control panel page.

Every page has a bar at the top. `A−` and `A+` make the text smaller or bigger, and the percentage between them sets it back to 100%. ☀/🌙 switches between light and dark; tapping back to Telegram's own scheme follows Telegram again. The page keeps your choices on the phone.

The Mini App needs a public HTTPS address that reaches this Mac. It is off until `url` is set. With Tailscale:

1. Start the Funnel for rex-tg's port: `tailscale funnel --bg --https=8443 http://127.0.0.1:8770`. `--bg` keeps it running after the command ends. Funnel works on ports 443, 8443, and 10000. Funnel must be on for your tailnet: HTTPS certificates in the admin console, and the `funnel` attribute in your access controls. `tailscale funnel status` shows its address.
2. Put the address in the config. Its port must be the Funnel's port, and the Funnel's target must be the `listen` address:

```toml
[miniapp]
url = "https://<your-mac>.<tailnet>.ts.net:8443"   # https:// only. Empty turns the Mini App off.
listen = "127.0.0.1:8770"                           # the local target of the Funnel; keep it on 127.0.0.1
answer_preview = 3000                               # longer answers open in the Mini App
files = true                                        # /files: browse the agent's folder
changes = true                                      # /changes: what changed in the agent's repository
```

Restart rex-tg with `launchctl kickstart -k gui/$(id -u)/local.rex-tg`. Then check the log: `mini app: serving` means the server runs. Send a long answer and tap `📄 Whole answer` to test it.

Pages are reachable only through Telegram's signed requests, and only for the owner. Each page request carries the Mini App's init data, which Telegram signs for the account that opened it. rex-tg checks the signature with a key made from the bot token. It refuses a signature that is more than 24 hours old, or one that is not the owner's, and the log says `mini app: refused a request`, with the reason. The shell files `/app` and `/app.js` hold no data, so they need no signature. Pages are kept for 30 days.

Funnel makes the server reachable from the internet, so keep `listen` on `127.0.0.1`. To turn the Mini App off, set `url = ""` and restart rex-tg. `tailscale funnel reset` removes every Funnel setting of this Mac.

Answers are kept for 30 days, changes for 14, live screens, file browsers, changes views and the control panel for 7 days after the last `/screen`, `/files`, `/changes` or `/rt` that sent their button, and at most 500 pages in all. While the Mini App's server is not running, answers come whole in the chat.

### Tables and rich messages

A code block with a line longer than 80 characters does not fit a phone. It stays in the message as a short preview (3 lines), and the whole block comes right after as a file, such as `code-1.go` (named by the block's language, else `.txt`). Tap it to open or copy it.

A table that is wider than a phone (more than 40 characters) becomes a list: one block per row, with the first cell in bold and `Column: value` for the others. A narrower table stays a monospace grid.

Final answers go out in Telegram's rich message format (`[display] rich_messages`, on by default), so Telegram renders tables, code, and headings itself. An answer of up to 32768 characters goes as one rich message. If Telegram rejects one, rex-tg sends the answer again in its own format (Telegram HTML, with the tables as above): as one message when it is up to 4000 characters, else as a `.md` file. Set `rich_messages = false` to always use rex-tg's own format. There, an answer longer than 16000 characters comes as a `.md` file. With the Mini App set up, an answer longer than `answer_preview` shows its start instead (see [Mini App](#mini-app)).

## Voice notes

Voice notes need ffmpeg and whisper.cpp.

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

rex-tg converts a voice note to 16 kHz WAV with ffmpeg, and whisper-cli transcribes it. The bot replies with 🎤 and the text. The text then goes to the agent like any message.

When voice is off, the bot replies `Voice notes need whisper.cpp; see the README.`

## Logs

- `rex-tg status` reads only the database.
- The daemon log `~/.local/state/rex-tg/logs/rex-tg.log` has one JSON object per line. It rotates at 10 MB and keeps 3 old copies.
- launchd writes `launchd.out.log` and `launchd.err.log` to the same folder.
- Watch the log: `tail -f ~/.local/state/rex-tg/logs/rex-tg.log`.

## Uninstall

`rex-tg uninstall` removes the Claude Code and Codex hook entries, the pi extension, and the launchd job. It keeps the database, the file backups, and the token.

- `--only pi`, `--only claude`, or `--only codex` removes one agent's parts. The launchd job stays.
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
| Rex is not running | All topics show as offline. rex-tg retries every 10 seconds. It never starts Rex. Start Rex Beta. If `doctor` cannot find the Rex CLI, set `[rex] cli`. |
| No owner is paired | Run `rex-tg pair`, then send `/start <code>` to the bot. |
| "waiting for session" | rex-tg learns the session file at the agent's next prompt or turn end. |
| "message thread not found" | You deleted the topic in Telegram, so rex-tg closed it. Open the agent again with `/agents`. |
| "This topic is not linked" | The topic is closed, or rex-tg did not make it. Open the agent again with `/agents`. |
| "Checking whether it arrived" | Telegram did not confirm a reply. rex-tg checks the chat's message numbers. A reply that never arrived is sent again by itself. |
| "may not have arrived" | The reply may be in the chat already, so rex-tg does not send it again by itself. Tap `🔁 Send again`, or send `/resend`, if it is missing. |
| "may be in the input box without Enter" | The daemon stopped between the paste and Enter. Check the pane with `/screen`. rex-tg does not type the text again. |
| `/new`: "exited with code 127" | The shell did not find the agent. Set its full path, for example `[agents.codex] launch = ["/opt/homebrew/bin/codex"]`. |
| `/new`: "has not reported its session yet" | The agent may wait for something on its screen, such as a login. The message shows the screen. Deal with it on the Mac, then open the agent with `/agents`. |
| `/new`: "did not react to a key" | The startup question did not move its cursor. Answer it on the Mac. |
| Telegram returns 409 Conflict | Another program uses the same bot token. Only one daemon may poll Telegram. |
| Mini App page does not open | Check that `[miniapp] url` is the Funnel's `https://` address with the Funnel's port, and that the Funnel runs (`tailscale funnel status`). `mini app: serving` in the log means the server runs. See [Mini App](#mini-app). |
| The log says `mini app: listen` | The `[miniapp] listen` address is in use or not valid. The buttons still show, but their pages do not open. Set `listen`, then restart rex-tg. |
| Telegram says the bot was blocked | Unblock the bot in Telegram. |
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
- The database keeps message text. rex-tg deletes sent messages after 30 days.
- Only you can read the state folder and the spool folder.
- Before rex-tg types, it checks that the sender is the owner, the topic is live, the agent is in front in its pane, and no permission prompt is open. `/force` skips only the last check.
- rex-tg makes outgoing connections to Telegram and to Rex, and never starts a Rex server. Its one listening port is the Mini App's. It exists only when `[miniapp] url` is set (see [Mini App](#mini-app)).
