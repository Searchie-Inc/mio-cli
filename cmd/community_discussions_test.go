package cmd

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// MIO-2283: `community discussions update` drops --title/--body (the admin PATCH
// cannot set them) and replaces --pinned with the three moderation booleans
// --is-pinned / --is-locked / --is-broadcast, mapped to snake_case attributes on
// the JSON:API PATCH envelope (type "discussions").

// TestCommunityDiscussionsUpdate_MapsBoolFlags verifies a full update PATCHes
// .../discussions/{id} with type "discussions" and all three booleans mapped.
func TestCommunityDiscussionsUpdate_MapsBoolFlags(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "discussions", "update", "disc_abc",
			"--is-pinned=true", "--is-locked=true", "--is-broadcast=false")...)

	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", *gotMethod)
	}
	if want := "/api/v1/admin/teams/t_team1/hubs/hub_123/discussions/disc_abc"; *gotPath != want {
		t.Errorf("path = %q, want %q", *gotPath, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "discussions" {
		t.Errorf("type = %q, want discussions", typ)
	}
	if attrs["is_pinned"] != true {
		t.Errorf("is_pinned = %v, want true", attrs["is_pinned"])
	}
	if attrs["is_locked"] != true {
		t.Errorf("is_locked = %v, want true", attrs["is_locked"])
	}
	if v, ok := attrs["is_broadcast"]; !ok || v != false {
		t.Errorf("is_broadcast = %v (present=%v), want false", v, ok)
	}
}

// TestCommunityDiscussionsUpdate_PartialOnlyChangedFlags verifies only the flags
// the caller set are sent (partial update — no false-y defaults leak in).
func TestCommunityDiscussionsUpdate_PartialOnlyChangedFlags(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "discussions", "update", "disc_abc", "--is-locked=true")...)

	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	_, attrs := decodeDataTypeAttrs(t, *gotBody)
	if len(attrs) != 1 {
		t.Fatalf("attrs = %v, want exactly {is_locked}", attrs)
	}
	if attrs["is_locked"] != true {
		t.Errorf("is_locked = %v, want true", attrs["is_locked"])
	}
}

// TestCommunityDiscussionsUpdate_NoFlags_UsageNoRequest verifies that supplying
// no field flags is a usage error that fires zero HTTP requests.
func TestCommunityDiscussionsUpdate_NoFlags_UsageNoRequest(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "discussions", "update", "disc_abc")...)

	if res.Code != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); stderr=%s", res.Code, res.Stderr)
	}
	if *gotMethod != "" {
		t.Errorf("a request fired (%s) but none was expected", *gotMethod)
	}
}

// TestCommunityDiscussionsUpdate_NoFlagsRejectBeforeTeamResolve verifies the
// no-field-flags usage error fires BEFORE auth/team/hub resolution (repo
// contract) — even with a team NAME that would otherwise need a resolving GET —
// so no HTTP request is made and the exit is usage (2), not auth (3).
func TestCommunityDiscussionsUpdate_NoFlagsRejectBeforeTeamResolve(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		"community", "discussions", "update", "disc_abc",
		"--team", "acme-name", // NOT id-shaped → would trigger a ResolveTeam GET
		"--hub", "hub_123",
		// no --is-pinned/--is-locked/--is-broadcast
	)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage before team resolve); stderr=%s", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *gotMethod != "" {
		t.Errorf("a request fired (%s) — the no-flags usage error must precede team resolution", *gotMethod)
	}
}

// TestCommunityDiscussionsUpdate_RemovedFlagsRejected verifies the dropped flags
// (--title/--body/--pinned) are no longer accepted and fire no request.
func TestCommunityDiscussionsUpdate_RemovedFlagsRejected(t *testing.T) {
	cases := [][]string{
		{"--title", "x"},
		{"--body", "x"},
		{"--pinned=true"},
	}
	for _, extra := range cases {
		srv, gotMethod, _, _ := captureHubRequest(t, http.StatusOK)
		args := withTeam("t_team1", "--hub", "hub_123",
			"community", "discussions", "update", "disc_abc")
		args = append(args, extra...)

		res := runContract(t, baseEnv(srv.URL), args...)
		if res.Code == 0 {
			t.Errorf("%v accepted, want rejected (flag removed)", extra)
		}
		if *gotMethod != "" {
			t.Errorf("%v fired a request (%s), want none", extra, *gotMethod)
		}
	}
}

// MIO-2808: `community discussions create` — the CLI half of mio-backend #544
// (0da17745), the impersonation-free admin welcome-post endpoint
// POST /api/admin/teams/{team_id}/hubs/{hub_id}/discussions.

// TestCommunityDiscussionsCreate_PostsEnvelopeToCollection pins the whole wire
// contract: POST to the COLLECTION path, JSON:API type "discussions" (derived
// from the path — the backend schema's type Literal), and exactly the four
// attributes the create schema accepts.
func TestCommunityDiscussionsCreate_PostsEnvelopeToCollection(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "discussions", "create",
			"--space-id", "space_abc", "--title", "Welcome!", "--body", "Say hi.",
			"--is-published=false")...)

	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if want := "/api/v1/admin/teams/t_team1/hubs/hub_123/discussions"; *gotPath != want {
		t.Errorf("path = %q, want %q (collection, no id)", *gotPath, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "discussions" {
		t.Errorf("type = %q, want discussions", typ)
	}
	want := map[string]any{
		"space_id":     "space_abc",
		"title":        "Welcome!",
		"body":         "Say hi.",
		"is_published": false,
	}
	if !reflect.DeepEqual(attrs, want) {
		t.Errorf("attributes = %v, want %v", attrs, want)
	}
}

// TestCommunityDiscussionsCreate_OnlyChangedFlagsSerialized: an unset --body /
// --is-published must NOT be sent. is_published in particular defaults to TRUE
// server-side, so shipping the flag's Go zero value would silently turn every
// minimal create into a draft.
func TestCommunityDiscussionsCreate_OnlyChangedFlagsSerialized(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "discussions", "create",
			"--space-id", "space_abc", "--title", "Welcome!")...)

	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	_, attrs := decodeDataTypeAttrs(t, *gotBody)
	want := map[string]any{"space_id": "space_abc", "title": "Welcome!"}
	if !reflect.DeepEqual(attrs, want) {
		t.Errorf("attributes = %v, want exactly %v (unset flags are not serialized)", attrs, want)
	}
}

// TestCommunityDiscussionsCreate_MissingRequiredFlags_UsageNoRequest: a missing
// --space-id or --title is ExitUsage BEFORE auth/team/hub resolution, so no HTTP
// fires at all — asserted with a team NAME that would otherwise force a
// resolving GET.
func TestCommunityDiscussionsCreate_MissingRequiredFlags_UsageNoRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no flags at all", nil},
		{"title without space", []string{"--title", "Welcome!"}},
		{"space without title", []string{"--space-id", "space_abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, gotMethod, _, _ := captureHubRequest(t, http.StatusCreated)
			args := append([]string{
				"community", "discussions", "create",
				"--team", "acme-name", // NOT id-shaped → would trigger a ResolveTeam GET
				"--hub", "hub_123",
			}, tc.args...)

			res := runContract(t, baseEnv(srv.URL), args...)
			if res.Code != errs.ExitUsage {
				t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%s", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *gotMethod != "" {
				t.Errorf("a request fired (%s) — the usage error must precede team resolution", *gotMethod)
			}
		})
	}
}

// TestCommunityDiscussionsCreate_NoAuthorFlag: the author is server-derived by
// design (MIO-2262). Offering --author-contact-id would re-open the exact
// impersonation surface that got v1 of the endpoint reverted — and there is no
// wire field to carry it (the request schema is extra="forbid"), so the flag must
// not exist and must fire no request.
func TestCommunityDiscussionsCreate_NoAuthorFlag(t *testing.T) {
	for _, flag := range []string{"--author-contact-id", "--author-contact", "--author"} {
		srv, gotMethod, _, _ := captureHubRequest(t, http.StatusCreated)

		res := runContract(t, baseEnv(srv.URL),
			withTeam("t_team1", "--hub", "hub_123",
				"community", "discussions", "create",
				"--space-id", "space_abc", "--title", "Welcome!",
				flag, "contact_xyz")...)

		if res.Code != errs.ExitUsage {
			t.Errorf("%s exit = %d, want %d (unknown flag)", flag, res.Code, errs.ExitUsage)
		}
		if *gotMethod != "" {
			t.Errorf("%s fired a request (%s), want none", flag, *gotMethod)
		}
	}
}
