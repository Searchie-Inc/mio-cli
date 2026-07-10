package cmd

import (
	"net/http"
	"testing"
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
