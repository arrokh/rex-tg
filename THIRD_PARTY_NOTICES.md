# Third-party notices

The root [MIT License](LICENSE) covers rex-tg's original code and documentation. It does not replace the licenses of third-party components, or license Rex, Telegram, Tailscale, agent CLIs, models, macOS fonts, or other external products.

## Vendored browser assets

| Component | Version | Files | License |
| --- | --- | --- | --- |
| xterm.js (`@xterm/xterm`) | 5.5.0 | `internal/miniapp/third_party/xterm-5.5.0.js` and `.css` | MIT; full copyright and permission notice in [LICENSE-xterm](internal/miniapp/third_party/LICENSE-xterm) |

These assets are embedded in the binary and served by the Mini App. Keep the full notice with copies and redistributions. [Vendoring provenance and integrity](internal/miniapp/third_party/README.md) records the upstream package and update process. The original notice is retained unchanged.

The Mini App also loads Telegram's Web App JavaScript SDK from Telegram's origin at runtime; that remote resource is not relicensed by this project.

## Go dependencies

Exact direct and indirect dependency versions are in [go.mod](go.mod); [go.sum](go.sum) records module checksums. Dependencies include TOML and JSON utilities, fsnotify, goldmark, image/text packages, log rotation, and SQLite with its support libraries. **They are not all covered by rex-tg's MIT license.**

Go fetches each module's source and license files into its module cache. For a release, inventory the exact dependency graph and retain all copyright, license, and attribution material required by those versions. For example, from a development checkout:

```sh
go list -m all
go list -m -json all
go version -m bin/rex-tg
```

Inspect the upstream license files at the pinned module versions. A successful build or secret scan is not a license-compliance check. This document identifies bundled assets and the dependency source of truth; it is not a complete transitive license inventory or legal assurance for a binary redistribution.

## External tools and assets

Rex Beta, pi, Claude Code/cux, Codex, Telegram, Tailscale, ffmpeg, whisper.cpp, and speech models are installed or accessed separately. Their own licenses, service terms, and account requirements apply. The screen renderer uses fonts installed on macOS; no font files are vendored here. Review separate distribution obligations before bundling any of these products or models with rex-tg.
