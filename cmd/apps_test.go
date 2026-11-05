package cmd

// apps_test.go — wire tests for `mio apps` and `mio apps installs` (MIO-6609).
// The routes are not on the dev server yet, so these run against a stub that
// answers with the shapes in mio-backend app/embedded_apps/schemas.py.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const appSecretBody = `{"data":{"id":"app_1","type":"embedded_apps","attributes":{
	"name":"Board","client_id":"cid_1","hub_id":"hub_123","team_id":"t_team1",
	"launch_url":"https://a.example.com/x","allowed_origins":["https://a.example.com"],
	"client_secret":"SECRET_CLIENT_VALUE","webhook_signing_secret":"SECRET_WEBHOOK_VALUE"}}}`

const appPlainBody = `{"data":{"id":"app_1","type":"embedded_apps","attributes":{
	"name":"Board","client_id":"cid_1","hub_id":"hub_123","team_id":"t_team1",
	"launch_url":"https://a.example.com/x","allowed_origins":["https://a.example.com"]}}}`

const appInstallBody = `{"data":{"id":"inst_1","type":"embedded_app_installs","attributes":{
	"app_id":"app_1","hub_id":"hub_123","policy_type":"everyone","approved_scopes":["progress:read"],
	"enabled":true,"credential_generation":0}}}`

func TestApps_Create_ExactBodyAndSecretWarning(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, appSecretBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--hub", "hub_123", "apps", "create",
		"--name", "Board", "--launch-url", "https://a.example.com/x",
		"--allowed-origin", "https://a.example.com", "--allowed-origin", "https://b.example.com",
		"--redirect-uri", "https://a.example.com/cb", "--webhook-url", "https://a.example.com/hook")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if cap.Method != "POST" || cap.Path != "/api/v1/teams/t_team1/embedded-apps" {
		t.Errorf("got %s %s", cap.Method, cap.Path)
	}
	assertExactBody(t, cap.Body, `{"hub_id":"hub_123","name":"Board","launch_url":"https://a.example.com/x",
		"allowed_origins":["https://a.example.com","https://b.example.com"],
		"redirect_uris":["https://a.example.com/cb"],"webhook_url":"https://a.example.com/hook"}`)
	if !strings.Contains(res.Stdout, "SECRET_CLIENT_VALUE") {
		t.Errorf("secret must be printed once on stdout; stdout=%q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "warning:") || !strings.Contains(res.Stderr, "client_secret and webhook_signing_secret") {
		t.Errorf("want a shown-once warning on stderr; stderr=%q", res.Stderr)
	}
	if strings.Contains(res.Stderr, "SECRET_CLIENT_VALUE") || strings.Contains(res.Stderr, "SECRET_WEBHOOK_VALUE") {
		t.Errorf("the warning must never repeat a secret; stderr=%q", res.Stderr)
	}
}

func TestApps_Create_RequiredFlags(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, appSecretBody)
	for _, args := range [][]string{
		{"apps", "create", "--launch-url", "https://a.example.com", "--allowed-origin", "https://a.example.com"},
		{"apps", "create", "--name", "x", "--allowed-origin", "https://a.example.com"},
		{"apps", "create", "--name", "x", "--launch-url", "https://a.example.com"},
	} {
		res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", append([]string{"--hub", "hub_123"}, args...)...)...)
		if res.Code != errs.ExitUsage {
			t.Errorf("%v: exit = %d, want %d", args, res.Code, errs.ExitUsage)
		}
	}
	if cap.Method != "" {
		t.Errorf("no request may be sent when a required flag is missing; got %s %s", cap.Method, cap.Path)
	}
}

func TestApps_List_Retrieve_Paths(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, `{"data":[],"meta":{"has_more":false}}`)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "list", "--limit", "5", "--after", "cur")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("list exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if cap.Method != "GET" || cap.Path != "/api/v1/teams/t_team1/embedded-apps" ||
		!strings.Contains(cap.RawQuery, "page%5Bsize%5D=5") || !strings.Contains(cap.RawQuery, "page%5Bafter%5D=cur") {
		t.Errorf("list: %s %s?%s", cap.Method, cap.Path, cap.RawQuery)
	}

	srv2, cap2 := captureServer(t, http.StatusOK, appPlainBody)
	res = runContract(t, baseEnv(srv2.URL), withTeam("t_team1", "apps", "retrieve", "app_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("retrieve exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if cap2.Method != "GET" || cap2.Path != "/api/v1/teams/t_team1/embedded-apps/app_1" {
		t.Errorf("retrieve: %s %s", cap2.Method, cap2.Path)
	}
	if strings.Contains(res.Stderr, "warning:") {
		t.Errorf("a plain retrieve carries no secret and no warning; stderr=%q", res.Stderr)
	}
}

func TestApps_Update_BodyAndClearWebhook(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, appPlainBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "update", "app_1",
		"--name", "Board 2", "--allowed-origin", "https://a.example.com")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if cap.Method != "PATCH" || cap.Path != "/api/v1/teams/t_team1/embedded-apps/app_1" {
		t.Errorf("got %s %s", cap.Method, cap.Path)
	}
	assertExactBody(t, cap.Body, `{"name":"Board 2","allowed_origins":["https://a.example.com"]}`)

	res = runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "update", "app_1", "--webhook-url", "")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("clear exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	assertExactBody(t, cap.Body, `{"webhook_url":null}`)
}

func TestApps_Update_NothingToUpdate(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, appPlainBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "update", "app_1")...)
	if res.Code != errs.ExitUsage || cap.Method != "" {
		t.Errorf("exit = %d, request = %q; want usage error and no request", res.Code, cap.Method)
	}
}

func TestApps_Delete_Guard(t *testing.T) {
	srv, cap := captureServer(t, http.StatusNoContent, ``)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "delete", "app_1")...)
	if res.Code != errs.ExitNeedsConfir || cap.Method != "" {
		t.Errorf("without --yes: exit = %d, request = %q", res.Code, cap.Method)
	}
	res = runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "delete", "app_1", "--yes")...)
	if res.Code != errs.ExitOK || cap.Method != "DELETE" || cap.Path != "/api/v1/teams/t_team1/embedded-apps/app_1" {
		t.Errorf("with --yes: exit = %d, %s %s; stderr=%q", res.Code, cap.Method, cap.Path, res.Stderr)
	}
}

func TestApps_Secrets_Update(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, strings.Replace(appSecretBody, `,"webhook_signing_secret":"SECRET_WEBHOOK_VALUE"`, "", 1))
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "secrets", "update", "app_1", "--secret", "client", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if cap.Method != "POST" || cap.Path != "/api/v1/teams/t_team1/embedded-apps/app_1/rotate-secret" {
		t.Errorf("got %s %s", cap.Method, cap.Path)
	}
	assertExactBody(t, cap.Body, `{"secret":"client"}`)
	if !strings.Contains(res.Stdout, "SECRET_CLIENT_VALUE") || !strings.Contains(res.Stderr, "client_secret shown once") {
		t.Errorf("want secret on stdout and warning on stderr; stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}

func TestApps_Secrets_Update_Validation(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, appSecretBody)
	for name, args := range map[string][]string{
		"bad kind":     {"apps", "secrets", "update", "app_1", "--secret", "admin", "--yes"},
		"missing kind": {"apps", "secrets", "update", "app_1", "--yes"},
	} {
		res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
		if res.Code != errs.ExitUsage {
			t.Errorf("%s: exit = %d, want %d", name, res.Code, errs.ExitUsage)
		}
	}
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "secrets", "update", "app_1", "--secret", "webhook")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("rotation without --yes: exit = %d, want %d", res.Code, errs.ExitNeedsConfir)
	}
	if cap.Method != "" {
		t.Errorf("no request may be sent on a rejected rotation; got %s", cap.Method)
	}
}

func TestApps_Installs_Create(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, appInstallBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "installs", "create", "app_1",
		"--policy-type", "product", "--policy-product-id", "prod_1",
		"--approved-scope", "progress:read", "--enabled")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if cap.Method != "POST" || cap.Path != "/api/v1/teams/t_team1/embedded-apps/app_1/installs" {
		t.Errorf("got %s %s", cap.Method, cap.Path)
	}
	assertExactBody(t, cap.Body, `{"policy_type":"product","policy_product_id":"prod_1","approved_scopes":["progress:read"],"enabled":true}`)
}

func TestApps_Installs_Create_RejectsUnknownPolicyType(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, appInstallBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "installs", "create", "app_1", "--policy-type", "admins")...)
	if res.Code != errs.ExitUsage || cap.Method != "" {
		t.Errorf("exit = %d, request = %q, stderr=%q", res.Code, cap.Method, res.Stderr)
	}
}

func TestApps_Installs_ListRetrieveUpdateDelete(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, `{"data":[],"meta":{"has_more":false}}`)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "installs", "list", "app_1")...)
	if res.Code != errs.ExitOK || cap.Method != "GET" || cap.Path != "/api/v1/teams/t_team1/embedded-apps/app_1/installs" || cap.RawQuery != "" {
		t.Errorf("list: exit=%d %s %s?%s", res.Code, cap.Method, cap.Path, cap.RawQuery)
	}

	srv, cap = captureServer(t, http.StatusOK, appInstallBody)
	res = runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "installs", "retrieve", "app_1", "inst_1")...)
	if res.Code != errs.ExitOK || cap.Method != "GET" || cap.Path != "/api/v1/teams/t_team1/embedded-apps/app_1/installs/inst_1" {
		t.Errorf("retrieve: exit=%d %s %s", res.Code, cap.Method, cap.Path)
	}

	res = runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "installs", "update", "app_1", "inst_1", "--enabled=false")...)
	if res.Code != errs.ExitOK || cap.Method != "PATCH" || cap.Path != "/api/v1/teams/t_team1/embedded-apps/app_1/installs/inst_1" {
		t.Errorf("update: exit=%d %s %s", res.Code, cap.Method, cap.Path)
	}
	assertExactBody(t, cap.Body, `{"enabled":false}`)

	res = runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "installs", "update", "app_1", "inst_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("empty update: exit = %d, want %d", res.Code, errs.ExitUsage)
	}

	srv, cap = captureServer(t, http.StatusNoContent, ``)
	res = runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "installs", "delete", "app_1", "inst_1")...)
	if res.Code != errs.ExitNeedsConfir || cap.Method != "" {
		t.Errorf("delete without --yes: exit=%d request=%q", res.Code, cap.Method)
	}
	res = runContract(t, baseEnv(srv.URL), withTeam("t_team1", "apps", "installs", "delete", "app_1", "inst_1", "--yes")...)
	if res.Code != errs.ExitOK || cap.Method != "DELETE" || cap.Path != "/api/v1/teams/t_team1/embedded-apps/app_1/installs/inst_1" {
		t.Errorf("delete: exit=%d %s %s", res.Code, cap.Method, cap.Path)
	}
}
