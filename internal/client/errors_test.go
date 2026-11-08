package client

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

func TestErrorForResponse_TransportStatusBeatsBodyStatus(t *testing.T) {
	c := &Client{}
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
	if r := metaRefs([]any{map[string]any{"message": "boom"}}); len(r) != 1 || r[0] != "boom" {
		t.Errorf("message-only object = %v, want [boom]", r)
	}
}

func TestHasAPIErrorCode_MatchesCodeNotMessage(t *testing.T) {
	err := errs.WrapHTTP(http.StatusConflict, &apiErrorList{Errors: []apiError{{
		Status: "409",
		Code:   "idempotency_fingerprint_mismatch",
		Detail: "key reused with a different request",
	}}})

	if strings.Contains(err.Error(), "idempotency_fingerprint_mismatch") {
		t.Fatalf("precondition broken: the code now appears in the message (%q) — "+
			"this test can no longer tell a code match from a message match", err.Error())
	}
	if !HasAPIErrorCode(err, "idempotency_fingerprint_mismatch") {
		t.Errorf("must match on the parsed `code` member; err=%v", err)
	}
	if HasAPIErrorCode(err, "catalog_digest_mismatch") {
		t.Error("must not match a different code")
	}
	if HasAPIErrorCode(nil, "anything") {
		t.Error("a nil error carries no codes")
	}
	if HasAPIErrorCode(errs.New(errs.ExitServer, "transport failed"), "idempotency_fingerprint_mismatch") {
		t.Error("a non-API error must not match any code")
	}
}

func TestAPIErrorDocument_IsTheBodyVerbatim(t *testing.T) {
	body := []byte(`{"errors":[{"id":"e1","status":"422","code":"invalid_price_config","title":"Invalid price config","detail":"too many","links":{"about":"https://example.test"},"meta":{"request_id":"rid-1","n":12345678901234567890}}],"meta":{"top":true}}`)

	c := &Client{}
	_, resErr := DecodeResource(body)
	_, colErr := DecodeCollection(body)
	for name, err := range map[string]error{
		"errorForResponse": c.errorForResponse(422, body),
		"DecodeResource":   resErr,
		"DecodeCollection": colErr,
	} {
		if got := errs.APIErrorDocumentOf(err); string(got) != string(body) {
			t.Errorf("%s: APIErrorDocumentOf = %q, want the response body verbatim", name, got)
		}
	}

	for _, nonDoc := range []string{`<html>bad gateway</html>`, ``, `{"errors":[]}`, `{"detail":"no errors member"}`} {
		if got := errs.APIErrorDocumentOf(c.errorForResponse(502, []byte(nonDoc))); got != nil {
			t.Errorf("body %q: APIErrorDocumentOf = %q, want nil (not a JSON:API errors document)", nonDoc, got)
		}
	}
}
