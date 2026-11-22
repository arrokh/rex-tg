package daemon_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/miniapp"
	"rex-tg/internal/telegram/tgtest"
)

// A final answer that names a PDF and a CSV gets one silent offer after it,
// with a button per file. Nothing is uploaded until a tap; a tap sends that
// file once, as a document, and a second tap sends nothing.
func TestFileOfferSendsAFileOnlyOnTap(t *testing.T) {
	e, p, thread := piTopic(t, "pi-files")
	dir := t.TempDir()
	pdf, csv := filepath.Join(dir, "report.pdf"), filepath.Join(dir, "data.csv")
	for _, f := range []string{pdf, csv} {
		if err := os.WriteFile(f, []byte("fake content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p.User("make the report")
	p.Final("Wrote `" + pdf + "` and " + csv + ".")
	answer := e.waitText(thread, "Wrote")
	offer := e.waitText(thread, "Files in this answer")
	if offer.ID < answer.ID || !offer.Silent || offer.Markup == nil || len(offer.Markup.InlineKeyboard) != 2 {
		t.Fatalf("offer %+v, answer %d: want a silent offer with 2 buttons after the answer", offer, answer.ID)
	}
	if got := offer.Markup.InlineKeyboard[0][0].Text; !strings.Contains(got, "Attach report.pdf") {
		t.Errorf("file action label = %q, want explicit attachment fallback", got)
	}
	documents := func(name string) []tgtest.Message {
		var out []tgtest.Message
		for _, m := range e.botMessages(thread) {
			if m.Filename == name {
				out = append(out, m)
			}
		}
		return out
	}
	e.never("an upload before a tap", 300*time.Millisecond, func() bool { return len(documents("report.pdf"))+len(documents("data.csv")) > 0 })

	e.pressButton(offer, buttonData(offer, "report.pdf"))
	e.waitFor("the PDF", func() bool { return len(documents("report.pdf")) == 1 })
	e.pressButton(offer, buttonData(offer, "report.pdf"))
	e.waitFor("the second tap's answer", func() bool {
		for _, s := range e.callbackTexts() {
			if strings.Contains(s, "sent already") {
				return true
			}
		}
		return false
	})
	if n := len(documents("report.pdf")); n != 1 || len(documents("data.csv")) != 0 {
		t.Fatalf("report.pdf sent %d times, data.csv %d times; want 1 and 0", n, len(documents("data.csv")))
	}

	// The tap checks the place again: data.csv became a link to a hidden
	// folder after the offer, so it is not sent.
	hidden := filepath.Join(dir, ".secret", "keys.csv")
	if err := os.MkdirAll(filepath.Dir(hidden), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hidden, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(csv); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hidden, csv); err != nil {
		t.Fatal(err)
	}
	e.pressButton(offer, buttonData(offer, "data.csv"))
	e.waitFor("the refusal", func() bool {
		for _, s := range e.callbackTexts() {
			if strings.Contains(s, "not in a place rex-tg sends files from") {
				return true
			}
		}
		return false
	})
	e.never("an upload of the linked file", 300*time.Millisecond, func() bool {
		return len(documents("data.csv"))+len(documents("keys.csv")) > 0
	})
}

// A file offer with the Mini App enabled has an authenticated, scoped preview
// action and keeps document delivery as an explicit fallback.
func TestFileOfferIncludesMiniAppPreviewAndAttachmentFallback(t *testing.T) {
	addr := freeAddr(t)
	e, p, thread, repo := repoTopic(t, "pi-file-preview", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.Listen = addr
		e.cfg.MiniApp.Files = true
		e.cfg.Display.SendFiles = []string{"md"}
	})
	path := filepath.Join(repo, "preview.md")
	if err := os.WriteFile(path, []byte("preview content"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.User("show the Markdown file")
	p.Final("Here is " + path)
	offer := e.waitText(thread, "Files in this answer")
	if offer.Markup == nil {
		t.Fatal("missing file offer buttons")
	}
	var pageID string
	var attach bool
	for _, row := range offer.Markup.InlineKeyboard {
		for _, button := range row {
			if button.WebApp != nil {
				u, err := url.Parse(button.WebApp.URL)
				if err != nil {
					t.Fatal(err)
				}
				pageID = u.Query().Get("p")
			}
			if button.CallbackData != "" && strings.Contains(button.Text, "Attach preview.md") {
				attach = true
			}
		}
	}
	if pageID == "" || !attach {
		t.Fatalf("offer buttons = %+v; want scoped Mini App preview and attachment fallback", offer.Markup.InlineKeyboard)
	}
	raw, ok, err := e.peek().Get(context.Background(), "page:"+pageID)
	var pg miniapp.Page
	if err != nil || !ok || json.Unmarshal([]byte(raw), &pg) != nil || pg.Dir != repo || pg.InitialFile != "preview.md" {
		t.Fatalf("preview is not bound to the project and file: %+v (found %v, err %v)", pg, ok, err)
	}
}
