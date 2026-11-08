package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const minimalAccessRuleBody = `{"data":{"id":"ar_1","type":"access_rules","attributes":{"target_type":"section","target_id":"sec_x","logic_operator":"any"}}}`

const minimalAccessOverrideBody = `{"data":{"id":"ao_1","type":"access_overrides","attributes":{"contact_id":"con_123","scope":"product","product_id":"prod_456"}}}`

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

	assertDataType(t, *gotBody, "access_rules")
}

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

	assertDataType(t, *gotBody, "access_overrides")
}

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
