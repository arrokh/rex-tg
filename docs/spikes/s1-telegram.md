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

## Topic renames that the app does not show (2026-11-14)

- `/rename rhasia-scret` in a topic: `editForumTopic` answered `true` both times (09:02 and 10:51), and rex-tg stored the name. An automatic rename at 09:00, to the session's name, had shown. The topic still showed the old name "Create a worktree for API…" in Telegram for iOS, in the topic list and in its header, almost two hours later.
- This is a known Telegram bug: [tdlib/telegram-bot-api#915](https://github.com/tdlib/telegram-bot-api/issues/915). In a private chat with topics, Telegram saves the new name but makes no "topic edited" service message and sends no update. An app that already knows the topic keeps the old name until it loads its topics again; Telegram for Android keeps it until its local data is cleared. A Telegram developer answered there: "This will be fixed after updates of user apps. There is no way to fix this from the bot's side."
- Earlier renames did show (the "personal" and "explore-lovina" topics), probably because the app had loaded its topics again since.
- Change made: the reply of a rename that Telegram accepted says that the app may show the old name until it reloads its topics. Only the name given to `createForumTopic` shows for sure.

## A 429 that stopped every reply for over an hour (2026-11-14)

- At 17:23 every send stopped: answers, progress edits, the agents board. The user's messages still reached the panes (getUpdates worked), so the topic showed the messages without a reply or a reaction.
- A goroutine dump showed the sender asleep in one bucket pause with 88 minutes left. Only a 429 `retry_after` pauses the bucket that long. The 429 was not logged, so its method is not known.
- After a restart, one held answer (a rich message) went out, and the next one met a 429 again at once.
- After the upgrade at 18:06 (v0.3.3, which held one method at a time), sendRichMessage, sendMessage, and editMessageText each answered 429 with the same time left: about 51 min 45 s. The wait ended at about 18:58. The times fit one wait for the whole chat, not one wait per method.
- An audit of the requests found that about 75% were progress edits. They ran at about 1 request a second for about 10 minutes, in 2 or 3 topics with long turns. The audit points to these edits as the cause.
- Changes made (the final design, design section 9.3):
  - A 429 with a wait of 5 min or more holds the whole chat. The client refuses every other Bot API call until the wait ends, without asking Telegram. `getUpdates`, `getMe`, `getFile`, the command-menu calls, and the webhook calls still go.
  - The hold is kept in the store (KV `telegram_hold`), so a restart does not post during the wait.
  - When the wait ends, the main chat gets one silent note: "⚠️ Telegram held rex-tg's messages from HH:MM to HH:MM, because rex-tg sent too many (429 Too Many Requests). Nothing was lost: the held messages are sent now." The held messages go out when the wait ends, and some may come before the note.
  - There is no fallback from rich messages to plain messages. A rich answer that meets a 429 waits like any other message. A 400 still sends the answer in rex-tg's own format.
  - Typing, drafts, and answers to button presses fail at once during a hold. `/status` shows the wait in one line.
  - Prevention: `display.progress_animation` is off by default (was 3 s), and `display.progress_edit_interval` is 5 s (was 2 s). Progress edits share a budget of 20 per minute for the chat (burst 3). After a 429 from Telegram, the budget runs at half rate for one hour.
  - Every 429 from Telegram is logged with its method and wait.
- Open question: Telegram does not say whether a wait covers one method or the whole chat. A Telegram developer wrote that `retry_after` is for one API call (see Sources below). The same time left on three methods does not fit that. rex-tg holds the chat as a precaution. No source shows that a request during a wait makes it longer.

### Sources for the 429 rules

- **Official.** [Telegram Bots FAQ](https://core.telegram.org/bots/faq): "In a single chat, avoid sending more than one message per second. We may allow short bursts that go over this limit, but eventually you'll begin receiving 429 errors." The same FAQ says that a bot cannot send more than 20 messages a minute in a group. It gives no limit for edits in a private chat. The [Bot API reference](https://core.telegram.org/bots/api) defines `retry_after` as "the number of seconds left to wait before the request can be repeated." It does not say whether the wait covers one method or the whole chat.
- **Telegram developer, not in the docs.** levlam, in [tdlib/telegram-bot-api#946](https://github.com/tdlib/telegram-bot-api/issues/946) (13 Nov 2026). The issue asks whether a bot must handle `retry_after` for all API calls. The answer: "The received retry-after time is for specific API call."
- **Anecdotal.** A user, in [tdlib/telegram-bot-api#535](https://github.com/tdlib/telegram-bot-api/issues/535) (2024), reported that the `retry_after` window grew with each recurring 429, when using a local Bot API server. A Telegram developer answered that the server gives the same values as api.telegram.org. The user waited out each window, so the report does not show that a request during a wait makes it longer.
- **Unofficial.** [grammY: flood limits](https://grammy.dev/advanced/flood) says that bots can only do up to 20 message edits a minute in a group chat. It calls this a known limit found outside the official docs.

The budget of 20 edits a minute for progress is rex-tg's own choice (`sender.Options.EditsPerMinute`). The official FAQ and grammY both give 20 a minute for groups. No source gives a limit for edits in a private chat.

## A folder that loaded forever in the Mini App (2026-11-16)

- In the control panel's folder explorer, Documents showed loading rows and never opened. The page had no time limit, and the crumbs still showed `~`, the folder before.
- A goroutine dump showed two folder requests stuck in `os.ReadDir` (an `open` call) of a 23-byte path in the home folder (`~/Documents` or `~/Downloads`), for 3 minutes.
- On the Mac, macOS showed "“rex-tg” would like to access files in your Documents folder." with Don't Allow and Allow. macOS asks before a program reads Desktop, Documents, Downloads, iCloud Drive, or a network volume, and the read waits until someone answers. rex-tg runs from launchd with no window, so the user on the phone did not see the question.
- rex-tg is signed ad hoc by the Go linker (`codesign -dv`: `Signature=adhoc`, `Identifier=a.out`). macOS probably treats each upgrade as a new program, so it may ask again after an upgrade. Not checked yet.
- Change made: the folder explorer and the file browser answer within 8 seconds. A read that waits goes on in the background, and the page says that macOS may be asking on the Mac (design section 8.9a, control panel; test `TestFolderThatWaitsForMacOS`). The page names the folder that opens, and Try again opens that folder, not the one before.

## Still to check

- `closeForumTopic` in the private chat. rex-tg uses it only when linking a new topic fails, so normal use does not reach it.
- The expandable quote of the summary line with tool rows (the turns seen so far used no tools).
- The command menu as the phone shows it after the chat-scoped menu replaced the old one (Telegram may cache the old menu until the chat is reopened).
