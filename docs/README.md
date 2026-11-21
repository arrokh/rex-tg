# Documentation

[Project README](../README.md) · [Quick start](quickstart.md)

rex-tg connects an agent in a local Rex pane to a topic in your private Telegram bot chat. The agent keeps running on your Mac. Tailscale is only for the optional Mini App.

## Use rex-tg

| Your task | Guide |
| --- | --- |
| Install, create a bot, and send your first prompt | [Quick start](quickstart.md) |
| Find an agent, check its screen, or schedule a prompt | [Daily controls](quickstart.md#daily-use) |
| Look up a CLI command, bot command, or button | [Command catalog](architecture.md#feature-and-command-catalog) |
| Understand a wait or an uncertain send | [Waiting and sending](reference.md#waiting-and-sending) |
| Change settings or add an optional feature | [Configuration](reference.md#configuration) · [Mini App](reference.md#mini-app) · [Voice notes](reference.md#voice-notes) |
| Restart, upgrade, or remove rex-tg | [Run, stop, and update](reference.md#run-stop-and-update) · [Uninstall](reference.md#uninstall) |
| Fix a problem or understand what data leaves your Mac | [Troubleshooting](reference.md#troubleshooting) · [Privacy](reference.md#privacy) |

## Understand or improve the project

- [Architecture](architecture.md): components, data flow, recovery, and limitations.
- [Design](design.md), [package contracts](implementation.md), and [transcript formats](agent-transcripts.md): implementation details for contributors.
- [Contributing](../CONTRIBUTING.md) and [security policy](../SECURITY.md): how to help and where to report problems.

## Related tool

[Rex Annotation](https://github.com/arrokh/rex-annotation) lets you quote and comment on an agent reply in Rex using Neovim. Use it when you want a local, keyboard-first review workflow. rex-tg's [Annotate](reference.md#annotate) is a separate feature for reviewing replies from Telegram; neither tool requires the other.
