package client

import (
	"errors"
	"strings"
)

type apiError struct {
	Status string          `json:"status"`
	Code   string          `json:"code"`
	Title  string          `json:"title"`
	Detail string          `json:"detail"`
	Source *apiErrorSource `json:"source"`
	Meta   map[string]any  `json:"meta"`
}

type apiErrorSource struct {
	Pointer   string `json:"pointer"`
	Parameter string `json:"parameter"`
}

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
	for _, key := range []string{"missing_slugs", "cross_team_refs"} {
		if refs := metaRefs(e.Meta[key]); len(refs) > 0 {
			msg += " (" + key + ": " + strings.Join(refs, ", ") + ")"
		}
	}
	return msg
}

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

type apiErrorList struct {
	Errors []apiError

	doc []byte
}

func newAPIErrorList(list []apiError, body []byte) *apiErrorList {
	return &apiErrorList{Errors: list, doc: append([]byte(nil), body...)}
}

func (l *apiErrorList) APIErrorDocument() []byte {
	if l == nil {
		return nil
	}
	return l.doc
}

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
