// Package extensions embeds the source of the pi extension that reports
// session and agent events to rex-tg.
package extensions

import _ "embed"

// PiExtension is the TypeScript source of the pi extension. The placeholder
// __REX_TG_BIN__ stands for a JSON string literal with the rex-tg binary path,
// and __REX_TG_LIVE__ for one with the folder of the live text files.
//
//go:embed pi/rex-tg.ts
var PiExtension []byte
