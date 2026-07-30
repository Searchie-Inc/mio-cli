package cmd

// pages_sections_type_test.go — the imperative-door `--type` validation
// (MIO-2340): `pages sections create --type` is validated against the catalog.
// It is read-tolerant — a KNOWN non-writable type (e.g. calendar) is
// rejected fast (ExitUsage) before any HTTP, but an UNKNOWN type is deferred to
// the backend (so a newly-added writable type the vendored catalog predates is
// not blocked client-side). compact flipped to writable:true in 0.10.0
// (MIO-2681) — see TestSectionsCreate_CompactNowWritable_Proceeds below.
// testimonials flipped writable:true in 0.13.0 — no longer usable as the
// known-non-writable example (see TestSectionsCreate_KnownNonWritableType_ExitUsageBeforeHTTP).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestSectionsCreate_KnownNonWritableType_ExitUsageBeforeHTTP(t *testing.T) {
	// `calendar` is a real section type but writable=false — reject it on
	// the imperative door, before any HTTP. (testimonials was the previous
	// example here; it flipped writable=false -> true in 0.13.0.)
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	err := executeCLI(t, baseEnv(srv.URL),
		"--team", "t_team1", "--hub", "hub_123",
		"pages", "sections", "create", "page_x", "--type", "calendar",
	)
	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if fired {
		t.Error("a known non-writable --type must be rejected before any HTTP request")
	}
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Errorf("error should explain the type is not writable and list the writable set; got %v", err)
	}
}

func TestSectionsCreate_UnknownType_DefersToBackend(t *testing.T) {
	// An unknown type (not in the vendored catalog) must be passed through to the
	// backend, not blocked — it may be a newly-added writable type.
	var gotType string
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fired = true
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"type":"brand-new-type"`) {
			gotType = "brand-new-type"
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"sec_1","type":"sections","attributes":{"type":"brand-new-type"}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "sections", "create", "page_x", "--type", "brand-new-type",
		)...)

	if !fired {
		t.Fatal("an unknown --type must be deferred to the backend, not blocked client-side")
	}
	if gotType != "brand-new-type" {
		t.Errorf("the unknown type should reach the create POST verbatim; gotType=%q", gotType)
	}
	if res.Code != errs.ExitOK {
		t.Errorf("exit = %d, want 0 (backend accepted); stderr=%q", res.Code, res.Stderr)
	}
}

func TestSectionsCreate_WritableType_Proceeds(t *testing.T) {
	// "text" was removed as a section type entirely in catalogVersion 0.12.0
	// (mio-page-catalog#19, along with cta/video) — use "row" (still
	// writable=true) so this test exercises the known-writable path rather
	// than the unknown-type-deferred-to-backend path.
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"type":"row"`) {
			gotType = "row"
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"sec_1","type":"sections","attributes":{"type":"row"}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "sections", "create", "page_x", "--type", "row", "--title", "Intro",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if gotType != "row" {
		t.Errorf("a writable --type must reach the create POST; gotType=%q", gotType)
	}
}

func TestSectionsCreate_CompactNowWritable_Proceeds(t *testing.T) {
	// compact flipped writable=false -> true in catalogVersion 0.10.0
	// (MIO-2681, imperative-door parity with grid) — it must now pass
	// client-side validation and reach the backend like any other writable
	// type.
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"type":"compact"`) {
			gotType = "compact"
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"sec_1","type":"sections","attributes":{"type":"compact"}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "sections", "create", "page_x", "--type", "compact", "--title", "Scroll",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if gotType != "compact" {
		t.Errorf("the now-writable compact --type must reach the create POST; gotType=%q", gotType)
	}
}
