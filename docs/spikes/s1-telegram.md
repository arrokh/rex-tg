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

### Network

- From this Mac, the connection to `api.telegram.org` failed in short bursts: `connection reset by peer`, then `TLS handshake timeout` for about a minute, at 13:08, 13:15, 13:18, and 13:23. Between the bursts, `curl` connected in about 0.5 s.
- The daemon retries and recovers without help. A message can arrive a minute or more late during a burst.
- The cause is outside rex-tg (the local network or the provider). It is not investigated further here.

## Still to check

- Steps 3–4: `createForumTopic`, `editForumTopic`, and `closeForumTopic` by the bot in the private chat (the `/agents` → Open flow exercises the first one).
- Step 5: how the topic list looks with several agent topics.
- Step 7: the topic link `https://t.me/<bot>/<message_thread_id>`.
- Steps 8–9: progress message edits, deletes, silent sends, and the expandable quote of the summary line on the phone.
- Step 10: whether Telegram accepts the reactions 👀, 👍, and 👎 (rex-tg logs a warning when it does not).
- Step 11: the command menu inside a topic.
