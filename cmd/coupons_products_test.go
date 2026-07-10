package cmd

// coupons_products_test.go — contract tests for `mio coupons products`
// {list,attach,detach} (MIO-2268). Pins method + path + JSON:API type + attrs +
// exit codes.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── coupons products list ─────────────────────────────────────────────────────

func TestCouponsProductsList_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureCommerceRequest(t, http.StatusOK, commerceListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "products", "list", "cpn_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/coupons/cpn_1/products") {
		t.Errorf("path %q does not end with /coupons/cpn_1/products", *gotPath)
	}
}

// ─── coupons products attach ───────────────────────────────────────────────────

func TestCouponsProductsAttach_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureCommerceRequest(t, http.StatusCreated,
		`{"data":{"id":"cpn_1:prod_9","type":"coupon_products","attributes":{"coupon_id":"cpn_1","product_id":"prod_9"}}}`)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "products", "attach", "cpn_1", "prod_9")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/coupons/cpn_1/products") {
		t.Errorf("path %q does not end with /coupons/cpn_1/products", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "coupon_products" {
		t.Errorf("data.type = %q, want coupon_products", typ)
	}
	if attrs["product_id"] != "prod_9" {
		t.Errorf("product_id = %v, want prod_9", attrs["product_id"])
	}
}

func TestCouponsProductsAttach_RejectsEmptyIDs(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "products", "attach", "cpn_1", "  ")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("empty product id must exit before any HTTP request")
	}
}

// ─── coupons products detach ───────────────────────────────────────────────────

func TestCouponsProductsDetach_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureCommerceRequest(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "products", "detach", "cpn_1", "prod_9", "--yes")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/coupons/cpn_1/products/prod_9") {
		t.Errorf("path %q does not end with /coupons/cpn_1/products/prod_9", *gotPath)
	}
}

func TestCouponsProductsDetach_NoYesBlocks(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "coupons", "products", "detach", "cpn_1", "prod_9")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("destructive detach without --yes must fire no HTTP request")
	}
}
