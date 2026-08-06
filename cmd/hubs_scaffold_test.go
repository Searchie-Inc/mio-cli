package cmd

// hubs_scaffold_test.go — orchestrator-skeleton contract tests for
// `mio hubs scaffold` (MIO-2543 Task 11).
//
// These pin the command shell, NOT the per-step bodies (those are filled in by
// Phase 4). They assert three things the skeleton must guarantee:
//   - --dry-run emits the ordered plan naming every pipeline step and fires no
//     mutating HTTP;
//   - resume mode (--hub <id>) GETs the hub so scaffoldContext.hubSlug is
//     populated (ResolveHub short-circuits id-shaped values with no lookup);
//   - an unknown --template is a usage error BEFORE any HTTP.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// scaffoldStepNames is the ordered pipeline the dry-run plan must name, in order.
var scaffoldStepNames = []string{
	"hub", "blobs", "spaces", "onboarding", "policies",
	"playlists", "pages", "publish", "welcome-post",
}

// humanScaffold pins a scaffold invocation to the PROSE surface (the plan /
// end-of-run summary) by asking for `--output table` explicitly.
//
// Since MIO-2574 the scaffold's output is FORMAT-DRIVEN like every other
// command: `table` renders the prose (and is the TTY default), while json and
// plain render the machine-readable result. runContract drives the command tree
// with a *bytes.Buffer, which is not a TTY, so the format resolved here is the
// off-a-TTY default — json. A test asserting on the summary/plan TEXT must
// therefore say `table`; the tests asserting the machine result pass
// `--output json` (or rely on that same default) instead.
func humanScaffold(args []string) []string {
	return append(args, "--output", "table")
}

// mutationGuardServer starts a test server that flips *mutated to true on ANY
// non-GET (mutating) request, so a dry-run can assert it created/changed
// nothing. GETs (context resolution + the live catalog fetch, answered with the
// 2.1 artifact) are allowed; other GETs get a minimal hub body.
func mutationGuardServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	srv, _, mutated := liveCatalogScaffoldServer(t, catalog21Body(t))
	return srv, mutated
}

// TestScaffold_DryRunEmitsPlanNoMutatingHTTP: `hubs scaffold --dry-run` prints
// the ordered plan naming every step and fires no MUTATING HTTP (the catalog
// GET is allowed — the plan is built from the backend's live catalog).
func TestScaffold_DryRunEmitsPlanNoMutatingHTTP(t *testing.T) {
	srv, mutated := mutationGuardServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run"))...)

	if res.Code != errs.ExitOK {
		t.Fatalf("dry-run exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *mutated {
		t.Errorf("dry-run must fire NO mutating (non-GET) request")
	}

	// The plan prints one step per line as "  N. <step> — <detail>". Extract the
	// step TOKEN from each line (line-anchored: ^\s*\d+\.\s+(\S+)) and compare the
	// ordered token slice to the expected pipeline order. Matching the per-line
	// token — NOT a substring search over the whole output — means a detail string
	// that happens to contain a later step's name (e.g. "publish" inside the
	// playlists detail) can never trip the ordering check. This removes the
	// invisible "don't put a step name in any detail" constraint that a raw
	// strings.Index scan imposed.
	stepLineRE := regexp.MustCompile(`(?m)^\s*\d+\.\s+(\S+)`)
	var gotSteps []string
	pagesLines := 0
	for _, m := range stepLineRE.FindAllStringSubmatch(res.Stdout, -1) {
		if m[1] == "pages" {
			pagesLines++
		}
		// The pages step records one plan entry PER PAGE (MIO-2672 Task 7), so
		// collapse CONSECUTIVE repeats of the same step name only. Out-of-order
		// repeats (a step name reappearing later, after a different step) still
		// survive into gotSteps and fail the DeepEqual — the order assertion is
		// not weakened.
		if n := len(gotSteps); n > 0 && gotSteps[n-1] == m[1] {
			continue
		}
		gotSteps = append(gotSteps, m[1])
	}
	if !reflect.DeepEqual(gotSteps, scaffoldStepNames) {
		t.Errorf("dry-run plan steps = %v, want %v (every step, in order); stdout:\n%s",
			gotSteps, scaffoldStepNames, res.Stdout)
	}
	// One plan entry per page: the community template has 3 pages[] entries.
	if pagesLines != 3 {
		t.Errorf("dry-run plan must record one `pages` entry per page (community has 3), got %d; stdout:\n%s",
			pagesLines, res.Stdout)
	}
	// The per-page detail hedges the apply method (Task 9: the op probe is
	// decided at APPLY time, so the plan cannot promise either branch) and
	// names the full client-side mutation set + the §5.1 re-run caveat.
	if !strings.Contains(res.Stdout,
		"apply via backend op if available, else create + set tree + publish + mark applied; re-runs follow §5.1 recovery") {
		t.Errorf("pages plan detail must hedge the apply method and name the recovery caveat; stdout:\n%s", res.Stdout)
	}
}

// TestScaffold_ResumeGetsHubForSlug: resume mode (--hub) GETs the hub and
// populates scaffoldContext.hubSlug from the response.
func TestScaffold_ResumeGetsHubForSlug(t *testing.T) {
	catBody := catalog21Body(t)
	gotGet := false
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The preflight's live catalog fetch — answered with the 2.1 artifact.
		// It fires AFTER the resume-mode hub retrieve, so it never clobbers the
		// first-GET recording below.
		if serveCatalogGET(w, r, catBody) {
			return
		}
		// W2b op probe (Task 9): absent here — this test pins the resume-mode
		// hub retrieve + client-side pipeline, so the probe 404s deterministically
		// instead of riding the empty-Pages tolerance of a catch-all 200.
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scaffold-from-template") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet && !gotGet {
			// Record only the FIRST GET — the resume-mode hub retrieve. Now that the
			// Phase-4 steps run for real (stepBlobs GETs the hub, stepSpaces lists
			// spaces), later GETs would clobber gotPath; the resume contract is about
			// that first hub retrieve populating hubSlug.
			gotGet = true
			gotPath = r.URL.Path
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)

	// Observe the resolved context via the test seam.
	var gotSlug string
	scaffoldAfterResolve = func(sc *scaffoldContext) { gotSlug = sc.hubSlug }
	defer func() { scaffoldAfterResolve = nil }()

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold", "--hub", "hub_1", "--template", "community")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("resume exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !gotGet {
		t.Fatalf("resume mode must GET the hub to populate hubSlug")
	}
	if !strings.HasSuffix(gotPath, "/teams/t_team1/hubs/hub_1") {
		t.Errorf("resume GET path = %q, want .../teams/t_team1/hubs/hub_1", gotPath)
	}
	if gotSlug != "acme" {
		t.Errorf("scaffoldContext.hubSlug = %q, want %q (populated from the hub GET)", gotSlug, "acme")
	}
}

// TestScaffold_CreateModeIgnoresConfiguredDefaultHub: a configured default hub
// (`mio config set current_hub`) must NOT turn a create-mode invocation into a resume.
// Resume keys on the EXPLICIT --hub flag only — gating on the merged
// resolved.HubID would silently resume onto a user's default hub, so no hub GET
// may fire for a create-mode (--name/--slug, no --hub) invocation here.
func TestScaffold_CreateModeIgnoresConfiguredDefaultHub(t *testing.T) {
	// Create mode now does real work (stepHub creates a NEW hub, then the blobs/
	// spaces steps run against it), so the old "no request at all" guard no longer
	// holds. The invariant it protected still does: a configured default hub
	// (current_hub) feeds resolved.HubID, NOT flags.hub, so it must never be
	// touched — a GET to it would mean create silently became a resume. The server
	// returns a NEW created-hub id so no legitimate step path can contain the
	// configured id.
	catBody := catalog21Body(t)
	touchedConfigured := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "hub_configured") {
			touchedConfigured = true
		}
		if serveCatalogGET(w, r, catBody) {
			return
		}
		// W2b op probe (Task 9): absent here — the create-vs-resume invariant is
		// about the CLIENT-SIDE pipeline's requests, so the probe 404s
		// deterministically instead of riding empty-Pages op tolerance.
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scaffold-from-template") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Whole-hub op (MIO-2976): absent here too, and for the same reason. The
		// catch-all below answers ANY request with a hubs resource carrying an id,
		// which the op path would otherwise accept as a successful whole-hub build
		// — so without this the run never reaches stepHub and this test silently
		// stops exercising the client-side pipeline it exists to pin.
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hubs/from-template") {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_created","type":"hubs","attributes":{"slug":"x","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)

	// Sandbox a config that sets a default hub (current_hub) — this feeds
	// resolved.HubID but NOT flags.hub.
	cfgHome := t.TempDir()
	mioDir := filepath.Join(cfgHome, "mio")
	if err := os.MkdirAll(mioDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mioDir, "config.toml"),
		[]byte("current_hub = \"hub_configured\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env := append(scaffoldEnv(t, srv.URL), "XDG_CONFIG_HOME="+cfgHome)
	res := runContract(t, env,
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("create-mode exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if touchedConfigured {
		t.Errorf("a configured default hub must NOT trigger resume in create mode (no request may touch hub_configured)")
	}
}

// ─── Phase 4 step tests (Tasks 12-14) ────────────────────────────────────────
//
// These drive the pipeline step functions DIRECTLY with an in-memory
// catalog.HubTemplate and a scaffoldContext wired to an httptest server
// (unit-style), per the Phase-4 plan — they do not depend on the full CLI
// wiring. (TestScaffold_UnknownTemplate was superseded by
// TestScaffold_UnknownTemplateListsAvailable: the template now comes from the
// backend's live catalog, so its absence is detected after the catalog GET.)

// newStepSC builds a non-dry-run scaffoldContext pointed at cl, for driving a
// single step directly. A nil plan + dryRun:false means sc.step runs the real fn.
func newStepSC(cl *client.Client, hubID, hubSlug string) *scaffoldContext {
	return &scaffoldContext{
		ctx:              context.Background(),
		cl:               cl,
		teamID:           "t_team1",
		hubID:            hubID,
		hubSlug:          hubSlug,
		spaceIDsBySlug:   map[string]string{},
		defIDsBySlug:     map[string]string{},
		playlistIDsByKey: map[string]string{},
	}
}

// scaffoldFixture parses the 2.1 catalog fixture and returns it, the community
// hub template sourced from it, and the page plan built from it (instantiated-
// once raw trees) — exactly the state scaffoldPreflight leaves on the context,
// for driving steps directly.
func scaffoldFixture(t *testing.T) (*catalog.Catalog, catalog.HubTemplate, *scaffoldPlan) {
	t.Helper()
	cat, err := catalog.Parse(catalog21Body(t))
	if err != nil {
		t.Fatalf("parse 2.1 catalog fixture: %v", err)
	}
	ht, ok := cat.HubTemplateByID("community")
	if !ok {
		t.Fatal("community hub template missing from the 2.1 fixture")
	}
	plan, err := buildScaffoldPlan(cat, ht)
	if err != nil {
		t.Fatalf("buildScaffoldPlan: %v", err)
	}
	return cat, ht, plan
}

// ─── Task 12: stepHub ─────────────────────────────────────────────────────────

// TestStepHub_CreateEmitsIdentityPostAndCapturesContext: create mode POSTs the
// hub with IDENTITY ONLY (title/slug from --name/--slug), never the presentation
// blobs (those are stepBlobs' job — the create/blobs split), and captures the
// server-assigned id/slug/is_private into the context.
func TestStepHub_CreateEmitsIdentityPostAndCapturesContext(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_made","type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "", "")
	sc.nameOverride = "My Community"
	sc.slugOverride = "my-community"

	// Branding present in the template MUST NOT leak into the create body.
	tmpl := &catalog.HubTemplate{ID: "community", Branding: map[string]any{"primary": "#111"}}
	if err := stepHub(sc, tmpl); err != nil {
		t.Fatalf("stepHub create: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/teams/t_team1/hubs") {
		t.Errorf("path = %q, want .../teams/t_team1/hubs", gotPath)
	}
	attrs := decodeHubAttrs(t, gotBody)
	if attrs["title"] != "My Community" {
		t.Errorf("title = %v, want My Community", attrs["title"])
	}
	if attrs["slug"] != "my-community" {
		t.Errorf("slug = %v, want my-community", attrs["slug"])
	}
	for _, k := range []string{"branding", "navigation", "settings", "meta"} {
		if _, present := attrs[k]; present {
			t.Errorf("create body must not carry %q (identity only; blobs belong to stepBlobs); attrs=%v", k, attrs)
		}
	}
	if sc.hubID != "hub_made" || sc.hubSlug != "my-community" || !sc.isPrivate {
		t.Errorf("captured context = {hubID:%q hubSlug:%q isPrivate:%v}, want {hub_made my-community true}",
			sc.hubID, sc.hubSlug, sc.isPrivate)
	}
}

// TestStepHub_ResumeFiresNoCreate: resume mode (hubID already set) makes no HTTP
// call — the hub exists, so there is nothing to create.
func TestStepHub_ResumeFiresNoCreate(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepHub(sc, &catalog.HubTemplate{ID: "community"}); err != nil {
		t.Fatalf("stepHub resume: %v", err)
	}
	if fired {
		t.Error("resume mode (hubID set) must fire NO HTTP in stepHub")
	}
	if sc.hubID != "hub_1" {
		t.Errorf("hubID = %q, want unchanged hub_1", sc.hubID)
	}
}

// ─── Task 13: stepBlobs ───────────────────────────────────────────────────────

// TestStepBlobs_OneGetOnePatchSiblingsPreserved: stepBlobs applies branding
// (RMW, siblings preserved), settings and a navigation REPLACE in exactly one GET
// (retrieve) + one PATCH.
func TestStepBlobs_OneGetOnePatchSiblingsPreserved(t *testing.T) {
	var gets, patches int
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
		case http.MethodPatch:
			patches++
			patchBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme","branding":{"primary":"#111","logo_url":"old"}}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID:       "community",
		Branding: map[string]any{"favicon_url": "f"},
		Settings: map[string]any{"registration": map[string]any{"enabled": true}},
		Navigation: map[string]any{"header": []any{
			map[string]any{"type": "url", "label": "Home", "href": "https://x.example.com"},
		}},
	}
	if err := stepBlobs(sc, tmpl); err != nil {
		t.Fatalf("stepBlobs: %v", err)
	}
	if gets != 1 || patches != 1 {
		t.Fatalf("want exactly 1 GET + 1 PATCH; got %d GET, %d PATCH", gets, patches)
	}
	attrs := decodeHubAttrs(t, patchBody)
	b, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH branding absent; attrs=%v", attrs)
	}
	if b["primary"] != "#111" || b["logo_url"] != "old" || b["favicon_url"] != "f" {
		t.Errorf("branding = %v, want RMW {primary:#111, logo_url:old, favicon_url:f} (siblings preserved)", b)
	}
	if _, ok := attrs["navigation"].(map[string]any); !ok {
		t.Errorf("navigation must be REPLACEd in the PATCH; attrs=%v", attrs)
	}
	if _, ok := attrs["settings"].(map[string]any); !ok {
		t.Errorf("settings must be in the PATCH; attrs=%v", attrs)
	}
}

// TestStepBlobs_NoNavigationInTemplateOmitsNavFromPatch: a hub template WITHOUT
// navigation must not put a navigation key in the PATCH at all — navigation is
// a whole-blob REPLACE, so an empty {} would WIPE a hub's existing menu
// (destructive on resume). Regression: CloneNode(nil) once returned a NON-nil
// empty map (a typed nil map matches deepClone's map case), which made
// stepBlobs's nil guard always-true.
func TestStepBlobs_NoNavigationInTemplateOmitsNavFromPatch(t *testing.T) {
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patchBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme","navigation":{"header":[{"type":"url","label":"Keep","href":"/acme/keep"}]}}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{ID: "community", Branding: map[string]any{"favicon_url": "f"}}
	if err := stepBlobs(sc, tmpl); err != nil {
		t.Fatalf("stepBlobs: %v", err)
	}
	attrs := decodeHubAttrs(t, patchBody)
	if _, has := attrs["navigation"]; has {
		t.Errorf("PATCH must NOT carry navigation when the template has none (whole-blob REPLACE would wipe the hub's existing menu); attrs=%v", attrs)
	}
	if _, has := attrs["branding"]; !has {
		t.Errorf("branding must still be PATCHed; attrs=%v", attrs)
	}
}

// TestStepBlobs_StrictRejectsUnknownSettingsKey: an unknown template settings key
// ERRORS under strict mode (ExitUsage) and fires NO PATCH — the whole point of
// the feature is that a malformed template is caught, not silently dropped.
func TestStepBlobs_StrictRejectsUnknownSettingsKey(t *testing.T) {
	patched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patched = true
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme"}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	// "registraton" is a typo of the accepted top-level key "registration".
	tmpl := &catalog.HubTemplate{
		ID:       "community",
		Settings: map[string]any{"registraton": map[string]any{"enabled": true}},
	}
	err := stepBlobs(sc, tmpl)
	if err == nil {
		t.Fatal("stepBlobs must ERROR under strict on an unknown settings key")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if patched {
		t.Error("no PATCH must fire when strict validation rejects a key")
	}
	// …and the guidance must be actionable HERE too (MIO-2604). The shared strict
	// message ends "drop --strict-keys", a flag `hubs scaffold` does not have —
	// a dead end whether the bad key came from the operator's --branding-json or,
	// as here, from the TEMPLATE. stepBlobs routes the rejection through the same
	// swap resolveScaffoldBranding uses, so the whole command speaks with one
	// voice.
	if strings.Contains(err.Error(), "drop --strict-keys") {
		t.Errorf("a template strict-key rejection must not tell the operator to drop --strict-keys (no such flag on scaffold); err=%v", err)
	}
	if !strings.Contains(err.Error(), "no --strict-keys to drop") {
		t.Errorf("a template strict-key rejection must carry the scaffold-specific guidance; err=%v", err)
	}
}

// TestStepBlobs_NonKeyErrorPassesThroughUnchanged: the strict-key message swap
// must be a NO-OP for every other failure. A PATCH that dies with a 500 has to
// keep its own text AND its own exit code (ExitServer) — rewriting every error
// stepBlobs returns into a usage error would corrupt the exit-code contract and
// mislabel a server outage as the operator's mistake.
func TestStepBlobs_NonKeyErrorPassesThroughUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"status":"500","detail":"boom"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme"}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{ID: "community", Branding: map[string]any{"favicon_url": "f"}}

	err := stepBlobs(sc, tmpl)
	if err == nil {
		t.Fatal("a 500 on the blobs PATCH must be an error")
	}
	if got := errs.CodeOf(err); got != errs.ExitServer {
		t.Errorf("error code = %d, want %d (ExitServer) — the strict-key swap must not relabel a server error as usage", got, errs.ExitServer)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("the server's own detail must survive untouched; err=%v", err)
	}
}

// TestStepBlobs_RejectsMalformedNavShape: a template whose navigation bucket is
// the wrong SHAPE (an object {items:[…]} instead of a bare array of typed items)
// is a silent-drop trap — applyHubBlobs' href check silently skips a non-array
// bucket, so without the shape check the malformed menu would be PATCHed and then
// dropped by the renderer. stepBlobs must run validateNavigationBlob and ERROR
// (ExitUsage) before any PATCH.
func TestStepBlobs_RejectsMalformedNavShape(t *testing.T) {
	patched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patched = true
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme"}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	// header as {items:[…]} instead of a bare array — the mio-hub parser drops it.
	tmpl := &catalog.HubTemplate{
		ID:         "community",
		Navigation: map[string]any{"header": map[string]any{"items": []any{}}},
	}
	err := stepBlobs(sc, tmpl)
	if err == nil {
		t.Fatal("stepBlobs must ERROR on a malformed navigation shape (silent-drop trap)")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if patched {
		t.Error("no PATCH must fire when nav shape validation fails")
	}
}

// ─── Task 14: stepSpaces ──────────────────────────────────────────────────────

// TestStepSpaces_ExistingSlugSkippedNewCreated: a template space whose slug
// already exists is skipped (no create); a new slug is created and its id
// recorded.
func TestStepSpaces_ExistingSlugSkippedNewCreated(t *testing.T) {
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			slug, _ := decodeHubAttrs(t, body)["slug"].(string)
			posted = append(posted, slug)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"sp_%s","type":"spaces","attributes":{"slug":%q}}}`, slug, slug)
			return
		}
		// GET list: one existing space "general".
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"sp_gen","type":"spaces","attributes":{"slug":"general"}}]}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Spaces: []catalog.TemplateSpace{
			{Name: "General", Slug: "general", AccessLevel: "public", PostingPermission: "any_member"},
			{Name: "Support", Slug: "support", AccessLevel: "public", PostingPermission: "any_member"},
		},
	}
	if err := stepSpaces(sc, tmpl); err != nil {
		t.Fatalf("stepSpaces: %v", err)
	}
	if len(posted) != 1 || posted[0] != "support" {
		t.Fatalf("POSTed slugs = %v, want only [support] (general exists → skipped)", posted)
	}
	if sc.spaceIDsBySlug["support"] != "sp_support" {
		t.Errorf("spaceIDsBySlug[support] = %q, want sp_support", sc.spaceIDsBySlug["support"])
	}
	if _, has := sc.spaceIDsBySlug["general"]; has {
		t.Errorf("general must not be recorded as created; map=%v", sc.spaceIDsBySlug)
	}
}

// TestStepSpaces_ExhaustiveLookupFindsPage2: the skip-if-exists pre-check must be
// EXHAUSTIVE. "general" exists only on page 2; a first-page-only scan (page 1 =
// "other") would miss it and wrongly create a duplicate. Following the backend's
// meta.page.next_cursor to exhaustion finds it → no POST.
func TestStepSpaces_ExhaustiveLookupFindsPage2(t *testing.T) {
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			slug, _ := decodeHubAttrs(t, body)["slug"].(string)
			posted = append(posted, slug)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"sp_%s","type":"spaces","attributes":{"slug":%q}}}`, slug, slug)
			return
		}
		// Real mio-backend cursor convention: meta.page.{has_more,next_cursor}.
		if r.URL.Query().Get("page[after]") == "cursor2" {
			_, _ = w.Write([]byte(`{"data":[{"id":"sp_gen","type":"spaces","attributes":{"slug":"general"}}],"meta":{"page":{"has_more":false}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"sp_oth","type":"spaces","attributes":{"slug":"other"}}],"meta":{"page":{"has_more":true,"next_cursor":"cursor2"}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID:     "community",
		Spaces: []catalog.TemplateSpace{{Name: "General", Slug: "general", AccessLevel: "public", PostingPermission: "any_member"}},
	}
	if err := stepSpaces(sc, tmpl); err != nil {
		t.Fatalf("stepSpaces: %v", err)
	}
	if len(posted) != 0 {
		t.Errorf("POSTed %v; want none — 'general' exists on page 2 and an exhaustive lookup must find it", posted)
	}
}

// ─── Task 15: stepOnboarding ──────────────────────────────────────────────────

// TestStepOnboarding_CreatesDefAndEnablesOnHubCollectionPath: a new onboarding
// def is CREATEd on the team, then ENABLEd on the hub via a hub-config CREATE that
// POSTs to the COLLECTION path (no /{def} suffix) with definition_id IN THE BODY
// (the MIO-2502 fix) and is_in_onboarding from the template. The def id is
// recorded by slug.
func TestStepOnboarding_CreatesDefAndEnablesOnHubCollectionPath(t *testing.T) {
	var defBody, cfgBody []byte
	var cfgPath string
	var defPosts, cfgPosts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		isHub := strings.Contains(r.URL.Path, "/hubs/")
		switch {
		case r.Method == http.MethodGet && !isHub: // defs list — no existing defs
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && !isHub: // def create
			defPosts++
			defBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"def_company","type":"contact_attributes","attributes":{"slug":"company"}}}`))
		case r.Method == http.MethodPost && isHub: // hub-config create
			cfgPosts++
			cfgPath = r.URL.Path
			cfgBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"cfg_1","type":"contact_attribute_hub_configs","attributes":{"definition_id":"def_company"}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Onboarding: []catalog.TemplateAttrDef{
			{Name: "Company", Slug: "company", FieldType: "text", InOnboarding: true, Required: false},
		},
	}
	if err := stepOnboarding(sc, tmpl); err != nil {
		t.Fatalf("stepOnboarding: %v", err)
	}
	if defPosts != 1 || cfgPosts != 1 {
		t.Fatalf("want 1 def POST + 1 hub-config POST; got %d def, %d cfg", defPosts, cfgPosts)
	}
	defAttrs := decodeHubAttrs(t, defBody)
	if defAttrs["name"] != "Company" || defAttrs["slug"] != "company" || defAttrs["type"] != "text" {
		t.Errorf("def create attrs = %v, want name=Company slug=company type=text", defAttrs)
	}
	// hub-config MUST POST to the COLLECTION path (no /{def} suffix).
	if !strings.HasSuffix(cfgPath, "/hubs/hub_1/contact-attributes") {
		t.Errorf("hub-config path = %q, want collection .../hubs/hub_1/contact-attributes (no def suffix)", cfgPath)
	}
	cfgAttrs := decodeHubAttrs(t, cfgBody)
	if cfgAttrs["definition_id"] != "def_company" {
		t.Errorf("hub-config definition_id = %v, want def_company (in the BODY, MIO-2502)", cfgAttrs["definition_id"])
	}
	if cfgAttrs["is_in_onboarding"] != true {
		t.Errorf("hub-config is_in_onboarding = %v, want true", cfgAttrs["is_in_onboarding"])
	}
	if sc.defIDsBySlug["company"] != "def_company" {
		t.Errorf("defIDsBySlug[company] = %q, want def_company", sc.defIDsBySlug["company"])
	}
}

// TestStepOnboarding_ExistingDefReusedNoDuplicateCreate: a def whose slug already
// exists on the team is REUSED (no duplicate def create) — its id is recorded and
// the hub-config is still enabled (upsert) with the reused id.
func TestStepOnboarding_ExistingDefReusedNoDuplicateCreate(t *testing.T) {
	var defPosts, cfgPosts int
	var cfgBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		isHub := strings.Contains(r.URL.Path, "/hubs/")
		switch {
		case r.Method == http.MethodGet && !isHub: // defs list — "company" already exists
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"def_existing","type":"contact_attributes","attributes":{"slug":"company"}}]}`))
		case r.Method == http.MethodPost && !isHub:
			defPosts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"def_new","type":"contact_attributes","attributes":{"slug":"company"}}}`))
		case r.Method == http.MethodPost && isHub:
			cfgPosts++
			cfgBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"cfg_1","type":"contact_attribute_hub_configs","attributes":{}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID:         "community",
		Onboarding: []catalog.TemplateAttrDef{{Name: "Company", Slug: "company", FieldType: "text", InOnboarding: true}},
	}
	if err := stepOnboarding(sc, tmpl); err != nil {
		t.Fatalf("stepOnboarding: %v", err)
	}
	if defPosts != 0 {
		t.Errorf("existing def slug must be reused — want 0 def POSTs, got %d", defPosts)
	}
	if cfgPosts != 1 {
		t.Errorf("hub-config must still be enabled with the reused def — want 1 cfg POST, got %d", cfgPosts)
	}
	if got := decodeHubAttrs(t, cfgBody)["definition_id"]; got != "def_existing" {
		t.Errorf("hub-config definition_id = %v, want reused def_existing", got)
	}
	if sc.defIDsBySlug["company"] != "def_existing" {
		t.Errorf("defIDsBySlug[company] = %q, want reused def_existing", sc.defIDsBySlug["company"])
	}
}

// TestStepOnboarding_ExhaustiveDefLookupFindsPage2: the skip-if-slug-exists
// pre-check must follow the pagination cursor to exhaustion — "company" exists
// only on page 2, so a first-page-only scan would wrongly create a duplicate.
func TestStepOnboarding_ExhaustiveDefLookupFindsPage2(t *testing.T) {
	defPosts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		isHub := strings.Contains(r.URL.Path, "/hubs/")
		switch {
		case r.Method == http.MethodPost && !isHub:
			defPosts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"def_new","type":"contact_attributes","attributes":{"slug":"company"}}}`))
		case r.Method == http.MethodPost && isHub:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"cfg_1","type":"contact_attribute_hub_configs","attributes":{}}}`))
		case r.URL.Query().Get("page[after]") == "cursor2":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"def_c","type":"contact_attributes","attributes":{"slug":"company"}}],"meta":{"page":{"has_more":false}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"def_o","type":"contact_attributes","attributes":{"slug":"other"}}],"meta":{"page":{"has_more":true,"next_cursor":"cursor2"}}}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID:         "community",
		Onboarding: []catalog.TemplateAttrDef{{Name: "Company", Slug: "company", FieldType: "text", InOnboarding: true}},
	}
	if err := stepOnboarding(sc, tmpl); err != nil {
		t.Fatalf("stepOnboarding: %v", err)
	}
	if defPosts != 0 {
		t.Errorf("def POSTed %d time(s); want 0 — 'company' exists on page 2 and an exhaustive lookup must find it", defPosts)
	}
	if sc.defIDsBySlug["company"] != "def_c" {
		t.Errorf("defIDsBySlug[company] = %q, want def_c (found on page 2)", sc.defIDsBySlug["company"])
	}
}

// ─── Task 16: stepPolicies ────────────────────────────────────────────────────

// TestStepPolicies_MapsKeysAndFieldsToPatches: the template's policies map is
// applied one PATCH per policy — friendly keys resolve to the backend
// policy_type enum, content maps to the body, and required maps to
// require_acceptance. Keys are sorted for deterministic ordering.
func TestStepPolicies_MapsKeysAndFieldsToPatches(t *testing.T) {
	var patchBodies [][]byte
	var patchPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPatch {
			b, _ := io.ReadAll(r.Body)
			patchBodies = append(patchBodies, b)
			patchPaths = append(patchPaths, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"pol_1","type":"hub_policies","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	// "privacy" sorts before "terms"; both are friendly aliases for the enum types.
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Policies: map[string]any{
			"terms":   map[string]any{"content": "TOS body", "required": true},
			"privacy": map[string]any{"content": "Privacy body"},
		},
	}
	if err := stepPolicies(sc, tmpl); err != nil {
		t.Fatalf("stepPolicies: %v", err)
	}
	if len(patchBodies) != 2 {
		t.Fatalf("want 2 PATCHes (one per policy), got %d", len(patchBodies))
	}
	for _, p := range patchPaths {
		if !strings.HasSuffix(p, "/hubs/hub_1/policies") {
			t.Errorf("policy PATCH path = %q, want .../hubs/hub_1/policies", p)
		}
	}
	// Sorted keys → privacy first, terms second.
	first := decodeHubAttrs(t, patchBodies[0])
	if first["policy_type"] != "privacy_policy" || first["content"] != "Privacy body" {
		t.Errorf("first PATCH = %v, want policy_type=privacy_policy content='Privacy body'", first)
	}
	second := decodeHubAttrs(t, patchBodies[1])
	if second["policy_type"] != "tos" || second["content"] != "TOS body" || second["require_acceptance"] != true {
		t.Errorf("second PATCH = %v, want policy_type=tos content='TOS body' require_acceptance=true", second)
	}
}

// TestStepPolicies_EmptyTemplateNoRequest: a template with no policies fires no
// request.
func TestStepPolicies_EmptyTemplateNoRequest(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			fired = true
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"x","type":"hub_policies","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepPolicies(sc, &catalog.HubTemplate{ID: "community"}); err != nil {
		t.Fatalf("stepPolicies empty: %v", err)
	}
	if fired {
		t.Error("empty policies template must fire NO request")
	}
}

// TestStepPolicies_UnknownKeyErrorsNoRequest: an unresolvable policy key ERRORS
// (ExitUsage) and fires no request — fail loud, never a silent drop.
func TestStepPolicies_UnknownKeyErrorsNoRequest(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			fired = true
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"x","type":"hub_policies","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{ID: "community", Policies: map[string]any{"bogus": map[string]any{}}}
	err := stepPolicies(sc, tmpl)
	if err == nil {
		t.Fatal("unknown policy key must ERROR")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if fired {
		t.Error("no PATCH must fire when a policy key is invalid")
	}
}

// ─── MIO-2567: the policy ENFORCEMENT gate ───────────────────────────────────
//
// The reported bug: a scaffolded + published community hub wrote a full ToS and
// Privacy Policy, `hub.policies_enabled` read true off the FE-facing derivation
// — and a freshly registered member still got `tos_acceptance_required:false`
// with `POST .../tos/accept` answering the enumeration-safe 404, because
// settings.policies.enabled was never written. The pipeline PATCHed
// .../policies and nothing ever PATCHed .../policies/gate, while the template's
// per-policy `enabled:true` was dropped on the floor by templateHubPolicy.

// policyStepServer records every PATCH (path + body) a policies step fires and
// answers each with a generic resource, so a test can assert the ORDER and
// SHAPE of the content writes and the gate write together.
func policyStepServer(t *testing.T) (*client.Client, *[]string, *[][]byte) {
	t.Helper()
	var paths []string
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method != http.MethodGet {
			b, _ := io.ReadAll(r.Body)
			paths = append(paths, r.URL.Path)
			bodies = append(bodies, b)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"pol_1","type":"hub_policies","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "k"), &paths, &bodies
}

// TestStepPolicies_GateFiresLastWhenTemplateDeclaresEnabled: the shipped
// community shape — terms {required, enabled}, privacy_policy {enabled} — writes
// both policy documents and THEN flips the hub-level gate exactly once, with
// enabled:true, to .../policies/gate. Gate LAST: enabling before the content
// lands would briefly demand acceptance of the default document the template is
// about to replace.
func TestStepPolicies_GateFiresLastWhenTemplateDeclaresEnabled(t *testing.T) {
	cl, paths, bodies := policyStepServer(t)
	sc := newStepSC(cl, "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Policies: map[string]any{
			"terms":          map[string]any{"required": true, "enabled": true},
			"privacy_policy": map[string]any{"enabled": true},
		},
	}
	if err := stepPolicies(sc, tmpl); err != nil {
		t.Fatalf("stepPolicies: %v", err)
	}
	if len(*paths) != 3 {
		t.Fatalf("want 3 PATCHes (2 policies + 1 gate), got %d: %v", len(*paths), *paths)
	}
	for _, p := range (*paths)[:2] {
		if !strings.HasSuffix(p, "/hubs/hub_1/policies") {
			t.Errorf("content PATCH path = %q, want .../hubs/hub_1/policies", p)
		}
	}
	if got := (*paths)[2]; !strings.HasSuffix(got, "/hubs/hub_1/policies/gate") {
		t.Errorf("gate PATCH path = %q, want .../hubs/hub_1/policies/gate (fired LAST)", got)
	}
	gate := decodeHubAttrs(t, (*bodies)[2])
	if gate["enabled"] != true {
		t.Errorf("gate PATCH body = %v, want enabled:true", gate)
	}
	// The per-policy `enabled` is NOT part of the policies PATCH body: the backend
	// stores no per-policy enabled (update_policy writes content + version only).
	for i, b := range (*bodies)[:2] {
		if _, ok := decodeHubAttrs(t, b)["enabled"]; ok {
			t.Errorf("content PATCH %d carries an `enabled` attribute; the gate is the only place it belongs: %s", i, b)
		}
	}
	if sc.policyGate == nil || !*sc.policyGate {
		t.Errorf("sc.policyGate = %v, want a non-nil true (the machine result reports it)", sc.policyGate)
	}
}

// TestStepPolicies_NoGateWhenNothingDeclaresEnabled: a template that states no
// enforcement intent writes its policy documents and leaves the hub's gate
// ALONE — no .../policies/gate request at all. Writing a false here would
// silently disable enforcement an operator had turned on by hand.
func TestStepPolicies_NoGateWhenNothingDeclaresEnabled(t *testing.T) {
	cl, paths, _ := policyStepServer(t)
	sc := newStepSC(cl, "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Policies: map[string]any{
			"terms":   map[string]any{"content": "TOS body", "required": true},
			"privacy": map[string]any{"content": "Privacy body"},
		},
	}
	if err := stepPolicies(sc, tmpl); err != nil {
		t.Fatalf("stepPolicies: %v", err)
	}
	for _, p := range *paths {
		if strings.HasSuffix(p, "/policies/gate") {
			t.Errorf("no policy declared `enabled` — the gate must NOT be written; got %v", *paths)
		}
	}
	if len(*paths) != 2 {
		t.Errorf("want exactly the 2 content PATCHes, got %d: %v", len(*paths), *paths)
	}
	if sc.policyGate != nil {
		t.Errorf("sc.policyGate = %v, want nil (no declaration ⇒ gate not managed)", *sc.policyGate)
	}
}

// TestStepPolicies_UnanimousFalseSkipsTheGateLoudly: the gate write is
// ENABLE-ONLY. The ratified applier contract (mio-page-catalog
// catalog.schema.json) requires PATCH .../policies/gate "when enabled is true"
// and says nothing about false, and acting on a false anyway would make every
// resume of such a template DISABLE enforcement an operator turned on by hand —
// the mirror of the reason an undeclared gate is left alone. Skipping is not
// silent: it is narrated, and it names the verb that actually disables one.
func TestStepPolicies_UnanimousFalseSkipsTheGateLoudly(t *testing.T) {
	cl, paths, _ := policyStepServer(t)
	var notes bytes.Buffer
	sc := newStepSC(cl, "hub_1", "acme")
	sc.noteW = &notes
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Policies: map[string]any{
			"terms":          map[string]any{"enabled": false},
			"privacy_policy": map[string]any{"enabled": false},
		},
	}
	if err := stepPolicies(sc, tmpl); err != nil {
		t.Fatalf("stepPolicies: %v", err)
	}
	for _, p := range *paths {
		if strings.HasSuffix(p, "/policies/gate") {
			t.Errorf("a declared enabled:false must NOT write the gate; got %v", *paths)
		}
	}
	if sc.policyGate != nil {
		t.Errorf("sc.policyGate = %v, want nil — the result reports what was WRITTEN", *sc.policyGate)
	}
	for _, want := range []string{"enforcement gate NOT written", `"enabled": false`, "hubs policies gate"} {
		if !strings.Contains(notes.String(), want) {
			t.Errorf("skip note must contain %q; notes=%q", want, notes.String())
		}
	}
}

// TestStepPolicies_NoDeclarationSaysSo: the other skip case is narrated too, and
// says something DIFFERENT — "no policy declares enabled" and "the template
// declares false" are genuinely different situations, and conflating them into
// one silence is how this bug hid for a release.
func TestStepPolicies_NoDeclarationSaysSo(t *testing.T) {
	cl, _, _ := policyStepServer(t)
	var notes bytes.Buffer
	sc := newStepSC(cl, "hub_1", "acme")
	sc.noteW = &notes
	tmpl := &catalog.HubTemplate{
		ID:       "community",
		Policies: map[string]any{"terms": map[string]any{"content": "TOS body"}},
	}
	if err := stepPolicies(sc, tmpl); err != nil {
		t.Fatalf("stepPolicies: %v", err)
	}
	if !strings.Contains(notes.String(), `no policy declares "enabled"`) {
		t.Errorf("the no-declaration case must say so on stderr; notes=%q", notes.String())
	}

	// …and the PLAN says it too, the way stepPublish's skip does — a --dry-run
	// that is silent about enforcement is the same ambiguity one surface over.
	var plan []planEntry
	dry := newStepSC(cl, "hub_1", "acme")
	dry.dryRun, dry.plan = true, &plan
	if err := stepPolicies(dry, tmpl); err != nil {
		t.Fatalf("stepPolicies dry-run: %v", err)
	}
	var joined []string
	for _, e := range plan {
		joined = append(joined, e.step+" — "+e.detail)
	}
	if len(plan) != 2 || !strings.Contains(plan[1].detail, "enforcement gate not written") {
		t.Errorf("dry-run plan must record the gate skip; plan=%v", joined)
	}
}

// TestStepPolicies_ConflictingEnabledErrorsBeforeAnyWrite: policy enforcement is
// ONE hub-level flag (settings.policies.enabled), so a template asking for
// per-policy enforcement is describing a granularity the backend has never had.
// The collapse is lossy and no winner is inferable, so it is a pre-write
// ExitUsage naming both keys — never a silent OR, never a silent drop.
func TestStepPolicies_ConflictingEnabledErrorsBeforeAnyWrite(t *testing.T) {
	cl, paths, _ := policyStepServer(t)
	sc := newStepSC(cl, "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Policies: map[string]any{
			"terms":          map[string]any{"enabled": true},
			"privacy_policy": map[string]any{"enabled": false},
		},
	}
	err := stepPolicies(sc, tmpl)
	if err == nil {
		t.Fatal("conflicting per-policy `enabled` must ERROR")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	for _, want := range []string{"privacy_policy", "terms", "single hub-level gate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err, want)
		}
	}
	if len(*paths) != 0 {
		t.Errorf("a conflicting template must fire NO write at all, got %v", *paths)
	}
}

// TestStepPolicies_NonBooleanEnabledErrors: the backend gate is identity-checked
// (`_policies_enabled` requires the JSON boolean true), so a stringly-typed
// "true" would enforce nothing. It must fail loud rather than read as "no
// declaration" and reproduce the original bug.
func TestStepPolicies_NonBooleanEnabledErrors(t *testing.T) {
	cl, paths, _ := policyStepServer(t)
	sc := newStepSC(cl, "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID:       "community",
		Policies: map[string]any{"terms": map[string]any{"enabled": "true"}},
	}
	err := stepPolicies(sc, tmpl)
	if err == nil {
		t.Fatal(`policies.terms.enabled = "true" (string) must ERROR`)
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if len(*paths) != 0 {
		t.Errorf("must fire no write, got %v", *paths)
	}
}

// TestTemplateHubPolicy_UnknownFieldIsLoud closes the asymmetry MIO-2567 named:
// an unknown policy KEY errored ("never a silent drop") while an unknown FIELD
// inside a policy value was dropped in silence — the same file, opposite rules.
// A wrong-typed field is the same class: a non-string content would leave
// Content nil, and applyHubPolicies sends nil as JSON null, silently RESETTING
// the policy to the backend default.
func TestTemplateHubPolicy_UnknownFieldIsLoud(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want string
	}{
		{"unknown field", map[string]any{"require_acceptence": true}, "unknown field"},
		{"non-object value", "yes", "must be an object"},
		{"non-string content", map[string]any{"content": 42}, `"content" must be a string`},
		{"non-boolean required", map[string]any{"required": "yes"}, `must be a JSON boolean`},
		{"alias contradiction", map[string]any{"require_acceptance": true, "required": false}, "contradicts its alias"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := templateHubPolicy("terms", tc.val)
			if err == nil {
				t.Fatalf("templateHubPolicy(%v) must ERROR", tc.val)
			}
			if errs.CodeOf(err) != errs.ExitUsage {
				t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q must contain %q", err, tc.want)
			}
		})
	}
}

// TestScaffold_PolicyGateResultMatchesWhatWasWritten pins the `policy_gate`
// contract against the REAL pipeline, one full command run per template shape.
//
// It replaces a version that hand-built a scaffoldContext and asserted three
// states including `false` — a value the enable-only pipeline cannot produce, so
// the test asserted a contract that did not exist. Pinning an unreachable state
// is the same class of mistake as a guard whose oracle is the thing it
// validates: it passes forever and proves nothing about the command. The oracle
// here is what the command actually prints, and the assertion pairs it with
// whether the gate PATCH was really sent.
func TestScaffold_PolicyGateResultMatchesWhatWasWritten(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policies  any
		want      any
		wantWrite bool
	}{
		{"declares enabled:true", map[string]any{
			"terms": map[string]any{"required": true, "enabled": true}}, true, true},
		{"declares enabled:false", map[string]any{
			"terms": map[string]any{"required": true, "enabled": false}}, nil, false},
		{"declares no enabled", map[string]any{
			"terms": map[string]any{"required": true}}, nil, false},
		{"no policies block at all", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := fullScaffoldServerWithCatalog(t, catalogWithPolicies(t, tc.policies))

			res := runContract(t, scaffoldEnv(t, srv.URL),
				withTeam("t_team1", "hubs", "scaffold",
					"--template", "community", "--name", "X", "--slug", "x")...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
			}

			got := decodeSoleJSON(t, res.Stdout)
			v, ok := got["policy_gate"]
			if !ok {
				t.Fatal("policy_gate must be emitted unconditionally, like every other result key")
			}
			if v != tc.want {
				t.Errorf("policy_gate = %#v, want %#v", v, tc.want)
			}
			// The field means "what this run WROTE", so it must agree with the wire.
			if wrote := len(rec.policyGateBodies) > 0; wrote != tc.wantWrite {
				t.Errorf("gate PATCH sent = %t, want %t (policy_gate reports the WRITE, not the declaration); bodies=%d",
					wrote, tc.wantWrite, len(rec.policyGateBodies))
			}
		})
	}
}

// policyFieldProbes gives each catalog-accepted policy field a value that, when
// it is the ONLY thing a policy declares, must change what the scaffold sends.
// Adding a field to catalog.HubPolicyFieldKeys without adding a probe here fails
// the guard below by name.
var policyFieldProbes = map[string]any{
	"content":            "PROBE CONTENT",
	"require_acceptance": true,
	"required":           true,
	"enabled":            true,
}

// recordPolicyStepRequests drives the REAL stepPolicies against a recording
// server with a single `terms` policy carrying val, and returns the requests it
// produced as comparable strings.
func recordPolicyStepRequests(t *testing.T, val map[string]any) []string {
	t.Helper()
	cl, paths, bodies := policyStepServer(t)
	sc := newStepSC(cl, "hub_1", "acme")
	sc.noteW = io.Discard
	if err := stepPolicies(sc, &catalog.HubTemplate{
		ID: "community", Policies: map[string]any{"terms": val},
	}); err != nil {
		t.Fatalf("stepPolicies(%v): %v", val, err)
	}
	out := make([]string, 0, len(*paths))
	for i, p := range *paths {
		out = append(out, p+" "+string((*bodies)[i]))
	}
	return out
}

// TestTemplatePolicyFields_EachOneChangesTheRequests is the guard that would
// have caught MIO-2567 at build time — and it asserts BEHAVIOUR, not
// membership.
//
// The obvious version of this test compares the catalog's allow-list against a
// second hand-maintained "consumed" list. That version is worthless: the
// cheapest way to make it go green when it fails is to add the new key to both
// lists, which leaves the field accepted at preflight and dropped at apply —
// the exact bug. `enabled` shipped that way for a release.
//
// So the oracle here is the wire: for every field the PREFLIGHT accepts, declare
// it alone on a policy, run the real step, and require the requests to differ
// from a policy that declares nothing. A field nothing acts on cannot pass, and
// no edit to any list can make it pass.
func TestTemplatePolicyFields_EachOneChangesTheRequests(t *testing.T) {
	baseline := recordPolicyStepRequests(t, map[string]any{})

	for _, f := range catalog.HubPolicyFieldKeys() {
		probe, ok := policyFieldProbes[f]
		if !ok {
			t.Errorf("catalog accepts policy field %q at preflight but this guard has no probe for it: add one to policyFieldProbes AND make the step act on the field, or the template's value is silently dropped (MIO-2567)", f)
			continue
		}
		t.Run(f, func(t *testing.T) {
			got := recordPolicyStepRequests(t, map[string]any{f: probe})
			if reflect.DeepEqual(got, baseline) {
				t.Errorf("declaring %q=%v changes NOTHING the scaffold sends — it is accepted at preflight and dropped at apply (MIO-2567).\nbaseline: %v\n   probe: %v", f, probe, baseline, got)
			}
		})
	}
}

// ─── Task 17: stepPlaylists (O1 = option c) ───────────────────────────────────

// TestStepPlaylists_EmptyHubCreatesItemsAndPublishes: on a hub with NO published
// playlists, the step creates each team playlist, adds an item per file id, and
// publishes it to the hub with visibility public + published_at set + playlist_id
// in the BODY to the COLLECTION path.
func TestStepPlaylists_EmptyHubCreatesItemsAndPublishes(t *testing.T) {
	var createBody, publishBody []byte
	var publishPath string
	var itemFileIDs []string
	var creates, items, publishes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/items"): // item add
			items++
			b, _ := io.ReadAll(r.Body)
			fid, _ := decodeHubAttrs(t, b)["file_id"].(string)
			itemFileIDs = append(itemFileIDs, fid)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"it_1","type":"playlist_items","attributes":{}}}`))
		case strings.Contains(path, "/hubs/") && r.Method == http.MethodGet: // O1 gate — empty
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.Contains(path, "/hubs/") && r.Method == http.MethodPost: // publish to hub
			publishes++
			publishPath = path
			publishBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"hm_1","type":"hub_media","attributes":{}}}`))
		case r.Method == http.MethodPost: // team playlist create
			creates++
			createBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"pl_made","type":"playlists","attributes":{}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Playlists: []catalog.TemplatePlaylist{
			{Title: "Welcome", Key: "welcome", Visibility: "public", FileIDs: []string{"file_a", "file_b"}},
		},
	}
	if err := stepPlaylists(sc, tmpl); err != nil {
		t.Fatalf("stepPlaylists: %v", err)
	}
	if creates != 1 || items != 2 || publishes != 1 {
		t.Fatalf("want 1 create + 2 items + 1 publish; got %d create, %d items, %d publish", creates, items, publishes)
	}
	ca := decodeHubAttrs(t, createBody)
	if ca["title"] != "Welcome" || ca["visibility"] != "public" {
		t.Errorf("playlist create attrs = %v, want title=Welcome visibility=public", ca)
	}
	if len(itemFileIDs) != 2 || itemFileIDs[0] != "file_a" || itemFileIDs[1] != "file_b" {
		t.Errorf("item file_ids = %v, want [file_a file_b] (in order)", itemFileIDs)
	}
	if !strings.HasSuffix(publishPath, "/hubs/hub_1/playlists") {
		t.Errorf("publish path = %q, want COLLECTION .../hubs/hub_1/playlists (id in body, not path)", publishPath)
	}
	pa := decodeHubAttrs(t, publishBody)
	if pa["playlist_id"] != "pl_made" {
		t.Errorf("publish playlist_id = %v, want pl_made (in the body)", pa["playlist_id"])
	}
	if pa["visibility"] != "public" {
		t.Errorf("publish visibility = %v, want public", pa["visibility"])
	}
	if at, _ := pa["published_at"].(string); at == "" {
		t.Errorf("publish published_at must be set (non-empty, sidesteps MIO-2536); attrs=%v", pa)
	}
	if sc.playlistIDsByKey["welcome"] != "pl_made" {
		t.Errorf("playlistIDsByKey[welcome] = %q, want pl_made", sc.playlistIDsByKey["welcome"])
	}
}

// TestStepPlaylists_NonEmptyHubSkipsWholeStep: the O1 gate — if the hub already
// has ≥1 published playlist, the ENTIRE step is skipped (create-only), so no
// playlist/item/publish request fires and nothing is recorded.
func TestStepPlaylists_NonEmptyHubSkipsWholeStep(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPost {
			posts++
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/hubs/") {
			// O1 gate: hub already has a published playlist.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"hm_existing","type":"hub_media","attributes":{}}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Playlists: []catalog.TemplatePlaylist{
			{Title: "Welcome", Key: "welcome", FileIDs: []string{"file_a"}},
		},
	}
	if err := stepPlaylists(sc, tmpl); err != nil {
		t.Fatalf("stepPlaylists: %v", err)
	}
	if posts != 0 {
		t.Errorf("non-empty hub must skip the WHOLE step — want 0 POSTs, got %d", posts)
	}
	if len(sc.playlistIDsByKey) != 0 {
		t.Errorf("no playlist should be recorded when the step is skipped; map=%v", sc.playlistIDsByKey)
	}
}

// ─── MIO-2672 Task 7: stepPages (general pages[] apply, replaces stepHomepage) ─

// newDryRunStepSC builds a dry-run scaffoldContext (fn is NOT executed; steps
// only record their plan detail) for asserting a step's skip-with-note without
// firing HTTP. The client is present only so a step that would otherwise dial
// has a target; dry-run guarantees it is never used.
func newDryRunStepSC(cl *client.Client) (*scaffoldContext, *[]planEntry) {
	plan := []planEntry{}
	sc := newStepSC(cl, "hub_1", "acme")
	sc.dryRun = true
	sc.plan = &plan
	return sc, &plan
}

// TestStepPages_AppliesAllPagesWithProvenance: stepPages applies EVERY plan
// page IN PLAN ORDER, each as: exhaustive slug check → create carrying the
// §5.1 provenance marker ("pending", and NOTHING else in meta) → tree PUT
// (If-Match 0, FINAL interpolation, no residual tokens) → publish (If-Match =
// that page's PUT-returned draft_version — dv varies per page to prove
// per-page threading) → marker PATCH ("applied" + that page's draft_version +
// the digest of the EXACT tree body PUT, recomputed here from the captured
// wire bytes). The homepage entry additionally runs the §5.1 foreign-homepage
// pre-check (one extra page-list walk) and lands its id + draft version in
// the context (summary + W0 guard compatibility).
func TestStepPages_AppliesAllPagesWithProvenance(t *testing.T) {
	type pagesReq struct {
		method, path, ifMatch string
		body                  []byte
	}
	var mu sync.Mutex
	var reqs []pagesReq
	putCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The W2b op probe (Task 9) is answered 404 BEFORE recording: this test
		// pins the CLIENT-SIDE mutation sequence, and the probe is not part of it
		// (the op-path wire contract lives in hubs_scaffold_op_test.go).
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scaffold-from-template") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, pagesReq{r.Method, r.URL.Path, r.Header.Get("If-Match"), body})
		putNo := putCount
		if r.Method == http.MethodPut {
			putCount++
			putNo = putCount
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pages"): // existingPageBySlug — none
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pages"): // create — id minted from the slug
			slug, _ := decodeHubAttrs(t, body)["slug"].(string)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"pg_%s","type":"pages","attributes":{"slug":%q}}}`, slug, slug)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/tree"): // dv varies PER PAGE: 1, 2, 3
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"pdt_%d","type":"page_draft_trees","attributes":{"draft_version":%d}}}`, putNo, putNo)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publish"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pp_1","type":"page-publishes","attributes":{}}}`))
		case r.Method == http.MethodPatch: // provenance marker PATCH
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pg_x","type":"pages","attributes":{}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	cat, ht, plan := scaffoldFixture(t)
	sc.cat, sc.hubTmpl, sc.pagePlan, sc.hubName = cat, ht, plan, "Acme Community"
	if err := stepPages(sc, &sc.hubTmpl); err != nil {
		t.Fatalf("stepPages: %v", err)
	}

	if len(plan.pages) != 3 {
		t.Fatalf("fixture community plan has %d page(s), want 3 (homepage/about/faq)", len(plan.pages))
	}
	// Split the wire log: page-list GETs (the recovery slug walks + the
	// homepage entry's §5.1 foreign-homepage pre-check) vs the MUTATION
	// sequence, whose per-page order create→PUT→publish→PATCH is the contract.
	var lists, muts []pagesReq
	for _, rq := range reqs {
		if rq.method == http.MethodGet && strings.HasSuffix(rq.path, "/hubs/hub_1/pages") {
			lists = append(lists, rq)
			continue
		}
		muts = append(muts, rq)
	}
	if len(lists) != len(plan.pages)+1 {
		t.Errorf("got %d page-list GETs, want %d (one recovery slug walk per page + the homepage pre-check)",
			len(lists), len(plan.pages)+1)
	}
	if len(muts) != 4*len(plan.pages) {
		t.Fatalf("got %d mutating requests, want %d (create+PUT+publish+PATCH per page, in plan order)",
			len(muts), 4*len(plan.pages))
	}
	wantApp := catalog.ApplicationID("hub_1", "community")
	// Literal title spot-checks (beyond the ref-derived compare below): the
	// homepage entry is titled "Home", the about entry exactly "About".
	wantTitles := map[string]string{"homepage": "Home", "about": "About"}
	for i := range plan.pages {
		pp := plan.pages[i]
		pageID := "pg_" + pp.ref.Slug
		create, put, publish, patch := muts[i*4], muts[i*4+1], muts[i*4+2], muts[i*4+3]
		wantDV := i + 1

		// (1) Create: identity from the ref + interpolated title + ONLY the marker in meta.
		if create.method != http.MethodPost || !strings.HasSuffix(create.path, "/hubs/hub_1/pages") {
			t.Fatalf("page %q req 2 = %s %s, want POST .../hubs/hub_1/pages", pp.ref.Slug, create.method, create.path)
		}
		attrs := decodeHubAttrs(t, create.body)
		if attrs["title"] != pp.ref.Title {
			t.Errorf("page %q title = %v, want %q (interpolated ref title)", pp.ref.Slug, attrs["title"], pp.ref.Title)
		}
		if title, _ := attrs["title"].(string); strings.Contains(title, "{{") {
			t.Errorf("page %q created title %q carries a residual token — titles must be interpolated at create", pp.ref.Slug, title)
		}
		if want, pinned := wantTitles[pp.ref.Slug]; pinned && attrs["title"] != want {
			t.Errorf("page %q title = %v, want exactly %q", pp.ref.Slug, attrs["title"], want)
		}
		if attrs["slug"] != pp.ref.Slug || attrs["privacy"] != pp.ref.Privacy {
			t.Errorf("page %q create attrs = %v, want slug=%q privacy=%q", pp.ref.Slug, attrs, pp.ref.Slug, pp.ref.Privacy)
		}
		if isHome, present := attrs["is_homepage"]; pp.ref.IsHomepage {
			if isHome != true {
				t.Errorf("homepage create must send is_homepage:true, got %v", isHome)
			}
		} else if present {
			t.Errorf("page %q must NOT send is_homepage (only the homepage entry does), got %v", pp.ref.Slug, isHome)
		}
		meta, _ := attrs["meta"].(map[string]any)
		if len(meta) != 1 {
			t.Fatalf("page %q create meta must carry ONLY template_provenance, got %v", pp.ref.Slug, meta)
		}
		tp, _ := meta["template_provenance"].(map[string]any)
		if len(tp) != 5 {
			t.Errorf("page %q pending marker must carry exactly its 5 §5.1 keys, got %v", pp.ref.Slug, tp)
		}
		if tp["applicationId"] != wantApp || tp["hubTemplateId"] != "community" ||
			tp["pageTemplateId"] != pp.ref.PageTemplate || tp["provenanceState"] != "pending" {
			t.Errorf("page %q pending marker = %v, want applicationId=%s hubTemplateId=community pageTemplateId=%s provenanceState=pending",
				pp.ref.Slug, tp, wantApp, pp.ref.PageTemplate)
		}
		if rev, ok := attrInt(tp["catalogRevision"]); !ok || rev != 7 {
			t.Errorf("page %q marker catalogRevision = %v, want 7", pp.ref.Slug, tp["catalogRevision"])
		}

		// (2) Tree PUT: fresh-create OCC sentinel + FINAL interpolation (no residual tokens).
		if put.method != http.MethodPut || !strings.HasSuffix(put.path, "/pages/"+pageID+"/tree") {
			t.Fatalf("page %q req 3 = %s %s, want PUT .../pages/%s/tree", pp.ref.Slug, put.method, put.path, pageID)
		}
		if put.ifMatch != "0" {
			t.Errorf("page %q tree PUT If-Match = %q, want 0 (fresh create)", pp.ref.Slug, put.ifMatch)
		}
		if strings.Contains(string(put.body), "{{") {
			t.Errorf("page %q PUT body carries a residual token: %s", pp.ref.Slug, put.body)
		}
		if pp.ref.IsHomepage && !strings.Contains(string(put.body), "Welcome to Acme Community") {
			t.Errorf("homepage PUT body must carry the interpolated hero headline (\"Welcome to Acme Community\"); body=%s", put.body)
		}

		// (3) Publish: If-Match threads THIS page's PUT-returned draft_version.
		if publish.method != http.MethodPost || !strings.HasSuffix(publish.path, "/pages/"+pageID+"/publish") {
			t.Fatalf("page %q req 4 = %s %s, want POST .../pages/%s/publish", pp.ref.Slug, publish.method, publish.path, pageID)
		}
		if publish.ifMatch != strconv.Itoa(wantDV) {
			t.Errorf("page %q publish If-Match = %q, want %d (per-page draft_version, not a shared counter)",
				pp.ref.Slug, publish.ifMatch, wantDV)
		}

		// (4) Marker PATCH: applied + this page's dv + the digest of the EXACT tree body PUT.
		if patch.method != http.MethodPatch || !strings.HasSuffix(patch.path, "/pages/"+pageID) {
			t.Fatalf("page %q req 5 = %s %s, want PATCH .../pages/%s", pp.ref.Slug, patch.method, patch.path, pageID)
		}
		pmeta, _ := decodeHubAttrs(t, patch.body)["meta"].(map[string]any)
		ptp, _ := pmeta["template_provenance"].(map[string]any)
		if ptp["provenanceState"] != "applied" {
			t.Errorf("page %q PATCH provenanceState = %v, want applied", pp.ref.Slug, ptp["provenanceState"])
		}
		if dv, ok := attrInt(ptp["appliedDraftVersion"]); !ok || dv != wantDV {
			t.Errorf("page %q appliedDraftVersion = %v, want %d", pp.ref.Slug, ptp["appliedDraftVersion"], wantDV)
		}
		// Recompute the digest from the CAPTURED wire bytes (UseNumber decode →
		// TreeDigest) — pins byte-level digest fidelity against what was sent.
		var putDoc struct {
			Data struct {
				Attributes struct {
					Tree map[string]any `json:"tree"`
				} `json:"attributes"`
			} `json:"data"`
		}
		dec := json.NewDecoder(bytes.NewReader(put.body))
		dec.UseNumber()
		if err := dec.Decode(&putDoc); err != nil {
			t.Fatalf("page %q: decode captured PUT body: %v", pp.ref.Slug, err)
		}
		wantDigest, derr := catalog.TreeDigest(putDoc.Data.Attributes.Tree)
		if derr != nil {
			t.Fatalf("page %q: recompute TreeDigest: %v", pp.ref.Slug, derr)
		}
		if ptp["appliedTreeDigest"] != wantDigest {
			t.Errorf("page %q appliedTreeDigest = %v, want %s (digest of the exact PUT tree body)",
				pp.ref.Slug, ptp["appliedTreeDigest"], wantDigest)
		}

		if pp.ref.IsHomepage && (sc.homePageID != pageID || sc.homeDraftVersion != wantDV) {
			t.Errorf("context homepage = {%q %d}, want {%q %d} (set from the homepage entry)",
				sc.homePageID, sc.homeDraftVersion, pageID, wantDV)
		}
	}
}

// (TestStepPages_ExistingSlugConflicts — Task 7's fail-safe "any existing page
// at a manifest slug is a conflict" — was superseded by the §5.1 recovery
// dispatch (MIO-2672 Task 8): an unmarked page at a slug is now the
// foreign-page row of decideRecovery, pinned by
// TestStepPages_ForeignSlugConflict in hubs_scaffold_pages_test.go alongside
// the other per-boundary rows.)

// TestStepPages_InterpolationCapAtApply: the FINAL interpolation (post-create
// vars) enforces the §4.3 post-substitution caps — a leaf value pushed over
// 5000 code points by the substituted hub name is ExitUsage BEFORE any HTTP
// for that page (mutation-guard style). Hand-rolled plan: the real fixture
// cannot trigger this (a hub name is capped at 255 cp).
func TestStepPages_InterpolationCapAtApply(t *testing.T) {
	srv, fired := firedGuardServer(t)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	sc.hubName = strings.Repeat("n", 200)
	sc.pagePlan = &scaffoldPlan{pages: []plannedPage{{
		ref: catalog.PageRef{PageTemplate: "page-x", Slug: "long", Title: "Long"},
		// 4900 + 200 substituted = 5100 cp > the 5000-cp leaf cap.
		rawTree: map[string]any{"id": "n1", "kind": "text", "value": strings.Repeat("a", 4900) + "{{hub_name}}"},
	}}}

	err := stepPages(sc, &catalog.HubTemplate{ID: "community"})
	if err == nil {
		t.Fatal("stepPages must ERROR when final interpolation exceeds a post-substitution cap")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if *fired {
		t.Error("the interpolation cap must fail BEFORE any HTTP for the page")
	}
}

// TestPageDraftVersion_404IsFirstSetSentinel pins the recovery-snapshot
// tree-get helper (pageDraftVersion): a draft-less page's tree-get 404 is
// tolerated as draft_version 0 (the first-set sentinel) and a real
// draft_version is read off the author-draft query. The §5.1 recovery
// decision (decideRecovery, MIO-2672 Task 8) keys resumeFull-vs-conflict on
// exactly this value.
func TestPageDraftVersion_404IsFirstSetSentinel(t *testing.T) {
	var query string
	noDraft := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		query = r.URL.RawQuery
		if noDraft {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"no draft for this page"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":3}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	dv, err := sc.pageDraftVersion("page_x")
	if err != nil || dv != 0 {
		t.Errorf("404 tree-get: got (%d, %v), want (0, nil) — the first-set sentinel", dv, err)
	}
	if !strings.Contains(query, "audience=author") {
		t.Errorf("tree-get query = %q, want the author-draft query (audience=author)", query)
	}
	noDraft = false
	if dv, err = sc.pageDraftVersion("page_x"); err != nil || dv != 3 {
		t.Errorf("existing draft: got (%d, %v), want (3, nil)", dv, err)
	}
}

// (TestStepHomepage_CreatesPageThenSetsTreeWithIfMatch0 was superseded by
// TestStepPages_AppliesAllPagesWithProvenance — the same create → tree PUT →
// publish contract, now pinned per page with provenance markers. The
// resume/OCC coverage the deleted TestStepHomepage_Resume* tests held lives in
// the Task-8 §5.1 recovery tests (hubs_scaffold_pages_test.go):
// TestStepPages_ResumeAfterCreateBeforeDraft pins page reuse + the If-Match 0
// first-set sentinel, and the TestStepPages_Conflict*/Noop* boundary tests pin
// every other recovery row.)

// TestBuildScaffoldPlan_UnknownPageTemplateErrors: a pages[] ref whose
// pageTemplate is missing from the catalog is ExitUsage from buildScaffoldPlan
// itself — defense-in-depth under HubTemplate.Validate, and trivially before
// any HTTP: the plan is built by the WRITE-FREE preflight from pure inputs.
// (Supersedes TestStepHomepage_UnknownCatalogTemplateErrorsNoHTTP: the bad-ref
// check moved from the step into the preflight.)
func TestBuildScaffoldPlan_UnknownPageTemplateErrors(t *testing.T) {
	cat, ht, _ := scaffoldFixture(t)
	ht.Pages[0].PageTemplate = "no-such-page-template"
	_, err := buildScaffoldPlan(cat, ht)
	if err == nil {
		t.Fatal("buildScaffoldPlan must ERROR on a pages[] ref to a missing pageTemplate")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	// A ref to a SECTION template (not a whole-page root) is equally rejected.
	cat2, ht2, _ := scaffoldFixture(t)
	ht2.Pages[0].PageTemplate = "hero"
	_, err = buildScaffoldPlan(cat2, ht2)
	if err == nil {
		t.Fatal("buildScaffoldPlan must ERROR on a pages[] ref to a section template")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
}

// ─── Task 19: stepPublish ─────────────────────────────────────────────────────

// TestStepPublish_TruePatchesIsPrivateFalse: with publish intent set, the step
// PATCHes the hub to is_private:false (via publishedStateAttrs) — "published" is
// NOT a writable attribute, so it must never appear in the body.
func TestStepPublish_TruePatchesIsPrivateFalse(t *testing.T) {
	var method, path string
	var body []byte
	var patches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPatch {
			patches++
			method, path = r.Method, r.URL.Path
			body, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme","is_private":false}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	sc.isPrivate = true
	sc.publish = true
	if err := stepPublish(sc, &catalog.HubTemplate{ID: "community"}); err != nil {
		t.Fatalf("stepPublish: %v", err)
	}
	if patches != 1 {
		t.Fatalf("want exactly 1 PATCH, got %d", patches)
	}
	if method != http.MethodPatch || !strings.HasSuffix(path, "/hubs/hub_1") {
		t.Errorf("request = %s %s, want PATCH .../hubs/hub_1", method, path)
	}
	attrs := decodeHubAttrs(t, body)
	if attrs["is_private"] != false {
		t.Errorf("is_private = %v, want false (go public)", attrs["is_private"])
	}
	if _, present := attrs["published"]; present {
		t.Errorf("body must NOT carry `published` (not a writable attr); attrs=%v", attrs)
	}
	if sc.isPrivate {
		t.Errorf("context isPrivate = %v, want false after publish", sc.isPrivate)
	}
}

// TestStepPublish_FalseSkipsNoRequest: without publish intent the step fires NO
// request — the hub stays private.
func TestStepPublish_FalseSkipsNoRequest(t *testing.T) {
	srv, fired := firedGuardServer(t)
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	sc.publish = false
	if err := stepPublish(sc, &catalog.HubTemplate{ID: "community"}); err != nil {
		t.Fatalf("stepPublish skip: %v", err)
	}
	if *fired {
		t.Error("publish=false must fire NO request (hub stays private)")
	}
}

// TestStepPublish_FalseRecordsSkipNote: the dry-run plan detail for a private
// scaffold names the skip and points at --publish.
func TestStepPublish_FalseRecordsSkipNote(t *testing.T) {
	sc, plan := newDryRunStepSC(client.New("http://unused", "k"))
	sc.publish = false
	if err := stepPublish(sc, &catalog.HubTemplate{ID: "community"}); err != nil {
		t.Fatalf("stepPublish dry-run: %v", err)
	}
	if len(*plan) != 1 || (*plan)[0].step != "publish" {
		t.Fatalf("plan = %v, want one `publish` entry", *plan)
	}
	if !strings.Contains((*plan)[0].detail, "--publish") {
		t.Errorf("skip detail = %q, want a note pointing at --publish", (*plan)[0].detail)
	}
}

// ─── Task 20 / MIO-2558: stepWelcomePost ──────────────────────────────────────
//
// Was `stepBackendGated`, a skip-with-note for two backend tickets. Both shipped
// (MIO-2540 in 6565362d — server-side only, nothing for the CLI to do; MIO-2262
// in 0da17745 — the admin create-discussion endpoint), so the step now POSTs the
// template's welcome post and these tests pin that contract instead of the note.

// templateWithWelcomePost is the minimal hub template the welcome-post tests
// drive: one space, and a welcomePost referencing it by slug.
func templateWithWelcomePost() *catalog.HubTemplate {
	return &catalog.HubTemplate{
		ID:     "community",
		Spaces: []catalog.TemplateSpace{{Name: "General", Slug: "general"}},
		WelcomePost: &catalog.TemplateWelcomePost{
			Space: "general", Title: "Welcome!", Body: "Say hi.", Published: true,
		},
	}
}

// welcomePostStub is a STATEFUL stand-in for the discussions admin endpoints:
// it keeps the rows it was POSTed and serves them back on the list, so a test can
// run the step TWICE and assert the second run adopts what the first one wrote.
//
// It is server-FAITHFUL in the two ways this step depends on, both verified
// against mio-backend origin/main:
//
//   - a created discussion is stored with its title STRIPPED
//     (discussion_text.py::normalize_discussion_title returns title.strip()), so
//     a stub that echoed the request verbatim would hide a padded-title bug —
//     which is exactly how one shipped in the first revision of this step;
//   - the list serializes space_id and deleted_at but NOT is_removed
//     (routers/discussions_admin.py::_discussion_to_resource), so client-side
//     space matching is exercised and removal is correctly unobservable.
type welcomePostStub struct {
	mu         sync.Mutex
	rows       []map[string]any // JSON:API resources, newest first (id DESC, like list_for_hub)
	posts      int
	postBodies [][]byte
	listQuery  string
	nextDiscID int
}

// lastPostBody returns the most recent create body, for wire assertions.
func (s *welcomePostStub) lastPostBody(t *testing.T) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.postBodies) == 0 {
		t.Fatal("no discussion POST was captured")
	}
	return s.postBodies[len(s.postBodies)-1]
}

// serveDiscussions handles the two discussions routes; reports whether it did.
func (s *welcomePostStub) serveDiscussions(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasSuffix(r.URL.Path, "/discussions") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/vnd.api+json")
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		_, attrs, _ := decodeDataTypeAttrsRaw(body) // nil-map reads are safe; the test asserts on the body

		s.posts++
		s.postBodies = append(s.postBodies, body)
		s.nextDiscID++
		id := fmt.Sprintf("disc_%d", s.nextDiscID)
		title, _ := attrs["title"].(string)
		spaceID, _ := attrs["space_id"].(string)
		row := map[string]any{
			"id": id, "type": "discussions",
			"attributes": map[string]any{
				// The server stores the STRIPPED title — see the type comment.
				"title": strings.TrimSpace(title), "space_id": spaceID, "deleted_at": nil,
			},
		}
		s.rows = append([]map[string]any{row}, s.rows...)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": row})
		return true
	}
	s.listQuery = r.URL.RawQuery
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": s.rows})
	return true
}

// newWelcomePostServer wires welcomePostStub behind the spaces list the step
// resolves its space slug through.
func newWelcomePostServer(t *testing.T, spacesBody string) (*httptest.Server, *welcomePostStub) {
	t.Helper()
	stub := &welcomePostStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stub.serveDiscussions(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(spacesBody))
	}))
	t.Cleanup(srv.Close)
	return srv, stub
}

// oneSpaceBody is the spaces listing the welcome-post tests resolve against.
const oneSpaceBody = `{"data":[{"id":"sp_gen","type":"spaces","attributes":{"slug":"general"}}]}`

// TestStepWelcomePost_NoTemplateDeclaration_FiresNoRequest: a template with no
// welcomePost — the shape of EVERY catalog shipped so far, `community` at 0.14.1
// included — must converge to a no-op. The CLI holds no templates, so it must
// never invent welcome-post copy.
func TestStepWelcomePost_NoTemplateDeclaration_FiresNoRequest(t *testing.T) {
	srv, fired := firedGuardServer(t)
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepWelcomePost(sc, &catalog.HubTemplate{ID: "community"}); err != nil {
		t.Fatalf("stepWelcomePost: %v", err)
	}
	if *fired {
		t.Error("a template with no welcomePost must fire NO request")
	}
	if sc.welcomePostID != "" {
		t.Errorf("welcomePostID = %q, want empty", sc.welcomePostID)
	}
}

// TestStepWelcomePost_CreatesOnceInResolvedSpace: with a welcomePost declared,
// the step resolves the space SLUG to the hub's real space id, pre-checks the
// discussions list for the title, and POSTs the create envelope — carrying the
// endpoint's four attributes and NO author field.
func TestStepWelcomePost_CreatesOnceInResolvedSpace(t *testing.T) {
	srv, stub := newWelcomePostServer(t, oneSpaceBody)
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")

	if err := stepWelcomePost(sc, templateWithWelcomePost()); err != nil {
		t.Fatalf("stepWelcomePost: %v", err)
	}
	if stub.posts != 1 {
		t.Fatalf("POSTs = %d, want exactly 1", stub.posts)
	}
	// The pre-check must NOT scope by filter[space_id]: that branch runs
	// list_for_space, which hides moderation-removed rows, so a removed welcome
	// post would be re-created on every resume. Space matching is client-side.
	if strings.Contains(stub.listQuery, "filter") {
		t.Errorf("discussions list query = %q, want NO filter[...] (the filtered branch hides removed rows)", stub.listQuery)
	}
	if !strings.Contains(stub.listQuery, "page%5Bsize%5D=100") {
		t.Errorf("discussions list query = %q, want page[size]=100 (the backend clamp ceiling)", stub.listQuery)
	}
	typ, attrs := decodeDataTypeAttrs(t, stub.lastPostBody(t))
	if typ != "discussions" {
		t.Errorf("type = %q, want discussions", typ)
	}
	want := map[string]any{
		"space_id":     "sp_gen", // the SLUG resolved to the hub's real id
		"title":        "Welcome!",
		"body":         "Say hi.",
		"is_published": true,
	}
	if !reflect.DeepEqual(attrs, want) {
		t.Errorf("attributes = %v, want %v (no author field — it is server-derived)", attrs, want)
	}
	if sc.welcomePostID != "disc_1" || sc.welcomePostStatus != welcomePostCreated {
		t.Errorf("welcomePost = %q/%q, want disc_1/created", sc.welcomePostID, sc.welcomePostStatus)
	}
}

// TestStepWelcomePost_ResumeAdoptsInsteadOfDuplicating: the endpoint has no
// upsert and its schema carries no meta field, so the only available idempotency
// key is the TITLE within the target space. Running the step TWICE against the
// same stateful backend — the real resume shape — must leave exactly one post.
//
// The padded-title case is the one that matters and the one that regressed: the
// server stores title.strip(), so a template title with any leading/trailing
// space never matches its own earlier post if the pre-check compares the raw
// string, and EVERY resume posts another copy. The stub strips on create like the
// real server, so this test can actually see that.
func TestStepWelcomePost_ResumeAdoptsInsteadOfDuplicating(t *testing.T) {
	for _, tc := range []struct {
		name, title string
	}{
		{"exact title", "Welcome!"},
		{"padded title (server stores it stripped)", "  Welcome!  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, stub := newWelcomePostServer(t, oneSpaceBody)
			tmpl := templateWithWelcomePost()
			tmpl.WelcomePost.Title = tc.title

			first := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
			if err := stepWelcomePost(first, tmpl); err != nil {
				t.Fatalf("first run: %v", err)
			}
			// Second run = a resume: fresh context (nothing carried over), same hub.
			second := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
			if err := stepWelcomePost(second, tmpl); err != nil {
				t.Fatalf("resume run: %v", err)
			}

			if stub.posts != 1 {
				t.Errorf("POSTs after two runs = %d, want 1 — a resume must never create a second welcome post", stub.posts)
			}
			if first.welcomePostStatus != welcomePostCreated {
				t.Errorf("first run status = %q, want created", first.welcomePostStatus)
			}
			if second.welcomePostStatus != welcomePostAdopted {
				t.Errorf("resume status = %q, want adopted", second.welcomePostStatus)
			}
			if second.welcomePostID != first.welcomePostID {
				t.Errorf("resume adopted %q, want the first run's id %q", second.welcomePostID, first.welcomePostID)
			}
		})
	}
}

// TestStepWelcomePost_TitleIsPostedStripped: the CLI sends what the server would
// store, so the request body and the stored row never disagree.
func TestStepWelcomePost_TitleIsPostedStripped(t *testing.T) {
	srv, stub := newWelcomePostServer(t, oneSpaceBody)
	tmpl := templateWithWelcomePost()
	tmpl.WelcomePost.Title = "  Welcome!  "

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepWelcomePost(sc, tmpl); err != nil {
		t.Fatalf("stepWelcomePost: %v", err)
	}
	_, attrs := decodeDataTypeAttrs(t, stub.lastPostBody(t))
	if attrs["title"] != "Welcome!" {
		t.Errorf("posted title = %q, want %q (stripped, matching what the server stores)", attrs["title"], "Welcome!")
	}
}

// TestStepWelcomePost_SameTitleOtherSpaceIsNotAdopted: the scan is hub-wide (the
// only view that sees removed rows), so the space match has to happen
// client-side. A same-titled discussion in a DIFFERENT space must not be mistaken
// for the welcome post.
func TestStepWelcomePost_SameTitleOtherSpaceIsNotAdopted(t *testing.T) {
	srv, stub := newWelcomePostServer(t, oneSpaceBody)
	stub.rows = []map[string]any{{
		"id": "disc_elsewhere", "type": "discussions",
		"attributes": map[string]any{"title": "Welcome!", "space_id": "sp_other", "deleted_at": nil},
	}}

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepWelcomePost(sc, templateWithWelcomePost()); err != nil {
		t.Fatalf("stepWelcomePost: %v", err)
	}
	if stub.posts != 1 {
		t.Errorf("POSTs = %d, want 1 — a match in another space is not this hub's welcome post", stub.posts)
	}
	if sc.welcomePostStatus != welcomePostCreated {
		t.Errorf("status = %q, want created", sc.welcomePostStatus)
	}
}

// TestStepWelcomePost_SoftDeletedMatchIsAdoptedNotResurrected: a welcome post the
// operator DELETED must stay deleted — never fight the operator — but "adopted
// something members cannot see" is a different outcome from "posted a fresh one",
// so the run must say which it got rather than leave a caller to infer it.
func TestStepWelcomePost_SoftDeletedMatchIsAdoptedNotResurrected(t *testing.T) {
	srv, stub := newWelcomePostServer(t, oneSpaceBody)
	stub.rows = []map[string]any{{
		"id": "disc_gone", "type": "discussions",
		"attributes": map[string]any{
			"title": "Welcome!", "space_id": "sp_gen", "deleted_at": "2026-07-29T00:00:00Z",
		},
	}}

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepWelcomePost(sc, templateWithWelcomePost()); err != nil {
		t.Fatalf("stepWelcomePost: %v", err)
	}
	if stub.posts != 0 {
		t.Errorf("POSTs = %d, want 0 — a deleted welcome post must not be resurrected", stub.posts)
	}
	if sc.welcomePostID != "disc_gone" || sc.welcomePostStatus != welcomePostAdoptedDeleted {
		t.Errorf("welcomePost = %q/%q, want disc_gone/adopted_deleted", sc.welcomePostID, sc.welcomePostStatus)
	}
}

// TestStepWelcomePost_ExhaustiveTitleLookupFindsPage2: the welcome post is a
// hub's OLDEST discussion and the admin list is ordered last_activity_at DESC,
// so in an active community it sits on the last page. The pre-check must follow
// this endpoint's own cursor envelope — a BARE meta.next_cursor/meta.has_more,
// NOT the meta.page.* shape the spaces lookup reads — or a resume duplicates it.
func TestStepWelcomePost_ExhaustiveTitleLookupFindsPage2(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPost:
			posts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"disc_dupe","type":"discussions","attributes":{}}}`))
		case strings.HasSuffix(r.URL.Path, "/spaces"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"sp_gen","type":"spaces","attributes":{"slug":"general"}}]}`))
		case r.URL.Query().Get("page[after]") == "":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"disc_other","type":"discussions","attributes":{"title":"Chatter","space_id":"sp_gen"}}],` +
				`"meta":{"has_more":true,"next_cursor":"2026-07-29T00:00:00Z|disc_other"}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"disc_prev","type":"discussions","attributes":{"title":"Welcome!","space_id":"sp_gen"}}],` +
				`"meta":{"has_more":false,"next_cursor":null}}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepWelcomePost(sc, templateWithWelcomePost()); err != nil {
		t.Fatalf("stepWelcomePost: %v", err)
	}
	if posts != 0 {
		t.Errorf("POSTs = %d, want 0 — the page-2 match must be found", posts)
	}
	if sc.welcomePostID != "disc_prev" {
		t.Errorf("welcomePostID = %q, want disc_prev", sc.welcomePostID)
	}
}

// TestStepWelcomePost_TruncatedWalkFailsRatherThanDuplicating: the server can
// answer {has_more:true, next_cursor:null} — _list_response computes has_more
// from the row count while _build_cursor returns None when the page's last row
// has a null last_activity_at. Reading that as end-of-list makes the scan report
// "no match" and the step POST a SECOND welcome post. The step must fail instead:
// a failed scaffold is re-runnable, a duplicate welcome post is not.
func TestStepWelcomePost_TruncatedWalkFailsRatherThanDuplicating(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPost:
			posts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"disc_dupe","type":"discussions","attributes":{}}}`))
		case strings.HasSuffix(r.URL.Path, "/spaces"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(oneSpaceBody))
		default:
			// A full page whose last row has no last_activity_at: more pages exist,
			// but the server cannot name one.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"disc_other","type":"discussions","attributes":{"title":"Chatter","space_id":"sp_gen"}}],` +
				`"meta":{"has_more":true,"next_cursor":null}}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	err := stepWelcomePost(sc, templateWithWelcomePost())
	if err == nil {
		t.Fatal("a truncated scan must fail the step, not fall through to a create")
	}
	if posts != 0 {
		t.Errorf("POSTs = %d, want 0 — the step must not create after an incomplete scan", posts)
	}
	if !strings.Contains(err.Error(), "has_more") {
		t.Errorf("error = %v, want it to name the truncation cause", err)
	}
	if sc.welcomePostID != "" || sc.welcomePostStatus != "" {
		t.Errorf("welcomePost = %q/%q, want both empty on failure", sc.welcomePostID, sc.welcomePostStatus)
	}
}

// TestStepWelcomePost_RepeatedCursorFailsRatherThanDuplicating: a server that
// hands back the SAME cursor instead of advancing has not shown us every row.
// Breaking out as "no match" would create a duplicate, so it must fail — the same
// treatment the null-cursor envelope gets. Unlike that one this is defence in
// depth (no backend path is known to repeat a cursor), but it is one of the two
// exits an earlier revision left falling through to a create.
func TestStepWelcomePost_RepeatedCursorFailsRatherThanDuplicating(t *testing.T) {
	var posts, lists int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPost:
			posts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"disc_dupe","type":"discussions","attributes":{}}}`))
		case strings.HasSuffix(r.URL.Path, "/spaces"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(oneSpaceBody))
		default:
			// Always the same cursor, page after page.
			lists++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"disc_other","type":"discussions","attributes":{"title":"Chatter","space_id":"sp_gen"}}],` +
				`"meta":{"has_more":true,"next_cursor":"stuck"}}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	err := stepWelcomePost(sc, templateWithWelcomePost())
	if err == nil {
		t.Fatal("a non-advancing cursor must fail the step, not fall through to a create")
	}
	if posts != 0 {
		t.Errorf("POSTs = %d, want 0", posts)
	}
	if lists > 3 {
		t.Errorf("list GETs = %d, want the walk to stop as soon as the cursor repeats", lists)
	}
	// The recovery must name the hand-create path: re-running cannot help, since
	// the condition is the server's, not this run's.
	if !strings.Contains(err.Error(), "mio community discussions create") {
		t.Errorf("error = %v, want it to name the hand-create recovery", err)
	}
}

// TestStepWelcomePost_ScanErrorsNameTheRecoveryThatWorks: every incomplete-scan
// error must point at creating the post by hand, NOT at re-running. The
// conditions are persistent — stored data or a server bug — so a re-run fails
// identically; a hand-created post is the newest discussion, so the next scan
// finds it on page 1 (list_for_hub is id DESC over UUIDv7) and adopts it.
func TestStepWelcomePost_ScanErrorsNameTheRecoveryThatWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if strings.HasSuffix(r.URL.Path, "/spaces") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(oneSpaceBody))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"has_more":true,"next_cursor":null}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	err := stepWelcomePost(sc, templateWithWelcomePost())
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"mio community discussions create", "adopts it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
	// A malformed envelope is an UPSTREAM fault, not a local one — exit 7, so a
	// caller can tell it apart from a usage error without parsing prose.
	if got := errs.CodeOf(err); got != errs.ExitServer {
		t.Errorf("exit code = %d, want %d (ExitServer — the server's envelope is unusable)", got, errs.ExitServer)
	}
}

// TestStepWelcomePost_AdoptsMemberAuthoredPaddedTitle pins the one place the
// pre-check is deliberately WIDER than server equality: the stored title is
// compared stripped as well as the template's.
//
// This matters because the MEMBER write path does not strip — discussions_member
// .py passes the raw title straight through to create_discussion, so only posts
// written through the admin endpoint are stored stripped. A member-authored
// "  Welcome!  " is therefore adopted and the scaffold never posts its own. That
// is the deliberate direction (never create a near-duplicate the operator has to
// clean up), and it errs toward under-creating, but it is a real behaviour and
// belongs in a test rather than in an untested TrimSpace.
func TestStepWelcomePost_AdoptsMemberAuthoredPaddedTitle(t *testing.T) {
	srv, stub := newWelcomePostServer(t, oneSpaceBody)
	stub.rows = []map[string]any{{
		"id": "disc_member", "type": "discussions",
		"attributes": map[string]any{
			// Raw, unstripped — the shape only the member path can produce.
			"title": "  Welcome!  ", "space_id": "sp_gen", "deleted_at": nil,
		},
	}}

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepWelcomePost(sc, templateWithWelcomePost()); err != nil {
		t.Fatalf("stepWelcomePost: %v", err)
	}
	if stub.posts != 0 {
		t.Errorf("POSTs = %d, want 0 — a stored title that matches once stripped is adopted", stub.posts)
	}
	if sc.welcomePostID != "disc_member" || sc.welcomePostStatus != welcomePostAdopted {
		t.Errorf("welcomePost = %q/%q, want disc_member/adopted", sc.welcomePostID, sc.welcomePostStatus)
	}
}

// TestStepWelcomePost_DryRunRecordsPlanEntry: --dry-run must show the step in
// the plan (naming the post and its target space) and fire nothing.
func TestStepWelcomePost_DryRunRecordsPlanEntry(t *testing.T) {
	sc, plan := newDryRunStepSC(client.New("http://unused", "k"))
	if err := stepWelcomePost(sc, templateWithWelcomePost()); err != nil {
		t.Fatalf("stepWelcomePost dry-run: %v", err)
	}
	if len(*plan) != 1 || (*plan)[0].step != "welcome-post" {
		t.Fatalf("plan = %v, want one `welcome-post` entry", *plan)
	}
	detail := (*plan)[0].detail
	if !strings.Contains(detail, "Welcome!") || !strings.Contains(detail, "general") {
		t.Errorf("plan detail = %q, want it to name the post title and target space", detail)
	}
}

// TestStepWelcomePost_DryRunNoDeclarationSaysSo: the no-op branch still records a
// plan entry — the plan names every step, and an operator reading it must be able
// to tell "nothing to post" from "step missing".
func TestStepWelcomePost_DryRunNoDeclarationSaysSo(t *testing.T) {
	sc, plan := newDryRunStepSC(client.New("http://unused", "k"))
	if err := stepWelcomePost(sc, &catalog.HubTemplate{ID: "community"}); err != nil {
		t.Fatalf("stepWelcomePost dry-run: %v", err)
	}
	if len(*plan) != 1 || (*plan)[0].step != "welcome-post" {
		t.Fatalf("plan = %v, want one `welcome-post` entry", *plan)
	}
	if !strings.Contains((*plan)[0].detail, "no welcome post in template") {
		t.Errorf("plan detail = %q, want the no-declaration note", (*plan)[0].detail)
	}
}

// ─── Task 21: `hubs templates` + --publish / override flags (CLI-level) ────────
//
// These drive the FULL cobra command tree (runContract) — not the step functions
// directly — so they prove the Phase-5 flags are registered AND thread end-to-end
// through runHubsScaffold → scaffoldContext → the relevant step. The step-level
// tests above already pin each step's request shape; these pin the WIRING.

// scaffoldCapture records the hub PATCH bodies a full scaffold run emits, so a
// CLI-level test can inspect the blobs PATCH (branding/settings overrides) and
// the publish PATCH (is_private) without threading through every step.
type scaffoldCapture struct {
	hubID          string
	hubPatchBodies [][]byte
	// policyGateBodies records every PATCH .../policies/gate the run fires
	// (MIO-2567) — the write that turns settings.policies.enabled on. It is
	// captured separately from the policy-content PATCHes because the reported
	// bug was exactly that the content write happened and this one did not.
	policyGateBodies [][]byte
	// discussionPosts are the bodies POSTed to .../discussions — the welcome-post
	// step's writes (MIO-2558). Empty on a run whose template declares none, which
	// is every catalog shipped so far.
	discussionPosts [][]byte
}

// fullScaffoldServer answers every request a full CREATE-mode scaffold run of the
// community template makes (hub id hub_new, is_private:true), so a CLI-level test
// can drive `hubs scaffold` end-to-end.
func fullScaffoldServer(t *testing.T) (*httptest.Server, *scaffoldCapture) {
	return fullScaffoldServerFor(t, "hub_new", true)
}

// fullScaffoldServerFor serves the checked-in 2.1 catalog for a given hub id;
// fullScaffoldServerWithCatalog (below) takes the catalog BODY, for tests that
// need a template shape the shipped fixture does not carry — MIO-2567's
// policy-gate variants and MIO-2558's welcomePost both do. Both funnel into
// fullScaffoldServerAll so there is one stub, not one per ticket.
func fullScaffoldServerFor(t *testing.T, hubID string, isPrivate bool) (*httptest.Server, *scaffoldCapture) {
	t.Helper()
	return fullScaffoldServerAll(t, hubID, isPrivate, catalog21Body(t))
}

// fullScaffoldServerWithCatalog is fullScaffoldServer serving a CUSTOM catalog
// body, so a CLI-level test can drive a full run against a hub-template shape
// the shipped 2.1 fixture does not carry (MIO-2567: the policy-gate result
// contract, one run per template shape).
func fullScaffoldServerWithCatalog(t *testing.T, catBody []byte) (*httptest.Server, *scaffoldCapture) {
	t.Helper()
	return fullScaffoldServerAll(t, "hub_new", true, catBody)
}

func fullScaffoldServerAll(t *testing.T, hubID string, isPrivate bool, catBody []byte) (*httptest.Server, *scaffoldCapture) {
	t.Helper()
	rec := &scaffoldCapture{hubID: hubID}
	// Match the hub itself by SUFFIX: the client emits paths under /api/v1/... , so
	// an exact "/api/teams/…/hubs/<id>" compare would never match. The hub's own
	// sub-collections all carry a further segment (/spaces, /pages, …), so only the
	// hub resource and its PATCHes end in "/hubs/<id>".
	hubSuffix := "/hubs/" + hubID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catBody) { // preflight live catalog fetch
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPatch && strings.HasSuffix(path, hubSuffix): // blobs OR publish PATCH
			body, _ := io.ReadAll(r.Body)
			rec.hubPatchBodies = append(rec.hubPatchBodies, body)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":{"slug":"my-community","is_private":false}}}`, hubID)
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/policies/gate"): // policy GATE PATCH (MIO-2567)
			body, _ := io.ReadAll(r.Body)
			rec.policyGateBodies = append(rec.policyGateBodies, body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hub_policy_gate","attributes":{"enabled":true}}}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/policies"): // policy PATCH
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pol_1","type":"hub_policies","attributes":{}}}`))
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/tree"): // homepage draft tree PUT
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, hubSuffix): // hub retrieve (resume + blobs RMW); branded so the merge has siblings
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":{"slug":"my-community","is_private":%t,"branding":{"primary":"#000"}}}}`, hubID, isPrivate)
		case r.Method == http.MethodGet: // any other GET is a collection list — empty on a fresh hub
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/scaffold-from-template"):
			// W2b op probe (Task 9): absent on this backend — the full-run tests
			// pin the CLIENT-SIDE pipeline, so the probe 404s and falls back.
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/hubs/from-template"):
			// Whole-hub op probe (MIO-2976): absent on this backend. The shape is
			// the REAL one and it matters — an op-less backend does not 404 here,
			// it 405s with `Allow: GET`, because the admin router's
			// GET|PATCH|DELETE /hubs/{identifier} matches the literal
			// "from-template" segment. Only that shape sets ErrHubOpAbsent, which
			// is what makes these tests exercise the client-side pipeline.
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/discussions"): // welcome post (MIO-2558)
			body, _ := io.ReadAll(r.Body)
			rec.discussionPosts = append(rec.discussionPosts, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"disc_new","type":"discussions","attributes":{"title":"Welcome!"}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/hubs"): // hub create
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`, hubID)
		case r.Method == http.MethodPost: // any created child (space/def/hub-config/playlist/page/publish)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"res_new","type":"resources","attributes":{"slug":"home","is_homepage":true}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// (TestHubsTemplates_ListsCommunity — the offline, embedded listing — was
// superseded by TestHubsTemplates_ListsFromLiveCatalog: `hubs templates` now
// lists the TARGET BACKEND's catalog, so it needs credentials + a server.)

// TestScaffold_PublishFlagReachesStep8: with --publish, the pipeline reaches step
// 8 and fires a hub PATCH carrying is_private:false (never `published`), and the
// end-of-run summary reports the hub LIVE.
func TestScaffold_PublishFlagReachesStep8(t *testing.T) {
	srv, rec := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--publish"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold --publish exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	found := false
	for _, b := range rec.hubPatchBodies {
		attrs := decodeHubAttrs(t, b)
		v, ok := attrs["is_private"]
		if !ok {
			continue
		}
		found = true
		if v != false {
			t.Errorf("publish PATCH is_private = %v, want false", v)
		}
		if _, present := attrs["published"]; present {
			t.Errorf("publish PATCH must NOT carry `published` (not a writable attr); attrs=%v", attrs)
		}
	}
	if !found {
		t.Errorf("--publish must reach step 8 and PATCH the hub is_private:false; captured %d hub PATCH(es)", len(rec.hubPatchBodies))
	}
	if !strings.Contains(res.Stdout, "LIVE") {
		t.Errorf("summary must report the hub LIVE when published; stdout=%q", res.Stdout)
	}
}

// TestScaffold_NoPublishStaysPrivate: without --publish, step 8 fires NO hub
// PATCH carrying is_private, and the summary reports PRIVATE + how to publish.
func TestScaffold_NoPublishStaysPrivate(t *testing.T) {
	srv, rec := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	for _, b := range rec.hubPatchBodies {
		if _, ok := decodeHubAttrs(t, b)["is_private"]; ok {
			t.Errorf("without --publish no hub PATCH may carry is_private (hub stays private); body=%s", b)
		}
	}
	if !strings.Contains(res.Stdout, "PRIVATE") {
		t.Errorf("summary must report the hub PRIVATE without --publish; stdout=%q", res.Stdout)
	}
	// The summary must say how to go live — both re-run --publish and the update path.
	if !strings.Contains(res.Stdout, "--published") || !strings.Contains(res.Stdout, "--publish") {
		t.Errorf("summary must explain how to publish (mio hubs update --published / re-run --publish); stdout=%q", res.Stdout)
	}
}

// TestScaffold_ResumePublishedHubSummaryLive: a RESUME (--hub) onto a hub that is
// ALREADY published (GET returns is_private:false) must report LIVE — even though
// --publish was NOT passed. The state label keys off the real server-observed
// is_private, not the --publish intent, so it never tells the operator to publish
// a hub that is already live.
func TestScaffold_ResumePublishedHubSummaryLive(t *testing.T) {
	srv, _ := fullScaffoldServerFor(t, "hub_pub", false) // already public

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold", "--hub", "hub_pub", "--template", "community"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("resume scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "LIVE") {
		t.Errorf("resume onto an already-public hub must report LIVE; stdout=%q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "PRIVATE") {
		t.Errorf("resume onto an already-public hub must NOT report PRIVATE / a publish instruction; stdout=%q", res.Stdout)
	}
}

// TestScaffold_CommunityTemplateEnablesPolicyGate is the end-to-end proof of the
// MIO-2567 acceptance criterion, one layer below the live repro: a full
// CREATE-mode run of the SHIPPED community template (whose policies block is
// `terms{required,enabled} + privacy_policy{enabled}`) fires exactly one
// PATCH .../policies/gate carrying enabled:true. Before the fix the pipeline
// never touched that endpoint, so a freshly registered member saw
// tos_acceptance_required:false and POST .../tos/accept answered 404.
func TestScaffold_CommunityTemplateEnablesPolicyGate(t *testing.T) {
	srv, rec := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--publish")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(rec.policyGateBodies) != 1 {
		t.Fatalf("want exactly 1 PATCH .../policies/gate, got %d", len(rec.policyGateBodies))
	}
	if gate := decodeHubAttrs(t, rec.policyGateBodies[0]); gate["enabled"] != true {
		t.Errorf("gate PATCH body = %v, want enabled:true", gate)
	}
	// …and the machine result says so, so an agent can verify enforcement without
	// a second call to an endpoint that has no admin READ (MIO-2574 additive key).
	if got := decodeSoleJSON(t, res.Stdout)["policy_gate"]; got != true {
		t.Errorf("result.policy_gate = %v, want true", got)
	}
	// …and a human sees it too: the note goes to STDERR, so the byte-exact prose
	// summary on stdout is untouched and a `| jq` pipeline still parses.
	if !strings.Contains(res.Stderr, "enforcement gate set to enabled=true") {
		t.Errorf("the run must narrate the gate write on stderr; stderr=%q", res.Stderr)
	}
}

// TestScaffold_ResumeAppliesGateExactlyOnce: the gate write is part of an
// idempotent, resumable pipeline — a resume onto an existing hub re-asserts the
// template's declared enforcement exactly once, with the same value, never a
// flip-flop or a doubled write. (The backend's update_policy_gate is itself a
// no-op when the stored state already matches.)
func TestScaffold_ResumeAppliesGateExactlyOnce(t *testing.T) {
	srv, rec := fullScaffoldServerFor(t, "hub_pub", false)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold", "--hub", "hub_pub", "--template", "community")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("resume scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(rec.policyGateBodies) != 1 {
		t.Fatalf("resume must fire exactly 1 gate PATCH, got %d", len(rec.policyGateBodies))
	}
	if gate := decodeHubAttrs(t, rec.policyGateBodies[0]); gate["enabled"] != true {
		t.Errorf("resume gate PATCH body = %v, want enabled:true", gate)
	}
}

// catalogWithPolicies rewrites the 2.1 fixture's community hubTemplate to carry
// the given policies block (nil DELETES the key entirely) and re-digests it, so
// a REAL scaffold run can be driven end-to-end against a template shape the
// shipped catalog does not contain. Digest-valid, so it passes the same
// verification the live artifact does.
func catalogWithPolicies(t *testing.T, policies any) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(catalog21Body(t)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("parse 2.1 catalog fixture: %v", err)
	}
	hts, _ := doc["hubTemplates"].([]any)
	if len(hts) == 0 {
		t.Fatal("2.1 catalog fixture has no hubTemplates[]")
	}
	ht, _ := hts[0].(map[string]any)
	if policies == nil {
		delete(ht, "policies")
	} else {
		ht["policies"] = policies
	}
	meta, ok := doc["meta"].(map[string]any)
	if !ok {
		t.Fatal("2.1 catalog fixture has no meta object")
	}
	delete(meta, "digest")
	digest, err := catalog.Digest(doc)
	if err != nil {
		t.Fatalf("recompute catalog digest: %v", err)
	}
	meta["digest"] = digest
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	return out
}

// conflictingPoliciesCatalogBody is the shape only the preflight can reject: a
// contradictory pair of per-policy `enabled` values.
func conflictingPoliciesCatalogBody(t *testing.T) []byte {
	t.Helper()
	return catalogWithPolicies(t, map[string]any{
		"terms":          map[string]any{"required": true, "enabled": true},
		"privacy_policy": map[string]any{"enabled": false},
	})
}

// TestScaffold_ConflictingPoliciesFailInPreflightNotMidPipeline: the policies
// block is validated in the WRITE-FREE preflight, so a contradictory template
// exits 2 having created NOTHING.
//
// This is the test the step-level one cannot be. Driving stepPolicies in
// isolation proves only that the step itself writes nothing — but the step is
// stage 5 of 9, so an error raised there still leaves a hub, its blobs, its
// spaces and its onboarding defs written and unrollbackable. The assertion that
// matters is on the whole command: exit 2, and the server saw no mutation at
// all (MIO-2567 review).
func TestScaffold_ConflictingPoliciesFailInPreflightNotMidPipeline(t *testing.T) {
	srv, _, mutated := liveCatalogScaffoldServer(t, conflictingPoliciesCatalogBody(t))

	// A REAL run (no --dry-run): dry-run would prove nothing, since it never
	// writes anyway.
	err := executeCLI(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x")...)

	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", got, errs.ExitUsage, err)
	}
	if *mutated {
		t.Error("a contradictory policies block must be rejected BEFORE any write — no hub, no blobs, no spaces (there is no rollback)")
	}
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"privacy_policy", "terms", "single hub-level gate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q; err=%v", want, err)
		}
	}
	// It must NOT be tagged as a pipeline-step failure: the whole point is that
	// it never reached the pipeline.
	if strings.Contains(err.Error(), `step "policies" failed`) {
		t.Errorf("the error must come from preflight, not from pipeline stage 5; err=%v", err)
	}
}

// TestScaffold_DryRunPlanNamesPolicyGate: the gate is a real write, so the
// dry-run plan must name it — under the `policies` step (it is the enforcement
// half of the same declaration, not a tenth pipeline stage) and with no
// mutating HTTP.
func TestScaffold_DryRunPlanNamesPolicyGate(t *testing.T) {
	srv, mutated := mutationGuardServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("dry-run exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *mutated {
		t.Error("dry-run must fire NO mutating request — including the gate PATCH")
	}
	for _, want := range []string{"/policies/gate", "enable policy enforcement", "settings.policies.enabled=true"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("dry-run plan must name %q; stdout:\n%s", want, res.Stdout)
		}
	}
}

// TestScaffold_OverridesReachBlobsPatch: the Phase-5 presentation overrides thread
// end-to-end into the blobs PATCH — this is the "don't assume" wiring proof. The
// community template sets branding.favicon_url and settings.registration.enabled:true;
// passing --favicon-url/--logo-url with new values and --registration-enabled=FALSE
// must WIN over the template, proving the overrides actually reach stepBlobs (not
// just that the template values passed through).
func TestScaffold_OverridesReachBlobsPatch(t *testing.T) {
	srv, rec := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x",
			"--favicon-url", "https://override.example/fav.png",
			"--logo-url", "https://override.example/logo.png",
			"--registration-enabled=false")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold overrides exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	blobs := scaffoldBlobsPatch(t, rec)
	branding, _ := blobs["branding"].(map[string]any)
	if branding["favicon_url"] != "https://override.example/fav.png" {
		t.Errorf("branding.favicon_url = %v, want the --favicon-url override (proves --favicon-url reached stepBlobs)", branding["favicon_url"])
	}
	if branding["logo_url"] != "https://override.example/logo.png" {
		t.Errorf("branding.logo_url = %v, want the --logo-url override (proves --logo-url reached stepBlobs)", branding["logo_url"])
	}
	settings, _ := blobs["settings"].(map[string]any)
	reg, _ := settings["registration"].(map[string]any)
	// The template sets enabled:true; --registration-enabled=false must override it.
	if reg["enabled"] != false {
		t.Errorf("settings.registration.enabled = %v, want false — the --registration-enabled=false override must reach stepBlobs and WIN over the template's true", reg["enabled"])
	}
}

// scaffoldBlobsPatch returns the attributes of the run's BLOBS patch — the hub
// PATCH carrying branding. (The publish PATCH, when there is one, carries only
// is_private, so "has a branding key" identifies the blobs step unambiguously.)
func scaffoldBlobsPatch(t *testing.T, rec *scaffoldCapture) map[string]any {
	t.Helper()
	var blobs map[string]any
	for _, b := range rec.hubPatchBodies {
		attrs := decodeHubAttrs(t, b)
		if _, ok := attrs["branding"]; ok {
			blobs = attrs
		}
	}
	if blobs == nil {
		t.Fatalf("no blobs PATCH (with branding) captured; %d hub PATCH(es)", len(rec.hubPatchBodies))
	}
	return blobs
}

// TestScaffold_RealRunPrintsSummaryWithSlugAndID: a real (non-dry-run) scaffold
// prints the end-of-run summary echoing the hub's slug + id and the HOST-RELATIVE
// public URL (never a fabricated absolute URL — MIO-2521).
func TestScaffold_RealRunPrintsSummaryWithSlugAndID(t *testing.T) {
	srv, _ := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	for _, want := range []string{"my-community", "hub_new"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("real-run summary must echo the hub reference %q; stdout=%q", want, res.Stdout)
		}
	}
	if !strings.Contains(res.Stdout, "<your-hub-frontend-host>/my-community") {
		t.Errorf("summary must echo the host-relative public URL form (MIO-2521), not a fabricated URL; stdout=%q", res.Stdout)
	}
}

// TestScaffold_DryRunPrintsPlanNotSummary: --dry-run prints the plan and NOT the
// real-run summary (the dry-run output stays exactly as it was).
func TestScaffold_DryRunPrintsPlanNotSummary(t *testing.T) {
	srv, _ := mutationGuardServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("dry-run exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "dry-run") {
		t.Errorf("dry-run must print the plan header; stdout=%q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "Scaffolded hub") {
		t.Errorf("dry-run must NOT print the real-run summary; stdout=%q", res.Stdout)
	}
}

// ─── MIO-2574: --output json|plain machine-readable result ───────────────────
//
// The reported bug: `hubs scaffold … -o json` printed the prose summary, so an
// agent driving the CLI could not recover the id of the hub it had just created
// and had to scrape `mio hubs list` out-of-band to find it. These tests pin the
// fix from the agent's side — parse stdout, read a field — and pin that the
// prose surface itself did not change.

// decodeSoleJSON decodes stdout as EXACTLY ONE JSON value and fails if anything
// else is on the stream. This is the "no progress chatter on stdout" assertion:
// the scaffold is a nine-step pipeline that narrates as it goes, and a single
// stray note line would break every `mio hubs scaffold … | jq` pipeline. A
// plain json.Unmarshal would not catch trailing content.
func decodeSoleJSON(t *testing.T, stdout string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var got map[string]any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v; stdout=%q", err, stdout)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		t.Fatalf("stdout carries more than the JSON result (progress chatter must go to stderr): trailing=%v err=%v; stdout=%q",
			trailing, err, stdout)
	}
	return got
}

// TestScaffold_JSONOutputCarriesHubID: `-o json` emits a parseable result whose
// hub_id is the id of the hub the run just created — the headline of MIO-2574 —
// along with the rest of what the run learned (slug, path, template, page ids,
// space ids). Stdout carries the JSON and NOTHING else.
func TestScaffold_JSONOutputCarriesHubID(t *testing.T) {
	srv, _ := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold -o json exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	got := decodeSoleJSON(t, res.Stdout)
	for _, tc := range []struct{ key, want string }{
		{"hub_id", "hub_new"},           // the field the whole ticket is about
		{"hub_slug", "my-community"},    // as the create response reported it
		{"hub_path", "/my-community"},   // host-relative (MIO-2521): no fabricated host
		{"template_id", "community"},    // which template produced this hub
		{"homepage_page_id", "res_new"}, // the stub answers every create with res_new
	} {
		if got[tc.key] != tc.want {
			t.Errorf("%s = %v, want %q; result=%v", tc.key, got[tc.key], tc.want, got)
		}
	}
	if got["published"] != false {
		t.Errorf("published = %v, want false — no --publish, so the hub stays private", got["published"])
	}
	if got["dry_run"] != false {
		t.Errorf("dry_run = %v, want false on a real run", got["dry_run"])
	}

	// Every template page is listed IN TEMPLATE ORDER with the id this run
	// minted, so an agent can address a specific page (not just the homepage).
	pages, _ := got["pages"].([]any)
	if len(pages) != 3 {
		t.Fatalf("pages = %v, want the community template's 3 entries", got["pages"])
	}
	first, _ := pages[0].(map[string]any)
	if first["slug"] != "homepage" || first["page_id"] != "res_new" || first["is_homepage"] != true {
		t.Errorf("pages[0] = %v, want the homepage entry carrying its created page id", first)
	}
	// Spaces created by the run carry their ids too.
	spaces, _ := got["spaces"].([]any)
	if len(spaces) != 2 {
		t.Fatalf("spaces = %v, want the community template's 2 entries", got["spaces"])
	}
	if s0, _ := spaces[0].(map[string]any); s0["slug"] != "general" || s0["space_id"] != "res_new" {
		t.Errorf("spaces[0] = %v, want the created space id under its slug", s0)
	}
}

// TestScaffold_JSONIsTheOffTTYDefault: with NO --output flag and stdout not a
// TTY (a pipe — how every agent runs it), the result is JSON. This is the
// AGENTS.md contract ("piped ⇒ JSON, equivalent to --output json") that every
// other command honors and that `hubs scaffold` was the sole holdout on.
func TestScaffold_JSONIsTheOffTTYDefault(t *testing.T) {
	srv, _ := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if got := decodeSoleJSON(t, res.Stdout); got["hub_id"] != "hub_new" {
		t.Errorf("off-a-TTY default hub_id = %v, want hub_new; result=%v", got["hub_id"], got)
	}
}

// TestScaffold_JQSelectsHubID: `--jq .hub_id` yields the bare id — the exact
// one-liner an agent uses to capture the new hub
// (HUB_ID=$(mio hubs scaffold … --jq .hub_id)).
func TestScaffold_JQSelectsHubID(t *testing.T) {
	srv, _ := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--jq", ".hub_id")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold --jq exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if got := strings.TrimSpace(res.Stdout); got != `"hub_new"` {
		t.Errorf("--jq .hub_id stdout = %q, want %q", got, `"hub_new"`)
	}
}

// TestScaffold_PlainOutputEmitsHubID: `-o plain` emits the repo's key=value
// lines, so `… -o plain | grep ^hub_id=` works without a JSON parser.
func TestScaffold_PlainOutputEmitsHubID(t *testing.T) {
	srv, _ := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--output", "plain")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold -o plain exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	for _, want := range []string{"hub_id=hub_new\n", "hub_slug=my-community\n", "template_id=community\n"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("plain output must contain %q; stdout=%q", want, res.Stdout)
		}
	}
}

// TestScaffold_DryRunJSONPlan: `--dry-run -o json` emits the ordered plan AS
// DATA. A dry-run that kept printing prose onto a json stdout would break the
// same `| jq` pipeline the real run now serves.
func TestScaffold_DryRunJSONPlan(t *testing.T) {
	srv, mutated := mutationGuardServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("dry-run -o json exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *mutated {
		t.Errorf("dry-run must fire NO mutating (non-GET) request")
	}

	got := decodeSoleJSON(t, res.Stdout)
	if got["dry_run"] != true || got["template_id"] != "community" {
		t.Errorf("plan result = %v, want dry_run:true + template_id:community", got)
	}
	// The same ordered pipeline the prose plan names, with consecutive per-page
	// `pages` entries collapsed (one plan entry per page, MIO-2672 Task 7).
	steps, _ := got["steps"].([]any)
	var names []string
	for _, s := range steps {
		m, _ := s.(map[string]any)
		name, _ := m["step"].(string)
		if n := len(names); n > 0 && names[n-1] == name {
			continue
		}
		names = append(names, name)
	}
	if !reflect.DeepEqual(names, scaffoldStepNames) {
		t.Errorf("plan steps = %v, want %v (every step, in order)", names, scaffoldStepNames)
	}
}

// TestScaffold_TableSummaryUnchanged: the prose summary is byte-for-byte what it
// always was. MIO-2574 added a machine surface; it must not have edited the
// human one — this golden is what an operator on a TTY still sees.
func TestScaffold_TableSummaryUnchanged(t *testing.T) {
	srv, _ := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold -o table exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	want := `Scaffolded hub "my-community" (id hub_new) from template "community".
  Includes: 2 space(s), 0 playlist(s), 2 onboarding attribute(s), 2 policy(ies), page(s): homepage (homepage), about, faq.
  State: PRIVATE — the hub is not published yet. Publish with: mio hubs update hub_new --published (or re-run scaffold with --publish).
  Public URL: <your-hub-frontend-host>/my-community (the API does not return the hub's public URL; substitute your hub frontend host).
`
	if res.Stdout != want {
		t.Errorf("human summary changed.\n got:\n%s\nwant:\n%s", res.Stdout, want)
	}
}

// TestScaffold_MidPipelineFailureNamesCreatedHub: scaffold does not roll back,
// so a step that fails AFTER the hub was created must not lose the id — losing
// it is the same pain MIO-2574 is about, one exit code over. The id therefore
// rides the ERROR (which main.go renders into the machine-readable stderr
// envelope) as well as the operator-facing resume line, and stdout stays empty
// per the error-path contract.
func TestScaffold_MidPipelineFailureNamesCreatedHub(t *testing.T) {
	catBody := catalog21Body(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catBody) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hubs/from-template"):
			// No whole-hub op on this backend (MIO-2976) — 405 + Allow: GET, the
			// real absence shape. This test is ABOUT the client-side pipeline
			// failing mid-run, which only happens when the op is not taken.
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hubs"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_partial","type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`))
		case r.Method == http.MethodPatch:
			// The blobs step (step 2) dies here — after the hub exists.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"status":"500","detail":"boom"}]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_partial","type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`))
		}
	}))
	t.Cleanup(srv.Close)

	args := withTeam("t_team1", "hubs", "scaffold",
		"--template", "community", "--name", "X", "--slug", "x", "--output", "json")

	err := executeCLI(t, scaffoldEnv(t, srv.URL), args...)
	if err == nil {
		t.Fatal("a failing step must return an error")
	}
	if !strings.Contains(err.Error(), "hub_partial") {
		t.Errorf("the failure must name the created hub so it is not lost; err=%v", err)
	}
	if !strings.Contains(err.Error(), `step "blobs"`) {
		t.Errorf("the failure must name the failing step; err=%v", err)
	}

	// Same run through the capturing driver: no stdout on the error path (the
	// json contract stays clean), and the resume command on stderr.
	res := runContract(t, scaffoldEnv(t, srv.URL), args...)
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("the error path must produce no stdout; got %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "mio hubs scaffold --hub hub_partial") {
		t.Errorf("stderr must carry the resume command naming the created hub; stderr=%q", res.Stderr)
	}
}

// TestScopeNavHrefs verifies hub-relative menu hrefs are rewritten to stay
// within the hub ("/{slug}/…"), while absolute/typed/already-scoped items pass
// through unchanged (MIO-2543 — templates are authored slug-agnostically).
func TestScopeNavHrefs(t *testing.T) {
	nav := map[string]any{
		"header": []any{
			map[string]any{"type": "url", "label": "Home", "href": "/"},
			map[string]any{"type": "url", "label": "Content", "href": "/content"},
			map[string]any{"type": "discussions", "label": "Discussions"},
			map[string]any{"type": "url", "label": "Ext", "href": "https://example.com/x"},
			map[string]any{"type": "url", "label": "Already", "href": "/demo/members"},
		},
	}
	scopeNavHrefs(nav, "demo")
	items := nav["header"].([]any)
	got := func(i int) string { h, _ := items[i].(map[string]any)["href"].(string); return h }
	if got(0) != "/demo" {
		t.Errorf("Home href = %q, want /demo", got(0))
	}
	if got(1) != "/demo/content" {
		t.Errorf("Content href = %q, want /demo/content", got(1))
	}
	if _, has := items[2].(map[string]any)["href"]; has {
		t.Errorf("discussions item must not gain an href")
	}
	if got(3) != "https://example.com/x" {
		t.Errorf("absolute href must pass through, got %q", got(3))
	}
	if got(4) != "/demo/members" {
		t.Errorf("already-scoped href must be unchanged, got %q", got(4))
	}
	// Empty slug must be a no-op (leave everything as-is).
	nav2 := map[string]any{"header": []any{map[string]any{"type": "url", "href": "/content"}}}
	scopeNavHrefs(nav2, "")
	if h, _ := nav2["header"].([]any)[0].(map[string]any)["href"].(string); h != "/content" {
		t.Errorf("empty slug must be a no-op, got %q", h)
	}
}

// TestPrintScaffoldRecovery_PreservesIntent verifies the resume command echoes
// the caller's team, --publish, and overrides so following it verbatim doesn't
// leave a requested-published hub private or revert an override (MIO-2543).
func TestPrintScaffoldRecovery_PreservesIntent(t *testing.T) {
	fav := "https://cdn/x.ico"
	reg := false
	sc := &scaffoldContext{hubID: "hub_9", teamID: "t_1", publish: true, faviconOverride: &fav, registrationOverride: &reg}
	var b strings.Builder
	printScaffoldRecovery(&b, sc, "community")
	out := b.String()
	for _, want := range []string{"--hub hub_9", "--template community", "--team t_1", "--publish", "--favicon-url", "--registration-enabled=false"} {
		if !strings.Contains(out, want) {
			t.Errorf("recovery output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── MIO-2672: live-catalog preflight (spec §0 — the CLI holds no templates) ───

// catalog21Body reads the 2.1 catalog artifact fixture (the byte-copy of
// mio-page-catalog@rev7, hubTemplates present — see internal/catalog).
func catalog21Body(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../internal/catalog/testdata/catalog-2.1.json")
	if err != nil {
		t.Fatalf("read 2.1 catalog fixture: %v", err)
	}
	return b
}

// noHubTemplatesCatalogBody synthesizes a digest-valid catalog WITHOUT a
// hubTemplates[] key — the "backend predates the hub-template catalog" shape
// the pin-hint tests need. Derived from the checked-in 2.1 fixture rather
// than the production embed (internal/catalog/catalog.json), because the
// embed's contents move with main (it was re-pinned to a hubTemplates-bearing
// artifact by MIO-2589/MIO-2681, which broke the CI merge-ref run of tests
// that had assumed it stayed pre-2.1 forever).
func noHubTemplatesCatalogBody(t *testing.T) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(catalog21Body(t)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("parse 2.1 catalog fixture: %v", err)
	}
	delete(doc, "hubTemplates")
	meta, ok := doc["meta"].(map[string]any)
	if !ok {
		t.Fatal("2.1 catalog fixture has no meta object")
	}
	delete(meta, "digest")
	digest, err := catalog.Digest(doc)
	if err != nil {
		t.Fatalf("recompute no-hubTemplates catalog digest: %v", err)
	}
	meta["digest"] = digest
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal no-hubTemplates catalog: %v", err)
	}
	return out
}

// welcomePostCatalogBody synthesizes a digest-valid catalog whose `community`
// hub template DOES declare a welcomePost (MIO-2558). No shipped catalog carries
// one — the CLI holds no templates, so the wiring can only be exercised against a
// synthesized artifact — and it is derived from the checked-in 2.1 fixture rather
// than hand-written so it stays a real catalog in every other respect. Same
// re-digest dance as noHubTemplatesCatalogBody: mutate, drop meta.digest,
// recompute, put it back, or the mutating resolve fails closed.
func welcomePostCatalogBody(t *testing.T) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(catalog21Body(t)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("parse 2.1 catalog fixture: %v", err)
	}
	hubTemplates, ok := doc["hubTemplates"].([]any)
	if !ok || len(hubTemplates) == 0 {
		t.Fatal("2.1 catalog fixture has no hubTemplates[]")
	}
	ht, ok := hubTemplates[0].(map[string]any)
	if !ok {
		t.Fatal("hubTemplates[0] is not an object")
	}
	ht["welcomePost"] = map[string]any{
		"space": "general", // the community template's first space
		"title": "Welcome!",
		"body":  "Say hi in the comments.",
	}
	meta, ok := doc["meta"].(map[string]any)
	if !ok {
		t.Fatal("2.1 catalog fixture has no meta object")
	}
	delete(meta, "digest")
	digest, err := catalog.Digest(doc)
	if err != nil {
		t.Fatalf("recompute welcomePost catalog digest: %v", err)
	}
	meta["digest"] = digest
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal welcomePost catalog: %v", err)
	}
	return out
}

// TestScaffold_WelcomePostWiredEndToEnd: a template declaring a welcomePost makes
// the full CLI run POST it exactly once, into the space the run created, and
// report its id on the machine-readable result. This is the WIRING (runHubsScaffold
// → pipeline → step), on top of the step-level contract tests above.
func TestScaffold_WelcomePostWiredEndToEnd(t *testing.T) {
	srv, rec := fullScaffoldServerWithCatalog(t, welcomePostCatalogBody(t))

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(rec.discussionPosts) != 1 {
		t.Fatalf("discussion POSTs = %d, want exactly 1", len(rec.discussionPosts))
	}
	typ, attrs := decodeDataTypeAttrs(t, rec.discussionPosts[0])
	if typ != "discussions" {
		t.Errorf("type = %q, want discussions", typ)
	}
	// space_id is the id the stub minted for the created space, NOT the slug —
	// proof the template's slug ref was resolved through the pipeline.
	want := map[string]any{
		"space_id":     "res_new",
		"title":        "Welcome!",
		"body":         "Say hi in the comments.",
		"is_published": true, // template omits is_published → the endpoint's default
	}
	if !reflect.DeepEqual(attrs, want) {
		t.Errorf("welcome post attributes = %v, want %v", attrs, want)
	}
	got := decodeSoleJSON(t, res.Stdout)
	if got["welcome_post_id"] != "disc_new" {
		t.Errorf("welcome_post_id = %v, want disc_new; result=%v", got["welcome_post_id"], got)
	}
	if got["welcome_post_status"] != "created" {
		t.Errorf("welcome_post_status = %v, want created; result=%v", got["welcome_post_status"], got)
	}
}

// TestScaffold_WelcomePostAppearsInTableSummary: a created welcome post is the
// one thing a run writes that the human summary would otherwise never mention.
// The line is emitted ONLY when the template declares a welcomePost, which is why
// TestScaffold_TableSummaryUnchanged (no declaration) stays byte-exact.
func TestScaffold_WelcomePostAppearsInTableSummary(t *testing.T) {
	srv, _ := fullScaffoldServerWithCatalog(t, welcomePostCatalogBody(t))

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	want := "  Welcome post: created \"Welcome!\" (id disc_new).\n"
	if !strings.Contains(res.Stdout, want) {
		t.Errorf("summary must report the created welcome post.\n got:\n%s\nwant line:\n%s", res.Stdout, want)
	}
}

// TestScaffold_NoWelcomePostDeclared_NoDiscussionWrite: the shipped `community`
// template declares no welcomePost, so a normal run must POST no discussion at
// all and report welcome_post_id as JSON null (never "").
func TestScaffold_NoWelcomePostDeclared_NoDiscussionWrite(t *testing.T) {
	srv, rec := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(rec.discussionPosts) != 0 {
		t.Errorf("discussion POSTs = %d, want 0 — the template declares no welcome post", len(rec.discussionPosts))
	}
	got := decodeSoleJSON(t, res.Stdout)
	for _, key := range []string{"welcome_post_id", "welcome_post_status"} {
		if v, present := got[key]; !present || v != nil {
			t.Errorf("%s = %v (present=%v), want an explicit null", key, v, present)
		}
	}
}

// catalogRoute is a newMockServer handler serving the raw catalog body on
// GET /api/v1/page-builder/catalog (the canonical path the client emits).
func catalogRoute(body []byte) mockHandler {
	return mockHandler{Method: http.MethodGet, PathPfx: "/api/v1/page-builder/catalog", Status: 200, Body: string(body)}
}

// serveCatalogGET answers the preflight's live-catalog fetch
// (GET …/page-builder/catalog) with body and reports whether it handled the
// request — the shared route branch every hand-rolled scaffold test server
// starts with, extracted so the catalog wire shape cannot drift between them.
func serveCatalogGET(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/page-builder/catalog") {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}

// scaffoldEnv is baseEnv plus an isolated per-test catalog cache dir, so
// scaffold/hubs-templates tests never share catalog-cache state with each
// other or with the developer's real machine cache.
func scaffoldEnv(t *testing.T, srvURL string) []string {
	t.Helper()
	return append(baseEnv(srvURL), "MIO_CATALOG_CACHE_DIR="+t.TempDir())
}

// liveCatalogScaffoldServer serves catalogBody on the catalog route and a
// minimal hub body on every other GET. It reports whether the catalog route was
// hit and whether any mutating (non-GET) request fired.
func liveCatalogScaffoldServer(t *testing.T, catalogBody []byte) (*httptest.Server, *bool, *bool) {
	t.Helper()
	catalogHit, mutated := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutated = true
		}
		if serveCatalogGET(w, r, catalogBody) {
			catalogHit = true
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_x","type":"hubs","attributes":{"slug":"x","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &catalogHit, &mutated
}

// TestScaffold_FetchesCatalogFromTargetBackend: the scaffold resolves its hub
// template from the LIVE catalog of the very backend it is scaffolding against
// (spec §0) — a dry-run therefore GETs the catalog, prints the full plan, and
// fires zero mutating HTTP.
func TestScaffold_FetchesCatalogFromTargetBackend(t *testing.T) {
	srv, catalogHit, mutated := liveCatalogScaffoldServer(t, catalog21Body(t))

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*catalogHit {
		t.Errorf("the scaffold must GET the page-builder catalog from the target backend")
	}
	if *mutated {
		t.Errorf("dry-run must fire NO mutating (non-GET) request")
	}
	for _, step := range scaffoldStepNames {
		if !strings.Contains(res.Stdout, step) {
			t.Errorf("dry-run plan missing step %q; stdout=%q", step, res.Stdout)
		}
	}
}

// TestScaffold_UnknownTemplateListsAvailable: a --template id absent from the
// backend's catalog is ExitUsage, the error names the AVAILABLE hub templates,
// and nothing is written (only the catalog GET fired).
func TestScaffold_UnknownTemplateListsAvailable(t *testing.T) {
	srv, _, mutated := liveCatalogScaffoldServer(t, catalog21Body(t))

	err := executeCLI(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "nope", "--name", "X", "--slug", "x")...)

	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); err=%v", got, errs.ExitUsage, err)
	}
	if err == nil || !strings.Contains(err.Error(), "community") {
		t.Errorf("error must list the available hub templates (community); err=%v", err)
	}
	if *mutated {
		t.Errorf("an unknown template must fail BEFORE any mutating HTTP")
	}
}

// TestScaffold_BackendWithoutHubTemplatesExplains: a backend whose catalog
// predates 2.1 (no hubTemplates[] — e.g. the old 0.3.1 artifact) yields a
// pin-hint explanation (MIO-2666/W2a), ExitUsage, and zero mutating HTTP.
func TestScaffold_BackendWithoutHubTemplatesExplains(t *testing.T) {
	srv, _, mutated := liveCatalogScaffoldServer(t, noHubTemplatesCatalogBody(t))

	err := executeCLI(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x")...)

	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); err=%v", got, errs.ExitUsage, err)
	}
	if err == nil || !strings.Contains(err.Error(), "contains no hub templates") {
		t.Errorf("error must explain the backend catalog has no hub templates; err=%v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "the live catalog") {
		t.Errorf("the message must attribute the hub-template-less catalog to its SOURCE (live — it came from the backend); err=%v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "--catalog") {
		t.Errorf("the SCAFFOLD's message must point at its --catalog escape hatch; err=%v", err)
	}
	if *mutated {
		t.Errorf("a hub-template-less backend must fail BEFORE any mutating HTTP")
	}
}

// TestScaffold_NameBound255: a --name over the 255-code-point hub title bound
// (VARCHAR(255)) is ExitUsage BEFORE any HTTP at all — the very first preflight
// check, ahead of even the catalog fetch.
func TestScaffold_NameBound255(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", strings.Repeat("x", 256), "--slug", "x")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Errorf("an over-long --name must fail BEFORE any HTTP request")
	}
}

// TestScaffold_CatalogOverrideFile: --catalog <file> is the only escape hatch —
// the scaffold uses that (digest-verified) artifact exclusively, so a dry-run
// against a backend with NO catalog route succeeds with zero HTTP (no catalog
// GET, no anything).
func TestScaffold_CatalogOverrideFile(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x",
			"--catalog", "../internal/catalog/testdata/catalog-2.1.json", "--dry-run")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *fired {
		t.Errorf("--catalog dry-run must fire NO HTTP (no catalog GET)")
	}
}

// TestHubsTemplates_ListsFromLiveCatalog: `hubs templates` lists the hub
// templates from the TARGET BACKEND's catalog, LIVE-OR-FAIL — a fetch failure
// surfaces as itself (with its typed exit code), never as a stale cache or
// vendored-fallback listing; the pin-hint explanation is reserved for a
// backend that actually SERVES a catalog without hubTemplates[].
func TestHubsTemplates_ListsFromLiveCatalog(t *testing.T) {
	t.Run("lists from the backend catalog", func(t *testing.T) {
		srv := newMockServer(t, []mockHandler{catalogRoute(catalog21Body(t))})
		res := runContract(t, scaffoldEnv(t, srv.URL), "hubs", "templates")
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
		}
		if !strings.Contains(res.Stdout, "community") {
			t.Errorf("output must include the backend's 'community' hub template; stdout=%q", res.Stdout)
		}
	})
	t.Run("catalog 401 surfaces the auth failure, not a fallback listing", func(t *testing.T) {
		srv := newMockServer(t, []mockHandler{{
			Method: http.MethodGet, PathPfx: "/api/v1/page-builder/catalog",
			Status: 401, Body: `{"errors":[{"status":"401","detail":"invalid api key"}]}`,
		}})
		err := executeCLI(t, scaffoldEnv(t, srv.URL), "hubs", "templates")
		if got := errs.CodeOf(err); got != errs.ExitAuth {
			t.Errorf("exit code = %d, want %d (ExitAuth — the fetch failure's typed code must survive); err=%v", got, errs.ExitAuth, err)
		}
		if err == nil || !strings.Contains(err.Error(), "live fetch failed") {
			t.Errorf("error must surface the underlying fetch failure; err=%v", err)
		}
		if err != nil && strings.Contains(err.Error(), "contains no hub templates") {
			t.Errorf("an auth failure must NOT masquerade as a hub-template-less (fallback) catalog; err=%v", err)
		}
	})
	t.Run("no catalog route fails live-or-fail, never a vendored listing", func(t *testing.T) {
		srv := newMockServer(t, nil) // catalog GET → 404: surface it, do NOT degrade to the vendored copy
		err := executeCLI(t, scaffoldEnv(t, srv.URL), "hubs", "templates")
		if got := errs.CodeOf(err); got != errs.ExitNotFound {
			t.Errorf("exit code = %d, want %d (ExitNotFound — the 404 fetch failure's typed code); err=%v", got, errs.ExitNotFound, err)
		}
		if err == nil || !strings.Contains(err.Error(), "live fetch failed") {
			t.Errorf("error must surface the fetch failure; err=%v", err)
		}
		if err != nil && strings.Contains(err.Error(), "no hub templates") {
			t.Errorf("a fetch failure must NOT be reported as a (vendored) catalog without hub templates; err=%v", err)
		}
	})
	t.Run("backend serving a pre-2.1 catalog gets the pin hint", func(t *testing.T) {
		srv := newMockServer(t, []mockHandler{catalogRoute(noHubTemplatesCatalogBody(t))})
		err := executeCLI(t, scaffoldEnv(t, srv.URL), "hubs", "templates")
		if err == nil || !strings.Contains(err.Error(), "contains no hub templates") {
			t.Errorf("a SERVED catalog without hubTemplates[] must get the pin-hint explanation; err=%v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "the live catalog") {
			t.Errorf("the message must attribute the hub-template-less catalog to its SOURCE (live); err=%v", err)
		}
		if err != nil && strings.Contains(err.Error(), "--catalog") {
			t.Errorf("hubs templates has no --catalog flag, so its message must not advertise one; err=%v", err)
		}
	})
}

// catalogErrorScaffoldServer answers the catalog route with the given HTTP
// error status (JSON:API error body) and every other GET with a minimal hub
// body, flipping *mutated on any non-GET request.
func catalogErrorScaffoldServer(t *testing.T, status int) (*httptest.Server, *bool) {
	t.Helper()
	mutated := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutated = true
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/page-builder/catalog") {
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"errors":[{"status":"%d","detail":"catalog fetch failed"}]}`, status)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_x","type":"hubs","attributes":{"slug":"x","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &mutated
}

// TestScaffold_CatalogFetchHTTPErrorPreservesExitCode: catalog.Resolve keeps
// the client's typed HTTP error in the chain (%w), and the preflight must
// surface THAT code — 401 → ExitAuth, 5xx → ExitServer — not collapse
// everything to ExitGeneric. Nothing may be written either way.
func TestScaffold_CatalogFetchHTTPErrorPreservesExitCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   int
	}{
		{"401 unauthorized → ExitAuth", 401, errs.ExitAuth},
		{"502 bad gateway → ExitServer", 502, errs.ExitServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, mutated := catalogErrorScaffoldServer(t, tc.status)
			res := runContract(t, scaffoldEnv(t, srv.URL),
				withTeam("t_team1", "hubs", "scaffold",
					"--template", "community", "--name", "X", "--slug", "x")...)
			if res.Code != tc.want {
				t.Errorf("exit = %d, want %d (the catalog fetch failure's typed code); stderr=%q", res.Code, tc.want, res.Stderr)
			}
			if *mutated {
				t.Errorf("a failed catalog fetch must fire NO mutating HTTP")
			}
		})
	}
}

// TestScaffold_CatalogOverrideDigestMismatchExitsUsage: a --catalog file that
// fails digest verification is bad USER-SUPPLIED INPUT — ExitUsage, not a
// generic failure — and nothing is fetched or written (the override is
// exclusive; the mutating resolve fails closed before any HTTP).
func TestScaffold_CatalogOverrideDigestMismatchExitsUsage(t *testing.T) {
	srv, fired := firedGuardServer(t)

	// Corrupt the pinned digest so verification fails while the JSON still parses.
	corrupted := strings.Replace(string(catalog21Body(t)), "sha256:ab30e06a", "sha256:ab30e06b", 1)
	if !strings.Contains(corrupted, "sha256:ab30e06b") {
		t.Fatal("fixture drift: expected the pinned 2.1 digest prefix in the fixture")
	}
	path := filepath.Join(t.TempDir(), "catalog-corrupt.json")
	if werr := os.WriteFile(path, []byte(corrupted), 0o600); werr != nil {
		t.Fatalf("write corrupted catalog: %v", werr)
	}

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x",
			"--catalog", path)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want %d (ExitUsage — a bad user-supplied --catalog file); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Errorf("a digest-mismatched --catalog must fail before ANY HTTP")
	}
}

// ─── MIO-2604: scaffold-time palette / branding overrides ─────────────────────
//
// The reported bug: scaffold exposed --favicon-url/--logo-url but no way to
// touch the PALETTE, so every hub built from `community` shipped the template's
// indigo primary regardless of brand, and recoloring took a second command plus
// a hand-authored --branding-json blob. These tests pin the fix from the
// operator's side — pass a flag, watch it land in the blobs PATCH — plus the two
// things that are easy to get subtly wrong: the flags must MERGE over the
// template (not replace its branding block), and the --primary-color →
// header_color cascade must fire exactly when the operator gave no header color.
//
// Template branding these assert against (catalog-2.1.json `community`, the same
// block the embedded 0.12.0 catalog carries):
//
//	logo_url/favicon_url/social_image_url …, primary #4F46E5, secondary #15803D,
//	background #FFFFFF, text #111827, header_color #4F46E5, header_accent #A5B4FC

// tmplCommunityLogoURL is the community template's branding.logo_url — a key NO
// palette flag writes, so it is the canary for "the flags merged over the
// template's block instead of replacing it".
const tmplCommunityLogoURL = "https://assets.searchie.io/hub-templates/community/logo.png"

// scaffoldBrandingFromRun runs a full create-mode scaffold with the given extra
// args and returns the branding object of the blobs PATCH.
func scaffoldBrandingFromRun(t *testing.T, extra ...string) map[string]any {
	t.Helper()
	srv, rec := fullScaffoldServer(t)
	args := withTeam("t_team1", "hubs", "scaffold",
		"--template", "community", "--name", "X", "--slug", "x")
	res := runContract(t, scaffoldEnv(t, srv.URL), append(args, extra...)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold %v exit = %d, want %d (ExitOK); stderr=%q", extra, res.Code, errs.ExitOK, res.Stderr)
	}
	branding, _ := scaffoldBlobsPatch(t, rec)["branding"].(map[string]any)
	if branding == nil {
		t.Fatal("blobs PATCH carried no branding object")
	}
	return branding
}

// assertBranding checks the expected key→value pairs on a branding object.
func assertBranding(t *testing.T, branding map[string]any, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if branding[k] != v {
			t.Errorf("branding.%s = %v, want %q; branding=%v", k, branding[k], v, branding)
		}
	}
}

// TestScaffoldBrandingFlags_KeysAreOnTheAllowlist: every palette flag writes a
// key on the MIO-2515 branding allowlist, and is actually registered on the
// command. Without this a flag could ship writing a key the scaffold's own
// strict-key validation then REJECTS at apply time — a flag that always errors.
func TestScaffoldBrandingFlags_KeysAreOnTheAllowlist(t *testing.T) {
	seenFlag, seenKey := map[string]bool{}, map[string]bool{}
	for _, f := range scaffoldBrandingFlags {
		if !brandingKeys[f.key] {
			t.Errorf("--%s writes branding key %q, which is NOT on the MIO-2515 allowlist — scaffold runs strict, so it would be rejected at apply time", f.flag, f.key)
		}
		if seenFlag[f.flag] || seenKey[f.key] {
			t.Errorf("duplicate flag/key in scaffoldBrandingFlags: --%s → %s", f.flag, f.key)
		}
		seenFlag[f.flag], seenKey[f.key] = true, true
		if hubsScaffoldCmd.Flags().Lookup(f.flag) == nil {
			t.Errorf("--%s is in the table but not registered on `hubs scaffold`", f.flag)
		}
	}
	// The cascade keys off these two by name — they must be in the table.
	if !seenFlag[scaffoldPrimaryFlag] || !seenFlag[scaffoldHeaderColorFlag] {
		t.Errorf("the cascade's flags (--%s / --%s) must both be in scaffoldBrandingFlags", scaffoldPrimaryFlag, scaffoldHeaderColorFlag)
	}
	if hubsScaffoldCmd.Flags().Lookup("branding-json") == nil {
		t.Error("--branding-json must be registered on `hubs scaffold`")
	}
}

// TestScaffold_PaletteFlagsReachBlobsPatch: EVERY scalar palette flag threads
// end-to-end into the branding of the blobs PATCH, overriding the template's
// value — and the template keys no flag names (logo_url here) SURVIVE, proving
// the overrides merge over the template's branding block rather than replacing
// it.
func TestScaffold_PaletteFlagsReachBlobsPatch(t *testing.T) {
	branding := scaffoldBrandingFromRun(t,
		"--primary-color", "#B91C1C",
		"--secondary-color", "#F59E0B",
		"--text-color", "#0F172A",
		"--background-color", "#FAFAF9",
		"--header-color", "#111827",
		"--header-accent", "#FCA5A5",
		"--social-image-url", "https://cdn.example/social.png",
	)
	assertBranding(t, branding, map[string]string{
		// Each flag → the SHORT-form key the template itself sets (writing the
		// legacy primary_color/secondary_color spelling would land BESIDE the
		// template's value instead of overriding it).
		"primary":          "#B91C1C",
		"secondary":        "#F59E0B",
		"text":             "#0F172A",
		"background":       "#FAFAF9",
		"header_color":     "#111827",
		"header_accent":    "#FCA5A5",
		"social_image_url": "https://cdn.example/social.png",
		// MERGE, not replace: a template branding key no flag names survives.
		"logo_url": tmplCommunityLogoURL,
	})
}

// TestScaffold_PrimaryColorCascadesToHeaderColor: with no header color given,
// --primary-color also fills header_color (the cascade MIO-2604 requires) and
// BEATS the template's own header_color. The community template sets
// header_color to the same value as primary (#4F46E5) — its header IS its
// primary — so a cascade that yielded to the template would never fire and would
// leave a red-branded hub with an indigo header: the exact mismatch reported.
// Sibling template keys (header_accent, secondary) are untouched.
func TestScaffold_PrimaryColorCascadesToHeaderColor(t *testing.T) {
	branding := scaffoldBrandingFromRun(t, "--primary-color", "#B91C1C")
	assertBranding(t, branding, map[string]string{
		"primary":       "#B91C1C",
		"header_color":  "#B91C1C", // cascaded — NOT the template's #4F46E5
		"header_accent": "#A5B4FC", // template value, untouched by the cascade
		"secondary":     "#15803D", // template value, untouched
	})
}

// TestScaffold_ExplicitHeaderColorSuppressesCascade: an explicit --header-color
// wins and the cascade does NOT fire — the escape hatch for decoupling the
// header from the brand color.
func TestScaffold_ExplicitHeaderColorSuppressesCascade(t *testing.T) {
	branding := scaffoldBrandingFromRun(t,
		"--primary-color", "#B91C1C", "--header-color", "#0F172A")
	assertBranding(t, branding, map[string]string{
		"primary":      "#B91C1C",
		"header_color": "#0F172A",
	})
}

// TestScaffold_BrandingJSONHeaderColorSuppressesCascade: a header_color inside
// --branding-json counts as "the operator gave a header color" too, so the
// cascade must not clobber it. Without this the cascade would silently beat an
// explicit key in the operator's own blob.
func TestScaffold_BrandingJSONHeaderColorSuppressesCascade(t *testing.T) {
	branding := scaffoldBrandingFromRun(t,
		"--primary-color", "#B91C1C",
		"--branding-json", `{"header_color":"#0F172A"}`)
	assertBranding(t, branding, map[string]string{
		"primary":      "#B91C1C",
		"header_color": "#0F172A",
	})
}

// TestScaffold_BrandingJSONMergesAndScalarFlagsWin: --branding-json (ticket
// Option B) merges over the template, and a scalar flag WINS over the same key
// in it — the documented precedence, and the same order applyHubBlobs already
// applies for --logo-url/--favicon-url over --branding-json on `hubs update`.
func TestScaffold_BrandingJSONMergesAndScalarFlagsWin(t *testing.T) {
	branding := scaffoldBrandingFromRun(t,
		"--branding-json", `{"primary":"#111111","font_body":"Inter"}`,
		"--primary-color", "#B91C1C")
	assertBranding(t, branding, map[string]string{
		"primary":   "#B91C1C", // the scalar flag beats the JSON key
		"font_body": "Inter",   // a JSON-only key still lands
		"secondary": "#15803D", // template value survives the merge
		"logo_url":  tmplCommunityLogoURL,
	})
}

// TestScaffold_BrandingJSONBadInputFailsBeforeAnyHTTP: --branding-json is parsed
// and key-checked PRE-AUTH, so malformed JSON or a misspelled branding key exits
// ExitUsage having fired no request at all — the scaffold reuses the `hubs
// update` parser + the MIO-2515 allowlist rather than adding a second validator,
// and runs it in STRICT mode like every other blob key it applies.
func TestScaffold_BrandingJSONBadInputFailsBeforeAnyHTTP(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"malformed", `{"primary":`},
		{"non-object", `["#fff"]`},
		{"unknown key", `{"primry":"#fff"}`}, // typo of "primary"
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := firedGuardServer(t)
			res := runContract(t, scaffoldEnv(t, srv.URL),
				withTeam("t_team1", "hubs", "scaffold",
					"--template", "community", "--name", "X", "--slug", "x",
					"--branding-json", tc.value)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Errorf("bad --branding-json must fail before ANY HTTP request")
			}
		})
	}
}

// TestScaffold_StrictKeyErrorDoesNotAdviseAMissingFlag: the SHARED strict
// blob-key message ends "drop --strict-keys …" — sound advice on `hubs
// create`/`hubs update`, a dead end on `hubs scaffold`, which has no such flag
// and always checks branding keys strictly. The scaffold swaps that tail for
// guidance that works (fix the key, or apply it afterwards via `hubs update`),
// and the swap is anchored on the shared const so a reworded hint fails loudly
// here rather than silently reinstating the dead end.
func TestScaffold_StrictKeyErrorDoesNotAdviseAMissingFlag(t *testing.T) {
	srv, _ := firedGuardServer(t)
	err := executeCLI(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x",
			"--branding-json", `{"primry":"#fff"}`)...)
	if err == nil {
		t.Fatal("an unknown --branding-json key must be an error")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", errs.CodeOf(err), errs.ExitUsage)
	}
	// The dead-end instruction is gone. (The message may still MENTION
	// --strict-keys — it says there is none to drop here — but it must never
	// tell the operator to drop one.)
	if strings.Contains(err.Error(), "drop --strict-keys to send") {
		t.Errorf("the scaffold error must not tell the operator to drop --strict-keys (no such flag here); err=%v", err)
	}
	for _, want := range []string{
		`unknown key "branding.primry"`, // the offending key is still named
		"no --strict-keys to drop",      // …why there is no opt-out here
		"mio hubs update",               // …and the escape hatch that does exist
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("scaffold strict-key error must contain %q; err=%v", want, err)
		}
	}
	// The shared hint itself is untouched for the commands that DO have the flag.
	if !strings.Contains(strictKeyDropHint, "--strict-keys") {
		t.Error("strictKeyDropHint no longer mentions --strict-keys — the scaffold swap is now anchored on stale text")
	}
}

// TestScaffold_DryRunPlanShowsPalette: --dry-run REFLECTS the palette it would
// apply, cascade annotated, and still fires no mutation. A dry run that named
// only the step would leave the operator unable to preview the one thing these
// flags exist to change.
func TestScaffold_DryRunPlanShowsPalette(t *testing.T) {
	srv, mutated := mutationGuardServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run",
			"--primary-color", "#B91C1C", "--secondary-color", "#F59E0B"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("dry-run exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *mutated {
		t.Errorf("a palette dry-run must still fire NO mutating request")
	}
	for _, want := range []string{
		"branding overrides:",
		"primary=#B91C1C",
		"secondary=#F59E0B",
		"header_color=#B91C1C (cascaded from --primary-color)",
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("dry-run plan must show %q; stdout:\n%s", want, res.Stdout)
		}
	}
}

// TestScaffold_JSONResultCarriesBrandingOverrides: the machine-readable result
// reports the RESOLVED override layer — cascade included — so an agent can see
// what the CLI actually sent without a second GET. The key is emitted
// unconditionally ({} when nothing was overridden), like every other key in the
// MIO-2574 contract.
func TestScaffold_JSONResultCarriesBrandingOverrides(t *testing.T) {
	run := func(t *testing.T, extra ...string) map[string]any {
		t.Helper()
		srv, _ := fullScaffoldServer(t)
		args := withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--output", "json")
		res := runContract(t, scaffoldEnv(t, srv.URL), append(args, extra...)...)
		if res.Code != errs.ExitOK {
			t.Fatalf("scaffold -o json exit = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
		}
		return decodeSoleJSON(t, res.Stdout)
	}

	got := run(t, "--primary-color", "#B91C1C", "--branding-json", `{"font_body":"Inter"}`)
	overrides, ok := got["branding_overrides"].(map[string]any)
	if !ok {
		t.Fatalf("branding_overrides = %v, want an object; result=%v", got["branding_overrides"], got)
	}
	for k, want := range map[string]string{
		"primary":      "#B91C1C",
		"header_color": "#B91C1C", // the cascade, visible without a second GET
		"font_body":    "Inter",   // --branding-json keys are part of the layer
	} {
		if overrides[k] != want {
			t.Errorf("branding_overrides.%s = %v, want %q; overrides=%v", k, overrides[k], want, overrides)
		}
	}
	// A template key the operator never touched is NOT an override — this field
	// is the override LAYER, not the hub's final branding.
	if _, has := overrides["secondary"]; has {
		t.Errorf("branding_overrides must carry only what the OPERATOR set; got %v", overrides)
	}

	bare := run(t)
	empty, ok := bare["branding_overrides"].(map[string]any)
	if !ok || len(empty) != 0 {
		t.Errorf("branding_overrides = %v, want an empty object on a run with no overrides", bare["branding_overrides"])
	}
}

// TestScaffold_TableSummaryReportsBrandingOverrides: the prose summary gains a
// "Branding overrides" line — but ONLY when there are some, which is why
// TestScaffold_TableSummaryUnchanged (the byte-exact golden for an override-free
// run) still passes untouched.
func TestScaffold_TableSummaryReportsBrandingOverrides(t *testing.T) {
	srv, _ := fullScaffoldServer(t)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x",
			"--primary-color", "#B91C1C"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	want := "  Branding overrides: header_color=#B91C1C (cascaded from --primary-color), primary=#B91C1C.\n"
	if !strings.Contains(res.Stdout, want) {
		t.Errorf("summary must report the branding overrides.\n got:\n%s\nwant line:\n%s", res.Stdout, want)
	}
}

// TestPrintScaffoldRecovery_PreservesBrandingIntent: the resume command echoes
// the branding flags the OPERATOR passed — following it verbatim after a
// mid-pipeline failure must not rebuild the hub in the template's palette. The
// CASCADED header_color is omitted: --primary-color is echoed and the resume
// re-derives it, so the printed command stays the one the operator ran.
func TestPrintScaffoldRecovery_PreservesBrandingIntent(t *testing.T) {
	sc := &scaffoldContext{
		hubID: "hub_9", teamID: "t_1",
		branding: scaffoldBranding{
			jsonBlob: map[string]any{"font_body": "Inter"},
			scalars:  map[string]string{"primary": "#B91C1C", "header_color": "#B91C1C"},
			cascaded: map[string]bool{"header_color": true},
		},
	}
	var b strings.Builder
	printScaffoldRecovery(&b, sc, "community")
	out := b.String()
	for _, want := range []string{`--primary-color "#B91C1C"`, "--branding-json", "font_body"} {
		if !strings.Contains(out, want) {
			t.Errorf("recovery output missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--header-color") {
		t.Errorf("the CASCADED header_color must not be echoed as a flag (the resume re-derives it); got:\n%s", out)
	}
}
