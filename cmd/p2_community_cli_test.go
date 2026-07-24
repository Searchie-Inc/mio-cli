package cmd

// p2_community_cli_test.go — contract tests for the P2 community-population CLI
// wrappers: `hub-memberships add` (MIO-2261) and `hub-memberships set-role`
// (MIO-2263). The create-membership endpoint ships in mio-backend PR #487; these
// tests pin the request shape the CLI sends. (MIO-2262 admin-create-discussion
// was dropped — Won't Do — so there is no CLI command for it.)

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── hub-memberships add (MIO-2261) ──────────────────────────────────────────

func TestHubMembershipsAdd_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"hub-memberships", "add", "contact_x", "--role", "moderator",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/members") {
		t.Errorf("path %q does not end with /hubs/hub_123/members", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "hub_memberships" {
		t.Errorf("data.type = %q, want hub_memberships", typ)
	}
	if attrs["contact_id"] != "contact_x" {
		t.Errorf("contact_id = %v, want contact_x", attrs["contact_id"])
	}
	if attrs["role"] != "moderator" {
		t.Errorf("role = %v, want moderator", attrs["role"])
	}
}

func TestHubMembershipsAdd_RejectsInvalidRole(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"hub-memberships", "add", "contact_x", "--role", "superuser",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("invalid --role must exit before any HTTP request")
	}
}

// ─── hub-memberships set-role (MIO-2263) ─────────────────────────────────────

func TestHubMembershipsSetRole_PatchRole(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"hub-memberships", "set-role", "contact_x", "--role", "admin",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/members/contact_x/role") {
		t.Errorf("path %q does not end with /members/contact_x/role", gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, gotBody)
	if typ != "hub_memberships" {
		t.Errorf("data.type = %q, want hub_memberships", typ)
	}
	if attrs["role"] != "admin" {
		t.Errorf("role = %v, want admin", attrs["role"])
	}
}

// MIO-2598 — set-role --role member (or none) demotes an elevated member back to
// a plain member by sending an explicit null role, which the backend
// HubMembershipRoleUpdateAttributes (role: Literal["admin","moderator"] | None)
// treats as "regular member".
func TestHubMembershipsSetRole_DemoteSendsNullRole(t *testing.T) {
	for _, roleArg := range []string{"member", "none"} {
		t.Run(roleArg, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/vnd.api+json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(minimalHubBody))
			}))
			t.Cleanup(srv.Close)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1", "--hub", "hub_123",
					"hub-memberships", "set-role", "contact_x", "--role", roleArg,
				)...)

			if res.Code != errs.ExitOK {
				t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
			}
			if gotMethod != http.MethodPatch {
				t.Errorf("method=%q want PATCH", gotMethod)
			}
			if !strings.HasSuffix(gotPath, "/members/contact_x/role") {
				t.Errorf("path %q does not end with /members/contact_x/role", gotPath)
			}
			typ, attrs := decodeDataTypeAttrs(t, gotBody)
			if typ != "hub_memberships" {
				t.Errorf("data.type=%q want hub_memberships", typ)
			}
			// Demote must send an explicit null role (present key, nil value) — NOT
			// omit it, and NOT send the literal string "member".
			if v, ok := attrs["role"]; !ok || v != nil {
				t.Errorf("role=%v (present=%v), want explicit null for demote", v, ok)
			}
		})
	}
}

func TestHubMembershipsSetRole_RejectsBadRole(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"hub-memberships", "set-role", "contact_x", "--role", "superuser",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("invalid --role must exit before any HTTP request")
	}
}

func TestHubMembershipsSetRole_RequiresRole(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"hub-memberships", "set-role", "contact_x",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("missing --role must exit before any HTTP request")
	}
}
