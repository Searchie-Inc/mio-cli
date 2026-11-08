package cmd

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

type resumeReq struct {
	method, path string
	body         []byte
}

type resumeStub struct {
	hubID         string
	hubAttrs      string
	catBody       []byte
	hubConfigs    string
	defs          string
	pages         string
	hubAttrsLater string
	hubPatchError string
	opError       string

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
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hubs","attributes":%s}}`, s.hubID, s.hubAttrs)
		case r.Method == http.MethodGet && strings.HasSuffix(p, hubSuffix+"/contact-attributes"):
			_, _ = fmt.Fprintf(w, `{"data":%s,"meta":{}}`, orEmpty(s.hubConfigs))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/contact-attributes"):
			_, _ = fmt.Fprintf(w, `{"data":%s}`, orEmpty(s.defs))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pages"):
			_, _ = fmt.Fprintf(w, `{"data":%s}`, orEmpty(s.pages))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/tree"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"no draft"}]}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(p, "/policies/gate"):
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"hub_policy_gate","attributes":{"enabled":true}}}`, s.hubID)
		case r.Method == http.MethodPatch && strings.HasSuffix(p, "/policies"):
			_, _ = w.Write([]byte(`{"data":{"id":"x:tos","type":"policies","attributes":{}}}`))
		case r.Method == http.MethodPut && strings.HasSuffix(p, "/tree"):
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/scaffold-from-template"):
			if s.opError != "" {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(s.opError))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
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
		"primary":       "#2F4A2E",
		"secondary":     "#1C2419",
		"header_accent": "#E8A33D",
		"logo_url":      "https://cdn.example.com/trail-logo.png",
		"header_color":  "#4F46E5",
		"background":    "#FFFFFF",
		"text":          "#111827",
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

	for _, want := range []string{"branding.primary", "#2F4A2E", "settings.registration.enabled", "navigation.header", "--reapply-template"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr must report the kept value %q; stderr=%q", want, res.Stderr)
		}
	}
	if strings.Contains(res.Stderr, "navigation.footer") {
		t.Errorf("navigation.footer was FILLED, not kept — it must not be reported as kept; stderr=%q", res.Stderr)
	}
}

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
	if strings.Contains(res.Stderr, "branding.primary") || strings.Contains(res.Stderr, "settings.registration.enabled") {
		t.Errorf("a key an invocation flag overwrote must not be reported as kept; stderr=%q", res.Stderr)
	}
}

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

func TestScaffoldResume_NothingToFillSendsNoBlobsPatch(t *testing.T) {
	stub := &resumeStub{hubAttrs: completeHubAttrs}
	srv := stub.serve(t)

	res := runContract(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
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
		"is not a valid page for this hub's navigation",
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

func TestScaffoldResume_TemplateNavHrefsStillChecked(t *testing.T) {
	cat := catalogWithTemplateEdit(t, func(ht map[string]any) {
		nav, _ := ht["navigation"].(map[string]any)
		footer, _ := nav["footer"].([]any)
		footer[0].(map[string]any)["href"] = "/../escape"
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

const customToSHub = `{"slug":"trail","title":"Trail","is_private":true,
  "settings":{"policies":{"enabled":false,
    "tos":{"content":"Our own terms.","version":"v_24c5","require_acceptance":true}}}}`

var qaHubWithCustomPolicies = strings.Replace(qaHubAttrs, `"settings": {"registration": {"enabled": false}}`,
	`"settings": {"registration": {"enabled": false}, "policies": {"enabled": false, "tos": {"content": "Our own terms."}}}`, 1)

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
	if strings.Contains(res.Stderr, "settings.policies") {
		t.Errorf("settings.policies is not the blobs step's to keep or change; stderr=%q", res.Stderr)
	}
}

func TestScaffoldResume_PoliciesFillOnlyUnconfiguredText(t *testing.T) {
	cat := catalogWithPolicies(t, map[string]any{
		"terms":          map[string]any{"content": "Template terms.", "required": true, "enabled": true},
		"privacy_policy": map[string]any{"content": "Template privacy.", "enabled": true},
	})
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
	gates := stub.bodies(http.MethodPatch, "/policies/gate")
	if len(gates) != 1 || decodeHubAttrs(t, gates[0])["enabled"] != true {
		t.Errorf("an unset gate is a gap and must be enabled once; got %d gate PATCH(es)", len(gates))
	}
	if !strings.Contains(res.Stderr, "own tos text") || !strings.Contains(res.Stderr, "--reapply-template") {
		t.Errorf("stderr must report the kept tos text and name the way to overwrite it; stderr=%q", res.Stderr)
	}
}

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

func TestScaffoldResume_UnhomedHomepageWouldClearForeignHomepage(t *testing.T) {
	m := ourMarker("applied")
	m["applicationId"] = catalog.ApplicationID("hub_r", "community")
	marker, _ := json.Marshal(m)
	stub := &resumeStub{
		hubAttrs: qaHubAttrs,
		pages: fmt.Sprintf(`[{"id":"page_ours","type":"pages","attributes":{"slug":"homepage","is_homepage":false,"meta":{"template_provenance":%s}}},
		                     {"id":"page_legacy","type":"pages","attributes":{"slug":"welcome","is_homepage":true}}]`,
			marker),
	}
	srv := stub.serve(t)

	res, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d; err=%v", res.Code, errs.ExitUsage, err)
	}
	if w := stub.writes(); len(w) != 0 {
		t.Errorf("must stop before ANY write; got %d write(s), first %s %s", len(w), w[0].method, w[0].path)
	}
	if err == nil || !strings.Contains(err.Error(), "page_legacy") {
		t.Errorf("error must name page_legacy; err=%v", err)
	}
}

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
		"fill gaps", "branding.primary", "navigation.header", "settings.registration.enabled",
		"own text for [tos (14 characters)]", "enabled=false",
		`config for "company"`,
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("the dry-run plan must report %q; stdout:\n%s", want, res.Stdout)
		}
	}
}

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

func TestScaffoldFailureClaims(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sc           scaffoldContext
		want, absent string
	}{
		{"before_hub", scaffoldContext{}, `scaffold: step "pages" failed: boom`, "was created"},
		{"create", scaffoldContext{hubID: "hub_new"}, "hub hub_new was created and is NOT rolled back", ""},
		{"resume", scaffoldContext{hubID: "hub_r", resume: true}, "hub hub_r", "was created"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := scaffoldStepError(&tc.sc, "pages", fmt.Errorf("boom")).Error()
			if !strings.Contains(msg, tc.want) || (tc.absent != "" && strings.Contains(msg, tc.absent)) {
				t.Errorf("failure claim = %q, want %q and absent %q", msg, tc.want, tc.absent)
			}
			if tc.sc.resume && strings.Contains(msg, "rolled back") {
				t.Errorf("resume must not claim rollback state: %s", msg)
			}
			var b strings.Builder
			printScaffoldRecovery(&b, &tc.sc, "community")
			if tc.sc.hubID != "" && !strings.Contains(b.String(), "Resume with:") {
				t.Errorf("missing resume command: %s", b.String())
			}
			if tc.sc.resume && strings.Contains(b.String(), "was created") {
				t.Errorf("resume recovery claims creation: %s", b.String())
			}
			if tc.name == "create" && !strings.Contains(b.String(), "was created") {
				t.Errorf("create recovery must claim creation: %s", b.String())
			}
		})
	}
}

func TestScaffoldPagesOpRejectionGuidance(t *testing.T) {
	for _, tc := range []struct{ name, code, detail, want, absent string }{
		{"page_conflict", "page_conflict", "page conflict at slug 'about'", "inspect the hub's pages", "digest"},
		{"reserved_page_conflict", "reserved_page_conflict", "reserved slug 'about'", "inspect the hub's pages", "digest"},
		{"catalog_digest_mismatch", "catalog_digest_mismatch", "catalog rejected", "differs from the one this run sent", "inspect the hub's pages"},
		{"invalid_scaffold_request", "invalid_scaffold_request", "invalid tree", "template", "digest"},
		{"template_not_scaffoldable", "template_not_scaffoldable", "template unavailable", "template", "digest"},
		{"unknown", "some_future_code", "backend specific detail", "backend specific detail", "invented advice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MIO_CATALOG_CACHE_DIR", t.TempDir())
			body, _ := json.Marshal(map[string]any{"errors": []map[string]string{{"status": "409", "code": tc.code, "detail": tc.detail}}})
			stub := &resumeStub{hubAttrs: qaHubAttrs, opError: string(body)}
			srv := stub.serve(t)
			_, err := runResume(t, scaffoldEnv(t, srv.URL), resumeArgs("hub_r")...)
			if err == nil {
				t.Fatal("expected op rejection")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.detail) || !strings.Contains(msg, tc.want) || strings.Contains(msg, tc.absent) {
				t.Errorf("%s guidance = %q; want detail %q and %q, absent %q", tc.name, msg, tc.detail, tc.want, tc.absent)
			}
			if (tc.name == "page_conflict" || tc.name == "reserved_page_conflict") && strings.Contains(msg, "re-run") {
				t.Errorf("%s must not suggest re-run: %s", tc.name, msg)
			}
			if tc.name == "unknown" && !strings.HasSuffix(msg, tc.detail) {
				t.Errorf("unknown code must end with backend detail, without appended advice: %s", msg)
			}
			if tc.name == "catalog_digest_mismatch" && !strings.Contains(msg, "re-run") {
				t.Errorf("digest guidance must say re-run: %s", msg)
			}
		})
	}
}
