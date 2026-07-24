package cmd

// hubs_navigation_verbs_test.go — contract tests for `mio hubs navigation`
// list/add/remove/reorder (MIO-2633). Each mutating verb read-modify-writes the
// hub navigation blob: GET the hub (nav + slug), mutate one bucket, then PATCH
// navigation. Tests assert the exact PATCHed bucket and that usage errors fire
// with NO write (pre-HTTP) or NO PATCH (post-fetch validation).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// navMockServer serves a hub retrieve (GET) carrying the given slug + navigation,
// and captures the PATCH body. Returns the server, a pointer to the last PATCH
// body, and a PATCH counter.
func navMockServer(t *testing.T, slug, navJSON string) (*httptest.Server, *[]byte, *int) {
	t.Helper()
	var patchBody []byte
	patches := 0
	hubDoc := fmt.Sprintf(`{"data":{"id":"hub_x","type":"hubs","attributes":{"slug":%q,"navigation":%s}}}`, slug, navJSON)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patches++
			patchBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubDoc))
	}))
	t.Cleanup(srv.Close)
	return srv, &patchBody, &patches
}

func patchNavBucket(t *testing.T, body []byte, bucket string) []any {
	t.Helper()
	typ, attrs := decodeDataTypeAttrs(t, body)
	if typ != "hubs" {
		t.Errorf("PATCH data.type=%q want hubs", typ)
	}
	nav, ok := attrs["navigation"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH body has no navigation object: %s", body)
	}
	arr, ok := nav[bucket].([]any)
	if !ok {
		t.Fatalf("navigation.%s is not an array: %v", bucket, nav[bucket])
	}
	return arr
}

func navItemStr(t *testing.T, item any, key string) string {
	t.Helper()
	m, ok := item.(map[string]any)
	if !ok {
		t.Fatalf("nav item is not an object: %v", item)
	}
	s, _ := m[key].(string)
	return s
}

const twoHeaderItems = `{"header":[{"type":"url","href":"/my-hub/a","label":"A"},{"type":"url","href":"/my-hub/b","label":"B"}]}`

// ── add ─────────────────────────────────────────────────────────────────────

func TestNavAdd_UrlConvenience_Appends(t *testing.T) {
	srv, body, patches := navMockServer(t, "my-hub", twoHeaderItems)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "add", "hub_x", "header",
			"--type", "url", "--href", "/my-hub/about", "--label", "About")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 1 {
		t.Fatalf("PATCH count=%d want 1", *patches)
	}
	items := patchNavBucket(t, *body, "header")
	if len(items) != 3 {
		t.Fatalf("header len=%d want 3 (appended)", len(items))
	}
	if got := navItemStr(t, items[2], "href"); got != "/my-hub/about" {
		t.Errorf("appended href=%q want /my-hub/about", got)
	}
	if got := navItemStr(t, items[2], "type"); got != "url" {
		t.Errorf("appended type=%q want url", got)
	}
}

func TestNavAdd_ItemJSON_Appends(t *testing.T) {
	srv, body, patches := navMockServer(t, "my-hub", twoHeaderItems)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "add", "hub_x", "header",
			"--item-json", `{"type":"page","label":"Guide","page_id":"pg_1"}`)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 1 {
		t.Fatalf("PATCH count=%d want 1", *patches)
	}
	items := patchNavBucket(t, *body, "header")
	if len(items) != 3 || navItemStr(t, items[2], "page_id") != "pg_1" {
		t.Errorf("item-json not appended correctly: %v", items)
	}
}

func TestNavAdd_Position_Inserts(t *testing.T) {
	srv, body, _ := navMockServer(t, "my-hub", twoHeaderItems)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "add", "hub_x", "header",
			"--type", "url", "--href", "/my-hub/first", "--label", "First", "--position", "0")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	items := patchNavBucket(t, *body, "header")
	if len(items) != 3 || navItemStr(t, items[0], "href") != "/my-hub/first" {
		t.Errorf("--position 0 did not insert at front: %v", items)
	}
}

func TestNavAdd_RejectsHubEscapingHref(t *testing.T) {
	// A hub-relative href outside /{slug} must be rejected AFTER the fetch (slug
	// known) with NO PATCH.
	srv, _, patches := navMockServer(t, "my-hub", twoHeaderItems)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "add", "hub_x", "header",
			"--type", "url", "--href", "/other-hub/x", "--label", "X")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 0 {
		t.Error("an escaping href must not PATCH the hub")
	}
}

func TestNavAdd_UsageErrors_NoRequest(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"bad bucket", []string{"hubs", "navigation", "add", "hub_x", "sidebar", "--type", "url", "--href", "/my-hub/x"}},
		{"no item source", []string{"hubs", "navigation", "add", "hub_x", "header"}},
		{"item-json AND type", []string{"hubs", "navigation", "add", "hub_x", "header", "--item-json", `{"type":"url"}`, "--type", "url"}},
		{"non-url type via flags", []string{"hubs", "navigation", "add", "hub_x", "header", "--type", "page"}},
		{"url without href", []string{"hubs", "navigation", "add", "hub_x", "header", "--type", "url"}},
		{"item-json not an object", []string{"hubs", "navigation", "add", "hub_x", "header", "--item-json", `["x"]`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := firedGuardServer(t)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", tc.args...)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
			}
			if *fired {
				t.Error("usage error must fire before any HTTP request")
			}
		})
	}
}

// ── remove ──────────────────────────────────────────────────────────────────

func TestNavRemove_ByIndex(t *testing.T) {
	srv, body, patches := navMockServer(t, "my-hub", twoHeaderItems)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "remove", "hub_x", "header", "--index", "0")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 1 {
		t.Fatalf("PATCH count=%d want 1", *patches)
	}
	items := patchNavBucket(t, *body, "header")
	if len(items) != 1 || navItemStr(t, items[0], "href") != "/my-hub/b" {
		t.Errorf("index 0 not removed; remaining=%v", items)
	}
}

func TestNavRemove_OutOfRange_NoPatch(t *testing.T) {
	srv, _, patches := navMockServer(t, "my-hub", twoHeaderItems)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "remove", "hub_x", "header", "--index", "9")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 0 {
		t.Error("out-of-range index must not PATCH")
	}
}

func TestNavRemove_RequiresIndex_NoRequest(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "remove", "hub_x", "header")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --index must fire before any HTTP request")
	}
}

// ── reorder ─────────────────────────────────────────────────────────────────

func TestNavReorder_Permutes(t *testing.T) {
	const three = `{"header":[{"type":"url","href":"/my-hub/a","label":"A"},{"type":"url","href":"/my-hub/b","label":"B"},{"type":"url","href":"/my-hub/c","label":"C"}]}`
	srv, body, patches := navMockServer(t, "my-hub", three)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "reorder", "hub_x", "header", "--order", "2,0,1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 1 {
		t.Fatalf("PATCH count=%d want 1", *patches)
	}
	items := patchNavBucket(t, *body, "header")
	got := []string{navItemStr(t, items[0], "label"), navItemStr(t, items[1], "label"), navItemStr(t, items[2], "label")}
	if got[0] != "C" || got[1] != "A" || got[2] != "B" {
		t.Errorf("reorder 2,0,1 => %v want [C A B]", got)
	}
}

func TestNavReorder_BadPermutation_NoPatch(t *testing.T) {
	cases := map[string]string{
		"duplicate index": "0,0,1",
		"wrong length":    "0,1",
		"out of range":    "0,1,9",
	}
	const three = `{"header":[{"type":"url","href":"/my-hub/a"},{"type":"url","href":"/my-hub/b"},{"type":"url","href":"/my-hub/c"}]}`
	for name, order := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _, patches := navMockServer(t, "my-hub", three)
			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1", "hubs", "navigation", "reorder", "hub_x", "header", "--order", order)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
			}
			if *patches != 0 {
				t.Error("a bad permutation must not PATCH")
			}
		})
	}
}

func TestNavReorder_BadOrderFormat_NoRequest(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "reorder", "hub_x", "header", "--order", "1,x,2")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("a non-integer --order must fire before any HTTP request")
	}
}

// ── list ────────────────────────────────────────────────────────────────────

func TestNavList_IndexesItems(t *testing.T) {
	srv, _, patches := navMockServer(t, "my-hub", twoHeaderItems)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "json", "hubs", "navigation", "list", "hub_x", "header")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 0 {
		t.Error("list must not PATCH")
	}
	var out map[string][]map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("list output not a JSON object: %v; stdout=%q", err, res.Stdout)
	}
	hdr := out["header"]
	if len(hdr) != 2 {
		t.Fatalf("header len=%d want 2", len(hdr))
	}
	if hdr[0]["index"] != float64(0) || hdr[1]["index"] != float64(1) {
		t.Errorf("items not indexed 0,1: %v", hdr)
	}
}

// The rendered nav output must be a generic JSON tree that --jq (gojq) can
// traverse — gojq panics on concrete Go slice types like []map[string]any, so
// indexedBucket must return []any. Regression guard for that class.
func TestNavList_JqTraversable(t *testing.T) {
	srv, _, _ := navMockServer(t, "my-hub", twoHeaderItems)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "list", "hub_x", "header", "--jq", ".header[1].index")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK (--jq must traverse the nav output); stderr=%q", res.Code, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "1" {
		t.Errorf("--jq .header[1].index = %q want 1", strings.TrimSpace(res.Stdout))
	}
}

// Codex R1: a present-but-non-object stored navigation is malformed data — the
// verb must reject it (with no PATCH) rather than silently replace/destroy it.
func TestNav_MalformedStoredNavigation_NoPatch(t *testing.T) {
	srv, _, patches := navMockServer(t, "my-hub", `"not-an-object"`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "add", "hub_x", "header",
			"--type", "url", "--href", "/my-hub/x", "--label", "X")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 0 {
		t.Error("a malformed stored navigation must not be overwritten")
	}
}

// Codex R1: an item carrying its own "index" field must NOT shadow the generated
// position, or 'list' would advertise the wrong index for remove/reorder.
func TestNavList_GeneratedIndexWins(t *testing.T) {
	const withBogusIndex = `{"header":[{"type":"url","href":"/my-hub/a","index":"BOGUS"},{"type":"url","href":"/my-hub/b","index":99}]}`
	srv, _, _ := navMockServer(t, "my-hub", withBogusIndex)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "list", "hub_x", "header", "--jq", ".header[1].index")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "1" {
		t.Errorf("generated index = %q want 1 (item's own index must not win)", strings.TrimSpace(res.Stdout))
	}
}

// Codex R1 / Opus: the url convenience flags build a header/footer-shaped item;
// they must be rejected for the mobile bucket (which uses {id,label,route,icon}),
// before any HTTP request.
func TestNavAdd_MobileRejectsUrlFlags_NoRequest(t *testing.T) {
	for _, args := range [][]string{
		{"hubs", "navigation", "add", "hub_x", "mobile", "--type", "url", "--href", "/my-hub/x", "--label", "X"},
		{"hubs", "navigation", "add", "hub_x", "mobile"}, // no source at all → also steered to --item-json
	} {
		srv, fired := firedGuardServer(t)
		res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
		if res.Code != errs.ExitUsage {
			t.Errorf("args=%v exit=%d want ExitUsage; stderr=%q", args, res.Code, res.Stderr)
		}
		if *fired {
			t.Errorf("args=%v: must fire no HTTP request", args)
		}
	}
}

// A mobile item supplied via --item-json is accepted and PATCHed as-is.
func TestNavAdd_MobileItemJSON(t *testing.T) {
	srv, body, patches := navMockServer(t, "my-hub", `{"mobile":[]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "add", "hub_x", "mobile",
			"--item-json", `{"id":"m1","label":"Home","route":"/","icon":"home"}`)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *patches != 1 {
		t.Fatalf("PATCH count=%d want 1", *patches)
	}
	items := patchNavBucket(t, *body, "mobile")
	if len(items) != 1 || navItemStr(t, items[0], "route") != "/" {
		t.Errorf("mobile item not appended: %v", items)
	}
}

// A footer add via --item-json PATCHes the footer bucket (not header).
func TestNavAdd_FooterItemJSON(t *testing.T) {
	srv, body, _ := navMockServer(t, "my-hub", `{"footer":[{"type":"url","href":"/my-hub/legal","label":"Legal"}]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "navigation", "add", "hub_x", "footer",
			"--item-json", `{"type":"url","href":"/my-hub/tos","label":"Terms"}`)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	items := patchNavBucket(t, *body, "footer")
	if len(items) != 2 || navItemStr(t, items[1], "href") != "/my-hub/tos" {
		t.Errorf("footer item not appended: %v", items)
	}
}

// --position out of range is a post-fetch data error: it fires the GET but no PATCH.
func TestNavAdd_PositionOutOfRange_NoPatch(t *testing.T) {
	for _, pos := range []string{"99", "-1"} {
		srv, _, patches := navMockServer(t, "my-hub", twoHeaderItems)
		res := runContract(t, baseEnv(srv.URL),
			withTeam("t_team1", "hubs", "navigation", "add", "hub_x", "header",
				"--type", "url", "--href", "/my-hub/x", "--label", "X", "--position", pos)...)
		if res.Code != errs.ExitUsage {
			t.Errorf("--position %s exit=%d want ExitUsage; stderr=%q", pos, res.Code, res.Stderr)
		}
		if *patches != 0 {
			t.Errorf("--position %s must not PATCH", pos)
		}
	}
}
