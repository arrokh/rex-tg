# Security policy

rex-tg can send input to terminals on your Mac. Its owner checks, target checks, and redaction are safeguards, not a sandbox for an agent or its tools.

## Report a vulnerability privately

Do not include credentials, bot tokens, keyboard-key URLs, real transcripts, screenshots, or exploit details in a public issue or PR.

Use GitHub's **Security → Report a vulnerability** option for this repository **if it is available**. If that option is not enabled, ask the maintainer for a private reporting channel through an issue with only a neutral request for contact. Do not post the vulnerability details until a private channel is agreed. This repository does not currently promise a response deadline or a supported-version matrix.

Useful report details: affected version, synthetic reproduction, expected authorization or safety check, observed result, and likely impact. Share only the minimum information needed to investigate.

## Deployment boundaries

- Run one daemon per bot token. Pair only the account that should control your Mac.
- Enable Telegram two-step verification and protect the Mac account and Keychain. Bot chats are not end-to-end encrypted.
- Keep the Mini App on loopback behind **Tailscale Serve** with `tailnet_only = true`. Do not use Funnel. A local process can impersonate proxy headers; this is not isolation from untrusted programs on the Mac.
- In a shared tailnet, set `tailnet_users` before first use. Otherwise the first keyboard-key login can be learned as trusted.
- Keyboard-key URLs allow reads and validated control-panel actions. They are not harmless share links. `/rekey` revokes them; it does not remove data already disclosed.
- `/pause` blocks typing. It does not stop outgoing replies, revoke the Telegram account, or stop agents already running on the Mac.
- `/force` bypasses ordinary readiness and prompt waiting. `/key`, `/approve`, and startup-answer buttons intentionally send input. Read the screen and prompt before using them.
- Redaction is best effort. The database holds message text; uploads and captured page content can contain sensitive information. Review all output before sharing it.

## Delivery and recovery limits

Uncertain sends and typing are not automatically repeated on a guess. `/resend` is an explicit owner action and can duplicate a message that did arrive. At-most-once-oriented recovery is not guaranteed exactly-once delivery.

The [hardening review](docs/reviews/implementation-hardening.md#remaining-issues-and-limits) records known reliability follow-ups, including an asynchronous control/note preparation crash window and unbounded tool history during very long turns. It is not a complete security audit.

See [configuration and privacy](docs/reference.md#privacy) and [architecture trust boundaries](docs/architecture.md#trust-boundaries).
