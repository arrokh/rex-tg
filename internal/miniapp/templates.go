package miniapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Template is a reusable message shared by the owner's devices.
type Template struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Text    string `json:"text"`
	Updated int64  `json:"updated"`
}

// TemplateRepository stores the owner's shared templates and their order.
type TemplateRepository interface {
	List(context.Context) ([]Template, error)
	Save(context.Context, Template) error
	Import(context.Context, []Template) (int, []string, error)
	Reorder(ctx context.Context, expected, ids []string) error
	Delete(context.Context, string) error
}

// ErrTemplateLimit means the shared collection already has 50 templates.
var ErrTemplateLimit = errors.New("miniapp: template limit reached")

// ErrTemplateDeleted means another device already deleted this template.
var ErrTemplateDeleted = errors.New("miniapp: template was deleted")

// ErrTemplateOrderChanged means another device changed the shared list first.
var ErrTemplateOrderChanged = errors.New("miniapp: template list changed")

// WithTemplates turns on shared template storage.
func (s *Server) WithTemplates(templates TemplateRepository) *Server {
	s.templates = templates
	return s
}

const maxTemplateCount = 50
const maxTemplateBody = 16000

func validTemplateID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func validTemplate(name, text string) bool {
	return strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 80 &&
		strings.TrimSpace(text) != "" && len([]byte(text)) <= maxTemplateBody
}

func (s *Server) templateList(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	if _, ok := s.loadKind(w, r, KindTemplates); !ok {
		return
	}
	if s.templates == nil {
		http.Error(w, `{"error":"Template storage is off."}`, http.StatusNotFound)
		return
	}
	items, err := s.templates.List(r.Context())
	if err != nil {
		http.Error(w, `{"error":"Could not read templates."}`, http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []Template{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"templates": items})
}

type templateRequest struct {
	Op        string     `json:"op"`
	ID        string     `json:"id,omitempty"`
	Name      string     `json:"name,omitempty"`
	Text      string     `json:"text,omitempty"`
	Templates []Template `json:"templates,omitempty"`
	Expected  []string   `json:"expected,omitempty"`
	IDs       []string   `json:"ids,omitempty"`
}

func (s *Server) templateAction(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	if _, ok := s.loadKind(w, r, KindTemplates); !ok {
		return
	}
	if s.templates == nil {
		http.Error(w, `{"error":"Template storage is off."}`, http.StatusNotFound)
		return
	}
	if !s.canSendBoundMessage(r) {
		http.Error(w, `{"error":"Saving templates requires fresh Telegram data, or a verified owner Tailscale login for keyboard launches."}`, http.StatusForbidden)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, `{"error":"Send JSON."}`, http.StatusUnsupportedMediaType)
		return
	}
	var body templateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTemplateCount*(maxTemplateBody*6+512)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, `{"error":"This template change is not valid."}`, http.StatusBadRequest)
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		http.Error(w, `{"error":"This template change is not valid."}`, http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	var deleted []string
	var err error
	switch body.Op {
	case "save":
		if !validTemplateID(body.ID) || !validTemplate(body.Name, body.Text) {
			http.Error(w, `{"error":"A template needs a name and message of the allowed size."}`, http.StatusBadRequest)
			return
		}
		err := s.templates.Save(ctx, Template{ID: body.ID, Name: strings.TrimSpace(body.Name), Text: body.Text, Updated: s.now().UnixMilli()})
		if err != nil {
			s.templateStoreError(w, err)
			return
		}
	case "delete":
		if !validTemplateID(body.ID) {
			http.Error(w, `{"error":"This template id is not valid."}`, http.StatusBadRequest)
			return
		}
		if err := s.templates.Delete(ctx, body.ID); err != nil {
			s.templateStoreError(w, err)
			return
		}
	case "reorder":
		if body.Expected == nil || body.IDs == nil || len(body.Expected) > maxTemplateCount ||
			len(body.IDs) > maxTemplateCount || len(body.Expected) != len(body.IDs) {
			http.Error(w, `{"error":"This template order is not valid."}`, http.StatusBadRequest)
			return
		}
		seenExpected, seenIDs := make(map[string]bool, len(body.Expected)), make(map[string]bool, len(body.IDs))
		for _, id := range body.Expected {
			if !validTemplateID(id) || seenExpected[id] {
				http.Error(w, `{"error":"This template order is not valid."}`, http.StatusBadRequest)
				return
			}
			seenExpected[id] = true
		}
		for _, id := range body.IDs {
			if !validTemplateID(id) || seenIDs[id] {
				http.Error(w, `{"error":"This template order is not valid."}`, http.StatusBadRequest)
				return
			}
			seenIDs[id] = true
		}
		if err := s.templates.Reorder(ctx, body.Expected, body.IDs); err != nil {
			s.templateStoreError(w, err)
			return
		}
	case "import":
		if len(body.Templates) > maxTemplateCount {
			http.Error(w, `{"error":"Import up to 50 templates at a time."}`, http.StatusBadRequest)
			return
		}
		seen := make(map[string]bool, len(body.Templates))
		for _, item := range body.Templates {
			if !validTemplateID(item.ID) || !validTemplate(item.Name, item.Text) || item.Updated <= 0 || seen[item.ID] {
				http.Error(w, `{"error":"A saved template could not be imported."}`, http.StatusBadRequest)
				return
			}
			seen[item.ID] = true
		}
		_, deleted, err = s.templates.Import(ctx, body.Templates)
		if err != nil {
			s.templateStoreError(w, err)
			return
		}
	default:
		http.Error(w, `{"error":"Choose a template action."}`, http.StatusBadRequest)
		return
	}
	items, err := s.templates.List(ctx)
	if err != nil {
		http.Error(w, `{"error":"Could not read templates."}`, http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []Template{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"templates": items, "deleted": deleted})
}

func (s *Server) templateStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrTemplateOrderChanged) {
		http.Error(w, `{"error":"The shared template list changed. Close and reopen Templates, then try again."}`, http.StatusConflict)
		return
	}
	if errors.Is(err, ErrTemplateLimit) {
		http.Error(w, `{"error":"You can save up to 50 templates."}`, http.StatusConflict)
		return
	}
	if errors.Is(err, ErrTemplateDeleted) {
		http.Error(w, `{"error":"This template was deleted on another device. Close and reopen Templates."}`, http.StatusConflict)
		return
	}
	http.Error(w, `{"error":"Could not save templates."}`, http.StatusInternalServerError)
}

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
