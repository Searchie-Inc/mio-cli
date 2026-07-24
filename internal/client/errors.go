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
	// Surface diagnostic reference arrays carried in the error's meta so an
	// otherwise-generic failure is self-diagnosable — e.g. a segment-search 422
	// "One or more condition references failed to compile." returns the exact
	// unresolved tag slugs / hub ids in meta.missing_slugs / meta.cross_team_refs
	// (MIO-2590). Without this they were silently dropped from the rendered error.
	for _, key := range []string{"missing_slugs", "cross_team_refs"} {
		if refs := metaStrings(e.Meta[key]); len(refs) > 0 {
			msg += " (" + key + ": " + strings.Join(refs, ", ") + ")"
		}
	}
	return msg
}

// metaStrings returns the non-empty string elements of a JSON:API meta value
// that decodes as an array ([]any); it returns nil for any other shape.
func metaStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		if s, ok := el.(string); ok && s != "" {
			out = append(out, s)
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
