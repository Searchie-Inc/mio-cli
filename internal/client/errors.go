package client

import "strings"

// apiError mirrors a single JSON:API error object. Every member is optional per
// the spec; we keep the ones that carry useful operator-facing signal.
type apiError struct {
	Status string          `json:"status"`
	Code   string          `json:"code"`
	Title  string          `json:"title"`
	Detail string          `json:"detail"`
	Source *apiErrorSource `json:"source"`
	Meta   map[string]any  `json:"meta"`
}

// apiErrorSource locates the offending field/parameter for an apiError.
type apiErrorSource struct {
	Pointer   string `json:"pointer"`
	Parameter string `json:"parameter"`
}

// message renders one error object into a short human-readable line, preferring
// detail > title > code, and appending the source pointer/parameter if present.
func (e apiError) message() string {
	msg := e.Detail
	if msg == "" {
		msg = e.Title
	}
	if msg == "" {
		msg = e.Code
	}
	if msg == "" {
		msg = "unknown error"
	}
	if e.Source != nil {
		switch {
		case e.Source.Pointer != "":
			msg += " (" + e.Source.Pointer + ")"
		case e.Source.Parameter != "":
			msg += " (parameter: " + e.Source.Parameter + ")"
		}
	}
	return msg
}

// apiErrorList is the error type returned when a response body carries a
// top-level JSON:API `errors` array. It joins every error object into one
// message so the cause string is useful to humans and greppable by agents.
type apiErrorList struct {
	Errors []apiError
}

// Error implements the error interface by joining all error messages.
func (l *apiErrorList) Error() string {
	if l == nil || len(l.Errors) == 0 {
		return "request failed"
	}
	parts := make([]string, 0, len(l.Errors))
	for _, e := range l.Errors {
		parts = append(parts, e.message())
	}
	return strings.Join(parts, "; ")
}
