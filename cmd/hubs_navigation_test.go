package cmd

// hubs_navigation_test.go — contract tests for authoring the hub navigation
// menu via `mio hubs update --navigation-json` and the shared typed-item
// validation (MIO-2255). The mio-hub parser silently drops header/footer items
// that lack a "type", so the CLI rejects untyped items up front rather than
// letting a caller ship a menu that renders empty.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// hubSlugRetrieveServer answers a GET retrieve with a hub whose slug is `slug`
// and captures the subsequent PATCH's method, path and body. `hubs update
// --navigation-json` now retrieves the hub for its slug before PATCHing so it
// can validate hub-scoped navigation hrefs (MIO-2270), so nav-update tests must
// capture the PATCH specifically rather than the first request.
func hubSlugRetrieveServer(t *testing.T, slug string) (srv *httptest.Server, patchMethod, patchPath *string, patchBody *[]byte) {
	t.Helper()
	var m, p string
	var b []byte
	body := fmt.Sprintf(`{"data":{"id":"hub_abc123","type":"hubs","attributes":{"title":"H","slug":%q,"is_private":false}}}`, slug)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			m = r.Method
			p = r.URL.Path
			b, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &m, &p, &b
}

// TestHubsUpdate_NavigationJSONFlag verifies --navigation-json is sent as
// data.attributes.navigation on the PATCH, with the typed header items intact.
// The update first retrieves the hub for its slug (MIO-2270), so the hrefs are
// scoped to the retrieved slug ("demo") and the PATCH — not the retrieve — is
// captured.
func TestHubsUpdate_NavigationJSONFlag(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := hubSlugRetrieveServer(t, "demo")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--navigation-json", `{"header":[{"type":"url","label":"Home","href":"/demo/","position":0}],"footer":[{"type":"url","label":"Privacy","href":"/demo/privacy","position":0}]}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", *gotMethod)
	}
	if !strings.Contains(*gotPath, "hub_abc123") {
		t.Errorf("path %q does not contain hub_abc123", *gotPath)
	}

	attrs := decodeHubAttrs(t, *gotBody)
	nav, ok := attrs["navigation"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.navigation is absent or not an object; attrs=%v", attrs)
	}
	header, ok := nav["header"].([]any)
	if !ok || len(header) != 1 {
		t.Fatalf("navigation.header should be a 1-item array; got %#v", nav["header"])
	}
	if item, _ := header[0].(map[string]any); item["type"] != "url" || item["label"] != "Home" {
		t.Errorf("navigation.header[0] = %#v, want a url item labeled Home", header[0])
	}
}

// TestHubsUpdate_NavigationRejectsUntypedItem verifies a header/footer item
// missing "type" is rejected with ExitUsage and fires NO HTTP request.
func TestHubsUpdate_NavigationRejectsUntypedItem(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			// legacy untyped item {id,label,route} — dropped by the FE parser.
			"--navigation-json", `{"header":[{"id":"n1","label":"Home","route":"/"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an untyped navigation item must be rejected before any HTTP request")
	}
}

// TestHubsUpdate_NavigationBucketMustBeArray verifies a non-array header/footer
// bucket is rejected with ExitUsage and no request.
func TestHubsUpdate_NavigationBucketMustBeArray(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--navigation-json", `{"header":{"not":"an-array"}}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("a non-array navigation bucket must be rejected before any HTTP request")
	}
}

// TestHubsUpdate_NavigationRejectBeforeTeamResolve verifies an invalid menu is
// rejected with ExitUsage BEFORE team resolution — even when --team is a
// name/slug that would otherwise trigger a ResolveTeam GET /api/teams. Guards
// the validate-before-resolve ordering against regression (cf. MIO-2254).
func TestHubsUpdate_NavigationRejectBeforeTeamResolve(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		"hubs", "update", "hub_abc123",
		"--team", "acme-name", // NOT id-shaped → would trigger ResolveTeam GET
		"--navigation-json", `{"header":[{"label":"Home","route":"/"}]}`,
	)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an invalid --navigation-json must be rejected before any HTTP request, even with a team name that needs resolution")
	}
}

// TestHubsCreate_NavigationRejectsUntypedItem verifies the same typed-item
// validation applies on `hubs create` (consistency with update).
func TestHubsCreate_NavigationRejectsUntypedItem(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--navigation-json", `{"footer":[{"label":"Privacy","url":"/privacy"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an untyped navigation item must be rejected before any HTTP request on create too")
	}
}

// ─── MIO-2270: hub-scoped href validation ────────────────────────────────────

// firedFlagHubServer starts a server that flips *fired to true on any request
// and replies with the given status + minimalHubBody. Used to assert that an
// href-scoping usage error fires NO HTTP request at all (create path).
func firedFlagHubServer(t *testing.T, status int) (*httptest.Server, *bool) {
	t.Helper()
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &fired
}

// TestHubsCreate_NavigationRejectsUnscopedRelativeHref verifies a header url
// item whose href is a hub-relative path NOT scoped to the --slug is rejected
// with ExitUsage before any HTTP request (create validates against --slug).
func TestHubsCreate_NavigationRejectsUnscopedRelativeHref(t *testing.T) {
	srv, fired := firedFlagHubServer(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "S", "--slug", "s",
			"--navigation-json", `{"header":[{"type":"url","label":"Home","href":"/foo"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("an unscoped relative href must be rejected before any HTTP request")
	}
}

// TestHubsCreate_NavigationRejectsSiblingSlugPrefix verifies the scope check is
// boundary-aware: a href that merely shares the slug as a string prefix
// ("/support" vs slug "s") escapes the hub and is rejected.
func TestHubsCreate_NavigationRejectsSiblingSlugPrefix(t *testing.T) {
	srv, fired := firedFlagHubServer(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "S", "--slug", "s",
			"--navigation-json", `{"header":[{"type":"url","label":"Support","href":"/support"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("a sibling-slug-prefix href (\"/support\" for slug \"s\") must be rejected before any HTTP request")
	}
}

// TestHubsCreate_NavigationRelativeHrefRequiresSlug verifies that a relative
// href with NO --slug (so the hub scope is unknown) is rejected with ExitUsage
// and no request, rather than shipping an unvalidatable link.
func TestHubsCreate_NavigationRelativeHrefRequiresSlug(t *testing.T) {
	srv, fired := firedFlagHubServer(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "NoSlug",
			"--navigation-json", `{"header":[{"type":"url","label":"Home","href":"/foo"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("a relative href with no --slug must be rejected before any HTTP request")
	}
}

// TestHubsCreate_NavigationAllowsAbsoluteHref verifies an absolute http(s)://
// href is passed through as-is (no scoping applied) and the POST fires.
func TestHubsCreate_NavigationAllowsAbsoluteHref(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "S", "--slug", "s",
			"--navigation-json", `{"header":[{"type":"url","label":"Docs","href":"https://docs.example.com/guide"}]}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	nav, ok := attrs["navigation"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.navigation absent or not an object; attrs=%v", attrs)
	}
	header, _ := nav["header"].([]any)
	if len(header) != 1 {
		t.Fatalf("navigation.header should be a 1-item array; got %#v", nav["header"])
	}
	if item, _ := header[0].(map[string]any); item["href"] != "https://docs.example.com/guide" {
		t.Errorf("absolute href was altered: %#v", header[0])
	}
}

// TestHubsCreate_NavigationAllowsHubScopedHref verifies a href scoped to the
// hub's own slug ("/s/about" for slug "s") is accepted and the POST fires.
func TestHubsCreate_NavigationAllowsHubScopedHref(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "S", "--slug", "s",
			"--navigation-json", `{"header":[{"type":"url","label":"About","href":"/s/about"}],"footer":[{"type":"url","label":"Root","href":"/s"}]}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	if _, ok := attrs["navigation"].(map[string]any); !ok {
		t.Fatalf("data.attributes.navigation absent or not an object; attrs=%v", attrs)
	}
}

// TestHubsUpdate_NavigationRejectsUnscopedRelativeHref verifies that on update
// the hub is retrieved for its slug, and a relative href not scoped to that
// slug is rejected with ExitUsage — the retrieve fires but NO PATCH does.
func TestHubsUpdate_NavigationRejectsUnscopedRelativeHref(t *testing.T) {
	getFired, patchFired := false, false
	body := `{"data":{"id":"hub_abc123","type":"hubs","attributes":{"title":"H","slug":"demo","is_private":false}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getFired = true
		case http.MethodPatch:
			patchFired = true
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--navigation-json", `{"header":[{"type":"url","label":"Home","href":"/foo"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if !getFired {
		t.Error("update must retrieve the hub for its slug before validating navigation hrefs")
	}
	if patchFired {
		t.Error("an unscoped relative href must block the PATCH")
	}
}

// TestHubsUpdate_NavigationAllowsHubScopedHref verifies that on update a href
// scoped to the retrieved slug is accepted and PATCHed as navigation.
func TestHubsUpdate_NavigationAllowsHubScopedHref(t *testing.T) {
	srv, gotMethod, _, patchBody := hubSlugRetrieveServer(t, "demo")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--navigation-json", `{"header":[{"type":"url","label":"About","href":"/demo/about"}]}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("expected a PATCH after the slug retrieve; got method %q", *gotMethod)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	nav, ok := attrs["navigation"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH navigation absent or not an object; attrs=%v", attrs)
	}
	header, _ := nav["header"].([]any)
	if len(header) != 1 {
		t.Fatalf("navigation.header should be a 1-item array; got %#v", nav["header"])
	}
	if item, _ := header[0].(map[string]any); item["href"] != "/demo/about" {
		t.Errorf("scoped href was altered: %#v", header[0])
	}
}

// TestHubsUpdate_NavigationScopesToChangingSlug verifies that when --slug is
// changed in the SAME update, hrefs are validated against the NEW slug (the
// hub's final slug), not the retrieved old slug — and validation happens
// pre-auth so a good link needs no retrieve (the first request is the PATCH).
func TestHubsUpdate_NavigationScopesToChangingSlug(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--slug", "renamed",
			"--navigation-json", `{"header":[{"type":"url","label":"Home","href":"/renamed/about"}]}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	// No retrieve is needed when --slug supplies the final slug: first request is
	// the PATCH.
	if *gotMethod != http.MethodPatch {
		t.Errorf("first request method = %q, want PATCH (no retrieve needed when --slug is set)", *gotMethod)
	}
}

// TestHubsUpdate_NavigationRejectsOldSlugWhenSlugChanges verifies the converse:
// an href scoped to the OLD slug while --slug renames the hub is rejected —
// against the NEW slug — with ExitUsage and NO request (validated pre-auth).
func TestHubsUpdate_NavigationRejectsOldSlugWhenSlugChanges(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--slug", "renamed",
			"--navigation-json", `{"header":[{"type":"url","label":"Old","href":"/old-slug/about"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an href scoped to the old slug while --slug renames the hub must be rejected before any HTTP request")
	}
}

// TestHubsCreate_NavigationRejectsDotSegmentEscape verifies that a dot-segment
// escape ("/s/../outside", which a browser resolves to "/outside") is rejected
// even though it shares the "/s/" prefix — including its percent-encoded form.
func TestHubsCreate_NavigationRejectsDotSegmentEscape(t *testing.T) {
	for _, href := range []string{"/s/../outside", "/s/%2e%2e/outside"} {
		href := href
		t.Run(href, func(t *testing.T) {
			srv, fired := firedFlagHubServer(t, http.StatusCreated)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1",
					"hubs", "create",
					"--name", "S", "--slug", "s",
					"--navigation-json", `{"header":[{"type":"url","label":"Escape","href":"`+href+`"}]}`,
				)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Errorf("a dot-segment escape (%q) must be rejected before any HTTP request", href)
			}
		})
	}
}

// TestHubsCreate_NavigationRejectsProtocolRelativeHref verifies origin-escaping
// href forms whose PATH happens to match the slug are still rejected with
// ExitUsage and no request: protocol-relative "//host/s", the empty-authority
// "///s" (browsers resolve to https://s/), and the backslash variant "/\host/s"
// (browsers fold "\" to "/").
func TestHubsCreate_NavigationRejectsProtocolRelativeHref(t *testing.T) {
	for _, href := range []string{"//evil.example/s", "//evil.example/s/about", "///s", `/\evil.example/s`, `\outside`, `\\evil.example\s`} {
		href := href
		t.Run(href, func(t *testing.T) {
			srv, fired := firedFlagHubServer(t, http.StatusCreated)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1",
					"hubs", "create",
					"--name", "S", "--slug", "s",
					"--navigation-json", `{"header":[{"type":"url","label":"Evil","href":"`+href+`"}]}`,
				)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Errorf("a protocol-relative href (%q) must be rejected before any HTTP request", href)
			}
		})
	}
}

// TestHubsCreate_NavigationAllowsInnerDotSegment verifies dot-segments that
// resolve back INSIDE the hub ("/s/a/../b" → "/s/b") are still accepted.
func TestHubsCreate_NavigationAllowsInnerDotSegment(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "S", "--slug", "s",
			"--navigation-json", `{"header":[{"type":"url","label":"Inner","href":"/s/a/../b"}]}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	if _, ok := attrs["navigation"].(map[string]any); !ok {
		t.Fatalf("data.attributes.navigation absent or not an object; attrs=%v", attrs)
	}
}
