package cmd

// hubs_scaffold_resume_test.go — `mio hubs scaffold --hub` FILLS GAPS by default
// (MIO-4166 + MIO-2818). A --hub run must not overwrite state the hub already
// has: branding/palette keys, navigation buckets, settings (registration
// included), policy text, the policy gate, onboarding hub-config. Flags given on
// the invocation still win, and --reapply-template (destructive, --hub only)
// restores the template-wins apply.
//
// The oracle throughout is THE WIRE — the bodies the stub server receives — so
// no edit to a list or a message can green these tests without the behaviour.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// resumeReq is one request the resume stub saw.
type resumeReq struct {
	method, path string
	body         []byte
}

// resumeStub is a full-pipeline stub for a --hub run. Everything a real run
// reads is configurable; every request is recorded.
type resumeStub struct {
	hubID      string
	hubAttrs   string // JSON object: the hub GET's attributes
	catBody    []byte
	hubConfigs string // JSON array: GET …/hubs/{id}/contact-attributes
	defs       string // JSON array: GET …/teams/{t}/contact-attributes
	pages      string // JSON array: GET …/pages
	// hubAttrsLater, when set, is served by every hub GET after the FIRST one
	// (the resume read the plan is made from): a hub that changed between that
	// read and the write a step makes.
	hubAttrsLater string
	// hubPatchError, when set, is the JSON:API error body every hub PATCH is
	// answered with, as a 422.
	hubPatchError string

	mu      sync.Mutex
	reqs    []resumeReq
	hubGETs int
}

func (s *resumeStub) record(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, resumeReq{method: r.Method, path: r.URL.Path, body: body})
	return body
}

// writes returns every non-GET request, in order.
func (s *resumeStub) writes() []resumeReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []resumeReq
	for _, r := range s.reqs {
		if r.method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

// bodies returns the bodies of every request with this method whose path ends
// with suffix.
func (s *resumeStub) bodies(method, suffix string) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for _, r := range s.reqs {
		if r.method == method && strings.HasSuffix(r.path, suffix) {
			out = append(out, r.body)
		}
	}
	return out
}

// blobsPatch returns the attributes of the hub PATCH that carries branding,
// settings or navigation (the blobs step), or nil when the run sent none.
func (s *resumeStub) blobsPatch(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	for _, b := range s.bodies(http.MethodPatch, "/hubs/"+s.hubID) {
		attrs := decodeHubAttrs(t, b)
		_, hasB := attrs["branding"]
		_, hasS := attrs["settings"]
		_, hasN := attrs["navigation"]
		if hasB || hasS || hasN {
			if out != nil {
				t.Fatalf("more than one blobs PATCH: %v and %v", out, attrs)
			}
			out = attrs
		}
	}
	return out
}

func (s *resumeStub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	if s.hubID == "" {
		s.hubID = "hub_r"
	}
	if s.catBody == nil {
		s.catBody = catalog21Body(t)
	}
	orEmpty := func(v string) string {
		if v == "" {
			return "[]"
		}
		return v
	}
	hubSuffix := "/hubs/" + s.hubID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := s.record(r)
		if serveCatalogGET(w, r, s.catBody) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, hubSuffix):
			s.mu.Lock()
			s.hubGETs++
			attrs := s.hubAttrs
			if s.hubAttrsLater != "" && s.hubGETs > 1 {
				attrs = s.hubAttrsLater
			}
			s.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":%s}}`, s.hubID, attrs)
		case r.Method == http.MethodPatch && strings.HasSuffix(p, hubSuffix) && s.hubPatchError != "":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(s.hubPatchError))
		case r.Method == http.MethodPatch && strings.HasSuffix(p, hubSuffix):
			// Echo what was sent (a real PATCH returns the stored hub).
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":%s}}`, s.hubID, s.hubAttrs)
		case r.Method == http.MethodGet && strings.HasSuffix(p, hubSuffix+"/contact-attributes"):
			_, _ = fmt.Fprintf(w, `{"data":%s,"meta":{}}`, orEmpty(s.hubConfigs))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/contact-attributes"):
			_, _ = fmt.Fprintf(w, `{"data":%s}`, orEmpty(s.defs))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pages"):
			_, _ = fmt.Fprintf(w, `{"data":%s}`, orEmpty(s.pages))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(p, "/policies/gate"):
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hub_policy_gate","attributes":{"enabled":true}}}`, s.hubID)
		case r.Method == http.MethodPatch && strings.HasSuffix(p, "/policies"):
			_, _ = w.Write([]byte(`{"data":{"id":"x:tos","type":"policies","attributes":{}}}`))
		case r.Method == http.MethodPut && strings.HasSuffix(p, "/tree"):
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/scaffold-from-template"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/hubs/from-template"):
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.Method == http.MethodPost && strings.HasSuffix(p, hubSuffix+"/contact-attributes"):
			w.WriteHeader(http.StatusCreated)
			var doc struct {
				Data struct {
					Attributes map[string]any `json:"attributes"`
				} `json:"data"`
			}
			_ = json.Unmarshal(body, &doc)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"cfg_new","type":"contact_attribute_hub_configs","attributes":{"definition_id":%q}}}`,
				doc.Data.Attributes["definition_id"])
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/contact-attributes"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"def_new","type":"contact_attribute_definitions","attributes":{}}}`))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"res_new","type":"resources","attributes":{"slug":"homepage"}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// qaHubAttrs is the hub QA reported (MIO-4166): a palette set at create, a
// logo, self-signup CLOSED by the owner, and a menu edited after the scaffold
// — one header item and a mobile bucket, and no footer at all.
const qaHubAttrs = `{
  "slug": "trail", "title": "Trail Academy", "is_private": true,
  "branding": {"primary": "#2F4A2E", "secondary": "#1C2419", "header_accent": "#E8A33D",
               "logo_url": "https://cdn.example.com/trail-logo.png"},
  "settings": {"registration": {"enabled": false}},
  "navigation": {
    "header": [{"type": "url", "label": "Join", "href": "/trail/join", "position": 1}],
    "mobile": [{"id": "m1", "label": "Home", "route": "/", "icon": "Home"}]
  }
}`

// runResume is runContract that also hands back the command's error, so a
// test can assert on the message (root.Execute does not print it; main.go
// does) as well as on the exit code and the captured streams.
func runResume(t *testing.T, env []string, args ...string) (contractResult, error) {
	t.Helper()
	restore := overlayEnv(t, env)
	defer restore()
	resetGlobalFlags()
	root := RootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	defer root.SetArgs(nil)
	err := root.Execute()
	return contractResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: codeForExecuteErr(err)}, err
}

func resumeArgs(hubID string, extra ...string) []string {
	return withTeam("t_team1", append([]string{"hubs", "scaffold", "--template", "community", "--hub", hubID}, extra...)...)
}

// TestScaffoldResume_BlobsKeepWhatTheHubHasAndFillTheRest is the MIO-4166 repro
// one layer below the live hub: a --hub run against QA's hub must PATCH the
// hub's own palette, registration and menu back unchanged, and fill ONLY the
// keys and buckets the hub does not have.
func TestScaffoldResume_BlobsKeepWhatTheHubHasAndFillTheRest(t *testing.T) {
	stub := &resumeStub{hubAttrs: qaHubAttrs}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	patch := stub.blobsPatch(t)
	if patch == nil {
		t.Fatal("the run must PATCH the gaps (header_color, background, footer, …); it sent no blobs PATCH")
	}

	branding, _ := patch["branding"].(map[string]any)
	for k, want := range map[string]string{
		// the hub's own values — the template's are #4F46E5 / #15803D / #A5B4FC / its CDN logo
		"primary":       "#2F4A2E",
		"secondary":     "#1C2419",
		"header_accent": "#E8A33D",
		"logo_url":      "https://cdn.example.com/trail-logo.png",
		// gaps, filled from the template
		"header_color": "#4F46E5",
		"background":   "#FFFFFF",
		"text":         "#111827",
	} {
		if branding[k] != want {
			t.Errorf("branding.%s = %v, want %q; branding=%v", k, branding[k], want, branding)
		}
	}

	settings, _ := patch["settings"].(map[string]any)
	reg, _ := settings["registration"].(map[string]any)
	if reg["enabled"] != false {
		t.Errorf("settings.registration.enabled = %v, want false — the owner closed self-signup and a resume must not reopen it", reg["enabled"])
	}
	if hdr, _ := settings["header"].(map[string]any); hdr["visibility"] != "always" {
		t.Errorf("settings.header (absent on the hub) must be filled from the template; settings=%v", settings)
	}

	nav, _ := patch["navigation"].(map[string]any)
	if nav == nil {
		t.Fatalf("navigation must be PATCHed: the hub has no footer and the template declares one; patch=%v", patch)
	}
	header, _ := nav["header"].([]any)
	if len(header) != 1 || header[0].(map[string]any)["label"] != "Join" {
		t.Errorf("navigation.header = %v, want the hub's own [Join] — a bucket the hub has is never replaced", nav["header"])
	}
	mobile, _ := nav["mobile"].([]any)
	if len(mobile) != 1 {
		t.Errorf("navigation.mobile = %v, want the hub's own item — the template has no mobile bucket and must not delete it", nav["mobile"])
	}
	footer, _ := nav["footer"].([]any)
	if len(footer) != 2 || footer[0].(map[string]any)["href"] != "/trail/about" {
		t.Errorf("navigation.footer = %v, want the template's two items, hub-scoped (/trail/about)", nav["footer"])
	}

	// Kept values are reported on stderr, by path, with the value kept.
	for _, want := range []string{"branding.primary", "#2F4A2E", "settings.registration.enabled", "navigation.header", "--reapply-template"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr must report the kept value %q; stderr=%q", want, res.Stderr)
		}
	}
	if strings.Contains(res.Stderr, "navigation.footer") {
		t.Errorf("navigation.footer was FILLED, not kept — it must not be reported as kept; stderr=%q", res.Stderr)
	}
}

// TestScaffoldResume_InvocationFlagsStillWin: the printed "Resume with:" command
// repeats the operator's flags, so a flag given on a --hub run must still win
// over the hub's own value.
func TestScaffoldResume_InvocationFlagsStillWin(t *testing.T) {
	stub := &resumeStub{hubAttrs: qaHubAttrs}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r",
		"--primary-color", "#B91C1C",
		"--logo-url", "https://cdn.example.com/new-logo.png",
		"--registration-enabled=true")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	patch := stub.blobsPatch(t)
	branding, _ := patch["branding"].(map[string]any)
	if branding["primary"] != "#B91C1C" {
		t.Errorf("branding.primary = %v, want the --primary-color value", branding["primary"])
	}
	if branding["logo_url"] != "https://cdn.example.com/new-logo.png" {
		t.Errorf("branding.logo_url = %v, want the --logo-url value", branding["logo_url"])
	}
	if branding["secondary"] != "#1C2419" {
		t.Errorf("branding.secondary = %v, want the hub's (no flag names it)", branding["secondary"])
	}
	settings, _ := patch["settings"].(map[string]any)
	if reg, _ := settings["registration"].(map[string]any); reg["enabled"] != true {
		t.Errorf("settings.registration.enabled = %v, want true from --registration-enabled", reg["enabled"])
	}
	// A value a flag overwrote was not KEPT, so it must not be reported as kept.
	if strings.Contains(res.Stderr, "branding.primary") || strings.Contains(res.Stderr, "settings.registration.enabled") {
		t.Errorf("a key an invocation flag overwrote must not be reported as kept; stderr=%q", res.Stderr)
	}
}

// completeHubAttrs is a hub that already has every blob value the community
// template declares — the second resume, typically — in a state the backend
// can actually hold. Its settings.policies is {"enabled": true} and nothing
// else: the gate endpoint writes only `enabled`, the hub PATCH pops `policies`
// wholesale (MIO-497), and nothing on the scaffold path ever stores the
// template's settings.policies.show. (The fixture this replaced carried
// "show": true, which no scaffolded hub can reach, and so hid a settings PATCH
// sent on every real --hub run.) Its own primary differs from the template's.
const completeHubAttrs = `{
	  "slug": "trail", "title": "Trail", "is_private": true,
	  "branding": {"logo_url": "https://assets.searchie.io/hub-templates/community/logo.png",
	    "favicon_url": "https://assets.searchie.io/hub-templates/community/favicon.png",
	    "social_image_url": "https://assets.searchie.io/hub-templates/community/social.png",
	    "primary": "#123456", "secondary": "#15803D", "background": "#FFFFFF", "text": "#111827",
	    "header_color": "#4F46E5", "header_accent": "#A5B4FC"},
	  "settings": {"registration": {"enabled": true}, "header": {"visibility": "always", "menuLayout": "tabs"},
	    "menu": {"layout": "tabs"}, "policies": {"enabled": true}},
	  "navigation": {"header": [{"type": "url", "label": "Mine", "href": "/trail/mine"}],
	    "footer": [{"type": "url", "label": "Mine", "href": "/trail/mine"}]}
	}`

// TestScaffoldResume_NothingToFillSendsNoBlobsPatch: a hub that already carries
// every template value (the second resume, typically) gets no blobs write.
func TestScaffoldResume_NothingToFillSendsNoBlobsPatch(t *testing.T) {
	stub := &resumeStub{hubAttrs: completeHubAttrs}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	// ANY hub PATCH, not just one carrying a blob: an empty-attributes PATCH is
	// still a write (it bumps updated_at and the hub's ETag). --publish is off,
	// so nothing else in the run PATCHes the hub itself.
	if p := stub.bodies(http.MethodPatch, "/hubs/"+stub.hubID); len(p) != 0 {
		t.Errorf("nothing is missing on the hub, so no hub PATCH may be sent; got %d, first %s", len(p), p[0])
	}
	if !strings.Contains(res.Stderr, "nothing to fill") {
		t.Errorf("stderr must say there was nothing to fill; stderr=%q", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "branding.primary") {
		t.Errorf("the differing primary must still be reported as kept; stderr=%q", res.Stderr)
	}
}

// TestScaffoldResume_OverrideFlagAloneStillPatches: on a hub with nothing to
// fill, each scalar override flag is by itself a reason to PATCH — "nothing to
// fill" must never swallow a value the operator asked for on this command.
// One flag per subtest, so each of the three is guarded on its own.
func TestScaffoldResume_OverrideFlagAloneStillPatches(t *testing.T) {
	for _, tc := range []struct {
		flag, blob, key string
		want            any
	}{
		{"--logo-url=https://cdn.example.com/new-logo.png", "branding", "logo_url", "https://cdn.example.com/new-logo.png"},
		{"--favicon-url=https://cdn.example.com/new-favicon.png", "branding", "favicon_url", "https://cdn.example.com/new-favicon.png"},
		{"--registration-enabled=false", "settings", "registration.enabled", false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			stub := &resumeStub{hubAttrs: completeHubAttrs}
			srv := stub.serve(t)

			res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r", tc.flag)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			patch := stub.blobsPatch(t)
			if patch == nil {
				t.Fatalf("%s was given, so the hub must be PATCHed even though nothing is missing; no blobs PATCH was sent (stderr=%q)", tc.flag, res.Stderr)
			}
			got := patch[tc.blob]
			for _, seg := range strings.Split(tc.key, ".") {
				m, _ := got.(map[string]any)
				got = m[seg]
			}
			if got != tc.want {
				t.Errorf("%s.%s = %v, want %v from %s", tc.blob, tc.key, got, tc.want, tc.flag)
			}
		})
	}
}

// TestScaffoldResume_StrictTemplateKeysStillChecked: fill-gaps must not make the
// strict template-key check depend on what the hub already has — a template
// key outside the allowlist fails even when the hub carries that key already
// (so the fill would never have sent it). Since MIO-4171 the keys the CLI
// checks at all are branding keys and settings.achievements sub-keys — the
// ones the API stores as sent — so each is pinned here; every other settings
// key is the API's to judge (TestScaffoldResume_TemplateSettingsKeysAreTheAPIsToCheck).
func TestScaffoldResume_StrictTemplateKeysStillChecked(t *testing.T) {
	for _, tc := range []struct {
		name, key, hubAttrs string
		edit                func(ht map[string]any)
	}{
		{
			name: "branding key",
			key:  "branding.not_a_real_branding_key",
			edit: func(ht map[string]any) {
				b, _ := ht["branding"].(map[string]any)
				b["not_a_real_branding_key"] = "x"
			},
			hubAttrs: `{"slug":"trail","title":"T","is_private":true,
			  "branding":{"not_a_real_branding_key":"x"}}`,
		},
		{
			name: "settings.achievements sub-key",
			key:  "settings.achievements.not_a_real_achievements_key",
			edit: func(ht map[string]any) {
				s, _ := ht["settings"].(map[string]any)
				s["achievements"] = map[string]any{"not_a_real_achievements_key": true}
			},
			hubAttrs: `{"slug":"trail","title":"T","is_private":true,
			  "settings":{"achievements":{"not_a_real_achievements_key":true}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &resumeStub{catBody: catalogWithTemplateEdit(t, tc.edit), hubAttrs: tc.hubAttrs}
			srv := stub.serve(t)

			res, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
			if res.Code != errs.ExitUsage {
				t.Fatalf("exit = %d, want %d (a malformed template is caught whatever the hub holds); err=%v", res.Code, errs.ExitUsage, err)
			}
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Errorf("the error must name the bad template key %s; err=%v", tc.key, err)
			}
			if w := stub.writes(); len(w) != 0 {
				t.Errorf("no write may fire; got %d (first %s %s)", len(w), w[0].method, w[0].path)
			}
		})
	}
}

// TestScaffoldResume_TemplateSettingsKeysAreTheAPIsToCheck (MIO-4171 on the
// --hub path): the fill-gaps key check must make the settings checks the
// template-wins apply makes, and no more. A template settings key the API
// accepts but the CLI's retired allowlist never listed (`language`,
// `email.from_localpart`) must reach the fill PATCH, not fail the run with a
// usage error on a hub the operator is trying to repair.
func TestScaffoldResume_TemplateSettingsKeysAreTheAPIsToCheck(t *testing.T) {
	cat := catalogWithTemplateEdit(t, func(ht map[string]any) {
		s, _ := ht["settings"].(map[string]any)
		s["language"] = "de"
		s["email"] = map[string]any{"from_localpart": "news"}
	})
	stub := &resumeStub{catBody: cat, hubAttrs: `{"slug":"trail","title":"T","is_private":true}`}
	srv := stub.serve(t)

	res, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 — settings keys are the API's to judge; err=%v stderr=%q", res.Code, err, res.Stderr)
	}
	patch := stub.blobsPatch(t)
	if patch == nil {
		t.Fatal("the hub lacks the template's settings, so the run must PATCH them; it sent no blobs PATCH")
	}
	settings, _ := patch["settings"].(map[string]any)
	if settings["language"] != "de" {
		t.Errorf("settings.language = %v, want de; settings=%v", settings["language"], settings)
	}
	if e, _ := settings["email"].(map[string]any); e["from_localpart"] != "news" {
		t.Errorf("settings.email.from_localpart = %v, want news; settings=%v", settings["email"], settings)
	}
}

// TestScaffoldResume_KeptNavigationIsNotReScoped: when a --hub run fills one
// navigation bucket it must send the hub's OTHER buckets back as they are —
// the API stores navigation whole — and it must not run them through the
// CLI's hub-scoped href check (MIO-2270). The backend accepts any root-relative
// href (app/hubs/validation.py _validate_nav_href, form 1): a "/content" link,
// a same-origin link to a sibling hub, or a "/old-slug/…" link a slug rename
// left behind. Re-judging those made the run exit 2 at the blobs step, blaming
// an item it had just reported as kept, on a hub the template-wins apply
// handled without complaint.
func TestScaffoldResume_KeptNavigationIsNotReScoped(t *testing.T) {
	for _, tc := range []struct {
		name, hubAttrs, keptHref, filledHref string
	}{
		{
			name: "root-relative href outside the hub",
			hubAttrs: `{"slug":"trail","title":"Trail","is_private":true,
			  "navigation":{"header":[{"type":"url","label":"Courses","href":"/content"}]}}`,
			keptHref: "/content", filledHref: "/trail/about",
		},
		{
			name: "href left behind by a slug rename",
			hubAttrs: `{"slug":"trail-academy","title":"Trail","is_private":true,
			  "navigation":{"header":[{"type":"url","label":"About","href":"/trail/about"}],"footer":[]}}`,
			keptHref: "/trail/about", filledHref: "/trail-academy/about",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &resumeStub{hubAttrs: tc.hubAttrs}
			srv := stub.serve(t)

			res, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0 — the hub's own menu is not this run's to re-validate; err=%v", res.Code, err)
			}
			nav, _ := stub.blobsPatch(t)["navigation"].(map[string]any)
			header, _ := nav["header"].([]any)
			if len(header) != 1 || header[0].(map[string]any)["href"] != tc.keptHref {
				t.Errorf("navigation.header = %v, want the hub's own item with href %q, unchanged", nav["header"], tc.keptHref)
			}
			footer, _ := nav["footer"].([]any)
			if len(footer) == 0 || footer[0].(map[string]any)["href"] != tc.filledHref {
				t.Errorf("navigation.footer = %v, want the template's footer filled and hub-scoped (%q)", nav["footer"], tc.filledHref)
			}
		})
	}
}

// TestScaffoldResume_DanglingPageInKeptBucketNamesTheWayOut: the API refuses
// every navigation write while a type=page item points at a deleted page (page
// deletion does not cascade to the menu), so filling one bucket on such a hub
// is a 422 about an item the run is only carrying back. The run must not drop
// that item to get through; it stops with the API's verdict, and says the item
// is on the hub's own menu and how to remove it, instead of leaving the
// printed "Resume with:" command to hit the same wall. (The 422 body is the
// one mio-backend returned live for this hub state.)
func TestScaffoldResume_DanglingPageInKeptBucketNamesTheWayOut(t *testing.T) {
	stub := &resumeStub{
		hubAttrs: `{"slug":"trail","title":"Trail","is_private":true,
		  "navigation":{"footer":[{"type":"page","label":"Extra","page_id":"01a0d36a-019d-7f82-9e5d-27c4be96164e","position":0}]}}`,
		hubPatchError: `{"errors":[{"status":"422","code":"navigation_page_invalid","title":"NavigationPageInvalidError",
		  "detail":"Page '01a0d36a-019d-7f82-9e5d-27c4be96164e' is not a valid page for this hub's navigation.",
		  "source":{"pointer":"/data/attributes/navigation/footer/0/page_id"}}]}`,
	}
	srv := stub.serve(t)

	res, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (the API's 422); err=%v", res.Code, errs.ExitUsage, err)
	}
	for _, want := range []string{
		"is not a valid page for this hub's navigation", // the API's own words survive
		"hub's own navigation bucket(s) [footer]",
		"mio hubs navigation remove hub_r <bucket> --index <n>",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the error must say %q; err=%v", want, err)
		}
	}
	if n := len(stub.bodies(http.MethodPatch, "/hubs/hub_r")); n != 1 {
		t.Errorf("want exactly the one refused blobs PATCH (no retry that drops the item); got %d", n)
	}
}

// TestScaffoldResume_TemplateNavHrefsStillChecked: the other half of the rule
// above. The template's OWN navigation is still hub-scope-checked in full,
// whatever the hub holds — even when every bucket it declares is kept and so
// never sent — exactly as StrictTemplateKeysStillChecked does for blob keys.
func TestScaffoldResume_TemplateNavHrefsStillChecked(t *testing.T) {
	cat := catalogWithTemplateEdit(t, func(ht map[string]any) {
		nav, _ := ht["navigation"].(map[string]any)
		footer, _ := nav["footer"].([]any)
		footer[0].(map[string]any)["href"] = "/../escape" // scoped to /trail/../escape, which resolves to /escape
	})
	stub := &resumeStub{catBody: cat, hubAttrs: completeHubAttrs}
	srv := stub.serve(t)

	res, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (a template href escaping the hub is caught whatever the hub holds); err=%v", res.Code, errs.ExitUsage, err)
	}
	if err == nil || !strings.Contains(err.Error(), "navigation.footer[0]") {
		t.Errorf("the error must name the template's footer item; err=%v", err)
	}
	if w := stub.writes(); len(w) != 0 {
		t.Errorf("no write may fire; got %d (first %s %s)", len(w), w[0].method, w[0].path)
	}
}

// ─── policies (MIO-2818) ─────────────────────────────────────────────────────

// customToSHub carries a hand-written ToS (the stored content is what the
// backend serves instead of the default) and an owner-set gate of false.
const customToSHub = `{"slug":"trail","title":"Trail","is_private":true,
  "settings":{"policies":{"enabled":false,
    "tos":{"content":"Our own terms.","version":"v_24c5","require_acceptance":true}}}}`

// qaHubWithCustomPolicies is QA's hub with a hand-written ToS and an owner-set
// gate of false as well.
var qaHubWithCustomPolicies = strings.Replace(qaHubAttrs, `"settings": {"registration": {"enabled": false}}`,
	`"settings": {"registration": {"enabled": false}, "policies": {"enabled": false, "tos": {"content": "Our own terms."}}}`, 1)

// TestScaffoldResume_PoliciesNeverRevertCustomText: the community template
// declares NO policy text. On a --hub run that must never become content:null
// — that is exactly the write that replaced QA's ToS with the platform default
// and re-prompted every member.
func TestScaffoldResume_PoliciesNeverRevertCustomText(t *testing.T) {
	stub := &resumeStub{hubAttrs: customToSHub}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	for _, b := range stub.bodies(http.MethodPatch, "/policies") {
		t.Errorf("a --hub run must not write a policy the template declares no text for; sent %s", b)
	}
	if n := len(stub.bodies(http.MethodPatch, "/policies/gate")); n != 0 {
		t.Errorf("the hub's gate is set (enabled=false) — a --hub run must not flip it; got %d gate PATCH(es)", n)
	}
	for _, want := range []string{"own tos text", "enabled=false", "mio hubs policies gate hub_r --enabled"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr must report the kept policy state (%q); stderr=%q", want, res.Stderr)
		}
	}
	if got := decodeSoleJSON(t, res.Stdout)["policy_gate"]; got != nil {
		t.Errorf("result.policy_gate = %v, want null — this run wrote no gate", got)
	}
	// The blobs step must not claim to have "kept" settings.policies: the backend
	// pops `policies` from every hub PATCH, so the template could never have
	// changed it there. The gate is reported by the policies step alone.
	if strings.Contains(res.Stderr, "settings.policies") {
		t.Errorf("settings.policies is not the blobs step's to keep or change; stderr=%q", res.Stderr)
	}
}

// TestScaffoldResume_PoliciesFillOnlyUnconfiguredText: template-declared text is
// written to a policy the hub has not configured, and ONLY to that one. The
// stored content is the backend's own "unconfigured" signal: the admin read
// renders the platform default exactly when it is null.
func TestScaffoldResume_PoliciesFillOnlyUnconfiguredText(t *testing.T) {
	cat := catalogWithPolicies(t, map[string]any{
		"terms":          map[string]any{"content": "Template terms.", "required": true, "enabled": true},
		"privacy_policy": map[string]any{"content": "Template privacy.", "enabled": true},
	})
	// tos is hand-written; privacy_policy was reset (content null) — a gap.
	stub := &resumeStub{catBody: cat, hubAttrs: `{"slug":"trail","title":"Trail","is_private":true,
	  "settings":{"policies":{"tos":{"content":"Our own terms."},"privacy_policy":{"content":null}}}}`}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	var sent []map[string]any
	for _, b := range stub.bodies(http.MethodPatch, "/policies") {
		sent = append(sent, decodeHubAttrs(t, b))
	}
	if len(sent) != 1 {
		t.Fatalf("want exactly one policy write (privacy_policy, the unconfigured one); got %v", sent)
	}
	if sent[0]["policy_type"] != "privacy_policy" || sent[0]["content"] != "Template privacy." {
		t.Errorf("policy write = %v, want privacy_policy with the template's text", sent[0])
	}
	// The gate is absent on the hub — a gap — so it IS written.
	gates := stub.bodies(http.MethodPatch, "/policies/gate")
	if len(gates) != 1 || decodeHubAttrs(t, gates[0])["enabled"] != true {
		t.Errorf("an unset gate is a gap and must be enabled once; got %d gate PATCH(es)", len(gates))
	}
	if !strings.Contains(res.Stderr, "own tos text") || !strings.Contains(res.Stderr, "--reapply-template") {
		t.Errorf("stderr must report the kept tos text and name the way to overwrite it; stderr=%q", res.Stderr)
	}
}

// TestScaffoldResume_PoliciesDecideOnTheReadBeforeTheWrite: the policies step
// decides against a read it makes right before it writes, not against the
// resume read the plan was made from. Here the owner writes their own ToS
// after that read: the template's text must not be written over it.
func TestScaffoldResume_PoliciesDecideOnTheReadBeforeTheWrite(t *testing.T) {
	cat := catalogWithPolicies(t, map[string]any{
		"terms":          map[string]any{"content": "Template terms.", "required": true, "enabled": true},
		"privacy_policy": map[string]any{"enabled": true},
	})
	stub := &resumeStub{catBody: cat,
		hubAttrs: `{"slug":"trail","title":"Trail","is_private":true,"settings":{"policies":{"enabled":true}}}`,
		hubAttrsLater: `{"slug":"trail","title":"Trail","is_private":true,
		  "settings":{"policies":{"enabled":true,"tos":{"content":"Terms the owner wrote meanwhile."}}}}`}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	for _, b := range stub.bodies(http.MethodPatch, "/policies") {
		t.Errorf("the hub had its own ToS by the time the step wrote — nothing may be written over it; sent %s", b)
	}
	if !strings.Contains(res.Stderr, "kept the hub's own tos text") {
		t.Errorf("stderr must report the ToS text as kept; stderr=%q", res.Stderr)
	}
}

// TestScaffoldResume_NonBooleanGateIsAGap: only a JSON boolean is a gate the
// hub has set. `hubs create --settings-json '{"policies":{"enabled":"true"}}'`
// can store a string there (the create path keeps the gate flags, and the
// backend's settings validator checks keys, not value types), and the backend
// reads the gate with `is True` — so that hub's enforcement is OFF, it was
// never set as a boolean, and a --hub run fills it like any other gap.
func TestScaffoldResume_NonBooleanGateIsAGap(t *testing.T) {
	stub := &resumeStub{hubAttrs: `{"slug":"trail","title":"Trail","is_private":true,
	  "settings":{"policies":{"enabled":"true"}}}`}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	gates := stub.bodies(http.MethodPatch, "/policies/gate")
	if len(gates) != 1 || decodeHubAttrs(t, gates[0])["enabled"] != true {
		t.Fatalf("a non-boolean stored gate is unset as far as the backend is concerned; want one gate PATCH enabling it, got %d (stderr=%q)", len(gates), res.Stderr)
	}
	if got := decodeSoleJSON(t, res.Stdout)["policy_gate"]; got != true {
		t.Errorf("result.policy_gate = %v, want true — this run wrote the gate", got)
	}
}

// ─── onboarding hub-config ───────────────────────────────────────────────────

// TestScaffoldResume_OnboardingKeepsExistingHubConfig: a (hub, definition)
// hub-config row the hub already has is not re-upserted; a missing one is
// created.
func TestScaffoldResume_OnboardingKeepsExistingHubConfig(t *testing.T) {
	stub := &resumeStub{
		hubAttrs: `{"slug":"trail","title":"Trail","is_private":true}`,
		defs: `[{"id":"def_company","type":"contact_attribute_definitions","attributes":{"slug":"company"}},
		        {"id":"def_role","type":"contact_attribute_definitions","attributes":{"slug":"role"}}]`,
		hubConfigs: `[{"id":"cfg_1","type":"contact_attribute_hub_configs","attributes":
		        {"definition_id":"def_company","is_in_onboarding":false,"is_required":true}}]`,
	}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	var posted []string
	for _, b := range stub.bodies(http.MethodPost, "/hubs/hub_r/contact-attributes") {
		posted = append(posted, fmt.Sprint(decodeHubAttrs(t, b)["definition_id"]))
	}
	if len(posted) != 1 || posted[0] != "def_role" {
		t.Errorf("hub-config upserts = %v, want only [def_role] — def_company's existing config must be kept", posted)
	}
	if !strings.Contains(res.Stderr, `config for "company"`) {
		t.Errorf("stderr must report the kept onboarding config; stderr=%q", res.Stderr)
	}
}

// ─── pages: checked before any write ─────────────────────────────────────────

// TestScaffoldResume_PageConflictFailsBeforeAnyWrite: a foreign page at a
// template slug used to exit 2 at step 7 — AFTER the blobs, spaces, onboarding
// and policies had been written. On --hub it is checked in the preflight, so
// the run exits 2 having written nothing.
func TestScaffoldResume_PageConflictFailsBeforeAnyWrite(t *testing.T) {
	stub := &resumeStub{
		hubAttrs: qaHubAttrs,
		pages:    `[{"id":"page_foreign","type":"pages","attributes":{"slug":"about","is_homepage":false}}]`,
	}
	srv := stub.serve(t)

	res, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d; err=%v", res.Code, errs.ExitUsage, err)
	}
	if w := stub.writes(); len(w) != 0 {
		t.Errorf("a page conflict on --hub must stop the run before ANY write; got %d write(s), first %s %s", len(w), w[0].method, w[0].path)
	}
	for _, want := range []string{`"about"`, "page_foreign", "nothing was written"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %q; err=%v", want, err)
		}
	}
}

// TestScaffoldResume_HomepageConflictFailsBeforeAnyWrite: the other half of the
// pages preflight. A hub whose homepage lives at a slug the template does not
// use has no page at any template slug, so no slug conflict is found; but the
// template's homepage entry would still clear that homepage server-side. That
// too must stop a --hub run before its first write.
func TestScaffoldResume_HomepageConflictFailsBeforeAnyWrite(t *testing.T) {
	stub := &resumeStub{
		hubAttrs: qaHubAttrs,
		pages:    `[{"id":"page_home_own","type":"pages","attributes":{"slug":"welcome","is_homepage":true}}]`,
	}
	srv := stub.serve(t)

	res, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d; err=%v", res.Code, errs.ExitUsage, err)
	}
	if w := stub.writes(); len(w) != 0 {
		t.Errorf("an existing homepage on --hub must stop the run before ANY write; got %d write(s), first %s %s", len(w), w[0].method, w[0].path)
	}
	for _, want := range []string{"existing homepage", "page_home_own", "nothing was written"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %q; err=%v", want, err)
		}
	}
}

// ─── --reapply-template ──────────────────────────────────────────────────────

// TestScaffoldResume_ReapplyTemplateOverwrites: the explicit opt-in restores the
// template-wins apply — palette, registration, the whole navigation blob, the
// policy text reset and the gate.
func TestScaffoldResume_ReapplyTemplateOverwrites(t *testing.T) {
	stub := &resumeStub{hubAttrs: qaHubWithCustomPolicies}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r", "--reapply-template", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	patch := stub.blobsPatch(t)
	branding, _ := patch["branding"].(map[string]any)
	if branding["primary"] != "#4F46E5" {
		t.Errorf("--reapply-template: branding.primary = %v, want the template's #4F46E5", branding["primary"])
	}
	settings, _ := patch["settings"].(map[string]any)
	if reg, _ := settings["registration"].(map[string]any); reg["enabled"] != true {
		t.Errorf("--reapply-template: registration.enabled = %v, want the template's true", reg["enabled"])
	}
	nav, _ := patch["navigation"].(map[string]any)
	if _, hasMobile := nav["mobile"]; hasMobile || len(nav["header"].([]any)) != 4 {
		t.Errorf("--reapply-template replaces the navigation blob with the template's; nav=%v", nav)
	}
	if n := len(stub.bodies(http.MethodPatch, "/policies")); n != 2 {
		t.Errorf("--reapply-template writes every template policy; got %d policy PATCH(es)", n)
	}
	if n := len(stub.bodies(http.MethodPatch, "/policies/gate")); n != 1 {
		t.Errorf("--reapply-template writes the declared gate; got %d gate PATCH(es)", n)
	}
}

// TestScaffoldResume_ReapplyTemplateIsDestructive: off a TTY without --yes the
// opt-in exits 5 and sends NOTHING — not even the catalog read.
func TestScaffoldResume_ReapplyTemplateIsDestructive(t *testing.T) {
	stub := &resumeStub{hubAttrs: qaHubAttrs}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r", "--reapply-template")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Fatalf("exit = %d, want %d (destructive, non-TTY, no --yes); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if n := len(stub.reqs); n != 0 {
		t.Errorf("a refused --reapply-template must fire no request at all; got %d", n)
	}
}

// TestScaffoldResume_ReapplyTemplateNeedsHub: without --hub there is nothing to
// re-apply onto — a usage error before any request.
func TestScaffoldResume_ReapplyTemplateNeedsHub(t *testing.T) {
	stub := &resumeStub{hubAttrs: qaHubAttrs}
	srv := stub.serve(t)

	res, err := runResume(t, scaffoldEnv(t, srv.URL), withTeam("t_team1", "hubs", "scaffold",
		"--template", "community", "--name", "X", "--slug", "x", "--reapply-template", "--yes")...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d; err=%v", res.Code, errs.ExitUsage, err)
	}
	if err == nil || !strings.Contains(err.Error(), "--hub") {
		t.Errorf("the usage error must name --hub; err=%v", err)
	}
	if n := len(stub.reqs); n != 0 {
		t.Errorf("the usage error must fire no request; got %d", n)
	}
}

// TestScaffoldResume_DryRunPlanReportsKeptValues: kept values are named in the
// --dry-run plan too, and the dry run writes nothing.
func TestScaffoldResume_DryRunPlanReportsKeptValues(t *testing.T) {
	stub := &resumeStub{
		hubAttrs: qaHubWithCustomPolicies,
		defs:     `[{"id":"def_company","type":"contact_attribute_definitions","attributes":{"slug":"company"}}]`,
		hubConfigs: `[{"id":"cfg_1","type":"contact_attribute_hub_configs","attributes":
		        {"definition_id":"def_company","is_in_onboarding":false,"is_required":true}}]`,
	}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), humanScaffold(resumeArgs("hub_r", "--dry-run"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if w := stub.writes(); len(w) != 0 {
		t.Errorf("a dry run writes nothing; got %d write(s)", len(w))
	}
	for _, want := range []string{
		"fill gaps", "branding.primary", "navigation.header", "settings.registration.enabled", // blobs
		"own text for [tos (14 characters)]", "enabled=false", // policies + gate
		`config for "company"`, // onboarding
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("the dry-run plan must report %q; stdout:\n%s", want, res.Stdout)
		}
	}
}

// TestScaffoldResume_ReapplyTemplateDryRunNeedsNoConfirmation: a dry run writes
// nothing, so --reapply-template --dry-run previews without --yes — and says
// the template will overwrite.
func TestScaffoldResume_ReapplyTemplateDryRunNeedsNoConfirmation(t *testing.T) {
	stub := &resumeStub{hubAttrs: qaHubAttrs}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), humanScaffold(resumeArgs("hub_r", "--reapply-template", "--dry-run"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 (a dry run is not destructive); stderr=%q", res.Code, res.Stderr)
	}
	if w := stub.writes(); len(w) != 0 {
		t.Errorf("a dry run writes nothing; got %d write(s)", len(w))
	}
	if !strings.Contains(res.Stdout, "--reapply-template: the template's values overwrite the hub's") {
		t.Errorf("the plan must say the template overwrites; stdout:\n%s", res.Stdout)
	}
}

// TestStepBlobs_FillGapsDecidesAndMergesOnOneRead: the fill is computed from a
// read and merged onto THAT read — exactly one hub GET and one PATCH. A second
// GET between the decision and the merge could see a key the fill decided was
// missing.
func TestStepBlobs_FillGapsDecidesAndMergesOnOneRead(t *testing.T) {
	var gets, patches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
		case http.MethodPatch:
			patches++
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme","branding":{"primary":"#111"}}}}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	sc.resume = true
	tmpl := &catalog.HubTemplate{ID: "community", Branding: map[string]any{"primary": "#222", "text": "#333"}}
	if err := stepBlobs(sc, tmpl); err != nil {
		t.Fatalf("stepBlobs: %v", err)
	}
	if gets != 1 || patches != 1 {
		t.Errorf("want exactly 1 GET + 1 PATCH (decide and merge on one read); got %d GET, %d PATCH", gets, patches)
	}
}

// TestFillMissing_Semantics pins what counts as a gap: an absent key and a null
// are gaps; a present value of any other kind (false, "", [], a scalar where
// the template has an object) is the hub's and is kept; objects both sides
// have are recursed into.
func TestFillMissing_Semantics(t *testing.T) {
	tmpl := map[string]any{
		"absent":  "t",
		"null":    "t",
		"false":   true,
		"empty":   "t",
		"arr":     []any{"t"},
		"scalar":  map[string]any{"x": "t"},
		"nested":  map[string]any{"keep": "t", "fill": "t"},
		"tplNull": nil,
	}
	cur := map[string]any{
		"null":   nil,
		"false":  false,
		"empty":  "",
		"arr":    []any{},
		"scalar": "hub",
		"nested": map[string]any{"keep": "hub"},
	}
	var kept []keptValue
	got := fillMissing("b", tmpl, cur, &kept)
	want := map[string]any{"absent": "t", "null": "t", "nested": map[string]any{"fill": "t"}}
	if !sameJSON(got, want) {
		t.Errorf("fill = %v, want %v", got, want)
	}
	var paths []string
	for _, k := range kept {
		paths = append(paths, k.path)
	}
	if strings.Join(paths, ",") != "b.arr,b.empty,b.false,b.nested.keep,b.scalar" {
		t.Errorf("kept = %v, want every present-and-different value, sorted", paths)
	}
}

// TestFillNavigation_EmptyBucketIsAGap: an empty bucket is filled like an absent
// one; a bucket the template does not declare is never dropped.
func TestFillNavigation_EmptyBucketIsAGap(t *testing.T) {
	tmpl := map[string]any{"header": []any{"t1"}, "footer": []any{"t2"}}
	cur := map[string]any{"header": []any{}, "mobile": []any{"m"}}
	var kept []keptValue
	got := fillNavigation(tmpl, cur, &kept)
	want := map[string]any{"header": []any{"t1"}, "footer": []any{"t2"}, "mobile": []any{"m"}}
	if !sameJSON(got, want) {
		t.Errorf("navigation = %v, want %v", got, want)
	}
	if got := fillNavigation(tmpl, map[string]any{"header": []any{"h"}, "footer": []any{"f"}}, &kept); got != nil {
		t.Errorf("no bucket to fill must send no navigation at all; got %v", got)
	}
}

// TestPrintScaffoldRecovery_EchoesReapplyTemplate: a --reapply-template run that
// dies mid-pipeline prints a resume command that keeps re-applying — without it
// the resume would silently switch to fill-gaps half way through.
func TestPrintScaffoldRecovery_EchoesReapplyTemplate(t *testing.T) {
	var buf strings.Builder
	printScaffoldRecovery(&buf, &scaffoldContext{hubID: "hub_r", teamID: "t1", resume: true, reapplyTemplate: true}, "community")
	if !strings.Contains(buf.String(), "--reapply-template") {
		t.Errorf("resume command must echo --reapply-template; got %q", buf.String())
	}
	buf.Reset()
	printScaffoldRecovery(&buf, &scaffoldContext{hubID: "hub_r", teamID: "t1", resume: true}, "community")
	if strings.Contains(buf.String(), "--reapply-template") {
		t.Errorf("a fill-gaps run's resume command must not add --reapply-template; got %q", buf.String())
	}
}

// catalogWithTemplateEdit rewrites the 2.1 fixture's community hubTemplate with
// edit and re-digests it (the same shape catalogWithPolicies builds).
func catalogWithTemplateEdit(t *testing.T, edit func(ht map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(catalogWithPolicies(t, map[string]any{
		"terms":          map[string]any{"required": true, "enabled": true},
		"privacy_policy": map[string]any{"enabled": true},
	}), &doc); err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	hts, _ := doc["hubTemplates"].([]any)
	ht, _ := hts[0].(map[string]any)
	edit(ht)
	return redigestCatalog(t, doc)
}

// redigestCatalog recomputes meta.digest over doc and marshals it, so an edited
// fixture passes the same digest verification the live artifact does.
func redigestCatalog(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	meta, ok := doc["meta"].(map[string]any)
	if !ok {
		t.Fatal("catalog has no meta object")
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
