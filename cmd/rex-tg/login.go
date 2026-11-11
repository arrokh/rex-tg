package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rex-tg/internal/config"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

func loginCmd(ctx context.Context, args []string, env Env) int {
	fs := newFlags("login", env)
	tokenStdin := fs.Bool("token-stdin", false, "read the token from one line of stdin")
	cfgPath := fs.String("config", "", "config file")
	if code, stop := parseFlags(fs, args); stop {
		return code
	}
	cfg, err := env.loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: config: %v\n", err)
		return 1
	}
	kc := env.keychain()
	var token string
	if *tokenStdin {
		line, err := bufio.NewReader(env.Stdin).ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			fmt.Fprintln(env.Stderr, "rex-tg: no token on stdin")
			return 1
		}
		token = strings.TrimSpace(line)
		if token == "" {
			fmt.Fprintln(env.Stderr, "rex-tg: the token is empty")
			return 1
		}
		// Check the token before it replaces a working one.
		me, err := env.telegram(cfg, token).GetMe(ctx)
		if err != nil {
			fmt.Fprintf(env.Stderr, "rex-tg: Telegram rejected the token: %v\n", err)
			return 1
		}
		if err := kc.Set(ctx, tokenService, tokenAccount, token); err != nil {
			fmt.Fprintf(env.Stderr, "rex-tg: %v\n", err)
			return 1
		}
		reportBot(env, me)
	} else {
		fmt.Fprintln(env.Stdout, "Paste the bot token from @BotFather when security asks for it. It is not shown.")
		if err := env.Interactive(ctx, env.Security, "add-generic-password", "-U", "-s", tokenService, "-a", tokenAccount, "-w"); err != nil {
			fmt.Fprintf(env.Stderr, "rex-tg: security: %v\n", err)
			return 1
		}
		token, err = kc.Get(ctx, tokenService, tokenAccount)
		if err != nil {
			fmt.Fprintf(env.Stderr, "rex-tg: read the token back: %v\n", err)
			return 1
		}
		me, err := env.telegram(cfg, token).GetMe(ctx)
		if err != nil {
			fmt.Fprintf(env.Stderr, "rex-tg: Telegram rejected the token: %v\nRun `rex-tg login` again with the right token.\n", err)
			return 1
		}
		reportBot(env, me)
	}
	return pairWith(ctx, env, cfg, token)
}

func reportBot(env Env, me telegram.User) {
	fmt.Fprintf(env.Stdout, "Bot: @%s\n", me.Username)
	if me.HasTopicsEnabled {
		fmt.Fprintln(env.Stdout, "Threaded Mode: on (has_topics_enabled = true)")
		return
	}
	fmt.Fprintln(env.Stdout, "Threaded Mode: OFF (has_topics_enabled = false)")
	fmt.Fprintln(env.Stdout, "Turn it on: open @BotFather, choose your bot, then Bot Settings > Threaded Mode, and enable it.")
}

func pairCmd(ctx context.Context, args []string, env Env) int {
	fs := newFlags("pair", env)
	cfgPath := fs.String("config", "", "config file")
	if code, stop := parseFlags(fs, args); stop {
		return code
	}
	cfg, err := env.loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: config: %v\n", err)
		return 1
	}
	token, err := env.keychain().Get(ctx, tokenService, tokenAccount)
	if err != nil {
		token = ""
	}
	return pairWith(ctx, env, cfg, token)
}

// pairWith prints a pairing code. token may be empty.
func pairWith(ctx context.Context, env Env, cfg config.Config, token string) int {
	dbPath := cfg.DBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: %v\n", err)
		return 1
	}
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: open database: %v\n", err)
		return 1
	}
	defer st.Close()
	bot := ""
	if token != "" {
		if me, err := env.telegram(cfg, token).GetMe(ctx); err == nil {
			bot = me.Username
		}
	}
	if bot == "" {
		if v, ok, err := st.Get(ctx, "bot_username"); err == nil && ok {
			bot = v
		}
	}
	code, err := env.PairingCode(ctx, st, env.Now())
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: pairing code: %v\n", err)
		return 1
	}
	target := "your bot"
	if bot != "" {
		target = "@" + strings.TrimPrefix(bot, "@")
	}
	fmt.Fprintf(env.Stdout, "Send /start %s to %s within 10 minutes.\n", code, target)
	if token == "" {
		fmt.Fprintln(env.Stdout, "No bot token found in the Keychain. Run `rex-tg login` if the daemon cannot start.")
	}
	return 0
}
