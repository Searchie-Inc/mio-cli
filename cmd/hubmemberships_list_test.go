package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// MIO-2264: `hub-memberships list` → GET .../members (admin list, all statuses),
// backed by the MIO-2284 backend route. Pagination via --limit/--after; optional
// --filter-status (active|banned|soft_banned|left), validated client-side.

func TestHubMembershipsList_PathAndQuery(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"has_more":false}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"hub-memberships", "list",
			"--limit", "25", "--after", "c_5", "--filter-status", "active")...)

	if res.Code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if want := "/api/v1/admin/teams/t_team1/hubs/hub_123/members"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	for _, sub := range []string{"page%5Bsize%5D=25", "page%5Bafter%5D=c_5", "filter%5Bstatus%5D=active"} {
		if !strings.Contains(gotQuery, sub) {
			t.Errorf("query %q missing %q", gotQuery, sub)
		}
	}
}

func TestHubMembershipsList_RejectsInvalidStatusBeforeContext(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		"hub-memberships", "list",
		"--team", "acme-name", // NOT id-shaped → would trigger team resolution if reached
		"--hub", "hub_123",
		"--filter-status", "bogus")

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage); stderr=%s", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *gotMethod != "" {
		t.Errorf("a request fired (%s) — invalid --filter-status must be rejected before context", *gotMethod)
	}
}
