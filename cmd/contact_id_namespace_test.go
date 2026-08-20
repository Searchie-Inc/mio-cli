package cmd

// contact_id_namespace_test.go — MIO-2504.
//
// Two server-side id namespaces exist: `mio contacts` surfaces the TEAM-contact
// id as .id (routes bind {team_contact_id}), while the member-shaped verbs
// (hub-memberships add/set-role/ban/unban/warn, activity contact, community
// members ban/unban/warn, email enrollments create) route on the GLOBAL
// {contact_id}. Piping the .id from `mio contacts` into a member verb 404s for a
// live contact. These tests pin the two mitigations:
//
//   1. member-verb + contacts help explains the two ids, and
//   2. a not-found (exit 4) from a member verb carries an actionable hint that
//      redirects the user to the GLOBAL contact id.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── 404 hint (behavioural, subprocess) ──────────────────────────────────────

// TestContract_MemberVerb404_HintsGlobalContactID: every member-shaped verb that
// routes on the GLOBAL {contact_id} must, on a 404, append an actionable hint
// telling the user to use the .attributes.contact_id from `mio contacts` (not
// the .id). The hint keys off exit code 4, not a server message string, so it
// survives divergent backend messages. Driven as a subprocess because the
// JSON:API error envelope is written by main.go after os.Exit.
func TestContract_MemberVerb404_HintsGlobalContactID(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Status: 404, Body: `{"errors":[{"status":"404","detail":"Membership Not Found"}]}`},
	})
	bin := buildBinary(t)

	cases := []struct {
		name string
		args []string
	}{
		{"hub-memberships add", []string{"--team", "t_team1", "--hub", "hub_1", "hub-memberships", "add", "contact_x"}},
		{"hub-memberships set-role", []string{"--team", "t_team1", "--hub", "hub_1", "hub-memberships", "set-role", "contact_x", "--role", "admin"}},
		{"hub-memberships ban", []string{"--team", "t_team1", "--hub", "hub_1", "hub-memberships", "ban", "contact_x", "--yes"}},
		{"activity contact", []string{"--team", "t_team1", "--hub", "hub_1", "activity", "contact", "contact_x"}},
		{"community members ban", []string{"--team", "t_team1", "--hub", "hub_1", "community", "members", "ban", "contact_x", "--yes"}},
		{"email enrollments create", []string{"--team", "t_team1", "--hub", "hub_1", "email", "enrollments", "create", "dc_1", "--contact-id", "contact_x"}},
		{"email enrollments list-by-contact", []string{"--team", "t_team1", "--hub", "hub_1", "email", "enrollments", "list-by-contact", "contact_x"}},
		{"access-rules overrides create", []string{"--team", "t_team1", "--hub", "hub_1", "access-rules", "overrides", "create", "--contact-id", "contact_x", "--scope", "full"}},
		// Achievements earn verbs (MIO-3412) route on the GLOBAL {contact_id}
		// too — and their 404 is extra-ambiguous (feature gates off, missing
		// achievement, or the wrong id namespace all return the same generic
		// 404), so the hint matters even more here.
		{"achievements grant", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "grant", "ach_1", "--contact-id", "contact_x"}},
		{"achievements revoke", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "revoke", "ach_1", "--contact-id", "contact_x", "--yes"}},
		{"achievements restore", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "restore", "ach_1", "--contact-id", "contact_x"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, exitCode := runBinary(t, bin, []string{
				"MIO_API_KEY=test-key",
				"MIO_API_BASE_URL=" + srv.URL,
			}, tc.args...)

			if exitCode != errs.ExitNotFound {
				t.Fatalf("exit code = %d, want %d (ExitNotFound); stderr=%q", exitCode, errs.ExitNotFound, stderr)
			}

			var envelope struct {
				Errors []struct {
					Detail string `json:"detail"`
				} `json:"errors"`
			}
			raw := strings.TrimSpace(stderr)
			if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
				t.Fatalf("stderr not valid JSON:API envelope: %v; stderr=%q", err, raw)
			}
			if len(envelope.Errors) == 0 {
				t.Fatalf("error envelope empty; stderr=%q", raw)
			}
			detail := envelope.Errors[0].Detail
			if !strings.Contains(strings.ToLower(detail), "global contact id") {
				t.Errorf("404 detail must hint the GLOBAL contact id; got %q", detail)
			}
		})
	}
}

// TestContract_MemberVerb_NonNotFoundError_NoHint: the hint must ONLY fire on a
// not-found (exit 4). A different failure class (e.g. a 422) must pass through
// unchanged so the hint never pollutes unrelated errors.
func TestContract_MemberVerb_NonNotFoundError_NoHint(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Status: 422, Body: `{"errors":[{"status":"422","detail":"invalid role"}]}`},
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_1",
			"hub-memberships", "add", "contact_x")...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage) for 422", res.Code, errs.ExitUsage)
	}
}

// ─── help substrings ─────────────────────────────────────────────────────────

// helpOutput drives `<args...> --help` in-process and returns the combined
// stdout the user sees.
func helpOutput(t *testing.T, args ...string) string {
	t.Helper()
	res := runContract(t, nil, append(args, "--help")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("help for %v exited %d; stderr=%q", args, res.Code, res.Stderr)
	}
	return res.Stdout
}

// TestContract_ContactsHelp_ExplainsIDNamespaces: `mio contacts create/retrieve/
// list --help` must distinguish the TEAM-contact .id from the GLOBAL
// .attributes.contact_id so users pipe the right one into member verbs.
func TestContract_ContactsHelp_ExplainsIDNamespaces(t *testing.T) {
	for _, verb := range []string{"create", "retrieve", "list"} {
		out := strings.ToLower(helpOutput(t, "contacts", verb))
		if !strings.Contains(out, "attributes.contact_id") {
			t.Errorf("contacts %s help must mention .attributes.contact_id (the GLOBAL id); got:\n%s", verb, out)
		}
		if !strings.Contains(out, "team-contact id") {
			t.Errorf("contacts %s help must call .id the team-contact id; got:\n%s", verb, out)
		}
	}
}

// TestContract_MemberVerbHelp_SaysGlobalContactID: every member-shaped verb's
// help must state that its positional takes the GLOBAL contact id, not the
// team-contact .id from `mio contacts`.
func TestContract_MemberVerbHelp_SaysGlobalContactID(t *testing.T) {
	cases := [][]string{
		{"hub-memberships", "add"},
		{"hub-memberships", "set-role"},
		{"hub-memberships", "ban"},
		{"hub-memberships", "unban"},
		{"hub-memberships", "warn"},
		{"activity", "contact"},
		{"community", "members", "ban"},
		{"community", "members", "unban"},
		{"community", "members", "warn"},
		{"community", "members", "soft-ban"},
		{"email", "enrollments", "create"},
		{"email", "enrollments", "list-by-contact"},
		{"access-rules", "overrides", "create"},
	}
	for _, args := range cases {
		out := strings.ToLower(helpOutput(t, args...))
		if !strings.Contains(out, "global contact id") {
			t.Errorf("`mio %s --help` must mention the GLOBAL contact id; got:\n%s", strings.Join(args, " "), out)
		}
	}
}
