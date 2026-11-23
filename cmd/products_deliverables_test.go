package cmd

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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
	if _, ok := attrs["type"]; ok {
		t.Errorf("attributes.type must not be present; got %v", attrs)
	}
}

func TestProductsDeliverablesCreate_ContentEnrollmentBody(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureCommerceRequest(t, http.StatusCreated, commerceResourceBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"products", "deliverables", "create", "prod_1",
			"--type", "content_enrollment",
			"--resource-id", "content_abc",
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
	_, attrs := decodeDataTypeAttrs(t, *gotBody)
	if attrs["deliverable_type"] != "content_enrollment" {
		t.Errorf("deliverable_type = %v, want content_enrollment", attrs["deliverable_type"])
	}
	if attrs["resource_id"] != "content_abc" {
		t.Errorf("resource_id = %v, want content_abc", attrs["resource_id"])
	}
}

func TestProductsDeliverablesCreate_HelpSaysResourceIDRequired(t *testing.T) {
	res := runContract(t, nil, "products", "deliverables", "create", "--help")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	for _, want := range []string{
		"--resource-id is required for both types: the hub ID for hub_access, the\ncontent ID for content_enrollment.",
		"Required. Id of the resource this deliverable grants: the hub ID for hub_access, the content ID for content_enrollment.",
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("help does not contain %q\n%s", want, res.Stdout)
		}
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

func TestProductsDeliverablesCreate_RejectsRemovedTypes(t *testing.T) {
	for _, typ := range []string{"tag", "file_download", "community_access"} {
		t.Run(typ, func(t *testing.T) {
			srv, fired := firedServer(t)
			restore := overlayEnv(t, baseEnv(srv.URL))
			defer restore()
			resetGlobalFlags()

			root := RootCmd()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(withTeam("t_team1",
				"products", "deliverables", "create", "prod_1", "--type", typ,
			))
			defer root.SetArgs(nil)

			err := root.Execute()
			if code := codeForExecuteErr(err); code != errs.ExitUsage {
				t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", code, errs.ExitUsage, err)
			}
			if *fired {
				t.Error("rejected --type must exit before any HTTP request")
			}
			msg := err.Error()
			for _, want := range []string{"invalid --type", typ, "hub_access", "content_enrollment"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not contain %q", msg, want)
				}
			}
			for _, banned := range []string{"tag", "file_download", "community_access"} {
				if banned != typ && strings.Contains(msg, banned) {
					t.Errorf("error %q still advertises %q", msg, banned)
				}
			}
		})
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
