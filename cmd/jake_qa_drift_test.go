package cmd

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

const minimalTagBody = `{"data":{"id":"tag_1","type":"tags","attributes":{"name":"VIP","slug":"vip"}}}`

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

const minimalContactAttrBody = `{"data":{"id":"attr_1","type":"contact_attribute_definitions","attributes":{"name":"Company","slug":"company","type":"text"}}}`

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

	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, *gotBody)
	}
	attrs := doc.Data.Attributes

	if attrs["type"] != "number" {
		t.Errorf("attributes[\"type\"] = %v, want \"number\" (MIO-968b: --field-type must map to \"type\")", attrs["type"])
	}
	if _, hasFieldType := attrs["field_type"]; hasFieldType {
		t.Errorf("attributes must NOT contain \"field_type\" key (MIO-968b: attrKey() produces wrong name; use setMappedString)")
	}
}

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

func TestWritePath_ContactAttributesUpdate_FieldTypeFlagRemoved(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "update", "attr_abc123",
			"--name", "Company",
			"--field-type", "text",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --field-type on update); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("PATCH must NOT be fired when an unknown flag is passed")
	}
}

func TestWritePath_ContactAttributesCreate_NoLabelFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Company",
			"--slug", "company",
			"--field-type", "text",
			"--label", "Company Label",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --label); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when an unknown flag is passed")
	}
}

func TestWritePath_ContactAttributesCreate_NoRequiredFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Company",
			"--slug", "company",
			"--field-type", "text",
			"--required",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --required); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when an unknown flag is passed")
	}
}

const minimalAutomationBody = `{"data":{"id":"auto_1","type":"automations","attributes":{"name":"Onboarding"}}}`
const minimalDefinitionJSON = `{"nodes":[{"type":"exit","id":"n1","config":{}}],"edges":[],"triggers":[]}`

func TestWritePath_AutomationsCreate_DefinitionRequired(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalAutomationBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "create",
			"--name", "Onboarding",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when --definition is missing")
	}
}

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

const minimalRoleResponseBody = `{"data":{"id":"role_1","type":"roles","attributes":{"name":"Editor","slug":"editor"}}}`

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

func TestWritePath_RolesCreate_NoDescriptionFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalRoleResponseBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "roles", "create",
			"--name", "Editor",
			"--slug", "editor",
			"--description", "Can edit content",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --description); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when an unknown flag is passed")
	}
}

func TestWritePath_RolesUpdate_NoDescriptionFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalRoleResponseBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "roles", "update", "role_abc123",
			"--name", "Senior Editor",
			"--description", "Can edit and publish",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --description on update); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("PATCH must NOT be fired when an unknown flag is passed")
	}
}

func TestConfig_GetKeyNames(t *testing.T) {
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

func TestConfig_ListIncludesCurrentTeam(t *testing.T) {
	res := runContract(t, nil, "config", "list")
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

func TestWhoami_TeamIDFromAPIKeyEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/me" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"usr_1","email":"alice@test.member.dev","team_id":"t_from_key"}`))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_from_config", "whoami")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	if !strings.Contains(res.Stdout, "t_from_key") {
		t.Errorf("MIO-847: whoami stdout %q does not contain \"t_from_key\" (team_id from /api/auth/me)", res.Stdout)
	}
	if strings.Contains(res.Stdout, `"team_id":"t_from_config"`) || strings.Contains(res.Stdout, `"team_id": "t_from_config"`) {
		t.Errorf("MIO-847: whoami stdout %q reports team_id from config (t_from_config) instead of API key team", res.Stdout)
	}
}

func TestLogin_HeadlessFlags_FlagsExist(t *testing.T) {
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

	if res.Code == errs.ExitUsage {
		t.Errorf("MIO-848: login --email --password: exit code = %d (ExitUsage) — flags must exist; stderr=%q",
			res.Code, res.Stderr)
	}
	if res.Code != errs.ExitAuth {
		t.Logf("MIO-848: expected ExitAuth(%d) from 401 response, got %d; stderr=%q stdout=%q",
			errs.ExitAuth, res.Code, res.Stderr, res.Stdout)
	}
}

func TestLogin_HeadlessFlags_FullMintFlow(t *testing.T) {
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

const minimalCouponBody = `{"data":{"id":"cpn_1","type":"coupons","attributes":{"code":"SAVE20","discount_type":"percent","discount_value":20}}}`

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

func TestWritePath_CouponsCreate_CurrencyNormalization(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalCouponBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "create",
			"--code", "TEN",
			"--discount-type", "amount",
			"--discount-value", "10",
			"--currency", "USD",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

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

func TestWritePath_CouponsUpdate_OmitUnsetSemantics(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalCouponBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "update", "cpn_abc123",
			"--is-active=false",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "coupons",
			"attributes": {
				"is_active": false
			}
		}
	}`)
}

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
