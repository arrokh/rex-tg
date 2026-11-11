package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"rex-tg/internal/agents"
)

const (
	maxHookInput    = 1 << 20
	hookReadTimeout = 5 * time.Second
)

// spoolFile is the JSON shape that registry.ParseSpool reads.
type spoolFile struct {
	Runtime    string          `json:"runtime"`
	Event      string          `json:"event"`
	At         string          `json:"at"`
	RexSession string          `json:"rex_session"`
	RexBlock   string          `json:"rex_block"`
	Payload    json.RawMessage `json:"payload"`
}

// hookCmd never prints and always returns 0, so an agent never sees it.
func hookCmd(_ context.Context, args []string, env Env) (code int) {
	defer func() {
		_ = recover()
		code = 0
	}()
	_ = writeSpool(args, env)
	return 0
}

func writeSpool(args []string, env Env) error {
	if len(args) != 1 {
		return fmt.Errorf("want one runtime")
	}
	rt := agents.Runtime(args[0])
	if !rt.Valid() {
		return fmt.Errorf("unknown runtime")
	}
	block := env.Getenv("REX_BLOCK")
	if block == "" {
		return nil
	}
	payload := readHookInput(env)
	var head struct {
		Event string `json:"hook_event_name"`
	}
	if len(payload) == 0 || json.Unmarshal(payload, &head) != nil || head.Event == "" {
		return fmt.Errorf("no usable payload")
	}
	now := env.Now()
	data, err := json.Marshal(spoolFile{
		Runtime:    string(rt),
		Event:      head.Event,
		At:         now.UTC().Format("2006-01-02T15:04:05.000Z"),
		RexSession: env.Getenv("REX_SESSION"),
		RexBlock:   block,
		Payload:    json.RawMessage(payload),
	})
	if err != nil {
		return err
	}
	// The spool folder comes from the config; a broken config falls back to
	// the defaults so events are not lost.
	cfg, err := env.loadConfig("")
	if err != nil {
		cfg, _ = env.loadConfig(os.DevNull)
	}
	dir := cfg.SpoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%d-%s.json", now.UnixNano(), os.Getpid(), hex.EncodeToString(rnd[:]))
	tmp := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readHookInput reads up to 1 MiB of stdin. It reads nothing from a terminal
// and gives up after a few seconds.
func readHookInput(env Env) []byte {
	if env.Stdin == nil || env.StdinIsTerminal() {
		return nil
	}
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(env.Stdin, maxHookInput))
		ch <- result{data, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil
		}
		return r.data
	case <-time.After(hookReadTimeout):
		return nil
	}
}
