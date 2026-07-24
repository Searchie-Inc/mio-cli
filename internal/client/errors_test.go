package client

import (
	"strings"
	"testing"
)

// MIO-2590: a segment-search 422 returns the unresolved refs in the error's
// meta.missing_slugs / meta.cross_team_refs; message() must surface them so the
// generic "failed to compile" is self-diagnosable.
func TestApiError_Message_SurfacesMetaRefs(t *testing.T) {
	e := apiError{
		Detail: "One or more condition references failed to compile.",
		Meta: map[string]any{
			"missing_slugs":   []any{"vip", "founding-member"},
			"cross_team_refs": []any{},
		},
	}
	got := e.message()
	if !strings.Contains(got, "missing_slugs: vip, founding-member") {
		t.Errorf("message() = %q, want it to name the missing slugs", got)
	}
	if strings.Contains(got, "cross_team_refs") {
		t.Errorf("an empty cross_team_refs must not be rendered: %q", got)
	}
}

func TestApiError_Message_NoMetaUnchanged(t *testing.T) {
	e := apiError{Detail: "boom"}
	if got := e.message(); got != "boom" {
		t.Errorf("message() = %q, want boom (nothing appended when meta is absent)", got)
	}
}

func TestMetaStrings(t *testing.T) {
	got := metaStrings([]any{"a", "", "b", 3})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("metaStrings = %v, want [a b] (skip empty + non-string elements)", got)
	}
	if metaStrings("not-an-array") != nil {
		t.Error("a non-array meta value must yield nil")
	}
}
