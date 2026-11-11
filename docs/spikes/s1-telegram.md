# Spike S1: Telegram topics in the private chat

Status: mostly done, 2026-11-11, in a live test with the paired bot and a phone. Three small checks are still open (end of this file).

Setup: a real bot made with @BotFather, Threaded Mode turned on, rex-tg installed on the Mac, and the Telegram app on an iPhone.

## Findings

### Threaded Mode

- `getMe` reports `has_topics_enabled = true` after Threaded Mode is turned on in @BotFather (Bot Settings → Threaded Mode). `rex-tg login` shows it.

### There is no main chat

- With Threaded Mode on, the private chat with the bot shows a tab bar: a **New Chat** button and one tab per topic.
- The first message that the user sent (`/start <code>`) did not go to a main chat. Telegram created a new topic for it and named the topic after the message text.
- So every message from the user arrives with a `message_thread_id`. A design that expects commands such as `/agents` in a main chat (no topic) does not work.
- Change made: a topic that is not linked to an agent now works as the main chat. Main-chat commands answer in that topic. The pairing answer and the open-topic link also go to the topic they came from (`internal/daemon/updates.go`, `pairing.go`, `topics.go`; test `TestThreadedPrivateChatHasNoMainChat`).

### Pairing

- The first pairing failed because the code had expired: it was made at about 13:03 and reached the daemon at 13:15:54. Before the fix, the bot said nothing in that case.
- Change made: a correct code that has expired now gets the answer "This pairing code has expired. Run `rex-tg pair` on your Mac for a new one." A wrong code still gets no answer.

### Topics and the agent list

- `/agents` in a topic listed the three running pi agents. **Open** created the topic with `createForumTopic`, and the intro card and the last three answers arrived in it (step 3, first part).
- Once, `createForumTopic` did not answer in time during a network burst. The second tap worked. Before the fix, the alert said "Is Threaded Mode on for the bot?", which was wrong for a timeout, and the answer to the button press may already have been too old to show.
- Change made: when Telegram's answer is unknown, the chat gets "Telegram did not answer in time. If no new topic appeared, open the agent again from /agents." Other errors keep the Threaded Mode hint (tests `TestTopicCreateWithoutAnAnswerSaysToTapAgain`, `TestTopicCreateRefusedMentionsThreadedMode`).
- Telegram turned the folder name `resume.md` into a web link. Change made: folder names are shown in `<code>`, and session titles get a zero-width space after such a dot (`noAutoLink` in `internal/daemon/names.go`).

### Typing into a vim-style editor

- A message sent in the topic got 👀 (delivered) but never reached pi: no `user` line appeared in the transcript. The pane's input box showed `──── NORMAL` at the right end of its top border. The pi-vim package (a modal editor for pi) was in NORMAL mode, and it dropped the paste.
- Change made: before the paste, rex-tg reads the mode label from the screen and presses one key at a time until pi-vim shows INSERT (design section 9.5; tests `TestPiVimReachesInsertBeforeThePaste`, `TestPiVimThatDoesNotReactGetsNoMoreKeys`, `TestInterruptInPiVimLeavesInsertModeFirst`).
- The message that was dropped stays `delivered`. rex-tg does not type it again (at most once).
- Claude Code's `/vim` mode was checked in its source (version 2.1.322), not live: a paste goes in whatever the mode, so Claude Code gets no extra keys.

### Deleted topics, names, and the menu

- The user deleted a topic in Telegram, and later tapped the agent's `↗` button in `/agents`. Telegram said "Message doesn't exist": the button was a link to the deleted topic, and Telegram does not tell the bot about a deleted topic.
- Change made: the button now reports the tap. rex-tg sets the topic's name with `editForumTopic`, which fails with "message thread not found" for a deleted topic, and then makes a new topic for the same session (test `TestDeletedTopicIsMadeAgainFromAgents`). The exact error text from Telegram in a private chat is still to be seen live; `TOPIC_ID_INVALID` and `TOPIC_DELETED` are handled too.
- Topic names were too long (`pi · resume.md · [resume.md][fix] Update the likely follow ups…`). Change made: a short name from the Rex pane's own name or the session name (design section 7).
- The menu did not show the rex-tg commands. `rex-tg doctor` found a list for the owner's chat, left by another program: `/commands /sessions /sync /upgrade`. Telegram shows that list before the default one. Change made: rex-tg also registers its menu for the owner's chat.
- The pi-vim fix now also cancels a pending vim command or an EX line with Escape, instead of refusing the message.

### A network stall and a chain of notices

- At 15:34 a reply was sent while the connection stalled: the request was written, and no answer came within the 15-second timeout. The reply was marked `unknown`, as designed. Its "may not have arrived" notice was sent into the same stall and became `unknown` too, which made a notice about the notice, and so on: four notices, 15 seconds apart, until the network came back.
- Tapping `/resend` in a notice sent the command without its id ("Usage: /resend <id>"). `/resend 15` and `/resend 14` then sent notices again, not the lost reply (#12).
- Changes made: a notice never gets a notice; the notice has a `🔁 Send again` button for the reply it is about; `/resend` without an id takes the newest reply that may not have arrived, and the id of a notice stands for its reply. A request that never left the Mac (no connection, a TLS handshake timeout) is now "not sent" and is retried by itself; only a request that was written and got no answer is `unknown` (design section 9.1).

### Network

- From this Mac, the connection to `api.telegram.org` failed in short bursts: `connection reset by peer`, then `TLS handshake timeout` for about a minute, at 13:08, 13:15, 13:18, and 13:23. Between the bursts, `curl` connected in about 0.5 s.
- The daemon retries and recovers without help. A message can arrive a minute or more late during a burst.
- The cause is outside rex-tg (the local network or the provider). It is not investigated further here.

## Confirmed in the live test (2026-11-11)

- Steps 3–4: `editForumTopic` renames topics in the private chat (a topic went from "pi · personal" to "[personal] Cek email dan…" when its session got a name). On a deleted topic it fails with an error that rex-tg reads as "thread not found", so the topic is closed (log "topic is gone; closing it").
- Step 5: the topic list shows several topics as tabs (New Chat, agent topics, and a topic Telegram made from a message).
- Step 7: the topic link `https://t.me/<bot>/<message_thread_id>` opens the topic; for a deleted topic Telegram says "Message doesn't exist" (handled, see above).
- Steps 8–9: the waiting notice ("⏳ Waiting until the agent is idle.") and the summary line ("✅ Done · 12s · 0 tools") show as designed.
- Step 10: Telegram accepts the reactions: 👀 showed on a typed message, and the 👍 calls of acknowledged messages logged no error.
- Step 11: `getMyCommands` for the owner's chat returns the 21 rex-tg commands (`rex-tg doctor`).

## Still to check

- `closeForumTopic` in the private chat. rex-tg uses it only when linking a new topic fails, so normal use does not reach it.
- The expandable quote of the summary line with tool rows (the turns seen so far used no tools).
- The command menu as the phone shows it after the chat-scoped menu replaced the old one (Telegram may cache the old menu until the chat is reopened).
