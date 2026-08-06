package client

import (
	"errors"
	"strings"
)

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
	// Surface diagnostic reference arrays carried in the error's meta so an
	// otherwise-generic failure is self-diagnosable — e.g. a segment-search 422
	// "One or more condition references failed to compile." returns the exact
	// unresolved refs in meta.missing_slugs / meta.cross_team_refs (MIO-2590).
	// Without this they were silently dropped from the rendered error.
	for _, key := range []string{"missing_slugs", "cross_team_refs"} {
		if refs := metaRefs(e.Meta[key]); len(refs) > 0 {
			msg += " (" + key + ": " + strings.Join(refs, ", ") + ")"
		}
	}
	return msg
}

// metaRefs flattens a JSON:API meta array into display strings. The backend uses
// two shapes: missing_slugs is a list of plain strings, while cross_team_refs is
// a list of {type, message} objects (list[dict[str,str]]) — so handle both, or
// the object form would render as nothing. Non-array meta yields nil.
func metaRefs(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		switch t := el.(type) {
		case string:
			if t != "" {
				out = append(out, t)
			}
		case map[string]any:
			typ, _ := t["type"].(string)
			m, _ := t["message"].(string)
			switch {
			case typ != "" && m != "":
				out = append(out, typ+": "+m)
			case m != "":
				out = append(out, m)
			case typ != "":
				out = append(out, typ)
			}
		}
	}
	return out
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

// HasAPIErrorCode reports whether err's chain carries a JSON:API error object
// with the given machine-readable `code`.
//
// Callers must use this rather than matching err.Error(): apiError.message()
// prefers detail > title > code, so the code is ABSENT from the rendered string
// whenever the server also sent a detail — which it normally does. A caller that
// grepped the message for a code would therefore silently never match, and would
// keep not matching no matter how the message was worded.
func HasAPIErrorCode(err error, code string) bool {
	var list *apiErrorList
	for e := err; e != nil; {
		if errors.As(e, &list) {
			for _, ae := range list.Errors {
				if ae.Code == code {
					return true
				}
			}
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
