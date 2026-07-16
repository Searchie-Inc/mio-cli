package cmd

// pages_sections_type_test.go — the imperative-door `--type` validation
// (MIO-2340): `pages sections create --type` is validated against the
// catalog-derived writable section-type allow-list instead of a hardcoded list,
// and rejects an unknown or non-writable type fast (ExitUsage) before any HTTP.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestSectionsCreate_UnknownType_ExitUsageBeforeHTTP(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	err := executeCLI(t, baseEnv(srv.URL),
		"--team", "t_team1", "--hub", "hub_123",
		"pages", "sections", "create", "page_x", "--type", "bogus",
	)

	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if fired {
		t.Error("an invalid --type must be rejected before any HTTP request")
	}
	// The error should surface the catalog-derived allow-list (e.g. feature).
	if err == nil || !strings.Contains(err.Error(), "feature") {
		t.Errorf("error should list the writable section types; got %v", err)
	}
}

func TestSectionsCreate_NonWritableType_ExitUsage(t *testing.T) {
	// `compact` is a real section type but writable=false — it must be rejected
	// on the imperative door.
	res := runContract(t, baseEnv("http://sections.invalid"),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "sections", "create", "page_x", "--type", "compact",
		)...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestSectionsCreate_WritableType_Proceeds(t *testing.T) {
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"type":"text"`) {
			gotType = "text"
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"sec_1","type":"sections","attributes":{"type":"text"}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "sections", "create", "page_x", "--type", "text", "--title", "Intro",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if gotType != "text" {
		t.Errorf("a writable --type must reach the create POST; gotType=%q", gotType)
	}
}
