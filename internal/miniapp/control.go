package miniapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The control panel (/rt) lists the agents in Rex panes and acts on
// them: it opens a session's topic, renames an agent (its topic, its pane,
// and its tab), opens the live screen, files, or changes of its topic,
// starts an agent in a folder that the folder explorer picks, and unlinks a
// topic. It shows whether typing is paused, but /pause and /unpause stay
// chat commands: init data that leaked cannot undo a /pause. The daemon does the work (Control); this file checks who
// asks and what they ask, and lists folders.

// KindControl is the control panel. There is one, for the whole bot.
const KindControl = "control"

// ControlState is what the control panel shows.
type ControlState struct {
	Paused   bool           `json:"paused"`   // /pause: nothing is typed into panes (the panel only shows it)
	Agents   []ControlAgent `json:"agents"`   // the agents in Rex panes, the ones that wait for you first
	Starters []Starter      `json:"starters"` // the agents that "New agent" can start
	Folders  []Folder       `json:"folders"`  // folders for a new agent: the pinned ones, then recent ones
	Home     Folder         `json:"home"`     // where the folder explorer starts
	Files    bool           `json:"files"`    // OpPage PageFiles is on ([miniapp] files)
	Changes  bool           `json:"changes"`  // OpPage PageChanges is on ([miniapp] changes)
	At       string         `json:"at"`       // when it was read, "15:04:05"
}

// ControlAgent is an agent in a Rex pane.
type ControlAgent struct {
	Pane    int64         `json:"pane"`              // the pane's id in rex-tg
	Name    string        `json:"name"`              // the name of its topic, or the one a topic would get
	Label   string        `json:"label"`             // the pane's own name in Rex ("" for none)
	Agent   string        `json:"agent"`             // "Claude Code", "pi", "Codex"
	Emoji   string        `json:"emoji"`             // the state's emoji
	State   string        `json:"state"`             // "working", "idle", "waiting for you", …
	Since   string        `json:"since,omitempty"`   // how long it has been in that state: "3m"
	Folder  string        `json:"folder,omitempty"`  // its working folder, with ~ for the home folder
	Title   string        `json:"title,omitempty"`   // the session's title, redacted
	Session string        `json:"session,omitempty"` // the Rex session the pane is in
	Ready   bool          `json:"ready"`             // the session is known, so a topic can open
	Mac     bool          `json:"mac,omitempty"`     // you work in the pane on the Mac
	Topic   *ControlTopic `json:"topic,omitempty"`   // its topic, when it has one that is not closed
}

// ControlTopic is the topic of an agent.
type ControlTopic struct {
	Thread int64  `json:"thread"` // the topic's id in rex-tg
	Name   string `json:"name"`
	Status string `json:"status"` // "live" or "offline"
	Link   string `json:"link"`   // https://t.me/<bot>/<topic>
	Custom bool   `json:"custom"` // a name you gave it: "auto" names it after the session again
}

// Starter is an agent that the control panel can start.
type Starter struct {
	Key  string `json:"key"`  // pi, claude, cux, codex
	Name string `json:"name"` // its name in the panel
}

// Folder is a folder: its absolute path, and how the panel shows it.
type Folder struct {
	Path  string `json:"path"`
	Shown string `json:"shown"`
	Git   bool   `json:"git,omitempty"` // in a folder list: it is the top folder of a git repository
}

// FolderList is one folder of the folder explorer.
type FolderList struct {
	Folder
	Parent string   `json:"parent,omitempty"` // "" in the home folder: the explorer goes no higher
	Dirs   []Folder `json:"dirs"`             // its folders; Shown is the name
	Git    bool     `json:"git"`              // it is the top folder of a git repository
	More   bool     `json:"more"`             // it has more folders than the list holds
}

// Operations of ControlAction.
const (
	OpOpen   = "open"   // Pane: open the session's topic, or make a new one
	OpRename = "rename" // Pane, Name: like /rename: the topic, the pane, and its tab ("auto" too)
	OpPage   = "page"   // Pane, Kind: a Mini App page of the agent's topic (PageScreen, PageFiles, PageChanges)
	OpNew    = "new"    // Agent, Path: start an agent in a new Rex tab, like /new
	OpUnlink = "unlink" // Thread: like /unlink in the topic
)

// Pages that OpPage opens: what /screen's live screen, /files, and /changes
// open for the agent's topic.
const (
	PageScreen  = "screen"
	PageFiles   = "files"
	PageChanges = "changes"
)

// ControlAction is one action of the control panel.
type ControlAction struct {
	Op     string `json:"op"`
	Act    string `json:"act"` // a random id that the page makes for each tap: an action runs at most once
	Pane   int64  `json:"pane,omitempty"`
	Thread int64  `json:"thread,omitempty"`
	Name   string `json:"name,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Path   string `json:"path,omitempty"`
	Kind   string `json:"kind,omitempty"` // OpPage: PageScreen, PageFiles, or PageChanges
}

// ControlResult is the outcome of an action, shown on the page.
type ControlResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`        // plain text
	Link    string `json:"link,omitempty"` // a topic to open, when there is one
	// Pending: the outcome is not known yet (rex-tg was busy, or the action
	// still runs). The page sends a retry with the same Act, so a retry
	// never runs the action a second time.
	Pending bool `json:"pending,omitempty"`
	// Page is the id of a Mini App page to go to (OpPage): the page opens
	// it as app?p=<Page>, in the same Mini App.
	Page string `json:"page,omitempty"`
}

// Control is the daemon's side of the control panel.
type Control interface {
	// State reads what the panel shows.
	State(ctx context.Context) (ControlState, error)
	// Do runs an action that Check accepted. An action whose Act was seen
	// before is not run again: Do returns its first result.
	Do(ctx context.Context, a ControlAction) ControlResult
}

// ActMaxAge is how old the signature of an action may be. A page shows
// for MaxAge (24 hours), but a change needs a signature from the last hour:
// init data that leaked acts for a shorter time. Reopen the panel after it.
const ActMaxAge = time.Hour

// Limits of an action.
const (
	maxActionBody = 4 << 10
	maxNameRunes  = 100
	maxPathBytes  = 1024
	maxFolders    = 1000
)

var actID = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// Check checks the shape of an action: a known operation with the fields it
// needs. The daemon checks the rest (that the pane or the topic exists, the
// name, the folder).
func (a ControlAction) Check() error {
	if !actID.MatchString(a.Act) {
		return errors.New("this action has no valid id; reload the page")
	}
	name := strings.TrimSpace(a.Name)
	switch a.Op {
	case OpOpen:
		if a.Pane <= 0 {
			return errors.New("no pane")
		}
	case OpRename:
		if a.Pane <= 0 {
			return errors.New("nothing to rename")
		}
		if name == "" || utf8.RuneCountInString(name) > maxNameRunes || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n\t") {
			return errors.New("a name has 1 to 100 characters, on one line")
		}
	case OpPage:
		if a.Pane <= 0 || a.Kind != PageScreen && a.Kind != PageFiles && a.Kind != PageChanges {
			return errors.New("no such page")
		}
	case OpNew:
		if a.Agent == "" || !filepath.IsAbs(a.Path) || len(a.Path) > maxPathBytes || strings.ContainsRune(a.Path, 0) {
			return errors.New("pick an agent and a folder")
		}
	case OpUnlink:
		if a.Thread <= 0 {
			return errors.New("no topic")
		}
	default:
		return errors.New("unknown action")
	}
	return nil
}

// ErrNoFolder is a folder that the explorer does not show: not there, not a
// folder, unreadable, or outside the home folder.
var ErrNoFolder = errors.New("miniapp: no such folder")

// ListFolders lists the folders in dir, which must be inside home (after
// links are followed). Only folders show, not files, and a link only when it
// leads to a folder inside home; names that start with "." show only with
// hidden. "" is home. The explorer only names folders:
// /files shows what is in them.
func ListFolders(home, dir string, hidden bool) (FolderList, error) {
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return FolderList{}, ErrNoFolder
	}
	if dir == "" {
		dir = home
	}
	if !filepath.IsAbs(dir) || len(dir) > maxPathBytes {
		return FolderList{}, ErrNoFolder
	}
	dir = filepath.Clean(dir)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || !within(realHome, real) || !within(filepath.Clean(home), dir) && !within(realHome, dir) {
		return FolderList{}, ErrNoFolder
	}
	des, err := os.ReadDir(real)
	if err != nil {
		return FolderList{}, ErrNoFolder
	}
	l := FolderList{Folder: Folder{Path: dir, Shown: tilde(home, dir)}, Dirs: []Folder{}}
	if real != realHome {
		l.Parent = filepath.Dir(dir)
	}
	for _, de := range des {
		name := de.Name()
		if name == ".git" {
			l.Git = true
		}
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		if !de.IsDir() {
			// A link to a folder shows when the folder is inside home too.
			if de.Type()&os.ModeSymlink == 0 {
				continue
			}
			to, err := filepath.EvalSymlinks(filepath.Join(real, name))
			if err != nil || !within(realHome, to) {
				continue
			}
			if fi, err := os.Stat(to); err != nil || !fi.IsDir() {
				continue
			}
		}
		if len(l.Dirs) == maxFolders {
			l.More = true
			break
		}
		_, gerr := os.Lstat(filepath.Join(real, name, ".git"))
		l.Dirs = append(l.Dirs, Folder{Path: filepath.Join(dir, name), Shown: name, Git: gerr == nil})
	}
	if !l.Git {
		if _, err := os.Stat(filepath.Join(real, ".git")); err == nil {
			l.Git = true // a worktree's .git is a file
		}
	}
	sort.Slice(l.Dirs, func(i, j int) bool {
		a, b := strings.ToLower(l.Dirs[i].Shown), strings.ToLower(l.Dirs[j].Shown)
		if a != b {
			return a < b
		}
		return l.Dirs[i].Shown < l.Dirs[j].Shown
	})
	return l, nil
}

// tilde shows a path inside home with "~".
func tilde(home, p string) string {
	home = filepath.Clean(home)
	switch {
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+string(filepath.Separator)):
		return "~" + p[len(home):]
	}
	return p
}

// WithControl turns on the control panel. home is where the folder
// explorer starts; it shows nothing outside it.
func (s *Server) WithControl(c Control, home string) *Server {
	s.control, s.home = c, home
	return s
}

// controlState answers the control panel's state.
func (s *Server) controlState(w http.ResponseWriter, r *http.Request) {
	if !s.controlAllowed(w, r) {
		return
	}
	st, err := s.control.State(r.Context())
	if err != nil {
		http.Error(w, `{"error":"Could not read the agents now. Try again."}`, http.StatusServiceUnavailable)
		return
	}
	st.At = s.now().Format("15:04:05")
	_ = json.NewEncoder(w).Encode(st)
}

// controlFolders answers a folder of the folder explorer.
func (s *Server) controlFolders(w http.ResponseWriter, r *http.Request) {
	if !s.controlAllowed(w, r) {
		return
	}
	q := r.URL.Query()
	l, err := ListFolders(s.home, q.Get("path"), q.Get("hidden") == "1")
	if err != nil {
		http.Error(w, `{"error":"This folder is not there, or it cannot be read."}`, http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(l)
}

// controlDo runs an action. It takes JSON only, so a form on another site
// cannot send one; the init data header is needed anyway.
func (s *Server) controlDo(w http.ResponseWriter, r *http.Request) {
	if !s.controlAllowed(w, r) {
		return
	}
	if _, err := CheckInitData(r.Header.Get("X-Telegram-Init-Data"), s.secret, s.now(), ActMaxAge); err != nil {
		http.Error(w, `{"error":"This panel was opened more than an hour ago. Close it and open it again with /rt."}`, http.StatusForbidden)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, `{"error":"Send JSON."}`, http.StatusUnsupportedMediaType)
		return
	}
	var a ControlAction
	dec := json.NewDecoder(io.LimitReader(r.Body, maxActionBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		http.Error(w, `{"error":"This action is not valid."}`, http.StatusBadRequest)
		return
	}
	a.Name = strings.TrimSpace(a.Name)
	if err := a.Check(); err != nil {
		b, _ := json.Marshal(map[string]string{"error": capital(err.Error()) + "."})
		http.Error(w, string(b), http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(s.control.Do(r.Context(), a))
}

// controlAllowed checks the owner's signature and that the page is the
// control panel and it is on. It answers a refused request itself.
func (s *Server) controlAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !s.allowed(w, r) {
		return false
	}
	if _, ok := s.loadKind(w, r, KindControl); !ok {
		return false
	}
	if s.control == nil {
		http.Error(w, `{"error":"The control panel is off."}`, http.StatusNotFound)
		return false
	}
	return true
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
