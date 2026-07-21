package cmd

// pages_tree_validate_test.go — pre-flight validation of a page-builder
// node-tree before `pages tree set` PUTs it (MIO-2537). The API validates
// STRUCTURE, not RENDERABILITY: a malformed node setting returns 200 and is then
// SILENTLY DROPPED by the renderer. The pre-flight rejects the well-defined
// malformed cases up front (ExitUsage, NO HTTP) so an author gets a clear error
// instead of a phantom success.
//
// Contract mirrors the imperative-door validator (pages_sections_type_test.go):
// a usage-level rejection fires BEFORE any HTTP request.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// writeTreeFile writes tree JSON to a temp file and returns its path.
func writeTreeFile(t *testing.T, body string) string {
	t.Helper()
	fp := t.TempDir() + "/tree.json"
	if err := os.WriteFile(fp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return fp
}

// TestPagesTreeSet_NumericWeight_Proceeds is the positive guard: a tree whose
// node carries a NUMERIC settings.weight (700) is valid and must reach the PUT.
func TestPagesTreeSet_NumericWeight_Proceeds(t *testing.T) {
	fp := writeTreeFile(t, `{"root":{"id":"root","kind":"stack","children":[{"id":"h","kind":"headline","settings":{"level":2,"weight":700}}]}}`)

	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
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
		t.Fatalf("exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !fired {
		t.Error("a valid numeric-weight tree must reach the PUT")
	}
}

// TestPagesTreeSet_StringWeight_ExitUsageNoHTTP rejects a CSS-keyword weight
// ("bold") — the API accepts it (200) but the renderer drops the node.
func TestPagesTreeSet_StringWeight_ExitUsageNoHTTP(t *testing.T) {
	fp := writeTreeFile(t, `{"root":{"id":"root","kind":"stack","children":[{"id":"h","kind":"headline","settings":{"level":2,"weight":"bold"}}]}}`)

	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	err := executeCLI(t, baseEnv(srv.URL),
		"--team", "t_team1", "--hub", "hub_123",
		"pages", "tree", "set", "page_x", "--file", fp,
	)
	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if fired {
		t.Error("a string weight must be rejected before any HTTP request")
	}
	if err == nil || !strings.Contains(err.Error(), "weight") {
		t.Errorf("error should name the weight field; got %v", err)
	}
}

// TestPagesTreeSet_SectionBlankTemplate_ExitUsageNoHTTP rejects a section node
// whose template is present but blank — the renderer cannot resolve a section
// type and drops it. (A node with NO template key is not identifiable as a
// section and is left alone; see TestPagesTreeSet_NoTemplate_Proceeds.)
func TestPagesTreeSet_SectionBlankTemplate_ExitUsageNoHTTP(t *testing.T) {
	fp := writeTreeFile(t, `{"root":{"id":"root","kind":"stack","children":[{"id":"sec","kind":"row","template":"","settings":{},"children":[]}]}}`)

	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	err := executeCLI(t, baseEnv(srv.URL),
		"--team", "t_team1", "--hub", "hub_123",
		"pages", "tree", "set", "page_x", "--file", fp,
	)
	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if fired {
		t.Error("a blank section template must be rejected before any HTTP request")
	}
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Errorf("error should name the template field; got %v", err)
	}
}

// TestPagesTreeSet_SectionNonStringTemplate_ExitUsageNoHTTP rejects a section
// node whose template is present but not a string (a typo like a bare number).
func TestPagesTreeSet_SectionNonStringTemplate_ExitUsageNoHTTP(t *testing.T) {
	fp := writeTreeFile(t, `{"root":{"id":"root","kind":"stack","children":[{"id":"sec","kind":"row","template":700,"settings":{}}]}}`)

	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	err := executeCLI(t, baseEnv(srv.URL),
		"--team", "t_team1", "--hub", "hub_123",
		"pages", "tree", "set", "page_x", "--file", fp,
	)
	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if fired {
		t.Error("a non-string section template must be rejected before any HTTP request")
	}
}

// TestPagesTreeSet_NoTemplate_Proceeds is the conservative guard: a node WITHOUT
// a template key is not identifiable as a section, so it must NOT be rejected —
// the existing minimal-tree contract tests depend on this (a `{"kind":"stack"}`
// root and a `{"kind":"hero"}` child both lack templates and must pass).
func TestPagesTreeSet_NoTemplate_Proceeds(t *testing.T) {
	fp := writeTreeFile(t, `{"root":{"id":"root","kind":"stack","children":[{"id":"hero","kind":"row","settings":{}}]}}`)

	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
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
		t.Fatalf("exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !fired {
		t.Error("a template-less node must not be treated as a malformed section")
	}
}

// TestPagesTreeSet_ButtonEmptyAction_NotOverRejected documents the deliberate
// scope boundary: the catalog scaffold emits VALID button nodes with an empty
// settings object (they inherit their action from scope, e.g.
// settings.actionFromScope). "Missing action" is therefore NOT malformed, and no
// concrete literal `action` object shape is pinned anywhere in the catalog or
// code to validate against — so the pre-flight does NOT reject buttons on a
// guessed shape (that would drop valid trees). Button-action validation is left
// to the backend (MIO-2538). This test guards against over-validation.
func TestPagesTreeSet_ButtonEmptyAction_NotOverRejected(t *testing.T) {
	fp := writeTreeFile(t, `{"root":{"id":"root","kind":"stack","children":[{"id":"cta","kind":"button","settings":{}}]}}`)

	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
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
		t.Fatalf("exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !fired {
		t.Error("a button with an empty/scope-inherited action must not be rejected")
	}
}
