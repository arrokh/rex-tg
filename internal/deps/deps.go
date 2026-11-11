//go:build tools

// Package deps pins the third-party modules that rex-tg packages use, so that
// go.mod stays stable while packages are written in parallel.
package deps

import (
	_ "github.com/BurntSushi/toml"
	_ "github.com/fsnotify/fsnotify"
	_ "github.com/tidwall/gjson"
	_ "github.com/tidwall/sjson"
	_ "github.com/yuin/goldmark"
	_ "gopkg.in/natefinch/lumberjack.v2"
	_ "modernc.org/sqlite"
)
