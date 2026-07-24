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
			"missing_slugs":   []any{"vip", "founding-member"}, // list[str]
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

// cross_team_refs is a list of {type, message} OBJECTS (not strings) — the object
// shape must render, not be silently dropped (Opus review of #72).
func TestApiError_Message_SurfacesCrossTeamRefObjects(t *testing.T) {
	e := apiError{
		Detail: "One or more condition references failed to compile.",
		Meta: map[string]any{
			"cross_team_refs": []any{
				map[string]any{"type": "has_tag", "message": "tag 'vip' belongs to another team"},
			},
		},
	}
	got := e.message()
	if !strings.Contains(got, "cross_team_refs: has_tag: tag 'vip' belongs to another team") {
		t.Errorf("message() = %q, want it to name the cross-team ref object", got)
	}
}

func TestApiError_Message_NoMetaUnchanged(t *testing.T) {
	e := apiError{Detail: "boom"}
	if got := e.message(); got != "boom" {
		t.Errorf("message() = %q, want boom (nothing appended when meta is absent)", got)
	}
}

func TestMetaRefs(t *testing.T) {
	// mixed: strings (missing_slugs shape) + a {type,message} object (cross_team
	// shape) + skippable junk (empty string, bare number, empty object).
	got := metaRefs([]any{
		"a", "",
		map[string]any{"type": "has_tag", "message": "cross-team"},
		3,
		map[string]any{},
	})
	want := []string{"a", "has_tag: cross-team"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("metaRefs = %v, want %v", got, want)
	}
	if metaRefs("not-an-array") != nil {
		t.Error("a non-array meta value must yield nil")
	}
	// message-only object (no type) renders just the message.
	if r := metaRefs([]any{map[string]any{"message": "boom"}}); len(r) != 1 || r[0] != "boom" {
		t.Errorf("message-only object = %v, want [boom]", r)
	}
}
