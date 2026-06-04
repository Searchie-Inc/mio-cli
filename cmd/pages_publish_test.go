package cmd

// pages_publish_test.go — contract tests for `mio pages publish` and the
// --tree flag on `mio pages retrieve`.
//
// Both tests reuse the in-process harness from contract_test.go (runContract,
// newMockServer, baseEnv, withTeam) which are package-level helpers in the same
// `cmd` package.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// pagePubBody is a minimal page-publishes resource response.
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

// TestPagesPublish_MissingIfMatch verifies that omitting --if-match exits 2
// (ExitUsage — required flag not provided).
//
// This test uses the subprocess harness (buildBinary + runBinary) rather than
// the in-process runContract harness, because the pagesPublishCmd is a package-
// level singleton and cobra does not reset flag Changed state between in-process
// Execute() calls. A prior in-process call with --if-match 7 would leave
// Changed=true on the singleton, causing the Changed() guard in RunE to
// produce false-pass. The subprocess has no shared state with other tests.
func TestPagesPublish_MissingIfMatch(t *testing.T) {
	srv := newMockServer(t, nil) // request must not reach the server
	bin := buildBinary(t)

	_, _, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	},
		"--team", "t_team1",
		"--hub", "hub_123",
		"pages", "publish", "page_x",
		// --if-match intentionally omitted
	)

	if exitCode != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", exitCode, errs.ExitUsage)
	}
}

// TestPagesPublish_PostWithIfMatchHeader verifies that:
//   - the HTTP method is POST
//   - the path ends in /pages/page_x/publish
//   - the If-Match header value equals "7" when --if-match 7 is passed
//   - the command exits 0
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

// pageTreeBody is a minimal page-trees resource response used by --tree tests.
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

// TestPagesRetrieve_TreeFlag verifies that passing --tree appends
// resolve=false to the GET query string and that the command exits 0.
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
