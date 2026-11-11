package install

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	claudeEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest", "Notification", "SessionEnd"}
	codexEvents  = []string{"SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest"}
)

const claudeTimeout = 5

// InstallClaude adds rex-tg's hook entries to the Claude Code settings file.
func InstallClaude(p Paths) (Change, error) {
	return p.installHooks(p.ClaudeSettings, "claude", claudeEvents, claudeTimeout)
}

// UninstallClaude removes rex-tg's hook entries from the Claude Code settings.
func UninstallClaude(p Paths) (Change, error) {
	return p.uninstallHooks(p.ClaudeSettings, "claude", claudeEvents, false)
}

// InstallCodex appends rex-tg's hook entries at the end of each event's list.
func InstallCodex(p Paths) (Change, error) {
	return p.installHooks(p.CodexHooks, "codex", codexEvents, 0)
}

// UninstallCodex removes rex-tg's hook entries. It returns ErrWouldShiftTrust
// (naming the entries) when other entries follow a rex-tg entry, unless force.
func UninstallCodex(p Paths, force bool) (Change, error) {
	return p.uninstallHooks(p.CodexHooks, "codex", codexEvents, !force)
}

func (p Paths) installHooks(path, runtime string, events []string, timeout int) (Change, error) {
	if err := p.validate(); err != nil {
		return Change{Path: path}, err
	}
	cmd := p.hookCommand(runtime)
	return p.editJSON(path, true, func(src []byte) ([]byte, string, error) {
		var added, updated []string
		for _, ev := range events {
			key := "hooks." + ev
			arr := gjson.GetBytes(src, key)
			var err error
			if !arr.Exists() {
				raw := []byte("[" + newGroup(cmd, timeout) + "]")
				switch h := gjson.GetBytes(src, "hooks"); {
				case !h.Exists():
					src, err = setMember(src, "", "hooks", []byte(`{"`+ev+`":`+string(raw)+`}`))
				case h.IsObject():
					src, err = setMember(src, "hooks", ev, raw)
				default:
					err = fmt.Errorf("hooks is not an object")
				}
				if err != nil {
					return nil, "", err
				}
				added = append(added, ev)
				continue
			}
			if !arr.IsArray() {
				return nil, "", fmt.Errorf("%s is not an array", key)
			}
			found := false
			var stale [][2]int
			forEachHook(arr, func(gi, hi int, h gjson.Result) {
				ours, exact := p.isOurs(h.Get("command").String(), runtime)
				if ours {
					found = true
					if !exact {
						stale = append(stale, [2]int{gi, hi})
					}
				}
			})
			for _, s := range stale {
				path := fmt.Sprintf("%s.%d.hooks.%d.command", key, s[0], s[1])
				if src, err = sjson.SetBytes(src, path, cmd); err != nil {
					return nil, "", err
				}
				updated = append(updated, ev)
			}
			if !found {
				if src, err = appendToArray(src, key, []byte(newGroup(cmd, timeout))); err != nil {
					return nil, "", err
				}
				added = append(added, ev)
			}
		}
		var parts []string
		if len(added) > 0 {
			parts = append(parts, "added "+strings.Join(added, ", "))
		}
		if len(updated) > 0 {
			parts = append(parts, "updated "+strings.Join(updated, ", "))
		}
		if len(parts) == 0 {
			return src, "already installed", nil
		}
		return src, strings.Join(parts, "; "), nil
	})
}

func newGroup(cmd string, timeout int) string {
	c, _ := json.Marshal(cmd)
	s := `{"hooks":[{"type":"command","command":` + string(c)
	if timeout > 0 {
		s += `,"timeout":` + strconv.Itoa(timeout)
	}
	return s + `}]}`
}

// forEachHook calls fn for every hook entry of an event's group array.
func forEachHook(arr gjson.Result, fn func(gi, hi int, h gjson.Result)) {
	gi := 0
	arr.ForEach(func(_, g gjson.Result) bool {
		hi := 0
		g.Get("hooks").ForEach(func(_, h gjson.Result) bool {
			fn(gi, hi, h)
			hi++
			return true
		})
		gi++
		return true
	})
}

func (p Paths) uninstallHooks(path, runtime string, events []string, guardTrust bool) (Change, error) {
	if err := p.validate(); err != nil {
		return Change{Path: path}, err
	}
	return p.editJSON(path, false, func(src []byte) ([]byte, string, error) {
		var removed []string
		var shifted []string
		type removal struct {
			ev        string
			wholeGrps []int
			hooks     [][2]int
		}
		var plans []removal
		for _, ev := range events {
			arr := gjson.GetBytes(src, "hooks."+ev)
			if !arr.IsArray() {
				continue
			}
			// Gather per group: hook count and which hooks are ours.
			type grp struct {
				total int
				ours  []int
			}
			var groups []grp
			arr.ForEach(func(_, g gjson.Result) bool {
				var gr grp
				g.Get("hooks").ForEach(func(_, h gjson.Result) bool {
					if ours, _ := p.isOurs(h.Get("command").String(), runtime); ours {
						gr.ours = append(gr.ours, gr.total)
					}
					gr.total++
					return true
				})
				groups = append(groups, gr)
				return true
			})
			var plan removal
			plan.ev = ev
			first := -1 // position of the first rex-tg group
			for gi, g := range groups {
				if len(g.ours) == 0 {
					continue
				}
				if first < 0 {
					first = gi
				}
				if len(g.ours) == g.total {
					plan.wholeGrps = append(plan.wholeGrps, gi)
				} else {
					for _, hi := range g.ours {
						plan.hooks = append(plan.hooks, [2]int{gi, hi})
					}
				}
			}
			if first < 0 {
				continue
			}
			// Entries that would move: any other entry after a rex-tg entry.
			seen := false
			for gi, g := range groups {
				for hi := 0; hi < g.total; hi++ {
					if containsInt(g.ours, hi) {
						seen = true
					} else if seen {
						shifted = append(shifted, fmt.Sprintf("%s:%d:%d", snake(ev), gi, hi))
					}
				}
			}
			plans = append(plans, plan)
		}
		if len(plans) == 0 {
			return src, "not installed", nil
		}
		if guardTrust && len(shifted) > 0 {
			return nil, "", fmt.Errorf("%w: %s (use --force to remove anyway, then trust them again in Codex)",
				ErrWouldShiftTrust, strings.Join(shifted, ", "))
		}
		for _, pl := range plans {
			key := "hooks." + pl.ev
			var err error
			// Delete from the end so earlier indexes stay valid.
			for i := len(pl.hooks) - 1; i >= 0; i-- {
				h := pl.hooks[i]
				if src, err = sjson.DeleteBytes(src, fmt.Sprintf("%s.%d.hooks.%d", key, h[0], h[1])); err != nil {
					return nil, "", err
				}
			}
			for i := len(pl.wholeGrps) - 1; i >= 0; i-- {
				if src, err = sjson.DeleteBytes(src, fmt.Sprintf("%s.%d", key, pl.wholeGrps[i])); err != nil {
					return nil, "", err
				}
			}
			if len(gjson.GetBytes(src, key).Array()) == 0 {
				if src, err = sjson.DeleteBytes(src, key); err != nil {
					return nil, "", err
				}
			}
			removed = append(removed, pl.ev)
		}
		return src, "removed " + strings.Join(removed, ", "), nil
	})
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// snake turns SessionStart into session_start, as Codex names events in trust keys.
func snake(ev string) string {
	var b strings.Builder
	for i, r := range ev {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
