package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"rex-tg/internal/registry"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const typingUnknownNotice = "⚠️ Your message may be in the input box without Enter. Check with /screen."

// botCommands is the menu registered with setMyCommands. Telegram cannot
// show a different menu per topic, so it holds both sets.
var botCommands = []telegram.BotCommand{
	{Command: "agents", Description: "List the agents in Rex panes"},
	{Command: "new", Description: "Start an agent in a new Rex tab"},
	{Command: "topics", Description: "List topics and their states"},
	{Command: "status", Description: "Daemon health"},
	{Command: "pause", Description: "Stop typing into panes"},
	{Command: "unpause", Description: "Allow typing again"},
	{Command: "info", Description: "Topic: session, pane, and state"},
	{Command: "screen", Description: "Topic: show the pane screen"},
	{Command: "approve", Description: "Topic: answer the open prompt with yes"},
	{Command: "deny", Description: "Topic: answer the open prompt with no"},
	{Command: "esc", Description: "Topic: press Escape"},
	{Command: "interrupt", Description: "Topic: stop the current turn"},
	{Command: "key", Description: "Topic: press keys"},
	{Command: "force", Description: "Topic: type text now"},
	{Command: "mode", Description: "Topic: progress, block, or final"},
	{Command: "verbose", Description: "Topic: off, on, or full"},
	{Command: "reasoning", Description: "Topic: on or off"},
	{Command: "resend", Description: "Topic: resend an unknown message"},
	{Command: "mute", Description: "Topic: answers without sound"},
	{Command: "unmute", Description: "Topic: answers with sound"},
	{Command: "unlink", Description: "Topic: stop mirroring this session"},
	{Command: "help", Description: "List the commands"},
}

// setCommands registers the command menu for the default scope and, once
// paired, for the owner's chat. Telegram shows a chat's own list first, so
// this also hides an older list that another program set for all private
// chats (found live: the menu showed old commands).
func (d *daemon) setCommands(ctx context.Context, tg *telegram.Client) {
	if err := tg.SetMyCommands(ctx, botCommands, nil); err != nil {
		d.log.Warn("setMyCommands", "err", err)
	}
	if d.ownerChat == 0 {
		return
	}
	scope := &telegram.BotCommandScope{Type: "chat", ChatID: d.ownerChat}
	if err := tg.SetMyCommands(ctx, botCommands, scope); err != nil {
		d.log.Warn("setMyCommands for the owner's chat", "err", err)
	}
}

// BotCommands returns a copy of the command menu that the daemon registers.
func BotCommands() []telegram.BotCommand {
	return append([]telegram.BotCommand(nil), botCommands...)
}

// startup does the steps of design section 3 that come before the loops.
func (d *daemon) startup(ctx context.Context) error {
	d.ownerUser = d.kvInt("owner_user_id")
	d.ownerChat = d.kvInt("owner_chat_id")
	if v, ok, _ := d.st.Get(ctx, "paused"); ok && v == "1" {
		d.paused.Store(true)
	}

	if err := d.snd.Recover(ctx); err != nil {
		return fmt.Errorf("daemon: recover sender: %w", err)
	}
	typing, err := d.st.RecoverTyping(ctx, d.now())
	if err != nil {
		return fmt.Errorf("daemon: recover typing: %w", err)
	}
	for _, in := range typing {
		if in.ThreadID != 0 {
			d.notice(in.ThreadID, fmt.Sprintf("typing-unknown:%d", in.UpdateID), typingUnknownNotice)
		}
	}

	if err := d.getMe(ctx); err != nil {
		return err
	}
	d.setCommands(ctx, d.tg)

	threads, err := d.st.Threads(ctx)
	if err != nil {
		return fmt.Errorf("daemon: list topics: %w", err)
	}
	for _, th := range threads {
		if th.Status == store.ThreadClosed {
			continue
		}
		d.dirty[th.ID] = true
		// An alert without a Telegram message was cut off by a crash.
		if als, err := d.st.OpenAlerts(ctx, th.ID); err == nil {
			for _, a := range als {
				if a.TGMessageID == 0 {
					_ = d.st.SetAlertState(ctx, a.ID, store.AlertGone)
				}
			}
		}
		if th.Status != store.ThreadLive {
			continue
		}
		d.tracker(th)
		conv, err := d.st.Conversation(ctx, th.CurrentConversationID)
		if err != nil {
			d.log.Warn("restore topic", "thread", th.ID, "err", err)
			continue
		}
		if err := d.startTailer(conv, th.ID, registry.TailStart{}); err != nil {
			d.log.Warn("restore tailer", "thread", th.ID, "err", err)
		}
	}
	return nil
}

// getMe checks the token, saves the bot's user name, and warns when topics
// are off. It retries on network errors.
func (d *daemon) getMe(ctx context.Context) error {
	backoff := time.Second
	for {
		me, err := d.tg.GetMe(ctx)
		if err == nil {
			d.botUser = me.Username
			if e := d.st.Set(ctx, "bot_username", me.Username); e != nil {
				d.log.Warn("save bot username", "err", e)
			}
			if !me.HasTopicsEnabled {
				d.log.Warn("the bot has no topics in private chats; turn on Threaded Mode in BotFather")
			}
			return nil
		}
		var ae *telegram.APIError
		if errors.As(err, &ae) && (ae.Code == 401 || ae.Code == 404) {
			return fmt.Errorf("daemon: getMe: %w", err)
		}
		d.log.Warn("getMe failed; retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// pollLoop reads updates with getUpdates and waits for the core to store each
// batch before it asks for the next one.
func (d *daemon) pollLoop(ctx context.Context) {
	offset := d.kvInt("tg_offset")
	backoff := time.Second
	allowed := []string{"message", "edited_message", "callback_query"}
	for ctx.Err() == nil {
		ups, err := d.tg.GetUpdates(ctx, offset, 50, allowed)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if telegram.Classify(err) == telegram.ClassConflict {
				d.emit(evFatal{errors.New("daemon: another program polls with the same bot token (409 Conflict)")})
				return
			}
			d.log.Warn("getUpdates failed", "err", err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if len(ups) == 0 {
			continue
		}
		done := make(chan struct{})
		var next int64
		d.emit(evUpdates{ups: ups, next: &next, done: done})
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
		if next > offset {
			offset = next
		} else if !sleepCtx(ctx, time.Second) { // nothing was stored; try again later
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// rexEventLoop keeps one `rex events` stream open and restarts it with a
// backoff. After a restart it asks for a scan, because events were missed.
func (d *daemon) rexEventLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := d.now()
		evs, errs := d.rx.Events(ctx)
		for ev := range evs {
			ev := ev
			d.regq.push(regJob{run: func(ctx context.Context) ([]registry.Change, error) {
				return d.reg.RexEvent(ctx, ev)
			}})
		}
		for err := range errs {
			if ctx.Err() != nil {
				break
			}
			var pe *rex.ParseError
			var de *rex.DroppedError
			switch {
			case errors.As(err, &pe):
				d.log.Debug("rex events: unreadable line")
			case errors.As(err, &de):
				d.log.Warn("rex events: events dropped", "count", de.Count)
				d.requestScan()
			default:
				d.log.Warn("rex events stream ended", "err", err)
			}
		}
		if ctx.Err() != nil {
			return
		}
		if d.now().Sub(started) > 30*time.Second {
			backoff = time.Second
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
		d.requestScan()
	}
}

// spoolLoop reads the hook spool folder: on file system events and every second.
func (d *daemon) spoolLoop(ctx context.Context) {
	dir := d.cfg.SpoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.log.Error("create spool folder", "err", err)
		return
	}
	wake := make(chan struct{}, 1)
	if w, err := fsnotify.NewWatcher(); err == nil {
		defer w.Close()
		if err := w.Add(dir); err == nil {
			d.spawn(func() {
				for {
					select {
					case <-ctx.Done():
						return
					case _, ok := <-w.Events:
						if !ok {
							return
						}
						select {
						case wake <- struct{}{}:
						default:
						}
					case _, ok := <-w.Errors:
						if !ok {
							return
						}
					}
				}
			})
		}
	}
	tk := time.NewTicker(d.timing.Spool)
	defer tk.Stop()
	for {
		d.processSpool(ctx, dir)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		case <-wake:
		}
	}
}

const (
	spoolMaxAge   = 24 * time.Hour
	spoolRetryFor = time.Minute
)

// processSpool applies the hook reports in the folder, oldest first, and
// deletes each file once it is applied. A report that cannot be applied yet
// (Rex is down) stays for a minute and is then dropped.
func (d *daemon) processSpool(ctx context.Context, dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if ctx.Err() != nil {
			return
		}
		path := filepath.Join(dir, n)
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		age := time.Since(fi.ModTime())
		if age > spoolMaxAge {
			_ = os.Remove(path)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rep, err := registry.ParseSpool(data)
		if err != nil {
			d.log.Warn("spool file is not valid; deleting it", "file", n)
			_ = os.Remove(path)
			continue
		}
		done := make(chan error, 1)
		d.regq.push(regJob{run: func(ctx context.Context) ([]registry.Change, error) {
			return d.reg.Hook(ctx, rep)
		}, done: done})
		var herr error
		select {
		case herr = <-done:
		case <-ctx.Done():
			return
		}
		if herr != nil && age < spoolRetryFor {
			continue // try again on the next pass
		}
		_ = os.Remove(path)
	}
}
