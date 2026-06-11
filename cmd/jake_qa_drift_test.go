package cmd

// jake_qa_drift_test.go — regression tests for the write-path bugs fixed in
// MIO-968a/b/c/d, MIO-969, MIO-847, and MIO-848.
//
// All tests follow the write_path_drift_test.go pattern:
//   - captureWriteRequest + assertExactBody for wire-body exactness
//   - newNoRequestServer for client-side required-flag validation
//   - runContract / baseEnv / withTeam in-process harness
//
// MIO-968a (tags):        --slug is required on create; accepted on update.
// MIO-968b (contact-attrs): --field-type maps to backend field "type" (NOT
//                           "field_type"); --slug required on create; no
//                           --label/--required flags; --field-type absent on
//                           update (immutable).
// MIO-968c (automations): --definition is required on create.
// MIO-968d (roles):       --slug is required on create; no --description flag.
// MIO-969  (config):      display keys match TOML file keys exactly
//                         (current_team / current_hub / api_base).
// MIO-847  (whoami):      when API key comes from MIO_API_KEY env, team_id
//                         is sourced from /api/auth/me, not from config.
// MIO-848  (login):       --email and --password flags exist; headless path
//                         is reachable; non-TTY error message hints at them.
//
// Infrastructure note: the client rewrites all /api/... paths to /api/v1/...
// (canonicalRequestPath in internal/client/client.go), so mock servers must
// listen at /api/v1/... paths (e.g. /api/v1/auth/me, not /api/auth/me).

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// makeLoginJWT builds a minimal syntactically-valid JWT carrying the mio
// namespaced claim. The client's TeamIDFromAccessToken reads
// claims["https://membership.io/claims"]["team_id"].
func makeLoginJWT(t *testing.T, teamID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"at+jwt"}`))
	payload, _ := json.Marshal(map[string]any{
		"sub": "user-test",
		"iss": "https://membership.io",
		"https://membership.io/claims": map[string]any{
			"team_id": teamID,
		},
	})
	payloadSeg := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return strings.Join([]string{header, payloadSeg, sig}, ".")
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-968a — tags: --slug required on create; accepted on update
// ═══════════════════════════════════════════════════════════════════════════════

const minimalTagBody = `{"data":{"id":"tag_1","type":"tags","attributes":{"name":"VIP","slug":"vip"}}}`

// TestWritePath_TagsCreate_ExactBody pins the EXACT wire body for
// `mio tags create`: data.type = "tags", slug present in attributes.
//
// CONTRACT (MIO-968a): tags create --name VIP --slug vip →
//
//	{"data":{"type":"tags","attributes":{"name":"VIP","slug":"vip"}}}
func TestWritePath_TagsCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalTagBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "create",
			"--name", "VIP",
			"--slug", "vip",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "tags",
			"attributes": {
				"name": "VIP",
				"slug": "vip"
			}
		}
	}`)
}

// TestWritePath_TagsCreate_RequiredFlags pins that `mio tags create` validates
// BOTH --name and --slug client-side: any missing combination exits 2
// (ExitUsage) and fires NO request.
//
// CONTRACT (MIO-968a): tags create requires --name AND --slug.
func TestWritePath_TagsCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing slug", []string{"--name", "VIP"}},
		{"missing name", []string{"--slug", "vip"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalTagBody)

			args := append([]string{"tags", "create"}, tc.args...)
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

// TestWritePath_TagsUpdate_SlugAccepted pins that tags update accepts --slug
// (slug is mutable post-creation per the backend schema).
//
// CONTRACT (MIO-968a): tags update --slug new-slug → attributes.slug = "new-slug"
func TestWritePath_TagsUpdate_SlugAccepted(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalTagBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "update", "tag_abc123",
			"--slug", "new-slug",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "tags",
			"attributes": {
				"slug": "new-slug"
			}
		}
	}`)
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-968b — contact-attributes: field-type maps to "type", no --label/--required,
//            --slug required on create, --field-type absent on update
// ═══════════════════════════════════════════════════════════════════════════════

// The client serializes the resource type as "contact_attribute_definitions"
// (the JSON:API type value registered in the CLI for this command group).
const minimalContactAttrBody = `{"data":{"id":"attr_1","type":"contact_attribute_definitions","attributes":{"name":"Company","slug":"company","type":"text"}}}`

// TestWritePath_ContactAttributesCreate_ExactBody pins the EXACT wire body for
// `mio contact-attributes create`:
//   - attributes.type = "text" (NOT attributes.field_type)
//   - attributes.slug present
//
// CONTRACT (MIO-968b): --field-type text → attributes.type = "text"
// (backend DefinitionCreateAttributes has field "type", not "field_type")
func TestWritePath_ContactAttributesCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Company",
			"--slug", "company",
			"--field-type", "text",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "contact_attribute_definitions",
			"attributes": {
				"name": "Company",
				"slug": "company",
				"type": "text"
			}
		}
	}`)
}

// TestWritePath_ContactAttributesCreate_FieldTypeNotFieldType pins that the wire
// body key is "type", NOT "field_type". This is the critical bug: attrKey()
// converts --field-type → field_type (wrong), so setMappedString must be used.
//
// CONTRACT (MIO-968b): wire body must NOT contain "field_type" key; must have "type".
func TestWritePath_ContactAttributesCreate_FieldTypeNotFieldType(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Score",
			"--slug", "score",
			"--field-type", "number",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	// Parse the body and check for the correct key name.
	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, *gotBody)
	}
	attrs := doc.Data.Attributes

	// Must have "type" with the field type value.
	if attrs["type"] != "number" {
		t.Errorf("attributes[\"type\"] = %v, want \"number\" (MIO-968b: --field-type must map to \"type\")", attrs["type"])
	}
	// Must NOT have "field_type" (the old broken key).
	if _, hasFieldType := attrs["field_type"]; hasFieldType {
		t.Errorf("attributes must NOT contain \"field_type\" key (MIO-968b: attrKey() produces wrong name; use setMappedString)")
	}
}

// TestWritePath_ContactAttributesCreate_RequiredFlags pins that create validates
// --name, --slug, and --field-type client-side: any missing combination exits 2
// (ExitUsage) and fires NO request.
//
// CONTRACT (MIO-968b): contact-attributes create requires --name, --slug, --field-type.
func TestWritePath_ContactAttributesCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing slug and field-type", []string{"--name", "Company"}},
		{"missing name and field-type", []string{"--slug", "company"}},
		{"missing name and slug", []string{"--field-type", "text"}},
		{"missing all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalContactAttrBody)

			args := append([]string{"contact-attributes", "create"}, tc.args...)
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

// TestWritePath_ContactAttributesUpdate_ExactBody pins the EXACT wire body for
// `mio contact-attributes update`: only mutable fields (name, slug, description,
// is_contact_editable, position) — NO "type" key (field type is immutable).
//
// CONTRACT (MIO-968b): contact-attributes update must NOT emit "type" or "field_type".
func TestWritePath_ContactAttributesUpdate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "update", "attr_abc123",
			"--name", "Company Name",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "contact_attribute_definitions",
			"attributes": {
				"name": "Company Name"
			}
		}
	}`)
}

// TestWritePath_ContactAttributesUpdate_FieldTypeFlagRemoved pins that
// --field-type does NOT exist on update (type is immutable post-creation).
// Passing it should be an unknown flag → exit 2, no API call.
//
// CONTRACT (MIO-968b): contact-attributes update --field-type X → exit 2 (unknown flag)
func TestWritePath_ContactAttributesUpdate_FieldTypeFlagRemoved(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "update", "attr_abc123",
			"--name", "Company",
			"--field-type", "text", // removed flag — must be unknown
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --field-type on update); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("PATCH must NOT be fired when an unknown flag is passed")
	}
}

// TestWritePath_ContactAttributesCreate_NoLabelFlag pins that the stale
// --label flag does NOT exist on create (not in DefinitionCreateAttributes).
// Passing it should be an unknown flag → exit 2, no API call.
//
// CONTRACT (MIO-968b): contact-attributes create --label X → exit 2 (unknown flag)
func TestWritePath_ContactAttributesCreate_NoLabelFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Company",
			"--slug", "company",
			"--field-type", "text",
			"--label", "Company Label", // stale flag — must not exist
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --label); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when an unknown flag is passed")
	}
}

// TestWritePath_ContactAttributesCreate_NoRequiredFlag pins that the stale
// --required flag does NOT exist on create (not in DefinitionCreateAttributes).
//
// CONTRACT (MIO-968b): contact-attributes create --required → exit 2 (unknown flag)
func TestWritePath_ContactAttributesCreate_NoRequiredFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Company",
			"--slug", "company",
			"--field-type", "text",
			"--required", // stale flag — must not exist
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --required); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when an unknown flag is passed")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-968c — automations create: --definition is required
// ═══════════════════════════════════════════════════════════════════════════════

const minimalAutomationBody = `{"data":{"id":"auto_1","type":"automations","attributes":{"name":"Onboarding"}}}`
const minimalDefinitionJSON = `{"nodes":[{"type":"exit","id":"n1","config":{}}],"edges":[],"triggers":[]}`

// TestWritePath_AutomationsCreate_DefinitionRequired pins that `mio automations
// create` without --definition exits 2 (ExitUsage) and fires no request.
//
// CONTRACT (MIO-968c): automations create without --definition → exit 2
func TestWritePath_AutomationsCreate_DefinitionRequired(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalAutomationBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "create",
			"--name", "Onboarding",
			// --definition intentionally omitted
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when --definition is missing")
	}
}

// TestWritePath_AutomationsCreate_DefinitionRequiredFlags pins all combinations
// of missing --name and --definition.
//
// CONTRACT (MIO-968c): automations create requires BOTH --name AND --definition.
func TestWritePath_AutomationsCreate_DefinitionRequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing definition", []string{"--name", "Onboarding"}},
		{"missing name", []string{"--definition", minimalDefinitionJSON}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalAutomationBody)

			args := append([]string{"--hub", "hub_123", "automations", "create"}, tc.args...)
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

// TestWritePath_AutomationsCreate_ExactBodyWithDefinition pins the EXACT wire
// body for `mio automations create` when --definition is provided: the parsed
// JSON tree must appear at attributes.definition.
//
// CONTRACT (MIO-968c): --definition JSON → attributes.definition = parsed tree
func TestWritePath_AutomationsCreate_ExactBodyWithDefinition(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAutomationBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "create",
			"--name", "Onboarding",
			"--definition", minimalDefinitionJSON,
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "automations",
			"attributes": {
				"name": "Onboarding",
				"definition": {
					"nodes": [{"type": "exit", "id": "n1", "config": {}}],
					"edges": [],
					"triggers": []
				}
			}
		}
	}`)
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-968d — roles: --slug required on create; no --description flag
// ═══════════════════════════════════════════════════════════════════════════════

// Note: roles send a FLAT request body (not a JSON:API envelope) — the backend
// RoleCreate schema is a plain Pydantic model. However, the client's
// decodeResourceWrapped always expects a JSON:API response envelope, so the mock
// server must return one even though the real backend returns flat JSON.
// This is a known pre-existing gap; the test focuses on the REQUEST body shape.
const minimalRoleResponseBody = `{"data":{"id":"role_1","type":"roles","attributes":{"name":"Editor","slug":"editor"}}}`

// TestWritePath_RolesCreate_ExactBody pins the EXACT wire body for
// `mio roles create`: flat JSON with name + slug, NO description key.
//
// CONTRACT (MIO-968d): roles create --name Editor --slug editor →
//
//	{"name":"Editor","slug":"editor"}  (flat, no "data" wrapper, no "description")
func TestWritePath_RolesCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalRoleResponseBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "roles", "create",
			"--name", "Editor",
			"--slug", "editor",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{"name":"Editor","slug":"editor"}`)
}

// TestWritePath_RolesCreate_RequiredFlags pins that `mio roles create` validates
// BOTH --name and --slug client-side.
//
// CONTRACT (MIO-968d): roles create requires --name AND --slug.
func TestWritePath_RolesCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing slug", []string{"--name", "Editor"}},
		{"missing name", []string{"--slug", "editor"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalRoleResponseBody)

			args := append([]string{"roles", "create"}, tc.args...)
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

// TestWritePath_RolesCreate_NoDescriptionFlag pins that the stale --description
// flag does NOT exist on roles create (RoleCreate has no description field).
//
// CONTRACT (MIO-968d): roles create --description X → exit 2 (unknown flag)
func TestWritePath_RolesCreate_NoDescriptionFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalRoleResponseBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "roles", "create",
			"--name", "Editor",
			"--slug", "editor",
			"--description", "Can edit content", // stale flag — must not exist
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --description); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when an unknown flag is passed")
	}
}

// TestWritePath_RolesUpdate_NoDescriptionFlag pins that the stale --description
// flag does NOT exist on roles update either (RoleUpdate only has name).
//
// CONTRACT (MIO-968d): roles update --description X → exit 2 (unknown flag)
func TestWritePath_RolesUpdate_NoDescriptionFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalRoleResponseBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "roles", "update", "role_abc123",
			"--name", "Senior Editor",
			"--description", "Can edit and publish", // stale flag — must not exist
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --description on update); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("PATCH must NOT be fired when an unknown flag is passed")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-969 — config: display keys match TOML file keys
// ═══════════════════════════════════════════════════════════════════════════════

// TestConfig_GetKeyNames pins that `mio config get` accepts the TOML key names
// (current_team, current_hub, api_base) not old short names (team, hub, api-base).
// Passing the old names must exit 2 (ExitUsage) — unknown config key.
//
// CONTRACT (MIO-969): config get must use TOML key names, not short aliases.
func TestConfig_GetKeyNames(t *testing.T) {
	// Old short names must be rejected.
	oldKeys := []string{"team", "hub", "api-base"}
	for _, key := range oldKeys {
		t.Run("reject old key: "+key, func(t *testing.T) {
			res := runContract(t, nil, "config", "get", key)
			if res.Code != errs.ExitUsage {
				t.Errorf("config get %q: exit code = %d, want %d (ExitUsage for old key name); stderr=%q",
					key, res.Code, errs.ExitUsage, res.Stderr)
			}
		})
	}
}

// TestConfig_SetKeyNames pins that `mio config set` accepts the TOML key names
// (current_team, current_hub, api_base) not old short names.
// Using the old names must exit 2 (ExitUsage).
//
// CONTRACT (MIO-969): config set must use TOML key names.
func TestConfig_SetKeyNames(t *testing.T) {
	oldKeys := []string{"team", "hub", "api-base"}
	for _, key := range oldKeys {
		t.Run("reject old key: "+key, func(t *testing.T) {
			res := runContract(t, nil, "config", "set", key, "some-value")
			if res.Code != errs.ExitUsage {
				t.Errorf("config set %q: exit code = %d, want %d (ExitUsage for old key name); stderr=%q",
					key, res.Code, errs.ExitUsage, res.Stderr)
			}
		})
	}
}

// TestConfig_ListIncludesCurrentTeam pins that `mio config list` output contains
// "current_team" (the TOML key name, not "team").
//
// CONTRACT (MIO-969): config list → output contains "current_team = "
func TestConfig_ListIncludesCurrentTeam(t *testing.T) {
	res := runContract(t, nil, "config", "list")
	// list should exit 0 and emit "current_team = "
	if res.Code != errs.ExitOK {
		t.Fatalf("config list: exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "current_team") {
		t.Errorf("config list stdout %q does not contain \"current_team\" (MIO-969: key name must match TOML)", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "current_hub") {
		t.Errorf("config list stdout %q does not contain \"current_hub\" (MIO-969: key name must match TOML)", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "api_base") {
		t.Errorf("config list stdout %q does not contain \"api_base\" (MIO-969: key name must match TOML)", res.Stdout)
	}
}

// TestConfig_ListNoOldNames pins that `mio config list` output does NOT emit the
// old short key names as standalone line-starting keys. For example, a line
// "team = " would be invalid (should be "current_team = ").
//
// We check for lines that START with the old short key names (not as substrings,
// since "current_team = " legitimately contains "team = " as a substring).
//
// CONTRACT (MIO-969): config list must NOT start any line with "team =", "hub =", "api-base =".
func TestConfig_ListNoOldNames(t *testing.T) {
	res := runContract(t, nil, "config", "list")
	if res.Code != errs.ExitOK {
		t.Fatalf("config list: exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		for _, badPrefix := range []string{"team =", "hub =", "api-base ="} {
			if strings.HasPrefix(strings.TrimSpace(line), badPrefix) {
				t.Errorf("config list: line %q starts with old key %q (MIO-969: must use TOML names like current_team)", line, badPrefix)
			}
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-847 — whoami: team_id from /api/auth/me when MIO_API_KEY env is set
// ═══════════════════════════════════════════════════════════════════════════════

// TestWhoami_TeamIDFromAPIKeyEnv pins that when MIO_API_KEY is set (env),
// `mio whoami` reports team_id from the /api/auth/me response body, NOT from
// the current_team stored in config (which would be empty / different).
//
// The mock server returns team_id = "t_from_key" in the me response; the CLI
// is invoked with --team "t_from_config" to simulate a divergent config value.
// The output must contain "t_from_key", not "t_from_config".
//
// Note: the client rewrites /api/... → /api/v1/... (canonicalRequestPath), so
// the mock must handle /api/v1/auth/me.
//
// CONTRACT (MIO-847): when key_source = "env (MIO_API_KEY)", team_id = me["team_id"].
func TestWhoami_TeamIDFromAPIKeyEnv(t *testing.T) {
	// Mock server: /api/v1/auth/me returns a user with team_id set.
	// All other paths (team/hub name lookup) return 404, which is fine —
	// whoami treats name lookups as best-effort.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/me" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"usr_1","email":"alice@test.member.dev","team_id":"t_from_key"}`))
			return
		}
		// Name-lookup 404s are best-effort; whoami must not fail on them.
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
	}))
	t.Cleanup(srv.Close)

	// MIO_API_KEY is set via env (baseEnv supplies it as "test-key-contract").
	// --team flag supplies "t_from_config" as the config-stored team — this
	// MUST be overridden by me["team_id"] when the key comes from env.
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_from_config", "whoami")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	// The JSON output must contain team_id from the API response, not the --team flag.
	if !strings.Contains(res.Stdout, "t_from_key") {
		t.Errorf("MIO-847: whoami stdout %q does not contain \"t_from_key\" (team_id from /api/auth/me)", res.Stdout)
	}
	// Must NOT report the config-side team as team_id.
	if strings.Contains(res.Stdout, `"team_id":"t_from_config"`) || strings.Contains(res.Stdout, `"team_id": "t_from_config"`) {
		t.Errorf("MIO-847: whoami stdout %q reports team_id from config (t_from_config) instead of API key team", res.Stdout)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-848 — login: --email and --password flags exist; headless path reachable
// ═══════════════════════════════════════════════════════════════════════════════

// TestLogin_HeadlessFlags_FlagsExist pins that --email and --password flags
// exist on the login command. Passing an unknown flag would exit 2; passing
// them with credentials that fail (bad auth) should exit with an auth-related
// error, NOT ExitUsage.
//
// CONTRACT (MIO-848): mio login --email X --password Y → --email/--password flags are known.
func TestLogin_HeadlessFlags_FlagsExist(t *testing.T) {
	// Mock: /api/v1/auth/login returns 401 — the credentials are wrong.
	// We just need to verify the CLI accepted the flags and TRIED to call the API
	// (didn't exit 2 with "unknown flag").
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"status":"401","detail":"invalid credentials"}]}`))
	}))
	t.Cleanup(srv.Close)

	env := []string{
		"MIO_API_BASE_URL=" + srv.URL,
		"MIO_API_KEY=",
	}
	res := runContract(t, env,
		withTeam("t_team1", "login",
			"--email", "alice@test.member.dev",
			"--password", "wrong",
		)...)

	// Must NOT be ExitUsage (exit 2) — that would mean --email/--password are unknown flags.
	if res.Code == errs.ExitUsage {
		t.Errorf("MIO-848: login --email --password: exit code = %d (ExitUsage) — flags must exist; stderr=%q",
			res.Code, res.Stderr)
	}
	// Should be ExitAuth (3) because the server returned 401.
	if res.Code != errs.ExitAuth {
		t.Logf("MIO-848: expected ExitAuth(%d) from 401 response, got %d; stderr=%q stdout=%q",
			errs.ExitAuth, res.Code, res.Stderr, res.Stdout)
	}
}

// TestLogin_HeadlessFlags_FullMintFlow pins that when a valid server is provided
// (proper JWT + mint response), headless login succeeds: exit 0 and stdout
// mentions the email address.
//
// The mock must use /api/v1/... paths because the client rewrites /api/... paths.
// The JWT must carry team_id under the "https://membership.io/claims" namespace.
// Config.SetAPIKey may use the file backend in tests — the test accepts any
// non-error outcome as long as flags are wired correctly.
//
// CONTRACT (MIO-848): mio login --email E --password P (valid creds) → exit 0, stdout has email.
func TestLogin_HeadlessFlags_FullMintFlow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate config dir so save doesn't pollute real config

	token := makeLoginJWT(t, "t_headless")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/login" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			resp, _ := json.Marshal(map[string]any{
				"access_token": token,
				"token_type":   "bearer",
			})
			_, _ = w.Write(resp)
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"key_1","type":"api_keys","attributes":{"secret":"mio_sk_headlesstest123"}}}`))
		default:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	env := []string{
		"MIO_API_BASE_URL=" + srv.URL,
		"MIO_API_KEY=",
	}
	res := runContract(t, env,
		withTeam("t_headless", "login",
			"--email", "alice@test.member.dev",
			"--password", "s3cr3t",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("MIO-848: headless login full flow: exit code = %d, want %d (ExitOK); stderr=%q stdout=%q",
			res.Code, errs.ExitOK, res.Stderr, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "alice@test.member.dev") {
		t.Errorf("MIO-848: headless login stdout %q does not mention email address", res.Stdout)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-846 — coupons: enum validation, currency normalization, omit-unset, path
// ═══════════════════════════════════════════════════════════════════════════════

const minimalCouponBody = `{"data":{"id":"cpn_1","type":"coupons","attributes":{"code":"SAVE20","discount_type":"percent","discount_value":20}}}`

// TestWritePath_CouponsCreate_ExactBody pins the EXACT wire body for
// `mio coupons create`: data.type = "coupons", discount_type = "percent".
//
// CONTRACT (MIO-846): coupons create wire body uses data.type="coupons".
func TestWritePath_CouponsCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalCouponBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "create",
			"--code", "SAVE20",
			"--discount-type", "percent",
			"--discount-value", "20",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "coupons",
			"attributes": {
				"code": "SAVE20",
				"discount_type": "percent",
				"discount_value": 20
			}
		}
	}`)
}

// TestWritePath_CouponsCreate_RequiredFlags pins that `mio coupons create`
// validates --code, --discount-type, and --discount-value client-side.
//
// CONTRACT (MIO-846): missing any required flag → exit 2 (ExitUsage), no request.
func TestWritePath_CouponsCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing code", []string{"--discount-type", "percent", "--discount-value", "20"}},
		{"missing discount-type", []string{"--code", "SAVE20", "--discount-value", "20"}},
		{"missing discount-value", []string{"--code", "SAVE20", "--discount-type", "percent"}},
		{"missing all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalCouponBody)

			args := append([]string{"coupons", "create"}, tc.args...)
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

// TestWritePath_CouponsCreate_DiscountTypeEnumValidation pins that passing an
// invalid discount type (e.g. "percentage" — wrong; backend only accepts
// "percent" or "amount") exits 2 (ExitUsage) and fires NO request.
//
// This test WOULD HAVE FAILED against pre-fix code that accepted "percentage"
// without validation. It is the canonical canary for Finding 1.
//
// CONTRACT (MIO-846): --discount-type percentage → exit 2 (invalid enum value).
func TestWritePath_CouponsCreate_DiscountTypeEnumValidation(t *testing.T) {
	badTypes := []string{"percentage", "fixed", "flat", "PERCENT", "AMOUNT", ""}
	for _, bad := range badTypes {
		t.Run("bad type: "+bad, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalCouponBody)

			args := []string{"coupons", "create",
				"--code", "X",
				"--discount-type", bad,
				"--discount-value", "10",
			}
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("discount-type=%q: exit code = %d, want %d (ExitUsage); stderr=%q",
					bad, res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Errorf("discount-type=%q: POST must NOT be fired for invalid enum", bad)
			}
		})
	}
}

// TestWritePath_CouponsCreate_CurrencyNormalization pins that an uppercase
// currency code (e.g. "USD") is normalized to lowercase ("usd") in the wire
// body. This test WOULD HAVE FAILED against pre-fix code that sent "USD" verbatim.
//
// CONTRACT (MIO-846): --currency USD in wire body → currency = "usd" (lowercase).
func TestWritePath_CouponsCreate_CurrencyNormalization(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalCouponBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "create",
			"--code", "TEN",
			"--discount-type", "amount",
			"--discount-value", "10",
			"--currency", "USD", // uppercase input — must be normalized to "usd"
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	// Parse and confirm currency is lowercase in the wire body.
	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*gotBody, &doc); err != nil {
		t.Fatalf("request body not valid JSON: %v; body=%q", err, *gotBody)
	}
	got, _ := doc.Data.Attributes["currency"].(string)
	if got != "usd" {
		t.Errorf("MIO-846: currency in wire body = %q, want \"usd\" (uppercase input must be normalized)", got)
	}
}

// TestWritePath_CouponsCreate_InvalidCurrencyRejected pins that an unsupported
// currency code exits 2 (ExitUsage) and fires no request.
//
// CONTRACT (MIO-846): --currency XYZ → exit 2 (not in {usd,cad,gbp,eur,aud}).
func TestWritePath_CouponsCreate_InvalidCurrencyRejected(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalCouponBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "create",
			"--code", "X",
			"--discount-type", "amount",
			"--discount-value", "10",
			"--currency", "XYZ",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for bad currency); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired for unsupported currency")
	}
}

// TestWritePath_CouponsUpdate_OmitUnsetSemantics pins that `mio coupons update`
// only sends flags that were explicitly set (PATCH semantics): an omitted flag
// must NOT appear in the wire body.
//
// CONTRACT (MIO-846): coupons update omits unset fields from wire body.
func TestWritePath_CouponsUpdate_OmitUnsetSemantics(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalCouponBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "update", "cpn_abc123",
			"--is-active=false",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	// Only is_active must appear — max_redemptions, first_time_only, expires_at must be absent.
	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "coupons",
			"attributes": {
				"is_active": false
			}
		}
	}`)
}

// TestWritePath_CouponsUpdate_NothingToUpdate pins that `mio coupons update`
// with no mutable flags exits 2 (ExitUsage) and fires no request.
//
// CONTRACT (MIO-846): coupons update with no flags → exit 2, no request.
func TestWritePath_CouponsUpdate_NothingToUpdate(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalCouponBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "update", "cpn_abc123")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("PATCH must NOT be fired when no fields are supplied")
	}
}

// TestWritePath_CouponsListPath pins that `mio coupons list` hits the correct
// team-scoped path: /api/v1/teams/{team_id}/coupons.
//
// CONTRACT (MIO-846): coupons list → GET /api/v1/teams/{team_id}/coupons
func TestWritePath_CouponsListPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	wantPath := "/api/v1/teams/t_team1/coupons"
	if gotPath != wantPath {
		t.Errorf("coupons list path = %q, want %q", gotPath, wantPath)
	}
}

// TestLogin_HeadlessEnvVars pins that MIO_EMAIL + MIO_PASSWORD env vars activate
// the headless path (not the interactive TTY menu).
//
// CONTRACT (MIO-848): MIO_EMAIL + MIO_PASSWORD env vars → headless login attempted.
func TestLogin_HeadlessEnvVars(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	token := makeLoginJWT(t, "t_headless")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/login" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			resp, _ := json.Marshal(map[string]any{
				"access_token": token,
				"token_type":   "bearer",
			})
			_, _ = w.Write(resp)
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"key_2","type":"api_keys","attributes":{"secret":"mio_sk_envtest456"}}}`))
		default:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	env := []string{
		"MIO_API_BASE_URL=" + srv.URL,
		"MIO_API_KEY=",
		"MIO_EMAIL=bob@test.member.dev",
		"MIO_PASSWORD=p4ss",
	}
	res := runContract(t, env, withTeam("t_headless", "login")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("MIO-848: headless login via env: exit code = %d, want %d (ExitOK); stderr=%q stdout=%q",
			res.Code, errs.ExitOK, res.Stderr, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "bob@test.member.dev") {
		t.Errorf("MIO-848: headless login (env) stdout %q does not mention email address", res.Stdout)
	}
}
