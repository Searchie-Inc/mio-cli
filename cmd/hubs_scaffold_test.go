package cmd

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

var scaffoldStepNames = []string{
	"hub", "blobs", "spaces", "onboarding", "policies",
	"playlists", "content-nodes", "pages", "publish", "welcome-post",
}

func humanScaffold(args []string) []string {
	return append(args, "--output", "table")
}

func mutationGuardServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	srv, _, mutated := liveCatalogScaffoldServer(t, catalog21Body(t))
	return srv, mutated
}

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

	stepLineRE := regexp.MustCompile(`(?m)^\s*\d+\.\s+(\S+)`)
	var gotSteps []string
	pagesLines := 0
	for _, m := range stepLineRE.FindAllStringSubmatch(res.Stdout, -1) {
		if m[1] == "pages" {
			pagesLines++
		}
		if n := len(gotSteps); n > 0 && gotSteps[n-1] == m[1] {
			continue
		}
		gotSteps = append(gotSteps, m[1])
	}
	if !reflect.DeepEqual(gotSteps, scaffoldStepNames) {
		t.Errorf("dry-run plan steps = %v, want %v (every step, in order); stdout:\n%s",
			gotSteps, scaffoldStepNames, res.Stdout)
	}
	if pagesLines != 3 {
		t.Errorf("dry-run plan must record one `pages` entry per page (community has 3), got %d; stdout:\n%s",
			pagesLines, res.Stdout)
	}
	if !strings.Contains(res.Stdout,
		"apply via backend op if available, else create + set tree + publish + mark applied; re-runs follow §5.1 recovery") {
		t.Errorf("pages plan detail must hedge the apply method and name the recovery caveat; stdout:\n%s", res.Stdout)
	}
}

func TestScaffold_ResumeGetsHubForSlug(t *testing.T) {
	catBody := catalog21Body(t)
	gotGet := false
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catBody) {
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scaffold-from-template") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet && !gotGet {
			gotGet = true
			gotPath = r.URL.Path
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)

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

func TestScaffold_CreateModeIgnoresConfiguredDefaultHub(t *testing.T) {
	catBody := catalog21Body(t)
	touchedConfigured := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "hub_configured") {
			touchedConfigured = true
		}
		if serveCatalogGET(w, r, catBody) {
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scaffold-from-template") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
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

func TestStepBlobs_StrictRejectsUnknownTemplateBrandingKey(t *testing.T) {
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
	tmpl := &catalog.HubTemplate{
		ID:       "community",
		Branding: map[string]any{"primry": "#4F46E5"},
	}
	err := stepBlobs(sc, tmpl)
	if err == nil {
		t.Fatal("stepBlobs must ERROR under strict on an unknown branding key")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if patched {
		t.Error("no PATCH must fire when strict validation rejects a key")
	}
	if strings.Contains(err.Error(), "drop --strict-keys") {
		t.Errorf("a template strict-key rejection must not tell the operator to drop --strict-keys (no such flag on scaffold); err=%v", err)
	}
	if !strings.Contains(err.Error(), "no --strict-keys to drop") {
		t.Errorf("a template strict-key rejection must carry the scaffold-specific guidance; err=%v", err)
	}
	if !strings.Contains(err.Error(), "HUB TEMPLATE") {
		t.Errorf("a template strict-key rejection must say the key came from the template, not a flag; err=%v", err)
	}
	if !strings.Contains(err.Error(), "The API stores branding keys as sent") {
		t.Errorf("the swap must keep what the API does with branding keys; err=%v", err)
	}
}

func TestStepBlobs_TemplateSettingsKeysAreTheAPIsToCheck(t *testing.T) {
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patchBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme"}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "community",
		Settings: map[string]any{
			"language": "de",
			"email":    map[string]any{"from_localpart": "news"},
		},
	}
	if err := stepBlobs(sc, tmpl); err != nil {
		t.Fatalf("stepBlobs must leave settings keys to the API; err=%v", err)
	}
	if len(patchBody) == 0 {
		t.Fatal("the blobs PATCH must fire")
	}
	s, ok := decodeHubAttrs(t, patchBody)["settings"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH carries no settings; body=%s", patchBody)
	}
	if s["language"] != "de" {
		t.Errorf("settings.language = %v, want de", s["language"])
	}
	if e, _ := s["email"].(map[string]any); e["from_localpart"] != "news" {
		t.Errorf("settings.email.from_localpart = %v, want news", s["email"])
	}
}

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

func TestStepOnboarding_CreatesDefAndEnablesOnHubCollectionPath(t *testing.T) {
	var defBody, cfgBody []byte
	var cfgPath string
	var defPosts, cfgPosts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		isHub := strings.Contains(r.URL.Path, "/hubs/")
		switch {
		case r.Method == http.MethodGet && !isHub:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && !isHub:
			defPosts++
			defBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"def_company","type":"contact_attributes","attributes":{"slug":"company"}}}`))
		case r.Method == http.MethodPost && isHub:
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

func TestStepOnboarding_ExistingDefReusedNoDuplicateCreate(t *testing.T) {
	var defPosts, cfgPosts int
	var cfgBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		isHub := strings.Contains(r.URL.Path, "/hubs/")
		switch {
		case r.Method == http.MethodGet && !isHub:
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
	first := decodeHubAttrs(t, patchBodies[0])
	if first["policy_type"] != "privacy_policy" || first["content"] != "Privacy body" {
		t.Errorf("first PATCH = %v, want policy_type=privacy_policy content='Privacy body'", first)
	}
	second := decodeHubAttrs(t, patchBodies[1])
	if second["policy_type"] != "tos" || second["content"] != "TOS body" || second["require_acceptance"] != true {
		t.Errorf("second PATCH = %v, want policy_type=tos content='TOS body' require_acceptance=true", second)
	}
}

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
	for i, b := range (*bodies)[:2] {
		if _, ok := decodeHubAttrs(t, b)["enabled"]; ok {
			t.Errorf("content PATCH %d carries an `enabled` attribute; the gate is the only place it belongs: %s", i, b)
		}
	}
	if sc.policyGate == nil || !*sc.policyGate {
		t.Errorf("sc.policyGate = %v, want a non-nil true (the machine result reports it)", sc.policyGate)
	}
}

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
			if wrote := len(rec.policyGateBodies) > 0; wrote != tc.wantWrite {
				t.Errorf("gate PATCH sent = %t, want %t (policy_gate reports the WRITE, not the declaration); bodies=%d",
					wrote, tc.wantWrite, len(rec.policyGateBodies))
			}
		})
	}
}

var policyFieldProbes = map[string]any{
	"content":            "PROBE CONTENT",
	"require_acceptance": true,
	"required":           true,
	"enabled":            true,
}

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

func TestStepPlaylists_EmptyHubCreatesItemsAndPublishes(t *testing.T) {
	var createBody, publishBody []byte
	var publishPath string
	var itemFileIDs []string
	var creates, items, publishes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/items"):
			items++
			b, _ := io.ReadAll(r.Body)
			fid, _ := decodeHubAttrs(t, b)["file_id"].(string)
			itemFileIDs = append(itemFileIDs, fid)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"it_1","type":"playlist_items","attributes":{}}}`))
		case strings.Contains(path, "/hubs/") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.Contains(path, "/hubs/") && r.Method == http.MethodPost:
			publishes++
			publishPath = path
			publishBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"hm_1","type":"hub_media","attributes":{}}}`))
		case r.Method == http.MethodPost:
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

func TestStepPlaylists_NonEmptyHubSkipsWholeStep(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPost {
			posts++
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/hubs/") {
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

func newDryRunStepSC(cl *client.Client) (*scaffoldContext, *[]planEntry) {
	plan := []planEntry{}
	sc := newStepSC(cl, "hub_1", "acme")
	sc.dryRun = true
	sc.plan = &plan
	return sc, &plan
}

func TestStepPages_AppliesAllPagesWithProvenance(t *testing.T) {
	type pagesReq struct {
		method, path, ifMatch string
		body                  []byte
	}
	var mu sync.Mutex
	var reqs []pagesReq
	putCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pages"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pages"):
			slug, _ := decodeHubAttrs(t, body)["slug"].(string)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"pg_%s","type":"pages","attributes":{"slug":%q}}}`, slug, slug)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/tree"):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"pdt_%d","type":"page_draft_trees","attributes":{"draft_version":%d}}}`, putNo, putNo)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publish"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pp_1","type":"page-publishes","attributes":{}}}`))
		case r.Method == http.MethodPatch:
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
	var lists, muts []pagesReq
	for _, rq := range reqs {
		if rq.method == http.MethodGet && strings.HasSuffix(rq.path, "/hubs/hub_1/pages") {
			lists = append(lists, rq)
			continue
		}
		muts = append(muts, rq)
	}
	if len(lists) != len(plan.pages)+2 {
		t.Errorf("got %d page-list GETs, want %d (one recovery slug walk per page + the homepage pre-check + the set-home recheck)",
			len(lists), len(plan.pages)+2)
	}
	if len(muts) != 4*len(plan.pages)+1 {
		t.Fatalf("got %d mutating requests, want %d (create+PUT+publish+PATCH per page, in plan order, + one is_homepage update)",
			len(muts), 4*len(plan.pages)+1)
	}
	wantApp := catalog.ApplicationID("hub_1", "community")
	wantTitles := map[string]string{"homepage": "Home", "about": "About"}
	for i := range plan.pages {
		pp := plan.pages[i]
		pageID := "pg_" + pp.ref.Slug
		base := i * 4
		if i > 0 {
			base++
		}
		create, put, publish, patch := muts[base], muts[base+1], muts[base+2], muts[base+3]
		wantDV := i + 1
		if pp.ref.IsHomepage {
			home := muts[base+4]
			if home.method != http.MethodPatch || !strings.HasSuffix(home.path, "/pages/"+pageID) {
				t.Fatalf("homepage req 6 = %s %s, want PATCH .../pages/%s (is_homepage)", home.method, home.path, pageID)
			}
			if hv := decodeHubAttrs(t, home.body)["is_homepage"]; hv != true {
				t.Errorf("homepage update must send is_homepage:true, got %v", hv)
			}
		}

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
		if isHome, present := attrs["is_homepage"]; present {
			t.Errorf("page %q create must NOT send is_homepage (the homepage is set after publish), got %v", pp.ref.Slug, isHome)
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

		if publish.method != http.MethodPost || !strings.HasSuffix(publish.path, "/pages/"+pageID+"/publish") {
			t.Fatalf("page %q req 4 = %s %s, want POST .../pages/%s/publish", pp.ref.Slug, publish.method, publish.path, pageID)
		}
		if publish.ifMatch != strconv.Itoa(wantDV) {
			t.Errorf("page %q publish If-Match = %q, want %d (per-page draft_version, not a shared counter)",
				pp.ref.Slug, publish.ifMatch, wantDV)
		}

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

func TestStepPages_InterpolationCapAtApply(t *testing.T) {
	srv, fired := firedGuardServer(t)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	sc.hubName = strings.Repeat("n", 200)
	sc.pagePlan = &scaffoldPlan{pages: []plannedPage{{
		ref:     catalog.PageRef{PageTemplate: "page-x", Slug: "long", Title: "Long"},
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

func templateWithWelcomePost() *catalog.HubTemplate {
	return &catalog.HubTemplate{
		ID:     "community",
		Spaces: []catalog.TemplateSpace{{Name: "General", Slug: "general"}},
		WelcomePost: &catalog.TemplateWelcomePost{
			Space: "general", Title: "Welcome!", Body: "Say hi.", Published: true,
		},
	}
}

type welcomePostStub struct {
	mu         sync.Mutex
	rows       []map[string]any
	posts      int
	postBodies [][]byte
	listQuery  string
	nextDiscID int
}

func (s *welcomePostStub) lastPostBody(t *testing.T) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.postBodies) == 0 {
		t.Fatal("no discussion POST was captured")
	}
	return s.postBodies[len(s.postBodies)-1]
}

func (s *welcomePostStub) serveDiscussions(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasSuffix(r.URL.Path, "/discussions") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/vnd.api+json")
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		_, attrs, _ := decodeDataTypeAttrsRaw(body)

		s.posts++
		s.postBodies = append(s.postBodies, body)
		s.nextDiscID++
		id := fmt.Sprintf("disc_%d", s.nextDiscID)
		title, _ := attrs["title"].(string)
		spaceID, _ := attrs["space_id"].(string)
		row := map[string]any{
			"id": id, "type": "discussions",
			"attributes": map[string]any{
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

const oneSpaceBody = `{"data":[{"id":"sp_gen","type":"spaces","attributes":{"slug":"general"}}]}`

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

func TestStepWelcomePost_CreatesOnceInResolvedSpace(t *testing.T) {
	srv, stub := newWelcomePostServer(t, oneSpaceBody)
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")

	if err := stepWelcomePost(sc, templateWithWelcomePost()); err != nil {
		t.Fatalf("stepWelcomePost: %v", err)
	}
	if stub.posts != 1 {
		t.Fatalf("POSTs = %d, want exactly 1", stub.posts)
	}
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
		"space_id":     "sp_gen",
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
	if !strings.Contains(err.Error(), "mio community discussions create") {
		t.Errorf("error = %v, want it to name the hand-create recovery", err)
	}
}

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
	if got := errs.CodeOf(err); got != errs.ExitServer {
		t.Errorf("exit code = %d, want %d (ExitServer — the server's envelope is unusable)", got, errs.ExitServer)
	}
}

func TestStepWelcomePost_AdoptsMemberAuthoredPaddedTitle(t *testing.T) {
	srv, stub := newWelcomePostServer(t, oneSpaceBody)
	stub.rows = []map[string]any{{
		"id": "disc_member", "type": "discussions",
		"attributes": map[string]any{
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

type scaffoldCapture struct {
	hubID            string
	hubPatchBodies   [][]byte
	policyGateBodies [][]byte
	discussionPosts  [][]byte
}

func fullScaffoldServer(t *testing.T) (*httptest.Server, *scaffoldCapture) {
	return fullScaffoldServerFor(t, "hub_new", true)
}

func fullScaffoldServerFor(t *testing.T, hubID string, isPrivate bool) (*httptest.Server, *scaffoldCapture) {
	t.Helper()
	return fullScaffoldServerAll(t, hubID, isPrivate, catalog21Body(t))
}

func fullScaffoldServerWithCatalog(t *testing.T, catBody []byte) (*httptest.Server, *scaffoldCapture) {
	t.Helper()
	return fullScaffoldServerAll(t, "hub_new", true, catBody)
}

func fullScaffoldServerAll(t *testing.T, hubID string, isPrivate bool, catBody []byte) (*httptest.Server, *scaffoldCapture) {
	t.Helper()
	rec := &scaffoldCapture{hubID: hubID}
	hubSuffix := "/hubs/" + hubID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catBody) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPatch && strings.HasSuffix(path, hubSuffix):
			body, _ := io.ReadAll(r.Body)
			rec.hubPatchBodies = append(rec.hubPatchBodies, body)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":{"slug":"my-community","is_private":false}}}`, hubID)
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/policies/gate"):
			body, _ := io.ReadAll(r.Body)
			rec.policyGateBodies = append(rec.policyGateBodies, body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hub_policy_gate","attributes":{"enabled":true}}}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/policies"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pol_1","type":"hub_policies","attributes":{}}}`))
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/tree"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, hubSuffix):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":{"slug":"my-community","is_private":%t,"branding":{"primary":"#000"}}}}`, hubID, isPrivate)
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/scaffold-from-template"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/hubs/from-template"):
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/discussions"):
			body, _ := io.ReadAll(r.Body)
			rec.discussionPosts = append(rec.discussionPosts, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"disc_new","type":"discussions","attributes":{"title":"Welcome!"}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/hubs"):
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`, hubID)
		case r.Method == http.MethodPost:
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
	if !strings.Contains(res.Stdout, "--published") || !strings.Contains(res.Stdout, "--publish") {
		t.Errorf("summary must explain how to publish (mio hubs update --published / re-run --publish); stdout=%q", res.Stdout)
	}
}

func TestScaffold_ResumePublishedHubSummaryLive(t *testing.T) {
	srv, _ := fullScaffoldServerFor(t, "hub_pub", false)

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
	if got := decodeSoleJSON(t, res.Stdout)["policy_gate"]; got != true {
		t.Errorf("result.policy_gate = %v, want true", got)
	}
	if !strings.Contains(res.Stderr, "enforcement gate set to enabled=true") {
		t.Errorf("the run must narrate the gate write on stderr; stderr=%q", res.Stderr)
	}
}

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

func conflictingPoliciesCatalogBody(t *testing.T) []byte {
	t.Helper()
	return catalogWithPolicies(t, map[string]any{
		"terms":          map[string]any{"required": true, "enabled": true},
		"privacy_policy": map[string]any{"enabled": false},
	})
}

func TestScaffold_ConflictingPoliciesFailInPreflightNotMidPipeline(t *testing.T) {
	srv, _, mutated := liveCatalogScaffoldServer(t, conflictingPoliciesCatalogBody(t))

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
	if strings.Contains(err.Error(), `step "policies" failed`) {
		t.Errorf("the error must come from preflight, not from pipeline stage 5; err=%v", err)
	}
}

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
	if reg["enabled"] != false {
		t.Errorf("settings.registration.enabled = %v, want false — the --registration-enabled=false override must reach stepBlobs and WIN over the template's true", reg["enabled"])
	}
}

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
		{"hub_id", "hub_new"},
		{"hub_slug", "my-community"},
		{"hub_path", "/my-community"},
		{"template_id", "community"},
		{"homepage_page_id", "res_new"},
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

	pages, _ := got["pages"].([]any)
	if len(pages) != 3 {
		t.Fatalf("pages = %v, want the community template's 3 entries", got["pages"])
	}
	first, _ := pages[0].(map[string]any)
	if first["slug"] != "homepage" || first["page_id"] != "res_new" || first["is_homepage"] != true {
		t.Errorf("pages[0] = %v, want the homepage entry carrying its created page id", first)
	}
	spaces, _ := got["spaces"].([]any)
	if len(spaces) != 2 {
		t.Fatalf("spaces = %v, want the community template's 2 entries", got["spaces"])
	}
	if s0, _ := spaces[0].(map[string]any); s0["slug"] != "general" || s0["space_id"] != "res_new" {
		t.Errorf("spaces[0] = %v, want the created space id under its slug", s0)
	}
}

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

func TestScaffold_MidPipelineFailureNamesCreatedHub(t *testing.T) {
	catBody := catalog21Body(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catBody) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hubs/from-template"):
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hubs"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_partial","type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`))
		case r.Method == http.MethodPatch:
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

	res := runContract(t, scaffoldEnv(t, srv.URL), args...)
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("the error path must produce no stdout; got %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "mio hubs scaffold --hub hub_partial") {
		t.Errorf("stderr must carry the resume command naming the created hub; stderr=%q", res.Stderr)
	}
}

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
	nav2 := map[string]any{"header": []any{map[string]any{"type": "url", "href": "/content"}}}
	scopeNavHrefs(nav2, "")
	if h, _ := nav2["header"].([]any)[0].(map[string]any)["href"].(string); h != "/content" {
		t.Errorf("empty slug must be a no-op, got %q", h)
	}
}

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

func catalog21Body(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../internal/catalog/testdata/catalog-2.1.json")
	if err != nil {
		t.Fatalf("read 2.1 catalog fixture: %v", err)
	}
	return b
}

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
		"space": "general",
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
	want := map[string]any{
		"space_id":     "res_new",
		"title":        "Welcome!",
		"body":         "Say hi in the comments.",
		"is_published": true,
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

func catalogRoute(body []byte) mockHandler {
	return mockHandler{Method: http.MethodGet, PathPfx: "/api/v1/page-builder/catalog", Status: 200, Body: string(body)}
}

func serveCatalogGET(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/page-builder/catalog") {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}

func scaffoldEnv(t *testing.T, srvURL string) []string {
	t.Helper()
	return append(baseEnv(srvURL), "MIO_CATALOG_CACHE_DIR="+t.TempDir())
}

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
		srv := newMockServer(t, nil)
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
		if err == nil || !strings.Contains(err.Error(), "--catalog") {
			t.Errorf("the message must point at the --catalog escape hatch this command now has; err=%v", err)
		}
	})
	t.Run("--catalog lists a local artifact with no catalog fetch", func(t *testing.T) {
		srv, fired := firedGuardServer(t)
		res := runContract(t, scaffoldEnv(t, srv.URL),
			"hubs", "templates", "--catalog", "../internal/catalog/testdata/catalog-2.1.json")
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
		}
		if !strings.Contains(res.Stdout, "community") {
			t.Errorf("output must list the OVERRIDE artifact's hub templates; stdout=%q", res.Stdout)
		}
		if *fired {
			t.Errorf("--catalog must fire NO HTTP (the override is exclusive — no catalog GET beside it)")
		}
	})
	t.Run("--catalog artifact without hubTemplates does not advertise --catalog back", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "catalog.json")
		if werr := os.WriteFile(path, noHubTemplatesCatalogBody(t), 0o600); werr != nil {
			t.Fatalf("write catalog: %v", werr)
		}
		srv, _ := firedGuardServer(t)
		err := executeCLI(t, scaffoldEnv(t, srv.URL), "hubs", "templates", "--catalog", path)
		if err == nil || !strings.Contains(err.Error(), "contains no hub templates") {
			t.Fatalf("an override artifact without hubTemplates[] must get the pin-hint explanation; err=%v", err)
		}
		if strings.Contains(err.Error(), "--catalog") {
			t.Errorf("the operator already passed --catalog; the message must not point back at it; err=%v", err)
		}
	})
}

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

func TestScaffold_CatalogOverrideDigestMismatchExitsUsage(t *testing.T) {
	srv, fired := firedGuardServer(t)

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

const tmplCommunityLogoURL = "https://assets.searchie.io/hub-templates/community/logo.png"

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

func assertBranding(t *testing.T, branding map[string]any, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if branding[k] != v {
			t.Errorf("branding.%s = %v, want %q; branding=%v", k, branding[k], v, branding)
		}
	}
}

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
	if !seenFlag[scaffoldPrimaryFlag] || !seenFlag[scaffoldHeaderColorFlag] {
		t.Errorf("the cascade's flags (--%s / --%s) must both be in scaffoldBrandingFlags", scaffoldPrimaryFlag, scaffoldHeaderColorFlag)
	}
	if hubsScaffoldCmd.Flags().Lookup("branding-json") == nil {
		t.Error("--branding-json must be registered on `hubs scaffold`")
	}
}

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
		"primary":          "#B91C1C",
		"secondary":        "#F59E0B",
		"text":             "#0F172A",
		"background":       "#FAFAF9",
		"header_color":     "#111827",
		"header_accent":    "#FCA5A5",
		"social_image_url": "https://cdn.example/social.png",
		"logo_url":         tmplCommunityLogoURL,
	})
}

func TestScaffold_PrimaryColorCascadesToHeaderColor(t *testing.T) {
	branding := scaffoldBrandingFromRun(t, "--primary-color", "#B91C1C")
	assertBranding(t, branding, map[string]string{
		"primary":       "#B91C1C",
		"header_color":  "#B91C1C",
		"header_accent": "#A5B4FC",
		"secondary":     "#15803D",
	})
}

func TestScaffold_ExplicitHeaderColorSuppressesCascade(t *testing.T) {
	branding := scaffoldBrandingFromRun(t,
		"--primary-color", "#B91C1C", "--header-color", "#0F172A")
	assertBranding(t, branding, map[string]string{
		"primary":      "#B91C1C",
		"header_color": "#0F172A",
	})
}

func TestScaffold_BrandingJSONHeaderColorSuppressesCascade(t *testing.T) {
	branding := scaffoldBrandingFromRun(t,
		"--primary-color", "#B91C1C",
		"--branding-json", `{"header_color":"#0F172A"}`)
	assertBranding(t, branding, map[string]string{
		"primary":      "#B91C1C",
		"header_color": "#0F172A",
	})
}

func TestScaffold_BrandingJSONMergesAndScalarFlagsWin(t *testing.T) {
	branding := scaffoldBrandingFromRun(t,
		"--branding-json", `{"primary":"#111111","font_body":"Inter"}`,
		"--primary-color", "#B91C1C")
	assertBranding(t, branding, map[string]string{
		"primary":   "#B91C1C",
		"font_body": "Inter",
		"secondary": "#15803D",
		"logo_url":  tmplCommunityLogoURL,
	})
}

func TestScaffold_BrandingJSONBadInputFailsBeforeAnyHTTP(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"malformed", `{"primary":`},
		{"non-object", `["#fff"]`},
		{"unknown key", `{"primry":"#fff"}`},
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
	if strings.Contains(err.Error(), "drop --strict-keys to send") {
		t.Errorf("the scaffold error must not tell the operator to drop --strict-keys (no such flag here); err=%v", err)
	}
	for _, want := range []string{
		`unknown key "branding.primry"`,
		"no --strict-keys to drop",
		"mio hubs update",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("scaffold strict-key error must contain %q; err=%v", want, err)
		}
	}
	if !strings.Contains(strictKeyDropHint, "--strict-keys") {
		t.Error("strictKeyDropHint no longer mentions --strict-keys — the scaffold swap is now anchored on stale text")
	}
}

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
		"header_color": "#B91C1C",
		"font_body":    "Inter",
	} {
		if overrides[k] != want {
			t.Errorf("branding_overrides.%s = %v, want %q; overrides=%v", k, overrides[k], want, overrides)
		}
	}
	if _, has := overrides["secondary"]; has {
		t.Errorf("branding_overrides must carry only what the OPERATOR set; got %v", overrides)
	}

	bare := run(t)
	empty, ok := bare["branding_overrides"].(map[string]any)
	if !ok || len(empty) != 0 {
		t.Errorf("branding_overrides = %v, want an empty object on a run with no overrides", bare["branding_overrides"])
	}
}

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

func TestStepHub_CreateSaysWhenTheBackendSuffixedTheSlug(t *testing.T) {
	for _, tc := range []struct {
		name, returnedSlug string
		wantNote           bool
	}{
		{"slug taken, backend suffixed it", "my-community-33d229", true},
		{"slug free, used as requested", "my-community", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.api+json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"data":{"id":"hub_second","type":"hubs","attributes":{"slug":"` + tc.returnedSlug + `","is_private":true}}}`))
			}))
			t.Cleanup(srv.Close)
			sc := newStepSC(client.New(srv.URL, "k"), "", "")
			sc.noteW = &bytes.Buffer{}
			sc.nameOverride, sc.slugOverride = "My Community", "my-community"
			if err := stepHub(sc, &catalog.HubTemplate{ID: "starter"}); err != nil {
				t.Fatalf("stepHub: %v", err)
			}
			notes := stepNotes(sc)
			if tc.wantNote {
				for _, want := range []string{`assigned slug "my-community-33d229"`, `requested "my-community"`, "NEW hub (hub_second)", "--hub"} {
					if !strings.Contains(notes, want) {
						t.Errorf("notes missing %q; notes=%q", want, notes)
					}
				}
			} else if strings.Contains(notes, "assigned slug") {
				t.Errorf("no note expected when the slug was used as requested; notes=%q", notes)
			}
			if sc.hubSlug != tc.returnedSlug {
				t.Errorf("hubSlug = %q, want the OBSERVED %q", sc.hubSlug, tc.returnedSlug)
			}
		})
	}
}

func TestReadBackHubIdentity_SaysWhenTheSlugDiffers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":{"id":"hub_op","type":"hubs","attributes":{"slug":"acme-ab12cd","title":"Acme","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)
	sc := newStepSC(client.New(srv.URL, "k"), "", "")
	sc.noteW = &bytes.Buffer{}
	sc.hubID, sc.hubSlug = "hub_op", "acme"
	readBackHubIdentity(sc, false)
	notes := stepNotes(sc)
	for _, want := range []string{`assigned slug "acme-ab12cd"`, `requested "acme"`, "--hub"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q; notes=%q", want, notes)
		}
	}
	if sc.hubSlug != "acme-ab12cd" {
		t.Errorf("hubSlug = %q, want the observed slug", sc.hubSlug)
	}
}

func TestReadBackHubIdentity_ReplayDoesNotClaimTheSlugWasTaken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":{"id":"hub_op","type":"hubs","attributes":{"slug":"acme-ab12cd","title":"Acme","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)
	sc := newStepSC(client.New(srv.URL, "k"), "", "")
	sc.noteW = &bytes.Buffer{}
	sc.hubID, sc.hubSlug = "hub_op", "acme"
	readBackHubIdentity(sc, true)
	notes := stepNotes(sc)
	if !strings.Contains(notes, `has slug "acme-ab12cd"`) {
		t.Errorf("a replay with a differing slug must state the stored slug; notes=%q", notes)
	}
	for _, bad := range []string{"taken", "creating", "--hub"} {
		if strings.Contains(notes, bad) {
			t.Errorf("a replay created nothing — the note must not say %q; notes=%q", bad, notes)
		}
	}
}
