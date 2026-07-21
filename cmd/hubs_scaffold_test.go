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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/hubtemplate"
)

// scaffoldStepNames is the ordered pipeline the dry-run plan must name, in order.
var scaffoldStepNames = []string{
	"hub", "blobs", "spaces", "onboarding", "policies",
	"playlists", "homepage", "publish", "backend-gated",
}

// mutationGuardServer starts a test server that flips *mutated to true on ANY
// non-GET (mutating) request, so a dry-run can assert it created/changed
// nothing. GETs (context resolution) are allowed and answered with a minimal
// hub body.
func mutationGuardServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	mutated := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutated = true
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_x","type":"hubs","attributes":{"slug":"x","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &mutated
}

// TestScaffold_DryRunEmitsPlanNoHTTP: `hubs scaffold --dry-run` prints the
// ordered plan naming every step and fires no mutating HTTP.
func TestScaffold_DryRunEmitsPlanNoHTTP(t *testing.T) {
	srv, mutated := mutationGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run")...)

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
	for _, m := range stepLineRE.FindAllStringSubmatch(res.Stdout, -1) {
		gotSteps = append(gotSteps, m[1])
	}
	if !reflect.DeepEqual(gotSteps, scaffoldStepNames) {
		t.Errorf("dry-run plan steps = %v, want %v (every step, in order); stdout:\n%s",
			gotSteps, scaffoldStepNames, res.Stdout)
	}
}

// TestScaffold_ResumeGetsHubForSlug: resume mode (--hub) GETs the hub and
// populates scaffoldContext.hubSlug from the response.
func TestScaffold_ResumeGetsHubForSlug(t *testing.T) {
	gotGet := false
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	res := runContract(t, baseEnv(srv.URL),
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
// (`mio config set hub`) must NOT turn a create-mode invocation into a resume.
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
	touchedConfigured := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "hub_configured") {
			touchedConfigured = true
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

	env := append(baseEnv(srv.URL), "XDG_CONFIG_HOME="+cfgHome)
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

// TestScaffold_UnknownTemplate: an unknown --template is ExitUsage before any HTTP.
func TestScaffold_UnknownTemplate(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold", "--template", "nope")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("unknown-template exit code = %d, want %d (ExitUsage); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Errorf("unknown template must fail BEFORE any HTTP request")
	}
}

// ─── Phase 4 step tests (Tasks 12-14) ────────────────────────────────────────
//
// These drive the pipeline step functions DIRECTLY with an in-memory
// *hubtemplate.Template and a scaffoldContext wired to an httptest server
// (unit-style), per the Phase-4 plan — they do not depend on the community.json
// content or the full CLI wiring.

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
	tmpl := &hubtemplate.Template{ID: "community", Branding: map[string]any{"primary": "#111"}}
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
	if err := stepHub(sc, &hubtemplate.Template{ID: "community"}); err != nil {
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
	tmpl := &hubtemplate.Template{
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
	tmpl := &hubtemplate.Template{
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
	tmpl := &hubtemplate.Template{
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
	tmpl := &hubtemplate.Template{
		ID: "community",
		Spaces: []hubtemplate.Space{
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
	tmpl := &hubtemplate.Template{
		ID:     "community",
		Spaces: []hubtemplate.Space{{Name: "General", Slug: "general", AccessLevel: "public", PostingPermission: "any_member"}},
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
	tmpl := &hubtemplate.Template{
		ID: "community",
		Onboarding: []hubtemplate.AttrDef{
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
	tmpl := &hubtemplate.Template{
		ID:         "community",
		Onboarding: []hubtemplate.AttrDef{{Name: "Company", Slug: "company", FieldType: "text", InOnboarding: true}},
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
	tmpl := &hubtemplate.Template{
		ID:         "community",
		Onboarding: []hubtemplate.AttrDef{{Name: "Company", Slug: "company", FieldType: "text", InOnboarding: true}},
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
	tmpl := &hubtemplate.Template{
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
	if err := stepPolicies(sc, &hubtemplate.Template{ID: "community"}); err != nil {
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
	tmpl := &hubtemplate.Template{ID: "community", Policies: map[string]any{"bogus": map[string]any{}}}
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
	tmpl := &hubtemplate.Template{
		ID: "community",
		Playlists: []hubtemplate.Playlist{
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
	tmpl := &hubtemplate.Template{
		ID: "community",
		Playlists: []hubtemplate.Playlist{
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

// ─── Task 18: stepHomepage ────────────────────────────────────────────────────

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

// TestStepHomepage_CreatesPageThenSetsTreeWithIfMatch0: on a hub with no homepage
// yet, the step (1) creates the "home" page with identity from the template
// homepage ref (title/slug/is_homepage + privacy), then (2) PUTs the draft tree —
// instantiated OFFLINE from the vendored catalog and wrapped as {"root":…} — with
// the first-set If-Match: 0 header. The page id is captured into the context.
func TestStepHomepage_CreatesPageThenSetsTreeWithIfMatch0(t *testing.T) {
	var createBody, putBody []byte
	var ifMatch, putPath string
	var lists, creates, puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/tree"):
			puts++
			ifMatch = r.Header.Get("If-Match")
			putPath = r.URL.Path
			putBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pages"):
			creates++
			createBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"page_home","type":"pages","attributes":{"slug":"home","is_homepage":true}}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pages"): // existingHomepage — none
			lists++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &hubtemplate.Template{
		ID:       "community",
		Homepage: &hubtemplate.HomepageRef{Template: "page-homepage", Privacy: "public"},
	}
	if err := stepHomepage(sc, tmpl); err != nil {
		t.Fatalf("stepHomepage: %v", err)
	}
	if lists != 1 || creates != 1 || puts != 1 {
		t.Fatalf("want 1 list + 1 create + 1 tree PUT; got %d list, %d create, %d put", lists, creates, puts)
	}
	// Create body carries the homepage identity — title/slug/is_homepage + privacy.
	ca := decodeHubAttrs(t, createBody)
	if ca["title"] != "Home" || ca["slug"] != "homepage" || ca["is_homepage"] != true || ca["privacy"] != "public" {
		t.Errorf("create attrs = %v, want title=Home slug=homepage is_homepage=true privacy=public", ca)
	}
	// The tree PUT uses the first-set OCC sentinel (If-Match 0) and the settable
	// {"root":…} envelope with instantiated nodes (fresh ids + the page children).
	if ifMatch != "0" {
		t.Errorf("If-Match = %q, want 0 (first set on a fresh page)", ifMatch)
	}
	if !strings.HasSuffix(putPath, "/pages/page_home/tree") {
		t.Errorf("tree PUT path = %q, want .../pages/page_home/tree", putPath)
	}
	tree, ok := decodeHubAttrs(t, putBody)["tree"].(map[string]any)
	if !ok {
		t.Fatalf("PUT body has no `tree` attribute; body=%s", putBody)
	}
	root, ok := tree["root"].(map[string]any)
	if !ok {
		t.Fatalf("tree not wrapped as {\"root\":…}; tree=%v", tree)
	}
	if _, ok := root["id"].(string); !ok {
		t.Errorf("root node missing an instantiated id; root=%v", root)
	}
	if kids, ok := root["children"].([]any); !ok || len(kids) == 0 {
		t.Errorf("root node has no children (page-homepage instantiates 3); root=%v", root)
	}
	if sc.homePageID != "page_home" {
		t.Errorf("homePageID = %q, want page_home", sc.homePageID)
	}
}

// TestStepHomepage_ResumeReusesExistingPageAndTreeGetsDraftVersion: when a
// homepage already exists (found by is_homepage in the exhaustive page list), the
// step does NOT create a duplicate — it reuses that page id and reads the OCC
// draft_version via a TREE-GET (RetrieveWithQuery on the author draft), NOT off the
// page list, then PUTs the tree with that draft_version as the If-Match token. The
// list here deliberately omits draft_version to prove it is never sourced there.
func TestStepHomepage_ResumeReusesExistingPageAndTreeGetsDraftVersion(t *testing.T) {
	var ifMatch, putPath, treeGetQuery string
	var creates, treeGets, puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/tree"):
			puts++
			ifMatch = r.Header.Get("If-Match")
			putPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_2","type":"page_draft_trees","attributes":{"draft_version":4}}}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tree"): // tree-get for draft_version
			treeGets++
			treeGetQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_2","type":"page_draft_trees","attributes":{"draft_version":3}}}`))
		case r.Method == http.MethodPost:
			creates++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"page_dupe","type":"pages","attributes":{}}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pages"): // homepage exists (no draft_version on the list)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"page_existing","type":"pages","attributes":{"slug":"home","is_homepage":true}}]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &hubtemplate.Template{
		ID:       "community",
		Homepage: &hubtemplate.HomepageRef{Template: "page-homepage"},
	}
	if err := stepHomepage(sc, tmpl); err != nil {
		t.Fatalf("stepHomepage resume: %v", err)
	}
	if creates != 0 {
		t.Errorf("existing homepage must be reused — want 0 page creates, got %d", creates)
	}
	if treeGets != 1 {
		t.Fatalf("resume must TREE-GET the draft_version exactly once, got %d", treeGets)
	}
	if !strings.Contains(treeGetQuery, "audience=author") {
		t.Errorf("tree-get query = %q, want the author-draft query (audience=author)", treeGetQuery)
	}
	if puts != 1 {
		t.Fatalf("want exactly 1 tree PUT, got %d", puts)
	}
	if ifMatch != "3" {
		t.Errorf("If-Match = %q, want 3 (the draft_version from the tree-get, not the list)", ifMatch)
	}
	if !strings.HasSuffix(putPath, "/pages/page_existing/tree") {
		t.Errorf("tree PUT path = %q, want .../pages/page_existing/tree (reused id)", putPath)
	}
	if sc.homePageID != "page_existing" {
		t.Errorf("homePageID = %q, want reused page_existing", sc.homePageID)
	}
}

// TestStepHomepage_ResumeTreeGet404FallsBackToIfMatch0: on resume, if the existing
// homepage has never had a draft, the tree-get 404s — the step TOLERATES that as
// draft_version 0 and PUTs the first tree with If-Match 0 (never propagating the
// 404), so a resume onto a created-but-never-tree-set page still converges.
func TestStepHomepage_ResumeTreeGet404FallsBackToIfMatch0(t *testing.T) {
	var ifMatch string
	var creates, puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/tree"):
			puts++
			ifMatch = r.Header.Get("If-Match")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tree"): // tree-get 404 — no draft yet
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"no draft for this page"}]}`))
		case r.Method == http.MethodPost:
			creates++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"page_dupe","type":"pages","attributes":{}}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pages"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"page_existing","type":"pages","attributes":{"slug":"home","is_homepage":true}}]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &hubtemplate.Template{
		ID:       "community",
		Homepage: &hubtemplate.HomepageRef{Template: "page-homepage"},
	}
	if err := stepHomepage(sc, tmpl); err != nil {
		t.Fatalf("stepHomepage resume (404 tree-get): %v", err)
	}
	if creates != 0 {
		t.Errorf("existing homepage must be reused — want 0 creates, got %d", creates)
	}
	if puts != 1 {
		t.Fatalf("want exactly 1 tree PUT, got %d", puts)
	}
	if ifMatch != "0" {
		t.Errorf("If-Match = %q, want 0 (tree-get 404 tolerated as first-set)", ifMatch)
	}
	if sc.homePageID != "page_existing" {
		t.Errorf("homePageID = %q, want reused page_existing", sc.homePageID)
	}
}

// TestStepHomepage_UnknownCatalogTemplateErrorsNoHTTP: a homepage ref pointing at a
// template id that is not in the vendored catalog fails LOUD (ExitUsage) before any
// HTTP — the tree is resolved offline first, so a bad ref never creates a page.
func TestStepHomepage_UnknownCatalogTemplateErrorsNoHTTP(t *testing.T) {
	srv, fired := firedGuardServer(t)
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &hubtemplate.Template{
		ID:       "community",
		Homepage: &hubtemplate.HomepageRef{Template: "no-such-page-template"},
	}
	err := stepHomepage(sc, tmpl)
	if err == nil {
		t.Fatal("stepHomepage must ERROR on an unknown catalog template")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if *fired {
		t.Error("an unknown homepage template must fail BEFORE any HTTP (tree resolved offline first)")
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
	if err := stepPublish(sc, &hubtemplate.Template{ID: "community"}); err != nil {
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
	if err := stepPublish(sc, &hubtemplate.Template{ID: "community"}); err != nil {
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
	if err := stepPublish(sc, &hubtemplate.Template{ID: "community"}); err != nil {
		t.Fatalf("stepPublish dry-run: %v", err)
	}
	if len(*plan) != 1 || (*plan)[0].step != "publish" {
		t.Fatalf("plan = %v, want one `publish` entry", *plan)
	}
	if !strings.Contains((*plan)[0].detail, "--publish") {
		t.Errorf("skip detail = %q, want a note pointing at --publish", (*plan)[0].detail)
	}
}

// ─── Task 20: stepBackendGated ────────────────────────────────────────────────

// TestStepBackendGated_FiresNoRequest: the backend-gated step (welcome post +
// auto-admin) is not CLI-doable yet, so it must fire no request at all.
func TestStepBackendGated_FiresNoRequest(t *testing.T) {
	srv, fired := firedGuardServer(t)
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	if err := stepBackendGated(sc, &hubtemplate.Template{ID: "community"}); err != nil {
		t.Fatalf("stepBackendGated: %v", err)
	}
	if *fired {
		t.Error("backend-gated step must fire NO request (no CLI endpoint exists yet)")
	}
}

// TestStepBackendGated_RecordsSkipNoteWithTickets: the skip note names both
// backend tickets (MIO-2262 welcome post, MIO-2540 auto-admin) so an operator
// knows exactly what is deferred and why.
func TestStepBackendGated_RecordsSkipNoteWithTickets(t *testing.T) {
	sc, plan := newDryRunStepSC(client.New("http://unused", "k"))
	if err := stepBackendGated(sc, &hubtemplate.Template{ID: "community"}); err != nil {
		t.Fatalf("stepBackendGated dry-run: %v", err)
	}
	if len(*plan) != 1 || (*plan)[0].step != "backend-gated" {
		t.Fatalf("plan = %v, want one `backend-gated` entry", *plan)
	}
	detail := (*plan)[0].detail
	if !strings.Contains(detail, "MIO-2262") || !strings.Contains(detail, "MIO-2540") {
		t.Errorf("skip detail = %q, want it to name MIO-2262 and MIO-2540", detail)
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
}

// fullScaffoldServer answers every request a full CREATE-mode scaffold run of the
// community template makes (hub id hub_new, is_private:true), so a CLI-level test
// can drive `hubs scaffold` end-to-end.
func fullScaffoldServer(t *testing.T) (*httptest.Server, *scaffoldCapture) {
	return fullScaffoldServerFor(t, "hub_new", true)
}

// fullScaffoldServerFor answers every request a full scaffold run of the community
// template makes for the given hub id, reporting the hub's is_private on the hub
// GET (so resume-mode tests can seed an already-published hub): the hub create +
// retrieve, empty collection lists (so every idempotency pre-check creates on a
// fresh hub), generic created children, the homepage tree PUT — and it CAPTURES
// every PATCH to the hub itself (blobs + publish).
func fullScaffoldServerFor(t *testing.T, hubID string, isPrivate bool) (*httptest.Server, *scaffoldCapture) {
	t.Helper()
	rec := &scaffoldCapture{hubID: hubID}
	// Match the hub itself by SUFFIX: the client emits paths under /api/v1/... , so
	// an exact "/api/teams/…/hubs/<id>" compare would never match. The hub's own
	// sub-collections all carry a further segment (/spaces, /pages, …), so only the
	// hub resource and its PATCHes end in "/hubs/<id>".
	hubSuffix := "/hubs/" + hubID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPatch && strings.HasSuffix(path, hubSuffix): // blobs OR publish PATCH
			body, _ := io.ReadAll(r.Body)
			rec.hubPatchBodies = append(rec.hubPatchBodies, body)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":{"slug":"my-community","is_private":false}}}`, hubID)
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

// TestHubsTemplates_ListsCommunity: `hubs templates` lists the built-in templates,
// including `community`, and needs no credentials (embedded, offline).
func TestHubsTemplates_ListsCommunity(t *testing.T) {
	res := runContract(t, offlineEnv(), "hubs", "templates")
	if res.Code != errs.ExitOK {
		t.Fatalf("hubs templates exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "community") {
		t.Errorf("hubs templates output must include 'community'; stdout=%q", res.Stdout)
	}
}

// TestScaffold_PublishFlagReachesStep8: with --publish, the pipeline reaches step
// 8 and fires a hub PATCH carrying is_private:false (never `published`), and the
// end-of-run summary reports the hub LIVE.
func TestScaffold_PublishFlagReachesStep8(t *testing.T) {
	srv, rec := fullScaffoldServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--publish")...)
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

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x")...)
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

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold", "--hub", "hub_pub", "--template", "community")...)
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

// TestScaffold_OverridesReachBlobsPatch: the Phase-5 presentation overrides thread
// end-to-end into the blobs PATCH — this is the "don't assume" wiring proof. The
// community template sets branding.favicon_url and settings.registration.enabled:true;
// passing --favicon-url/--logo-url with new values and --registration-enabled=FALSE
// must WIN over the template, proving the overrides actually reach stepBlobs (not
// just that the template values passed through).
func TestScaffold_OverridesReachBlobsPatch(t *testing.T) {
	srv, rec := fullScaffoldServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x",
			"--favicon-url", "https://override.example/fav.png",
			"--logo-url", "https://override.example/logo.png",
			"--registration-enabled=false")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold overrides exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	// Find the blobs PATCH — the one carrying branding (the publish PATCH, if any,
	// carries only is_private).
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

// TestScaffold_RealRunPrintsSummaryWithSlugAndID: a real (non-dry-run) scaffold
// prints the end-of-run summary echoing the hub's slug + id and the HOST-RELATIVE
// public URL (never a fabricated absolute URL — MIO-2521).
func TestScaffold_RealRunPrintsSummaryWithSlugAndID(t *testing.T) {
	srv, _ := fullScaffoldServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x")...)
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

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run")...)
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
