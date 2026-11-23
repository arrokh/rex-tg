package miniapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// KindConfig is a topic's session-scoped model and thinking-effort page.
const KindConfig = "config"

// ConfigItem is one setting the linked agent can change.
type ConfigItem struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Value   string   `json:"value,omitempty"`
	Choices []string `json:"choices,omitempty"`
	Free    bool     `json:"free,omitempty"`
}

// ConfigState is the current agent session's available settings.
type ConfigState struct {
	Controls []ConfigItem `json:"controls"`
	At       string       `json:"at"`
}

// Configurator reads settings only while the page's exact topic and session
// remain current. Config actions are dispatched through Control.
type Configurator interface {
	State(ctx context.Context, thread, conv int64) (ConfigState, error)
}

// ConfigAction is a page-bound change to a model or effort setting.
type ConfigAction struct {
	Act     string         `json:"act"`
	Changes []ConfigChange `json:"changes,omitempty"`
	Name    string         `json:"name,omitempty"`  // legacy single-setting action
	Value   string         `json:"value,omitempty"` // legacy single-setting action
}

// WithConfig turns on topic/session-bound configuration pages.
func (s *Server) WithConfig(c Configurator) *Server {
	s.config = c
	return s
}

// configState answers the settings for the page's exact session.
func (s *Server) configState(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	p, ok := s.loadKind(w, r, KindConfig, KindProject)
	if !ok {
		return
	}
	if s.config == nil {
		http.Error(w, `{"error":"Config is off."}`, http.StatusNotFound)
		return
	}
	st, err := s.config.State(r.Context(), p.Thread, p.Conv)
	if err != nil {
		http.Error(w, `{"error":"This Config page is no longer current. Open Config again from the topic."}`, http.StatusConflict)
		return
	}
	st.At = s.now().Format("15:04:05")
	_ = json.NewEncoder(w).Encode(st)
}

// configDo accepts a validated model/effort change. A keyboard key is
// sufficient only with the current key and verified owner Tailscale login;
// signed Telegram actions must be fresh.
func (s *Server) configDo(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	keyed := s.keyed(r)
	if keyed && !s.keyCanSendBoundMessage(r) {
		http.Error(w, `{"error":"Changing settings requires your verified Tailscale login."}`, http.StatusForbidden)
		return
	}
	p, ok := s.loadKind(w, r, KindConfig, KindProject)
	if !ok {
		return
	}
	if s.config == nil || s.control == nil {
		http.Error(w, `{"error":"Config is off."}`, http.StatusNotFound)
		return
	}
	if !keyed {
		if _, err := CheckInitData(r.Header.Get("X-Telegram-Init-Data"), s.secret, s.now(), ActMaxAge); err != nil {
			http.Error(w, `{"error":"This page was opened more than an hour ago. Close it and open it again."}`, http.StatusForbidden)
			return
		}
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, `{"error":"Send JSON."}`, http.StatusUnsupportedMediaType)
		return
	}
	var body ConfigAction
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, `{"error":"This setting change is not valid."}`, http.StatusBadRequest)
		return
	}
	if !actID.MatchString(body.Act) {
		http.Error(w, `{"error":"Choose a setting and value, then try again."}`, http.StatusBadRequest)
		return
	}
	a := ControlAction{Op: OpConfig, Act: body.Act, Thread: p.Thread, Conv: p.Conv,
		Name: body.Name, Value: body.Value, Changes: body.Changes}
	if err := a.Check(); err != nil {
		http.Error(w, `{"error":"This setting change is not valid."}`, http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(s.control.Do(r.Context(), a))
}
