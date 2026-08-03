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

// ─── MIO-2575: content value under settings ──────────────────────────────────
//
// The most-hit silent drop. Seven leaf kinds read the TOP-LEVEL node.value
// (headline, text, image, video, button, icon, quote — verified against mio-hub
// origin/main); exactly one, progress-ring, reads settings.value. So the check
// must fire for the seven and MUST NOT fire for progress-ring.

func treeWith(node map[string]any) map[string]any {
	return map[string]any{"root": map[string]any{
		"id": "root", "children": []any{node},
	}}
}

func TestValidatePageTree_ValueInSettingsRejected(t *testing.T) {
	for _, kind := range []string{"headline", "text", "image", "video", "button", "icon", "quote"} {
		t.Run(kind, func(t *testing.T) {
			err := validatePageTree(treeWith(map[string]any{
				"id": "n1", "kind": kind,
				"settings": map[string]any{"value": "Hello"},
			}))
			if err == nil {
				t.Fatalf("%s with settings.value must be rejected — the renderer reads node.value and emits an empty node", kind)
			}
			if !strings.Contains(err.Error(), "TOP-LEVEL") {
				t.Errorf("message must say where the value belongs; got: %v", err)
			}
		})
	}
}

// progress-ring is the ONE kind that legitimately reads settings.value. Flagging
// it would reject a tree the renderer handles correctly — the conduit rule.
func TestValidatePageTree_ProgressRingSettingsValueAllowed(t *testing.T) {
	if err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "progress-ring",
		"settings": map[string]any{"value": 42},
	})); err != nil {
		t.Errorf("progress-ring reads settings.value — it must NOT be rejected; got: %v", err)
	}
}

// A node carrying BOTH is not silently dropping anything: the renderer reads the
// top-level one. Rejecting it would break working trees.
func TestValidatePageTree_ValueBothPlacesAllowed(t *testing.T) {
	if err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "headline", "value": "Hello",
		"settings": map[string]any{"value": "ignored"},
	})); err != nil {
		t.Errorf("top-level value present — nothing is dropped, must not reject; got: %v", err)
	}
}

// An unknown or absent kind must not be rejected on a guess — this walker only
// flags shapes that can never render.
func TestValidatePageTree_UnknownKindWithSettingsValueAllowed(t *testing.T) {
	for _, n := range []map[string]any{
		{"id": "n1", "kind": "some-future-kind", "settings": map[string]any{"value": 1}},
		{"id": "n2", "settings": map[string]any{"value": 1}},
	} {
		if err := validatePageTree(treeWith(n)); err != nil {
			t.Errorf("unknown/absent kind must not be rejected on a guess; got: %v", err)
		}
	}
}

// ─── MIO-2799: the weight message must describe the real failure mode ────────

func TestValidatePageTree_WeightMessageDescribesFallbackNotDrop(t *testing.T) {
	err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "headline", "value": "Hi",
		"settings": map[string]any{"weight": "bold"},
	}))
	if err == nil {
		t.Fatal("a non-numeric weight must still be rejected — the check itself is correct")
	}
	msg := err.Error()
	// The behaviour is a per-kind fallback: headline -> font-normal, text -> no
	// class. The node RENDERS. Saying "silently dropped" sends the reader looking
	// for a missing node, and contradicts the mio-docs guides (MIO-2799).
	if strings.Contains(msg, "SILENTLY DROPPED") {
		t.Errorf("message still claims a silent drop; the weight is DISCARDED and a per-kind fallback applies. got: %v", msg)
	}
	if !strings.Contains(msg, "DISCARDED") {
		t.Errorf("message must name the real consequence (discarded + fallback); got: %v", msg)
	}
}

// TestPagesTreeSet_ValueInSettings_ExitUsageNoHTTP is the contract-level guard
// for the third rejection (MIO-2575), matching the shape the weight and template
// rules already have. The four tests above call validatePageTree directly and so
// pin only "an error"; they cannot see the EXIT CODE or the no-HTTP property,
// which are the stable public contract. Verified: flipping the check's
// errs.ExitUsage to errs.ExitGeneric left the whole package green without this.
func TestPagesTreeSet_ValueInSettings_ExitUsageNoHTTP(t *testing.T) {
	fp := writeTreeFile(t, `{"root":{"id":"root","kind":"stack","children":[{"id":"h","kind":"headline","settings":{"value":"Hello"}}]}}`)

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
		t.Error("a misplaced content value must be rejected before any HTTP request")
	}
	if err == nil || !strings.Contains(err.Error(), "settings.value") {
		t.Errorf("error should name settings.value; got %v", err)
	}
}

// The message must NOT claim every kind renders empty — icon falls back to the
// "star" glyph and quote renders nothing at all. Asserting a universal "renders
// EMPTY" would repeat, for value, exactly the misdescription MIO-2799 corrects
// for weight: it sends the author hunting a missing node when a star is sitting
// there.
func TestValidatePageTree_ValueMessageIsPerKindNotUniversallyEmpty(t *testing.T) {
	err := validatePageTree(treeWith(map[string]any{
		"id": "i1", "kind": "icon", "settings": map[string]any{"value": "rocket"},
	}))
	if err == nil {
		t.Fatal("icon with settings.value must still be rejected")
	}
	msg := err.Error()
	if !strings.Contains(msg, "star") {
		t.Errorf("message must name icon's actual fallback (the \"star\" glyph), not claim it renders empty; got: %v", msg)
	}
	if !strings.Contains(msg, "quote renders NOTHING") {
		t.Errorf("message must distinguish quote, which returns null and vanishes; got: %v", msg)
	}
}

// The message enumerates FOUR distinct render outcomes; probing only two of them
// is the "probe set smaller than the claim" shape from
// .claude/rules/verifying-guards.md. Each kind-group below was verified against
// mio-hub origin/main, and each is a case where "renders empty" would send the
// author looking in the wrong place.
func TestValidatePageTree_ValueMessageCoversEveryKindGroup(t *testing.T) {
	err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "headline", "settings": map[string]any{"value": "x"},
	}))
	if err == nil {
		t.Fatal("expected a rejection")
	}
	msg := err.Error()
	for _, want := range []struct{ phrase, why string }{
		{"headline/text render empty", "the only group that genuinely renders empty"},
		{"grey fallback TILE", "image passes \"\" to Thumbnail, which renders a tinted tile with the image sprite"},
		{"source not allowed", "video under embed_type \"iframe\" renders a VISIBLE red error box"},
		{"star", "icon falls back to the star glyph"},
		{"quote renders NOTHING", "quote returns null and is absent entirely"},
		{"blank label", "button renders, with no label"},
	} {
		if !strings.Contains(msg, want.phrase) {
			t.Errorf("message must cover %q — %s; got: %v", want.phrase, want.why, msg)
		}
	}
}

// A null top-level value is not a real value: String(null ?? "") is "", so the
// node drops exactly as if the key were absent. Treating null as "present"
// would let the commonest hand-edited shape through.
func TestValidatePageTree_NullTopLevelValueStillRejected(t *testing.T) {
	if err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "headline", "value": nil,
		"settings": map[string]any{"value": "Hello"},
	})); err == nil {
		t.Error("value:null with settings.value is still a silent drop — must be rejected")
	}
}
