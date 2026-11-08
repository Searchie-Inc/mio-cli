package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func writeTreeFile(t *testing.T, body string) string {
	t.Helper()
	fp := t.TempDir() + "/tree.json"
	if err := os.WriteFile(fp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return fp
}

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

func TestValidatePageTree_ProgressRingSettingsValueAllowed(t *testing.T) {
	if err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "progress-ring",
		"settings": map[string]any{"value": 42},
	})); err != nil {
		t.Errorf("progress-ring reads settings.value — it must NOT be rejected; got: %v", err)
	}
}

func TestValidatePageTree_ValueBothPlacesAllowed(t *testing.T) {
	if err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "headline", "value": "Hello",
		"settings": map[string]any{"value": "ignored"},
	})); err != nil {
		t.Errorf("top-level value present — nothing is dropped, must not reject; got: %v", err)
	}
}

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

func TestValidatePageTree_WeightMessageDescribesFallbackNotDrop(t *testing.T) {
	err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "headline", "value": "Hi",
		"settings": map[string]any{"weight": "bold"},
	}))
	if err == nil {
		t.Fatal("a non-numeric weight must still be rejected — the check itself is correct")
	}
	msg := err.Error()
	if strings.Contains(msg, "SILENTLY DROPPED") {
		t.Errorf("message still claims a silent drop; the weight is DISCARDED and a per-kind fallback applies. got: %v", msg)
	}
	if !strings.Contains(msg, "DISCARDED") {
		t.Errorf("message must name the real consequence (discarded + fallback); got: %v", msg)
	}
}

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

func TestValidatePageTree_NullTopLevelValueStillRejected(t *testing.T) {
	if err := validatePageTree(treeWith(map[string]any{
		"id": "n1", "kind": "headline", "value": nil,
		"settings": map[string]any{"value": "Hello"},
	})); err == nil {
		t.Error("value:null with settings.value is still a silent drop — must be rejected")
	}
}
