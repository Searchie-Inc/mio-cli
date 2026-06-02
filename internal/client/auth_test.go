package client

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
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
