package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestContract_MemberVerb404_HintsGlobalContactID(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Status: 404, Body: `{"errors":[{"status":"404","detail":"Membership Not Found"}]}`},
	})
	bin := buildBinary(t)

	rest := map[string][]string{
		"hub-memberships add":               {"contact_x"},
		"hub-memberships set-role":          {"contact_x", "--role", "admin"},
		"hub-memberships ban":               {"contact_x", "--yes"},
		"hub-memberships unban":             {"contact_x", "--yes"},
		"hub-memberships warn":              {"contact_x", "--yes"},
		"activity contact":                  {"contact_x"},
		"community members ban":             {"contact_x", "--yes"},
		"community members unban":           {"contact_x", "--yes"},
		"community members warn":            {"contact_x", "--yes"},
		"community members soft-ban":        {"contact_x", "--yes"},
		"email enrollments create":          {"dc_1", "--contact-id", "contact_x"},
		"email enrollments list-by-contact": {"contact_x"},
		"access-rules overrides create":     {"--contact-id", "contact_x", "--scope", "full"},
	}
	type memberVerbCase struct {
		name string
		args []string
	}
	var cases []memberVerbCase
	for _, page := range memberVerbHelpPages {
		name := strings.Join(page, " ")
		r, ok := rest[name]
		if !ok {
			t.Fatalf("member verb %q has no 404 invocation here; add one", name)
		}
		args := append([]string{"--team", "t_team1", "--hub", "hub_1"}, page...)
		cases = append(cases, memberVerbCase{name, append(args, r...)})
	}
	if len(rest) != len(cases) {
		t.Fatalf("%d 404 invocations for %d member verbs; keep them in step with memberVerbHelpPages", len(rest), len(cases))
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
			requireWorkingContactIDCapture(t, "the 404 hint of `mio "+tc.name+"`", detail)
		})
	}
}

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

func helpOutput(t *testing.T, args ...string) string {
	t.Helper()
	res := runContract(t, nil, append(args, "--help")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("help for %v exited %d; stderr=%q", args, res.Code, res.Stderr)
	}
	return res.Stdout
}

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

func TestContract_MemberVerbHelp_SaysGlobalContactID(t *testing.T) {
	for _, args := range memberVerbHelpPages {
		out := strings.ToLower(helpOutput(t, args...))
		if !strings.Contains(out, "global contact id") {
			t.Errorf("`mio %s --help` must mention the GLOBAL contact id; got:\n%s", strings.Join(args, " "), out)
		}
	}
}
