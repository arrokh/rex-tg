package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const (
	voiceSetupCallback = "vs"
	voiceSetupPrompt   = "Hex isn't set up yet. Do you want me to help set it up?"
	voiceActionItems   = "To set up local speech-to-text, I will:\n" +
		"1. Check whether Hex and its CLI are already installed.\n" +
		"2. If needed, install the Hex app and CLI with Homebrew.\n" +
		"3. Install or prepare Hex's selected speech model.\n" +
		"4. Verify the model setup.\n\n" +
		"Nothing is installed until you choose Yes. After setup, send a new voice note to test transcription."
	voiceSetupTimeout = 30 * time.Minute
)

var errHexUnavailable = errors.New("hex CLI is not available")

func voiceSetupMarkup() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{
		{{Text: "Yes", CallbackData: voiceSetupCallback + ":y"}, {Text: "No", CallbackData: voiceSetupCallback + ":n"}},
		{{Text: "Read the action items", CallbackData: voiceSetupCallback + ":a"}},
	}}
}

func (d *daemon) lookVoicePath(name string) (string, error) {
	if d.voiceLookPath != nil {
		return d.voiceLookPath(name)
	}
	return exec.LookPath(name)
}

func (d *daemon) hexCLI() string {
	candidates := []string{d.cfg.Voice.HexCLI, "hex"}
	if d.voiceLookPath == nil {
		if d.home != "" {
			candidates = append(candidates, filepath.Join(d.home, "Applications", "Hex.app", "Contents", "MacOS", "hex"))
		}
		candidates = append(candidates,
			"/Applications/Hex.app/Contents/MacOS/hex",
			"/opt/homebrew/bin/hex",
			"/usr/local/bin/hex",
		)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		path, err := d.lookVoicePath(candidate)
		if err == nil && executable(path) {
			return path
		}
	}
	return ""
}

func (d *daemon) brewCLI() string {
	for _, candidate := range []string{"brew", "/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		path, err := d.lookVoicePath(candidate)
		if err == nil && executable(path) {
			return path
		}
	}
	return ""
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func (d *daemon) runVoiceCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	if d.voiceCommand != nil {
		return d.voiceCommand(ctx, executable, args...)
	}
	return exec.CommandContext(ctx, executable, args...).CombinedOutput()
}

func (d *daemon) transcribeHex(ctx context.Context, cli, file string) (string, error) {
	args := []string{"transcribe", file}
	if language := strings.TrimSpace(d.cfg.Voice.Language); language != "" {
		args = append(args, "--language", language)
	}
	out, err := d.runVoiceCommand(ctx, cli, args...)
	if err != nil {
		return "", fmt.Errorf("hex transcribe: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.Join(strings.Fields(string(out)), " "), nil
}

func isHexSetupError(err error) bool {
	if errors.Is(err, errHexUnavailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "model-unavailable") ||
		strings.Contains(msg, "model-not-ready") ||
		strings.Contains(msg, "model required") ||
		strings.Contains(msg, "no speech model") ||
		strings.Contains(msg, "model is not installed")
}

func (d *daemon) voiceSetupButton(cb *telegram.CallbackQuery, action string) {
	th, ok := d.threadOfMessage(cb.Message)
	if !ok {
		d.answerCallback(cb.ID, "This topic is closed.", false)
		return
	}
	switch action {
	case "a":
		d.answerCallback(cb.ID, "Here are the setup steps.", false)
		d.say(th.ChatID, th.MessageThreadID, voiceActionItems, nil)
	case "n":
		d.answerCallback(cb.ID, "Okay. Nothing was installed.", false)
		d.removeButtons(cb.Message.Chat.ID, cb.Message.MessageID)
	case "y":
		d.answerCallback(cb.ID, "Checking Hex setup…", false)
		d.removeButtons(cb.Message.Chat.ID, cb.Message.MessageID)
		chat, topic := th.ChatID, th.MessageThreadID
		d.spawn(func() { d.setupHex(chat, topic) })
	default:
		d.answerCallback(cb.ID, "That setup choice is no longer available.", false)
	}
}

func (d *daemon) setupHex(chatID, threadID int64) {
	d.voiceSetupMu.Lock()
	defer d.voiceSetupMu.Unlock()

	msg, sendErr := d.sendNow(chatID, threadID, "Checking for Hex and its speech model…", nil)
	edit := func(text string) {
		if msg.MessageID != 0 {
			d.editMessage(chatID, msg.MessageID, text, nil)
		}
	}
	if sendErr != nil {
		d.log.Warn("voice setup progress", "err", sendErr)
	}
	if d.voiceSetupReady.Load() {
		edit("Hex is ready for local speech-to-text. Send a new voice note to test it.")
		return
	}

	ctx, cancel := context.WithTimeout(d.ctx, voiceSetupTimeout)
	defer cancel()
	cli := d.hexCLI()
	if cli == "" {
		brew := d.brewCLI()
		if brew == "" {
			edit("I couldn't find Hex or Homebrew. Install Homebrew on this Mac, then send a new voice note to try setup again.")
			return
		}
		edit("Hex isn't installed yet. Installing the Hex app and CLI with Homebrew…")
		if _, err := d.runVoiceCommand(ctx, brew, "install", "--cask", "anomalyco/tap/hex"); err != nil {
			d.log.Warn("install Hex", "err", err)
			edit("Hex installation did not finish. Check that this Mac can reach Homebrew, then send another voice note to try setup again.")
			return
		}
		cli = d.hexCLI()
		if cli == "" {
			edit("Homebrew finished, but I couldn't find the Hex CLI. Check the Hex app installation, then send another voice note.")
			return
		}
	}

	running, err := d.hexRunning(ctx, cli)
	if err != nil {
		d.log.Warn("check Hex status before model install", "err", err)
		edit("I couldn't check whether Hex is running. Check Hex, then send another voice note to retry setup.")
		return
	}
	edit("Hex is installed. Downloading and selecting its local speech model…")
	if _, err := d.runVoiceCommand(ctx, cli, "model", "install"); err != nil {
		d.log.Warn("install Hex speech model", "err", err)
		edit("Hex is installed, but its speech model isn't ready. Check the network connection and available disk space, then send another voice note to retry setup.")
		return
	}
	if !running {
		running, err = d.hexRunning(ctx, cli)
		if err != nil {
			d.log.Warn("check Hex status after model install", "err", err)
			edit("I couldn't confirm that Hex finished setting up its speech model. Check Hex, then send another voice note to retry setup.")
			return
		}
	}
	if running {
		edit("Waiting for Hex to finish preparing its speech model…")
		if err := d.waitForHexModel(ctx, cli); err != nil {
			d.log.Warn("verify Hex speech model", "err", err)
			edit("Hex is installed, but its speech model isn't ready yet. Check the network connection and available disk space, then send a new voice note after setup finishes. If Hex still needs setup, choose Yes on the new prompt.")
			return
		}
	}
	d.voiceSetupReady.Store(true)
	edit("Hex is ready for local speech-to-text. Send a new voice note to test transcription.")
}

// hexRunning reports whether the status command reached a running Hex app.
// The not-running reply is expected when model install runs synchronously.
func (d *daemon) hexRunning(ctx context.Context, cli string) (bool, error) {
	out, err := d.runVoiceCommand(ctx, cli, "status", "--json")
	if err == nil {
		return true, nil
	}
	if hexStatusCode(out) == "not-running" {
		return false, nil
	}
	return false, fmt.Errorf("hex status: %w: %s", err, strings.TrimSpace(string(out)))
}

func hexStatusCode(out []byte) string {
	var reply struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(out, &reply)
	return reply.Code
}

// waitForHexModel waits for a running Hex to finish preparing its model. If
// Hex stops before confirming readiness, setup remains incomplete. When Hex
// was stopped, model install runs synchronously and this check is not needed.
func (d *daemon) waitForHexModel(ctx context.Context, cli string) error {
	type status struct {
		Code  string `json:"code"`
		Model struct {
			Installed bool            `json:"installed"`
			Preparing json.RawMessage `json:"preparing"`
		} `json:"model"`
		Permissions struct {
			TranscriptionModel bool `json:"transcription_model"`
		} `json:"permissions"`
	}
	ticker := time.NewTicker(d.timing.VoiceModelPoll)
	defer ticker.Stop()
	for {
		out, err := d.runVoiceCommand(ctx, cli, "status", "--json")
		if err != nil {
			if hexStatusCode(out) == "not-running" {
				return errors.New("hex stopped before its speech model was ready")
			}
			return fmt.Errorf("hex status: %w: %s", err, strings.TrimSpace(string(out)))
		}
		var reply status
		if err := json.Unmarshal(out, &reply); err != nil {
			return fmt.Errorf("decode hex status: %w", err)
		}
		preparing := len(reply.Model.Preparing) != 0 && string(reply.Model.Preparing) != "null"
		if reply.Model.Installed && reply.Permissions.TranscriptionModel && !preparing {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d *daemon) promptHexSetup(th store.Thread) {
	d.replyTopicMarkup(th, voiceSetupPrompt, voiceSetupMarkup())
}
