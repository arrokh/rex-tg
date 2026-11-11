package telegram

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// APIError is Telegram's answer when it rejects a request (ok: false).
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

// Error implements error.
func (e *APIError) Error() string {
	s := fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
	if e.RetryAfter > 0 {
		s += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return s
}

// UnknownOutcomeError means the request may have taken effect: a network
// error, a timeout, a 5xx answer, or a malformed success body.
type UnknownOutcomeError struct {
	Method string
	Err    error
}

// Error implements error.
func (e *UnknownOutcomeError) Error() string {
	return fmt.Sprintf("telegram %s: outcome unknown: %v", e.Method, e.Err)
}

// Unwrap returns the underlying cause (token already removed from its text).
func (e *UnknownOutcomeError) Unwrap() error { return e.Err }

// Class groups errors by how a caller should react.
type Class int

// Error classes.
const (
	ClassNone Class = iota
	ClassRateLimited
	ClassParseEntities
	ClassNotModified
	ClassEditTargetGone
	ClassDeleteTargetGone
	ClassThreadNotFound
	ClassBlocked
	ClassConflict
	ClassBadRequest
	ClassUnknownOutcome
)

// String returns the class name.
func (c Class) String() string {
	names := [...]string{"none", "rate_limited", "parse_entities", "not_modified", "edit_target_gone",
		"delete_target_gone", "thread_not_found", "blocked", "conflict", "bad_request", "unknown_outcome"}
	if c < 0 || int(c) >= len(names) {
		return fmt.Sprintf("class(%d)", int(c))
	}
	return names[c]
}

// Classify maps an error from this package to a Class. nil gives ClassNone.
// Any error that is not an *APIError is treated as an unknown outcome.
func Classify(err error) Class {
	if err == nil {
		return ClassNone
	}
	var api *APIError
	if !errors.As(err, &api) {
		return ClassUnknownOutcome
	}
	d := strings.ToLower(api.Description)
	has := func(s string) bool { return strings.Contains(d, s) }
	switch {
	case api.Code >= 500:
		return ClassUnknownOutcome
	case has("can't parse entities"):
		return ClassParseEntities
	case has("message is not modified"):
		return ClassNotModified
	case has("message to edit not found"):
		return ClassEditTargetGone
	case has("message to delete not found"), has("message can't be deleted"):
		return ClassDeleteTargetGone
	case has("message thread not found"), has("topic_deleted"), has("thread not found"):
		return ClassThreadNotFound
	case api.Code == 429:
		return ClassRateLimited
	case api.Code == 403:
		return ClassBlocked
	case api.Code == 409:
		return ClassConflict
	}
	return ClassBadRequest
}

// sanitizedError carries an error message with the token removed while
// keeping the cause for errors.Is and errors.As.
type sanitizedError struct {
	msg   string
	cause error
}

func (e *sanitizedError) Error() string { return e.msg }
func (e *sanitizedError) Unwrap() error { return e.cause }
