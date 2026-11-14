package miniapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The folder explorer shows folders only, inside the home folder, and the
// ones that start with "." only when asked.
func TestListFolders(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	for _, d := range []string{"work/app/.git", "work/Beta", "work/alpha", "work/.cache", "notes"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "work", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "work", "out")); err != nil {
		t.Fatal(err)
	}
	// A link to a folder inside home shows, like a folder.
	if err := os.Symlink(filepath.Join(home, "notes"), filepath.Join(home, "work", "notes-link")); err != nil {
		t.Fatal(err)
	}

	top, err := ListFolders(home, "", false)
	if err != nil || top.Path != home || top.Shown != "~" || top.Parent != "" {
		t.Fatalf("home: %+v %v", top, err)
	}
	if got := folderNames(top.Dirs); got != "notes work" {
		t.Errorf("home folders: %q", got)
	}

	work, err := ListFolders(home, filepath.Join(home, "work"), false)
	if err != nil || work.Shown != "~/work" || work.Parent != home || work.Git {
		t.Fatalf("work: %+v %v", work, err)
	}
	// Folders only, sorted without case; a file and a link out of home are
	// left out.
	if got := folderNames(work.Dirs); got != "alpha app Beta notes-link" {
		t.Errorf("work folders: %q", got)
	}
	if hid, _ := ListFolders(home, filepath.Join(home, "work"), true); folderNames(hid.Dirs) != ".cache alpha app Beta notes-link" {
		t.Errorf("with hidden: %q", folderNames(hid.Dirs))
	}
	if app, err := ListFolders(home, filepath.Join(home, "work", "app"), false); err != nil || !app.Git {
		t.Errorf("a repository: %+v %v", app, err)
	}
	// Each folder of a list says whether it is a repository.
	for _, f := range work.Dirs {
		if f.Git != (f.Shown == "app") {
			t.Errorf("%s: git %v", f.Shown, f.Git)
		}
	}

	for _, bad := range []string{
		outside,                              // outside home
		filepath.Join(home, "work", "out"),   // a link out of home
		filepath.Join(home, "..", "x"),       // up and out
		"work",                               // not absolute
		filepath.Join(home, "work/file.txt"), // a file
		filepath.Join(home, "missing"),
	} {
		if _, err := ListFolders(home, bad, false); err != ErrNoFolder {
			t.Errorf("%s: %v, want ErrNoFolder", bad, err)
		}
	}
}

func folderNames(fs []Folder) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.Shown)
	}
	return strings.Join(s, " ")
}

func TestControlActionCheck(t *testing.T) {
	act := "0123456789abcdef"
	ok := []ControlAction{
		{Op: OpOpen, Act: act, Pane: 3},
		{Op: OpRename, Act: act, Pane: 3, Name: "my topic"},
		{Op: OpRename, Act: act, Pane: 3, Name: "auto"},
		{Op: OpPage, Act: act, Pane: 3, Kind: PageScreen},
		{Op: OpPage, Act: act, Pane: 3, Kind: PageChanges},
		{Op: OpNew, Act: act, Agent: "claude", Path: "/Users/me/app"},
		{Op: OpUnlink, Act: act, Thread: 2},
	}
	for _, a := range ok {
		if err := a.Check(); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	bad := []ControlAction{
		{Op: OpOpen, Act: "short", Pane: 3},
		{Op: OpOpen, Act: act},
		{Op: "delete", Act: act, Pane: 3},
		{Op: OpRename, Act: act, Pane: 3, Name: "two\nlines"},
		{Op: OpRename, Act: act, Pane: 3, Name: strings.Repeat("x", 101)},
		{Op: OpRename, Act: act, Name: "x"},
		{Op: OpPage, Act: act, Pane: 3, Kind: "answer"},
		{Op: OpPage, Act: act, Kind: PageFiles},
		{Op: OpNew, Act: act, Agent: "claude", Path: "relative/app"},
		{Op: OpNew, Act: act, Path: "/Users/me/app"},
		{Op: OpUnlink, Act: act},
		{Op: "pause", Act: act}, // /pause stays a chat command
	}
	for _, a := range bad {
		if err := a.Check(); err == nil {
			t.Errorf("%+v: accepted", a)
		}
	}
}

type fakeControl struct {
	did []ControlAction
}

func (f *fakeControl) State(context.Context) (ControlState, error) {
	return ControlState{Agents: []ControlAgent{{Pane: 3, Name: "api", Agent: "pi"}}}, nil
}

func (f *fakeControl) Do(_ context.Context, a ControlAction) ControlResult {
	f.did = append(f.did, a)
	return ControlResult{OK: true, Message: "done"}
}

// The control panel answers only the owner, only for the control page, and
// takes an action only as valid JSON.
func TestControlOnlyForTheOwner(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	pages := map[string]Page{
		"c1": {Kind: KindControl, Title: "rex-tg"},
		"a1": {Kind: KindAnswer, Title: "Answer", Text: "x"},
	}
	fc := &fakeControl{}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithControl(fc, home)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	call := func(method, path, initData, ctype, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
		req.Header.Set("X-Telegram-Init-Data", initData)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	owner, other := sign(t, fakeToken, fields(42, time.Now())), sign(t, fakeToken, fields(7, time.Now()))
	action := `{"op":"open","act":"0123456789abcdef","pane":3}`

	// Another user gets nothing, and no action runs.
	for _, c := range [][2]string{{"GET", "/api/control?id=c1"}, {"GET", "/api/control/folders?id=c1"}, {"POST", "/api/control/do?id=c1"}} {
		if code, _ := call(c[0], c[1], other, "application/json", action); code != http.StatusForbidden {
			t.Errorf("%s %s for another user: %d", c[0], c[1], code)
		}
	}
	if len(fc.did) != 0 {
		t.Fatal("an action ran for a refused request")
	}
	// Another kind of page is not the control panel.
	if code, _ := call("GET", "/api/control?id=a1", owner, "", ""); code != http.StatusNotFound {
		t.Errorf("an answer page as the panel: %d", code)
	}
	if code, body := call("GET", "/api/control?id=c1", owner, "", ""); code != http.StatusOK || body["at"] == "" || len(body["agents"].([]any)) != 1 {
		t.Errorf("state: %d %v", code, body)
	}
	if code, body := call("GET", "/api/control/folders?id=c1", owner, "", ""); code != http.StatusOK || body["shown"] != "~" || len(body["dirs"].([]any)) != 1 {
		t.Errorf("folders: %d %v", code, body)
	}
	if code, _ := call("GET", "/api/control/folders?id=c1&path=/etc", owner, "", ""); code != http.StatusNotFound {
		t.Errorf("a folder outside home: %d", code)
	}
	// An action must be JSON, of a known shape.
	if code, _ := call("GET", "/api/control/do?id=c1", owner, "", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET of an action: %d", code)
	}
	if code, _ := call("POST", "/api/control/do?id=c1", owner, "application/x-www-form-urlencoded", "op=open"); code != http.StatusUnsupportedMediaType {
		t.Errorf("a form: %d", code)
	}
	for _, body := range []string{`{"op":"open","act":"0123456789abcdef","pane":3,"extra":1}`, `{"op":"wipe","act":"0123456789abcdef"}`, `not json`} {
		if code, _ := call("POST", "/api/control/do?id=c1", owner, "application/json", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, code)
		}
	}
	if len(fc.did) != 0 {
		t.Fatalf("a bad action ran: %+v", fc.did)
	}
	if code, body := call("POST", "/api/control/do?id=c1", owner, "application/json", action); code != http.StatusOK || body["ok"] != true || len(fc.did) != 1 || fc.did[0].Pane != 3 {
		t.Errorf("open: %d %v %+v", code, body, fc.did)
	}
	// A panel opened two hours ago still reads, but an action needs a
	// signature from the last hour.
	old := sign(t, fakeToken, fields(42, time.Now().Add(-2*time.Hour)))
	if code, _ := call("GET", "/api/control?id=c1", old, "", ""); code != http.StatusOK {
		t.Errorf("state with an older signature: %d", code)
	}
	if code, body := call("POST", "/api/control/do?id=c1", old, "application/json", action); code != http.StatusForbidden || !strings.Contains(fmt.Sprint(body["error"]), "/rt") {
		t.Errorf("an action with an older signature: %d %v", code, body)
	}
	if len(fc.did) != 1 {
		t.Errorf("an action with an older signature ran: %+v", fc.did)
	}
}
