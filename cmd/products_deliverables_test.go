package cmd

// products_deliverables_test.go — contract tests for `mio products deliverables`
// {list,create,delete} (MIO-2268). Pins method + path + JSON:API type + attrs +
// exit codes; every required flag / enum has a no-request usage-error case.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── deliverables list ─────────────────────────────────────────────────────────

func TestProductsDeliverablesList_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureCommerceRequest(t, http.StatusOK, commerceListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "deliverables", "list", "prod_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/products/prod_1/deliverables") {
		t.Errorf("path %q does not end with /products/prod_1/deliverables", *gotPath)
	}
}

// ─── deliverables create ───────────────────────────────────────────────────────

func TestProductsDeliverablesCreate_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureCommerceRequest(t, http.StatusCreated, commerceResourceBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"products", "deliverables", "create", "prod_1",
			"--type", "hub_access",
			"--resource-id", "hub_xyz",
			"--duration-days", "30",
			"--position", "1",
			"--resource-meta", `{"note":"vip"}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/products/prod_1/deliverables") {
		t.Errorf("path %q does not end with /products/prod_1/deliverables", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "product_deliverables" {
		t.Errorf("data.type = %q, want product_deliverables", typ)
	}
	if attrs["deliverable_type"] != "hub_access" {
		t.Errorf("deliverable_type = %v, want hub_access", attrs["deliverable_type"])
	}
	if attrs["resource_id"] != "hub_xyz" {
		t.Errorf("resource_id = %v, want hub_xyz", attrs["resource_id"])
	}
	if attrs["duration_days"] != float64(30) {
		t.Errorf("duration_days = %#v, want 30", attrs["duration_days"])
	}
	if attrs["position"] != float64(1) {
		t.Errorf("position = %#v, want 1", attrs["position"])
	}
	meta, ok := attrs["resource_meta"].(map[string]any)
	if !ok {
		t.Fatalf("resource_meta = %#v, want a JSON object", attrs["resource_meta"])
	}
	if meta["note"] != "vip" {
		t.Errorf("resource_meta.note = %v, want vip", meta["note"])
	}
	// --type must NOT leak as a bare "type" attribute (that is the JSON:API type).
	if _, ok := attrs["type"]; ok {
		t.Errorf("attributes.type must not be present; got %v", attrs)
	}
}

func TestProductsDeliverablesCreate_RequiresType(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "deliverables", "create", "prod_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("missing --type must exit before any HTTP request")
	}
}

func TestProductsDeliverablesCreate_RejectsInvalidType(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"products", "deliverables", "create", "prod_1", "--type", "teleport",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("invalid --type must exit before any HTTP request")
	}
}

func TestProductsDeliverablesCreate_RejectsZeroDuration(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"products", "deliverables", "create", "prod_1",
			"--type", "hub_access", "--duration-days", "0",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("--duration-days 0 must exit before any HTTP request")
	}
}

func TestProductsDeliverablesCreate_RejectsBadResourceMeta(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"products", "deliverables", "create", "prod_1",
			"--type", "hub_access", "--resource-meta", "not-json",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("malformed --resource-meta must exit before any HTTP request")
	}
}

func TestProductsDeliverablesCreate_RejectsNegativePosition(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"products", "deliverables", "create", "prod_1",
			"--type", "hub_access", "--position", "-1",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("negative --position must exit before any HTTP request")
	}
}

// ─── deliverables delete ───────────────────────────────────────────────────────

func TestProductsDeliverablesDelete_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureCommerceRequest(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"products", "deliverables", "delete", "prod_1", "del_9", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/products/prod_1/deliverables/del_9") {
		t.Errorf("path %q does not end with /products/prod_1/deliverables/del_9", *gotPath)
	}
}

func TestProductsDeliverablesDelete_NoYesBlocks(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"products", "deliverables", "delete", "prod_1", "del_9",
		)...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("destructive delete without --yes must fire no HTTP request")
	}
}
