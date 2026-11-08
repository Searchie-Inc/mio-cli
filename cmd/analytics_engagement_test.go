package cmd

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const engagementBody = `{"data":{"id":"eng_1","type":"engagement","attributes":{"sections":[]}}}`

func TestAnalyticsEngagement_PathAndQuery(t *testing.T) {
	srv, gotMethod, gotPath, gotQuery, _ := captureAdminReq(t, http.StatusOK, engagementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"analytics", "engagement",
			"--from", "2026-05-01T00:00:00Z",
			"--to", "2026-06-01T00:00:00Z",
			"--section", "top_content",
			"--limit", "25",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/analytics/engagement") {
		t.Errorf("path %q does not end with /hubs/hub_123/analytics/engagement", *gotPath)
	}

	q, err := url.ParseQuery(*gotQuery)
	if err != nil {
		t.Fatalf("raw query not parseable: %v; raw=%q", err, *gotQuery)
	}
	if q.Get("from") != "2026-05-01T00:00:00Z" {
		t.Errorf("query from = %q, want 2026-05-01T00:00:00Z", q.Get("from"))
	}
	if q.Get("to") != "2026-06-01T00:00:00Z" {
		t.Errorf("query to = %q, want 2026-06-01T00:00:00Z", q.Get("to"))
	}
	if q.Get("section") != "top_content" {
		t.Errorf("query section = %q, want top_content", q.Get("section"))
	}
	if q.Get("page[size]") != "25" {
		t.Errorf("query page[size] = %q, want 25", q.Get("page[size]"))
	}
}

func TestAnalyticsEngagement_NoFilters(t *testing.T) {
	srv, gotMethod, gotPath, gotQuery, _ := captureAdminReq(t, http.StatusOK, engagementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "analytics", "engagement")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/analytics/engagement") {
		t.Errorf("path %q does not end with /analytics/engagement", *gotPath)
	}
	if strings.TrimSpace(*gotQuery) != "" {
		t.Errorf("expected empty query with no filters; got %q", *gotQuery)
	}
}

func TestAnalyticsEngagement_InvalidSection(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"analytics", "engagement", "--section", "bogus")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired for an invalid --section")
	}
}

func TestAnalyticsEngagement_InvalidLimit(t *testing.T) {
	for _, lim := range []string{"0", "101"} {
		srv, fired := firedGuardServer(t)
		res := runContract(t, baseEnv(srv.URL),
			withTeam("t_team1", "--hub", "hub_123",
				"analytics", "engagement", "--limit", lim)...)
		if res.Code != errs.ExitUsage {
			t.Errorf("--limit %s: exit code = %d, want %d (ExitUsage); stderr=%q", lim, res.Code, errs.ExitUsage, res.Stderr)
		}
		if *fired {
			t.Errorf("--limit %s: no HTTP request must be fired for an out-of-range limit", lim)
		}
	}
}
