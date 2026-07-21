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

	// The plan must name every step, in the pipeline order.
	prev := -1
	for _, name := range scaffoldStepNames {
		idx := strings.Index(res.Stdout, name)
		if idx < 0 {
			t.Errorf("dry-run plan is missing step %q; stdout:\n%s", name, res.Stdout)
			continue
		}
		if idx <= prev {
			t.Errorf("dry-run plan step %q is out of order (idx %d <= prev %d); stdout:\n%s",
				name, idx, prev, res.Stdout)
		}
		prev = idx
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
