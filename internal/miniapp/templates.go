package miniapp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// templateSend queues one saved template as a chat message for the agent in
// the page's bound topic and session. The action id makes retries safe.
func (s *Server) templateSend(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	keyed := s.keyed(r)
	if keyed && !s.keyCanSendBoundMessage(r) {
		http.Error(w, `{"error":"Sending to the agent requires your verified Tailscale login."}`, http.StatusForbidden)
		return
	}
	p, ok := s.loadKind(w, r, KindTemplates)
	if !ok {
		return
	}
	if p.Thread <= 0 || p.Conv <= 0 {
		http.Error(w, `{"error":"This template page has no agent topic."}`, http.StatusBadRequest)
		return
	}
	if s.control == nil {
		http.Error(w, `{"error":"Sending templates is off."}`, http.StatusNotFound)
		return
	}
	if !keyed {
		if _, err := CheckInitData(r.Header.Get("X-Telegram-Init-Data"), s.secret, s.now(), ActMaxAge); err != nil {
			http.Error(w, `{"error":"This page was opened more than an hour ago. Close it and open Templates again."}`, http.StatusForbidden)
			return
		}
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, `{"error":"Send JSON."}`, http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		Act  string `json:"act"`
		Text string `json:"text"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*maxDraftBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, `{"error":"This template message is not valid."}`, http.StatusBadRequest)
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		http.Error(w, `{"error":"This template message is not valid."}`, http.StatusBadRequest)
		return
	}
	a := ControlAction{Op: OpTemplateMessage, Act: body.Act, Thread: p.Thread, Conv: p.Conv, Text: body.Text}
	if err := a.Check(); err != nil {
		b, _ := json.Marshal(map[string]string{"error": capital(err.Error()) + "."})
		http.Error(w, string(b), http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(s.control.Do(r.Context(), a))
}
