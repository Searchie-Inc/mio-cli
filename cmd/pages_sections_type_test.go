package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestSectionsCreate_KnownNonWritableType_ExitUsageBeforeHTTP(t *testing.T) {
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
