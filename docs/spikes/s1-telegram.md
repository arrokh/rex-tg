# Spike S1: Telegram topics in the private chat

Status: partly done, 2026-11-11. The steps below that need the paired bot and a phone are still open.

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
- Change made: when Telegram's answer is unknown, the chat gets "Telegram did not answer in time. If no new topic appeared, tap Open again." Other errors keep the Threaded Mode hint (tests `TestTopicCreateWithoutAnAnswerSaysToTapAgain`, `TestTopicCreateRefusedMentionsThreadedMode`).
- Telegram turned the folder name `resume.md` into a web link. Change made: folder names are shown in `<code>`, and session titles get a zero-width space after such a dot (`noAutoLink` in `internal/daemon/vim.go`).

### Typing into a vim-style editor

- A message sent in the topic got 👀 (delivered) but never reached pi: no `user` line appeared in the transcript. The pane's input box showed `──── NORMAL` at the right end of its top border. The pi-vim package (a modal editor for pi) was in NORMAL mode, and it dropped the paste.
- Change made: before the paste, rex-tg reads the mode label from the screen and presses `i` (or `escape i` in VISUAL mode). A pending vim command is refused with a reason. `/interrupt` presses one more `escape` in INSERT mode, because pi-vim takes the first one to leave INSERT (design section 9.5; tests `TestVimNormalModeEntersInsertBeforeThePaste`, `TestVimPendingCommandIsRefused`, `TestInterruptInVimInsertModePressesEscapeTwice`).
- The message that was dropped stays `delivered`. rex-tg does not type it again (at most once).
- Claude Code's `/vim` mode was checked in its source (version 2.1.322), not live: a paste goes in whatever the mode, so Claude Code gets no extra keys.

### Network

- From this Mac, the connection to `api.telegram.org` failed in short bursts: `connection reset by peer`, then `TLS handshake timeout` for about a minute, at 13:08, 13:15, 13:18, and 13:23. Between the bursts, `curl` connected in about 0.5 s.
- The daemon retries and recovers without help. A message can arrive a minute or more late during a burst.
- The cause is outside rex-tg (the local network or the provider). It is not investigated further here.

## Still to check

- Steps 3–4: `editForumTopic` and `closeForumTopic` by the bot in the private chat (`createForumTopic` works, see above).
- Step 5: how the topic list looks with several agent topics.
- Step 7: the topic link `https://t.me/<bot>/<message_thread_id>`.
- Steps 8–9: progress message edits, deletes, silent sends, and the expandable quote of the summary line on the phone.
- Step 10: whether Telegram accepts the reactions 👀, 👍, and 👎 (rex-tg logs a warning when it does not).
- Step 11: the command menu inside a topic.
