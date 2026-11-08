package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const pagePubBody = `{
	"data": {
		"id": "ppub_1",
		"type": "page-publishes",
		"attributes": {
			"page_id": "page_x",
			"published_revision": 7,
			"section_count": 3,
			"gate_count": 1
		}
	}
}`

func TestPagesPublish_MissingIfMatch(t *testing.T) {
	srv := newMockServer(t, nil)
	bin := buildBinary(t)

	_, _, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	},
		"--team", "t_team1",
		"--hub", "hub_123",
		"pages", "publish", "page_x",
	)

	if exitCode != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", exitCode, errs.ExitUsage)
	}
}

func TestPagesPublish_PostWithIfMatchHeader(t *testing.T) {
	var (
		gotMethod  string
		gotPath    string
		gotIfMatch string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIfMatch = r.Header.Get("If-Match")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pagePubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"pages", "publish", "page_x",
			"--if-match", "7",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/pages/page_x/publish") {
		t.Errorf("path %q does not end with /pages/page_x/publish", gotPath)
	}
	if gotIfMatch != "7" {
		t.Errorf("If-Match header = %q, want \"7\"", gotIfMatch)
	}
}

const pageTreeBody = `{
	"data": {
		"id": "page_x",
		"type": "page-trees",
		"attributes": {
			"tree": {},
			"resolved": false,
			"published_revision": 5
		}
	}
}`

func TestPagesRetrieve_TreeFlag(t *testing.T) {
	var (
		gotQuery string
		gotPath  string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pageTreeBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"pages", "retrieve", "page_x",
			"--tree",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.HasSuffix(gotPath, "/pages/page_x") {
		t.Errorf("path %q does not end with /pages/page_x", gotPath)
	}
	if !strings.Contains(gotQuery, "resolve=false") {
		t.Errorf("query %q does not contain resolve=false", gotQuery)
	}
}
