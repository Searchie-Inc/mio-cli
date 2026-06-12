package cmd

// mio992_accessrules_type_test.go — regression tests for MIO-992.
//
// Bug: `mio access-rules rules create` and `mio access-rules overrides create`
// serialised the wrong JSON:API data.type values ("access-rules" and
// "access-overrides" — the raw kebab-case URL segments) instead of the correct
// snake_case values ("access_rules" and "access_overrides").  The backend's
// Literal-typed write schemas rejected both with 400.
//
// Root cause: resourceTypeFromPath in internal/client/client.go derives the
// JSON:API type from the last collection path segment and falls back to that
// segment verbatim when no typeOverrides entry matches.  "access-rules" and
// "access-overrides" had no entries, so the hyphenated segment was emitted.
//
// Fix: add bare-segment overrides in typeOverrides:
//   {"access-rules",    "access_rules"}
//   {"access-overrides","access_overrides"}
//
// These tests follow the exact pattern established in write_path_drift_test.go
// and jake_qa_drift_test.go:
//   - captureWriteRequest + assertExactBody for wire-body exactness
//   - newNoRequestServer for client-side required-flag validation
//   - runContract / baseEnv / withTeam in-process harness
//   - hub-scoped commands receive --hub via withTeam (same approach as
//     automations, content, etc.)

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ── canned response bodies ────────────────────────────────────────────────────

const minimalAccessRuleBody = `{"data":{"id":"ar_1","type":"access_rules","attributes":{"target_type":"section","target_id":"sec_x","logic_operator":"any"}}}`

const minimalAccessOverrideBody = `{"data":{"id":"ao_1","type":"access_overrides","attributes":{"contact_id":"con_123","scope":"product","product_id":"prod_456"}}}`

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-992 — access-rules rules create: data.type must be "access_rules"
// ═══════════════════════════════════════════════════════════════════════════════

// TestWritePath_AccessRulesRulesCreate_ExactBody pins the EXACT wire body for
// `mio access-rules rules create`: data.type = "access_rules" (snake_case,
// NOT "access-rules" — the old kebab-case URL segment that caused 400).
//
// CONTRACT (MIO-992): access-rules rules create →
//
//	{"data":{"type":"access_rules","attributes":{"target_type":…,"target_id":…}}}
func TestWritePath_AccessRulesRulesCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAccessRuleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "rules", "create",
			"--target-type", "section",
			"--target-id", "sec_x",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "access_rules",
			"attributes": {
				"target_type": "section",
				"target_id": "sec_x"
			}
		}
	}`)
}

// TestWritePath_AccessRulesRulesCreate_TypeNotKebab pins that the wire body
// does NOT contain "access-rules" as the data.type value (the pre-fix bug).
// This is the canonical canary for MIO-992 rules path.
//
// CONTRACT (MIO-992): data.type must be "access_rules", never "access-rules".
func TestWritePath_AccessRulesRulesCreate_TypeNotKebab(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAccessRuleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "rules", "create",
			"--target-type", "content_node",
			"--target-id", "cnt_1",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	// Parse and assert data.type is the snake_case value.
	assertDataType(t, *gotBody, "access_rules")
}

// TestWritePath_AccessRulesRulesCreate_WithLogicOperator pins that all three
// attributes (target_type, target_id, logic_operator) are serialised correctly.
//
// CONTRACT (MIO-992): optional --logic-operator is included when supplied.
func TestWritePath_AccessRulesRulesCreate_WithLogicOperator(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAccessRuleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "rules", "create",
			"--target-type", "section",
			"--target-id", "sec_x",
			"--logic-operator", "all",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "access_rules",
			"attributes": {
				"target_type": "section",
				"target_id": "sec_x",
				"logic_operator": "all"
			}
		}
	}`)
}

// TestWritePath_AccessRulesRulesCreate_RequiredFlags pins that create validates
// --target-type and --target-id client-side: missing either exits 2 (ExitUsage)
// and fires NO request.
//
// CONTRACT (MIO-992): access-rules rules create requires --target-type AND --target-id.
func TestWritePath_AccessRulesRulesCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing target-id", []string{"--target-type", "section"}},
		{"missing target-type", []string{"--target-id", "sec_x"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalAccessRuleBody)

			args := append([]string{"--hub", "hub_abc", "access-rules", "rules", "create"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("POST must NOT be fired when required flags are missing")
			}
		})
	}
}

// TestWritePath_AccessRulesRulesUpdate_ExactBody pins the EXACT wire body for
// `mio access-rules rules update`: data.type = "access_rules" on the PATCH path.
//
// CONTRACT (MIO-992): access-rules rules update →
//
//	{"data":{"type":"access_rules","attributes":{…}}}
func TestWritePath_AccessRulesRulesUpdate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalAccessRuleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "rules", "update", "ar_abc123",
			"--logic-operator", "all",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "access_rules",
			"attributes": {
				"logic_operator": "all"
			}
		}
	}`)
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-992 — access-rules overrides create: data.type must be "access_overrides"
// ═══════════════════════════════════════════════════════════════════════════════

// TestWritePath_AccessRulesOverridesCreate_ExactBody pins the EXACT wire body
// for `mio access-rules overrides create`: data.type = "access_overrides"
// (snake_case, NOT "access-overrides" — the old kebab-case URL segment that
// caused 400).
//
// CONTRACT (MIO-992): access-rules overrides create →
//
//	{"data":{"type":"access_overrides","attributes":{"contact_id":…,"scope":…,…}}}
func TestWritePath_AccessRulesOverridesCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAccessOverrideBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "overrides", "create",
			"--contact-id", "con_123",
			"--scope", "product",
			"--product-id", "prod_456",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "access_overrides",
			"attributes": {
				"contact_id": "con_123",
				"scope": "product",
				"product_id": "prod_456"
			}
		}
	}`)
}

// TestWritePath_AccessRulesOverridesCreate_TypeNotKebab pins that the wire body
// does NOT contain "access-overrides" as the data.type value (the pre-fix bug).
// This is the canonical canary for MIO-992 overrides path.
//
// CONTRACT (MIO-992): data.type must be "access_overrides", never "access-overrides".
func TestWritePath_AccessRulesOverridesCreate_TypeNotKebab(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAccessOverrideBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "overrides", "create",
			"--contact-id", "con_123",
			"--scope", "full",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	// Assert data.type is the snake_case value (not the kebab segment).
	assertDataType(t, *gotBody, "access_overrides")
}

// TestWritePath_AccessRulesOverridesCreate_AllAttributes pins that all five
// optional attributes are serialised under their snake_case keys when supplied.
//
// CONTRACT (MIO-992): optional override attributes serialise with snake_case keys.
func TestWritePath_AccessRulesOverridesCreate_AllAttributes(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAccessOverrideBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "overrides", "create",
			"--contact-id", "con_123",
			"--scope", "product",
			"--product-id", "prod_456",
			"--expires-at", "2026-12-31T00:00:00Z",
			"--reason", "Trial access",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "access_overrides",
			"attributes": {
				"contact_id": "con_123",
				"scope": "product",
				"product_id": "prod_456",
				"expires_at": "2026-12-31T00:00:00Z",
				"reason": "Trial access"
			}
		}
	}`)
}

// TestWritePath_AccessRulesOverridesCreate_RequiredFlags pins that create
// validates --contact-id and --scope client-side: missing either exits 2
// (ExitUsage) and fires NO request.
//
// CONTRACT (MIO-992): access-rules overrides create requires --contact-id AND --scope.
func TestWritePath_AccessRulesOverridesCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing scope", []string{"--contact-id", "con_123"}},
		{"missing contact-id", []string{"--scope", "full"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalAccessOverrideBody)

			args := append([]string{"--hub", "hub_abc", "access-rules", "overrides", "create"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("POST must NOT be fired when required flags are missing")
			}
		})
	}
}

// TestWritePath_AccessRulesOverridesUpdate_ExactBody pins the EXACT wire body
// for `mio access-rules overrides update`: data.type = "access_overrides" on
// the PATCH path.
//
// CONTRACT (MIO-992): access-rules overrides update →
//
//	{"data":{"type":"access_overrides","attributes":{…}}}
func TestWritePath_AccessRulesOverridesUpdate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalAccessOverrideBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "overrides", "update", "ao_abc123",
			"--scope", "basic",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "access_overrides",
			"attributes": {
				"scope": "basic"
			}
		}
	}`)
}

// ── helper ────────────────────────────────────────────────────────────────────

// assertDataType parses the captured request body and asserts that data.type
// equals want. This check is the focused canary for the MIO-992 bug class:
// a kebab URL segment must never appear verbatim as the JSON:API type.
func assertDataType(t *testing.T, gotBody []byte, want string) {
	t.Helper()
	var doc struct {
		Data struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	if doc.Data.Type != want {
		t.Errorf("data.type = %q, want %q (MIO-992: hyphenated URL segment must not leak into JSON:API type)",
			doc.Data.Type, want)
	}
}
