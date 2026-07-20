package cmd

// pages_tree_test.go — contract tests for `mio pages tree set/get` (MIO-2258):
// authoring a page's draft node-tree via PUT with If-Match OCC, and reading the
// author draft tree. The write derives the JSON:API type "page_draft_trees"
// from the .../pages/{id}/tree tail via a typeOverride.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const draftTreeBody = `{"data":{"id":"page_x","type":"page_draft_trees","attributes":{"draft_version":4,"tree":{"root":{"kind":"stack"}}}}}`

// TestPagesTreeSet_PutWithIfMatch verifies `tree set` PUTs .../pages/{id}/tree
// with the If-Match header and a {data:{type:page_draft_trees,attributes:{tree}}}
// body read from --file.
func TestPagesTreeSet_PutWithIfMatch(t *testing.T) {
	dir := t.TempDir()
	fp := dir + "/tree.json"
	if err := os.WriteFile(fp, []byte(`{"root":{"kind":"stack","children":[{"kind":"hero"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotMethod, gotPath, gotIfMatch string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotIfMatch = r.Method, r.URL.Path, r.Header.Get("If-Match")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(draftTreeBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "tree", "set", "page_x", "--if-match", "3", "--file", fp,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("HTTP method = %q, want PUT", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/pages/page_x/tree") {
		t.Errorf("path %q does not end with /pages/page_x/tree", gotPath)
	}
	if gotIfMatch != "3" {
		t.Errorf("If-Match = %q, want \"3\"", gotIfMatch)
	}

	var doc struct {
		Data struct {
			Type       string `json:"type"`
			Attributes struct {
				Tree map[string]any `json:"tree"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("body not valid JSON: %v; body=%q", err, gotBody)
	}
	if doc.Data.Type != "page_draft_trees" {
		t.Errorf("data.type = %q, want page_draft_trees (typeOverride pages/tree)", doc.Data.Type)
	}
	if _, ok := doc.Data.Attributes.Tree["root"]; !ok {
		t.Errorf("attributes.tree should carry the file's root node; got %#v", doc.Data.Attributes.Tree)
	}
}

// TestPagesTreeSet_DefaultsIfMatchZero verifies that omitting --if-match is
// allowed for the first tree on a draft-less page: the PUT still fires and sends
// If-Match: "0" (the first-set sentinel). This is the MIO-2518 relaxation — the
// backend's atomic OCC guard rejects a defaulted 0 against a page that already
// has a draft, so the default can never clobber an existing draft.
func TestPagesTreeSet_DefaultsIfMatchZero(t *testing.T) {
	dir := t.TempDir()
	fp := dir + "/tree.json"
	if err := os.WriteFile(fp, []byte(`{"root":{"kind":"stack"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotMethod, gotPath, gotIfMatch string
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fired = true
		gotMethod, gotPath, gotIfMatch = r.Method, r.URL.Path, r.Header.Get("If-Match")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(draftTreeBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "tree", "set", "page_x", "--file", fp,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !fired {
		t.Fatal("omitting --if-match must still fire the PUT (defaults to If-Match: 0)")
	}
	if gotMethod != http.MethodPut {
		t.Errorf("HTTP method = %q, want PUT", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/pages/page_x/tree") {
		t.Errorf("path %q does not end with /pages/page_x/tree", gotPath)
	}
	if gotIfMatch != "0" {
		t.Errorf("If-Match = %q, want \"0\" (first-set default)", gotIfMatch)
	}
}

// TestPagesTreeGet_Query verifies `tree get` GETs .../tree with audience=author
// and resolve=true.
func TestPagesTreeGet_Query(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(draftTreeBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "tree", "get", "page_x",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/pages/page_x/tree") {
		t.Errorf("path %q does not end with /pages/page_x/tree", gotPath)
	}
	if !strings.Contains(gotQuery, "audience=author") || !strings.Contains(gotQuery, "resolve=true") {
		t.Errorf("query %q must contain audience=author and resolve=true", gotQuery)
	}
}
