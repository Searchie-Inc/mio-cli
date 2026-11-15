package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const checkoutBody = `{"data":{"id":"chk_1","type":"checkouts","attributes":{"name":"Main","status":"open","url":"https://hub.example/checkout/main"}}}`

func attrsOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var env struct {
		Data struct {
			Type       string         `json:"type"`
			ID         string         `json:"id"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("body not JSON: %v: %s", err, raw)
	}
	if env.Data.Type != "checkouts" {
		t.Errorf("type = %q, want checkouts", env.Data.Type)
	}
	return env.Data.Attributes
}

func TestCheckoutsList_Path(t *testing.T) {
	srv, method, path, _ := captureCommerceRequest(t, http.StatusOK, commerceListBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--hub", "hub_123", "checkouts", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit %d stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet || !strings.HasSuffix(*path, "/hubs/hub_123/checkouts") {
		t.Errorf("got %s %s", *method, *path)
	}
}

func TestCheckoutsRetrieve_PathAndURL(t *testing.T) {
	srv, method, path, _ := captureCommerceRequest(t, http.StatusOK, checkoutBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--hub", "hub_123", "checkouts", "retrieve", "chk_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit %d stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet || !strings.HasSuffix(*path, "/hubs/hub_123/checkouts/chk_1") {
		t.Errorf("got %s %s", *method, *path)
	}
	if !strings.Contains(res.Stdout, "https://hub.example/checkout/main") {
		t.Errorf("buyer url missing from output: %q", res.Stdout)
	}
}

func TestCheckoutsCreate_Body(t *testing.T) {
	srv, method, path, body := captureCommerceRequest(t, http.StatusCreated, checkoutBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--hub", "hub_123",
		"checkouts", "create", "--name", "Main", "--slug", "main", "--title", "Join",
		"--price-id", "p1,p2", "--allow-promo-codes", "--default",
		"--after-action", "hub_page", "--after-page-id", "pg_1", "--closed-message", "bye")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit %d stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost || !strings.HasSuffix(*path, "/hubs/hub_123/checkouts") {
		t.Errorf("got %s %s", *method, *path)
	}
	a := attrsOf(t, *body)
	for k, want := range map[string]any{
		"name": "Main", "slug": "main", "title": "Join", "allow_promotion_codes": true,
		"is_default": true, "after_action": "hub_page", "after_page_id": "pg_1", "closed_message": "bye",
	} {
		if a[k] != want {
			t.Errorf("attrs[%s] = %v, want %v", k, a[k], want)
		}
	}
	ids, _ := a["price_ids"].([]any)
	if len(ids) != 2 || ids[0] != "p1" || ids[1] != "p2" {
		t.Errorf("price_ids = %v", a["price_ids"])
	}
	if _, ok := a["status"]; ok {
		t.Errorf("status sent without flag: %v", a["status"])
	}
}

func TestCheckoutsCreate_RejectsBeforeNetwork(t *testing.T) {
	srv, fired := firedServer(t)
	cases := [][]string{
		{"--slug", "s", "--title", "t", "--price-id", "p"},
		{"--name", "n", "--title", "t", "--price-id", "p"},
		{"--name", "n", "--slug", "s", "--title", "t"},
		{"--name", "n", "--slug", "s", "--title", "t", "--price-id", "p", "--status", "paused"},
		{"--name", "n", "--slug", "s", "--title", "t", "--price-id", "p", "--after-action", "nope"},
	}
	for _, c := range cases {
		args := append([]string{"checkouts", "create"}, c...)
		res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", append([]string{"--hub", "hub_123"}, args...)...)...)
		if res.Code != errs.ExitUsage {
			t.Errorf("%v: exit %d, want usage", c, res.Code)
		}
	}
	if *fired {
		t.Error("request reached the server")
	}
}

func TestCheckoutsUpdate_PatchOnlyChanged(t *testing.T) {
	srv, method, path, body := captureCommerceRequest(t, http.StatusOK, checkoutBody)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--hub", "hub_123",
		"checkouts", "update", "chk_1", "--status", "closed", "--after-url", "")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit %d stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPatch || !strings.HasSuffix(*path, "/hubs/hub_123/checkouts/chk_1") {
		t.Errorf("got %s %s", *method, *path)
	}
	a := attrsOf(t, *body)
	if len(a) != 2 || a["status"] != "closed" {
		t.Errorf("attrs = %v", a)
	}
	if v, ok := a["after_url"]; !ok || v != nil {
		t.Errorf("after_url should be explicit null, got %v (present=%v)", v, ok)
	}
}

func TestCheckoutsUpdate_NothingToUpdate(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--hub", "hub_123", "checkouts", "update", "chk_1")...)
	if res.Code != errs.ExitUsage || *fired {
		t.Errorf("exit %d fired=%v", res.Code, *fired)
	}
}
