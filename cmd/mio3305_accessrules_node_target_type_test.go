package cmd

// mio3305_accessrules_node_target_type_test.go — regression test for MIO-3305.
//
// `mio access-rules rules create --help` documented only "section" and
// "content_node" for --target-type, but the backend access-rules service
// (app/access_rules/service.py) also accepts "node" — the page-builder
// tree-node gate — and AccessRulesCreateAttributes.target_type is an
// unconstrained str (extra="forbid" on the envelope, but no Literal/enum on
// target_type itself), so the CLI's --target-type flag is a plain pass-through
// string with no client-side choice restriction to widen. The fix documents
// "node" in the flag help text; this test pins that "node" flows through to
// the wire body unmodified (the same pass-through already proven for
// "section"/"content_node" by MIO-992's tests in
// mio992_accessrules_type_test.go).
//
// TestContract_AccessRulesRulesCreateHelp_MentionsNodeTargetType is the
// actual regression coverage for the doc fix: the wire-body test above would
// pass identically even without this change (the flag was always an
// unconstrained pass-through string), so it alone doesn't prove --help was
// updated. This test drives `--help` via the shared helpOutput helper
// (cmd/contact_id_namespace_test.go) and asserts the fix's marker text is
// documented. It deliberately does NOT assert on the bare substring "node" —
// the pre-fix --target-type help already said "content_node", which contains
// "node" as a substring, so a bare-substring check would have passed even
// without this fix. "page-builder" is unique to the new text and only present
// after the fix.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestContract_AccessRulesRulesCreateHelp_MentionsNodeTargetType pins that
// `mio access-rules rules create --help` documents "node" as a valid
// --target-type value alongside section/content_node, so admins wiring up
// page-builder tree-node gates discover it without reading source.
//
// CONTRACT (MIO-3305): access-rules rules create --help must document the
// "node" --target-type value as the page-builder tree-node gate.
func TestContract_AccessRulesRulesCreateHelp_MentionsNodeTargetType(t *testing.T) {
	out := strings.ToLower(helpOutput(t, "access-rules", "rules", "create"))
	if !strings.Contains(out, "page-builder") {
		t.Errorf("access-rules rules create --help must document \"node\" as the page-builder tree-node gate --target-type value; got:\n%s", out)
	}
	if !strings.Contains(out, ", node") && !strings.Contains(out, "or node") {
		t.Errorf("access-rules rules create --help must list \"node\" as its own --target-type value (not just as a substring of \"content_node\"); got:\n%s", out)
	}
}

// TestWritePath_AccessRulesRulesCreate_TargetTypeNode_ExactBody pins that
// --target-type node is accepted client-side and sent verbatim as
// attributes.target_type — it is not rejected or rewritten.
//
// CONTRACT (MIO-3305): access-rules rules create --target-type node → attributes.target_type = "node"
func TestWritePath_AccessRulesRulesCreate_TargetTypeNode_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAccessRuleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "rules", "create",
			"--target-type", "node",
			"--target-id", "node_123",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "access_rules",
			"attributes": {
				"target_type": "node",
				"target_id": "node_123"
			}
		}
	}`)
}
