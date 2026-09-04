package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// makeTestToken constructs a syntactically-valid but cryptographically unsigned
// JWT (header.payload.fakesig) carrying the provided claims namespace payload.
// The signature is a constant placeholder — TeamIDFromAccessToken does not
// verify signatures, so this is sufficient for unit tests.
func makeTestToken(t *testing.T, nsClaims map[string]any) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"alg":"RS256","typ":"at+jwt"}`),
	)

	payload := map[string]any{
		"sub":           "user-001",
		"iss":           "https://membership.io",
		"aud":           "mio-api",
		claimsNamespace: nsClaims,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	payloadSeg := base64.RawURLEncoding.EncodeToString(payloadJSON)

	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return strings.Join([]string{header, payloadSeg, sig}, ".")
}

func TestTeamIDFromAccessToken_ExtractsTeamID(t *testing.T) {
	wantTeamID := "team-abc-123"
	token := makeTestToken(t, map[string]any{
		"team_id":       wantTeamID,
		"roles":         []string{"owner"},
		"token_type":    "access",
		"token_version": 0,
	})

	got := TeamIDFromAccessToken(token)
	if got != wantTeamID {
		t.Errorf("TeamIDFromAccessToken = %q, want %q", got, wantTeamID)
	}
}

func TestTeamIDFromAccessToken_ReturnsEmptyWhenNoTeamID(t *testing.T) {
	// Namespace present but team_id is null / absent.
	token := makeTestToken(t, map[string]any{
		"roles":      []string{},
		"token_type": "access",
	})
	got := TeamIDFromAccessToken(token)
	if got != "" {
		t.Errorf("TeamIDFromAccessToken = %q, want empty string", got)
	}
}

func TestTeamIDFromAccessToken_ReturnsEmptyForMalformedToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"one-segment", "onlyone"},
		{"two-segments", "header.payload"},
		{"invalid-base64", "hdr.!!!.sig"},
		{"not-json-payload", "hdr." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".sig"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TeamIDFromAccessToken(tc.token); got != "" {
				t.Errorf("TeamIDFromAccessToken(%q) = %q, want empty", tc.token, got)
			}
		})
	}
}

// MIO-2656 (codex review round 1): Login masks a failed sign-in with the
// friendlier "invalid email or password". That mask used to key off
// errs.CodeOf(err) == ExitAuth — but ExitAuth covers 401 AND 403, so a 403 on
// the login route was both mislabelled ("invalid email or password" when the
// credentials may have been perfectly correct) and stripped of its status,
// leaving the envelope to fall back to "401" — the exact mis-mapping this
// ticket exists to remove, at a second site.
//
// The mask is now narrowed to a true 401, and it carries that 401 explicitly so
// the envelope reports it rather than deriving it.
func TestLogin_401IsMaskedAndKeepsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Incorrect email or password"}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv, "").Login(context.Background(), "a@b.c", "pw")
	if err == nil {
		t.Fatal("Login must fail on a 401")
	}
	if got := errs.CodeOf(err); got != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth) — unchanged", got, errs.ExitAuth)
	}
	if got := errs.HTTPStatusOf(err); got != http.StatusUnauthorized {
		t.Errorf("HTTPStatusOf = %d, want 401 — the masked error must still carry its status", got)
	}
	if !strings.Contains(err.Error(), "invalid email or password") {
		t.Errorf("detail = %q, want the friendly bad-credentials message", err.Error())
	}
}

// A 403 on the login route is a DIFFERENT refusal from bad credentials (the
// account is locked, SSO is mandatory, the origin is blocked...). It must keep
// both the backend's own explanation and its own status, so an agent is not
// told to retry with a different password for a door that no password opens.
func TestLogin_403KeepsBackendDetailAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"status":"403","detail":"Account locked; contact support"}]}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv, "").Login(context.Background(), "a@b.c", "pw")
	if err == nil {
		t.Fatal("Login must fail on a 403")
	}
	if got := errs.HTTPStatusOf(err); got != http.StatusForbidden {
		t.Errorf("HTTPStatusOf = %d, want 403 — a 403 must not be reported as a 401", got)
	}
	// The exit code is unchanged: ExitCodeForStatus maps 401 and 403 alike.
	if got := errs.CodeOf(err); got != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth) — unchanged", got, errs.ExitAuth)
	}
	if !strings.Contains(err.Error(), "Account locked") {
		t.Errorf("detail = %q, want the backend's own explanation preserved", err.Error())
	}
	if strings.Contains(err.Error(), "invalid email or password") {
		t.Errorf("detail = %q, must NOT claim bad credentials for a 403", err.Error())
	}
}

func TestSubjectFromAccessToken_ExtractsSub(t *testing.T) {
	token := makeTestToken(t, map[string]any{"team_id": "team-abc"})
	got := SubjectFromAccessToken(token)
	if got != "user-001" {
		t.Errorf("SubjectFromAccessToken = %q, want %q (see makeTestToken)", got, "user-001")
	}
}

func TestSubjectFromAccessToken_ReturnsEmptyForMalformedToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"one-segment", "onlyone"},
		{"two-segments", "header.payload"},
		{"invalid-base64", "hdr.!!!.sig"},
		{"not-json-payload", "hdr." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".sig"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubjectFromAccessToken(tc.token); got != "" {
				t.Errorf("SubjectFromAccessToken(%q) = %q, want empty", tc.token, got)
			}
		})
	}
}

// TestListTeams_CapturesOwnerID pins that ListTeams surfaces each team's
// owner_id attribute on TeamInfo — the field MIO-3585's ownership check
// (resolveOwnedTeamID) compares against the JWT's own `sub` claim to decide
// which team the caller can actually mint an API key for.
func TestListTeams_CapturesOwnerID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"t1","type":"teams","attributes":{"name":"Owned","owner_id":"user-001"}},
			{"id":"t2","type":"teams","attributes":{"name":"Member Only","owner_id":"someone-else"}}
		]}`))
	}))
	defer srv.Close()

	teams, err := newTestClient(srv, "").ListTeams(context.Background(), "jwt_access_token")
	if err != nil {
		t.Fatalf("ListTeams error: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("len(teams) = %d, want 2", len(teams))
	}
	if teams[0].OwnerID != "user-001" {
		t.Errorf("teams[0].OwnerID = %q, want user-001", teams[0].OwnerID)
	}
	if teams[1].OwnerID != "someone-else" {
		t.Errorf("teams[1].OwnerID = %q, want someone-else", teams[1].OwnerID)
	}
}

func TestTeamIDFromAccessToken_HandlesNullTeamID(t *testing.T) {
	// team_id explicitly set to null (Python backend sets it to None for hub tokens).
	token := makeTestToken(t, map[string]any{
		"team_id":    nil,
		"token_type": "access",
	})
	got := TeamIDFromAccessToken(token)
	if got != "" {
		t.Errorf("TeamIDFromAccessToken with null team_id = %q, want empty", got)
	}
}
