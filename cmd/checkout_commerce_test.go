package cmd

// checkout_commerce_test.go — contract tests for `mio checkout hub-products`
// {list,attach,update,detach} and `mio checkout hub-prices` {list,update}
// (MIO-2268). Pins method + path + JSON:API type + attrs + exit codes.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// commerceResourceBody is a canned single-resource 2xx body for write commands.
const commerceResourceBody = `{"data":{"id":"row_1","type":"hub_product_displays","attributes":{"product_id":"prod_1"}}}`

// commerceListBody is a canned empty-collection 2xx body for list commands.
const commerceListBody = `{"data":[],"meta":{"count":0}}`

// captureCommerceRequest records the first request's method, path, and body and
// replies with the given status and body. Shared across the MIO-2268 test files.
func captureCommerceRequest(t *testing.T, status int, body string) (*httptest.Server, *string, *string, *[]byte) {
	t.Helper()
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotMethod, &gotPath, &gotBody
}

// firedServer returns a server that flips *fired to true on any request. Used by
// usage-error tests to prove no HTTP request was made.
func firedServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(commerceResourceBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &fired
}

// ─── hub-products list ─────────────────────────────────────────────────────────

func TestCheckoutHubProductsList_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureCommerceRequest(t, http.StatusOK, commerceListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "checkout", "hub-products", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/products") {
		t.Errorf("path %q does not end with /hubs/hub_123/products", *gotPath)
	}
}

// ─── hub-products attach ───────────────────────────────────────────────────────

func TestCheckoutHubProductsAttach_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureCommerceRequest(t, http.StatusCreated, commerceResourceBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-products", "attach", "prod_abc",
			"--position", "2", "--free-tier", "--visible=false",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/products") {
		t.Errorf("path %q does not end with /hubs/hub_123/products", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "hub_product_displays" {
		t.Errorf("data.type = %q, want hub_product_displays", typ)
	}
	if attrs["product_id"] != "prod_abc" {
		t.Errorf("product_id = %v, want prod_abc", attrs["product_id"])
	}
	if attrs["position"] != float64(2) {
		t.Errorf("position = %#v, want 2", attrs["position"])
	}
	if attrs["is_free_tier"] != true {
		t.Errorf("is_free_tier = %v, want true", attrs["is_free_tier"])
	}
	if attrs["visible"] != false {
		t.Errorf("visible = %v, want false", attrs["visible"])
	}
	// --free-tier must map to is_free_tier, not free_tier.
	if _, ok := attrs["free_tier"]; ok {
		t.Errorf("attributes.free_tier must not be present; got %v", attrs)
	}
}

func TestCheckoutHubProductsAttach_RejectsEmptyProductID(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-products", "attach", "  ",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("empty product id must exit before any HTTP request")
	}
}

func TestCheckoutHubProductsAttach_RejectsNegativePosition(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-products", "attach", "prod_abc", "--position", "-1",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("negative --position must exit before any HTTP request")
	}
}

// ─── hub-products update ───────────────────────────────────────────────────────

func TestCheckoutHubProductsUpdate_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureCommerceRequest(t, http.StatusOK, commerceResourceBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-products", "update", "hpd_1", "--visible=false",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/products/hpd_1") {
		t.Errorf("path %q does not end with /hubs/hub_123/products/hpd_1", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "hub_product_displays" {
		t.Errorf("data.type = %q, want hub_product_displays", typ)
	}
	if attrs["visible"] != false {
		t.Errorf("visible = %v, want false", attrs["visible"])
	}
}

func TestCheckoutHubProductsUpdate_RequiresAField(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-products", "update", "hpd_1",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no-field update must exit before any HTTP request")
	}
}

func TestCheckoutHubProductsUpdate_RejectsNegativePosition(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-products", "update", "hpd_1", "--position", "-1",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("negative --position must exit before any HTTP request")
	}
}

// ─── hub-products detach ───────────────────────────────────────────────────────

func TestCheckoutHubProductsDetach_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureCommerceRequest(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-products", "detach", "hpd_1", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/products/hpd_1") {
		t.Errorf("path %q does not end with /hubs/hub_123/products/hpd_1", *gotPath)
	}
}

func TestCheckoutHubProductsDetach_NoYesBlocks(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-products", "detach", "hpd_1",
		)...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("destructive detach without --yes must fire no HTTP request")
	}
}

// ─── hub-prices list ───────────────────────────────────────────────────────────

func TestCheckoutHubPricesList_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureCommerceRequest(t, http.StatusOK, commerceListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "checkout", "hub-prices", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/prices") {
		t.Errorf("path %q does not end with /hubs/hub_123/prices", *gotPath)
	}
}

// ─── hub-prices update ─────────────────────────────────────────────────────────

func TestCheckoutHubPricesUpdate_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureCommerceRequest(t, http.StatusOK, commerceResourceBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-prices", "update", "hprd_1", "--position", "3", "--visible=false",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/prices/hprd_1") {
		t.Errorf("path %q does not end with /hubs/hub_123/prices/hprd_1", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "hub_price_displays" {
		t.Errorf("data.type = %q, want hub_price_displays", typ)
	}
	if attrs["position"] != float64(3) {
		t.Errorf("position = %#v, want 3", attrs["position"])
	}
	if attrs["visible"] != false {
		t.Errorf("visible = %v, want false", attrs["visible"])
	}
}

func TestCheckoutHubPricesUpdate_RejectsNegativePosition(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-prices", "update", "hprd_1", "--position", "-1",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("negative --position must exit before any HTTP request")
	}
}

func TestCheckoutHubPricesUpdate_RequiresAField(t *testing.T) {
	srv, fired := firedServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"checkout", "hub-prices", "update", "hprd_1",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no-field update must exit before any HTTP request")
	}
}
