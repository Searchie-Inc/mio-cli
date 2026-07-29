package client

import (
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
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

// MIO-2656: errorForResponse must record the status the API answered with, for
// every body shape — a JSON:API errors array, a non-JSON:API body (gateway HTML,
// a plain string), and an empty body. Without this the envelope has to
// reverse-engineer the status out of the exit code, which is lossy.
func TestErrorForResponse_RecordsTransportStatus(t *testing.T) {
	c := &Client{}

	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int
	}{
		{"jsonapi errors array", 403, `{"errors":[{"status":"403","detail":"forbidden"}]}`, errs.ExitAuth},
		{"jsonapi errors array", 409, `{"errors":[{"status":"409","detail":"conflict"}]}`, errs.ExitUsage},
		{"jsonapi errors array", 422, `{"errors":[{"status":"422","detail":"invalid"}]}`, errs.ExitUsage},
		// A 502 from a proxy in front of the app has no JSON:API body at all, so
		// the body could never have been the source of the status.
		{"non-jsonapi body", 502, `<html>bad gateway</html>`, errs.ExitServer},
		{"empty body", 429, ``, errs.ExitRateLimited},
	}

	for _, tc := range cases {
		err := c.errorForResponse(tc.status, []byte(tc.body))
		if got := errs.HTTPStatusOf(err); got != tc.status {
			t.Errorf("%s: errorForResponse(%d).HTTPStatus = %d, want %d",
				tc.name, tc.status, got, tc.status)
		}
		if got := errs.CodeOf(err); got != tc.wantCode {
			t.Errorf("%s: errorForResponse(%d) exit code = %d, want %d (must be unchanged)",
				tc.name, tc.status, got, tc.wantCode)
		}
	}
}

// When the body's `status` member disagrees with the response line, the
// TRANSPORT status wins: the response line is what actually happened on the
// wire, it is what the exit code is derived from (so the two can never
// contradict each other in the envelope), and unlike the body member it always
// exists. The body's claim still reaches the user inside the detail message.
func TestErrorForResponse_TransportStatusBeatsBodyStatus(t *testing.T) {
	c := &Client{}
	// Response line says 403; the body claims 200 (a stale/hand-written member).
	err := c.errorForResponse(403, []byte(`{"errors":[{"status":"200","detail":"forbidden"}]}`))

	if got := errs.HTTPStatusOf(err); got != 403 {
		t.Errorf("HTTPStatusOf = %d, want 403 (the transport status, not the body's)", got)
	}
	if got := errs.CodeOf(err); got != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth)", got, errs.ExitAuth)
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("detail = %q, want the body's message preserved", err.Error())
	}
}

// A multi-error body has no single body status to promote — another reason the
// transport status is the only coherent source.
func TestErrorForResponse_MultipleBodyStatusesUseTransport(t *testing.T) {
	c := &Client{}
	err := c.errorForResponse(422, []byte(
		`{"errors":[{"status":"422","detail":"a"},{"status":"400","detail":"b"}]}`))

	if got := errs.HTTPStatusOf(err); got != 422 {
		t.Errorf("HTTPStatusOf = %d, want 422 (transport status)", got)
	}
	if msg := err.Error(); !strings.Contains(msg, "a") || !strings.Contains(msg, "b") {
		t.Errorf("detail = %q, want both messages joined", msg)
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
