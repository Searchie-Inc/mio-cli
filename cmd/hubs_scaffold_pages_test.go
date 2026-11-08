package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestPagesRecovery_DecisionTable(t *testing.T) {
	const ours = "app_ours"
	cases := []struct {
		name string
		p    *recoveredPage
		want recoveryAction
	}{
		{"absent page → create", nil, actionCreate},
		{"ours, pending, no draft written → resumeFull",
			&recoveredPage{id: "p1", appID: ours, state: "pending"}, actionResumeFull},
		{"ours, pending, draft written → conflict (crashed write vs user edit indistinguishable)",
			&recoveredPage{id: "p1", appID: ours, state: "pending", draftVersion: 2}, actionConflict},
		{"ours, applied, untouched → noop",
			&recoveredPage{id: "p1", appID: ours, state: "applied", draftVersion: 3, appliedDraftVersion: 3}, actionNoop},
		{"ours, applied, versions diverge → conflict",
			&recoveredPage{id: "p1", appID: ours, state: "applied", draftVersion: 5, appliedDraftVersion: 3}, actionConflict},
		{"foreign applicationId → conflict",
			&recoveredPage{id: "p1", appID: "app_other", state: "applied", draftVersion: 3, appliedDraftVersion: 3}, actionConflict},
		{"no/garbage marker (empty appID) → conflict",
			&recoveredPage{id: "p1"}, actionConflict},
		{"unknown provenance state → conflict",
			&recoveredPage{id: "p1", appID: ours, state: "weird"}, actionConflict},
	}
	for _, tc := range cases {
		if got := decideRecovery(ours, tc.p); got != tc.want {
			t.Errorf("%s: decideRecovery = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProvenanceMarkerFields_TolerantGarbage(t *testing.T) {
	for _, attrs := range []map[string]any{
		nil,
		{"meta": "not-an-object"},
		{"meta": map[string]any{"template_provenance": []any{"an", "array"}}},
		{"meta": map[string]any{"template_provenance": map[string]any{
			"applicationId": 42, "provenanceState": true, "appliedDraftVersion": "three"}}},
	} {
		appID, state, dv := provenanceMarkerFields(attrs)
		if appID != "" || state != "" || dv != 0 {
			t.Errorf("attrs=%v: got (%q, %q, %d), want zero values", attrs, appID, state, dv)
		}
	}
}

type recoveryBackend struct {
	mu        sync.Mutex
	pages     []map[string]any
	draftVers map[string]int

	listCalls    int
	createdSlugs []string
	createdAttrs map[string]map[string]any
	putIfMatch   map[string]string
	putSeq       int
	pubIfMatch   map[string]string
	patched      map[string]map[string]any
	mutations    int

	opHandler   func(w http.ResponseWriter, n int)
	opPosts     int
	opBodies    []map[string]any
	opIdemKeys  []string
	catalogBody []byte
	catalogGETs int
	events      []string

	enforceHomeNeedsPublish bool
	homeRejects             int
	publishedPages          map[string]bool
	pageEvents              []string

	onPublish func(be *recoveryBackend)
}

const homeNotPublished409 = `{"errors":[{"status":"409","code":"homepage_page_not_published","detail":"homepage must be published"}]}`

func pathPageID(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		if s == "pages" && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return ""
}

func newRecoveryBackend(t *testing.T, existing []map[string]any, draftVers map[string]int) (*httptest.Server, *recoveryBackend) {
	t.Helper()
	be := &recoveryBackend{
		pages:        existing,
		draftVers:    draftVers,
		createdAttrs: map[string]map[string]any{},
		putIfMatch:   map[string]string{},
		pubIfMatch:   map[string]string{},
		patched:      map[string]map[string]any{},

		publishedPages: map[string]bool{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		be.mu.Lock()
		defer be.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/scaffold-from-template"):
			be.opPosts++
			be.events = append(be.events, "op")
			body, _ := io.ReadAll(r.Body)
			be.opBodies = append(be.opBodies, decodeHubAttrs(t, body))
			be.opIdemKeys = append(be.opIdemKeys, r.Header.Get("Idempotency-Key"))
			if be.opHandler == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"Not Found"}]}`))
				return
			}
			be.opHandler(w, be.opPosts)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/page-builder/catalog"):
			be.catalogGETs++
			be.events = append(be.events, "catalog")
			if be.catalogBody == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(be.catalogBody)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/pages"):
			be.listCalls++
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": be.pages})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/tree"):
			dv, ok := be.draftVers[pathPageID(path)]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"no draft for this page"}]}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"pdt_r","type":"page_draft_trees","attributes":{"draft_version":%d}}}`, dv)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/publish"):
			be.mutations++
			be.pubIfMatch[pathPageID(path)] = r.Header.Get("If-Match")
			be.publishedPages[pathPageID(path)] = true
			be.pageEvents = append(be.pageEvents, "publish:"+pathPageID(path))
			if be.onPublish != nil {
				be.onPublish(be)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pp_1","type":"page-publishes","attributes":{}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/pages"):
			be.mutations++
			body, _ := io.ReadAll(r.Body)
			attrs := decodeHubAttrs(t, body)
			slug, _ := attrs["slug"].(string)
			if home, _ := attrs["is_homepage"].(bool); home {
				be.pageEvents = append(be.pageEvents, "create:"+slug+"+home")
				if be.enforceHomeNeedsPublish {
					be.homeRejects++
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(homeNotPublished409))
					return
				}
			} else {
				be.pageEvents = append(be.pageEvents, "create:"+slug)
			}
			be.createdSlugs = append(be.createdSlugs, slug)
			be.createdAttrs[slug] = attrs
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"pg_%s","type":"pages","attributes":{"slug":%q}}}`, slug, slug)
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/tree"):
			be.mutations++
			be.putSeq++
			be.putIfMatch[pathPageID(path)] = r.Header.Get("If-Match")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":"pdt_%d","type":"page_draft_trees","attributes":{"draft_version":%d}}}`, be.putSeq, be.putSeq)
		case r.Method == http.MethodPatch:
			be.mutations++
			body, _ := io.ReadAll(r.Body)
			pa := decodeHubAttrs(t, body)
			if home, _ := pa["is_homepage"].(bool); home {
				be.pageEvents = append(be.pageEvents, "home:"+pathPageID(path))
				if be.enforceHomeNeedsPublish && !be.publishedPages[pathPageID(path)] {
					be.homeRejects++
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(homeNotPublished409))
					return
				}
			}
			if _, isHomeOnly := pa["is_homepage"]; !isHomeOnly || pa["meta"] != nil {
				be.patched[pathPageID(path)] = pa
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pg_x","type":"pages","attributes":{}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, be
}

func seededPage(id, slug string, isHomepage bool, marker map[string]any) map[string]any {
	attrs := map[string]any{"slug": slug}
	if isHomepage {
		attrs["is_homepage"] = true
	}
	if marker != nil {
		attrs["meta"] = map[string]any{"template_provenance": marker}
	}
	return map[string]any{"id": id, "type": "pages", "attributes": attrs}
}

func ourMarker(state string) map[string]any {
	return map[string]any{
		"applicationId":   catalog.ApplicationID("hub_1", "community"),
		"hubTemplateId":   "community",
		"pageTemplateId":  "community-home",
		"catalogRevision": 7,
		"provenanceState": state,
	}
}

func driveStepPages(t *testing.T, srvURL string) (*scaffoldContext, error) {
	return driveStepPagesCfg(t, srvURL, nil)
}

func driveStepPagesCfg(t *testing.T, srvURL string, cfg func(*scaffoldContext)) (*scaffoldContext, error) {
	t.Helper()
	sc := newStepSC(client.New(srvURL, "k"), "hub_1", "acme")
	cat, ht, plan := scaffoldFixture(t)
	sc.cat, sc.hubTmpl, sc.pagePlan, sc.hubName = cat, ht, plan, "Acme"
	sc.noteW = &bytes.Buffer{}
	if cfg != nil {
		cfg(sc)
	}
	return sc, stepPages(sc, &sc.hubTmpl)
}

func stepNotes(sc *scaffoldContext) string {
	b, _ := sc.noteW.(*bytes.Buffer)
	if b == nil {
		return ""
	}
	return b.String()
}

func TestStepPages_ResumeAfterCreateBeforeDraft(t *testing.T) {
	srv, be := newRecoveryBackend(t,
		[]map[string]any{seededPage("pg_half", "homepage", true, ourMarker("pending"))},
		nil)

	sc, err := driveStepPages(t, srv.URL)
	if err != nil {
		t.Fatalf("stepPages must resume cleanly onto a pending+no-draft page: %v", err)
	}
	if !reflect.DeepEqual(be.createdSlugs, []string{"about", "faq"}) {
		t.Errorf("created slugs = %v, want [about faq] — the homepage must be REUSED, never re-created", be.createdSlugs)
	}
	if got := be.putIfMatch["pg_half"]; got != "0" {
		t.Errorf("resumed tree PUT If-Match = %q, want \"0\" (no draft was ever written — first-set sentinel)", got)
	}
	if got := be.pubIfMatch["pg_half"]; got != "1" {
		t.Errorf("resumed publish If-Match = %q, want \"1\" (the PUT-returned draft_version)", got)
	}
	meta, _ := be.patched["pg_half"]["meta"].(map[string]any)
	tp, _ := meta["template_provenance"].(map[string]any)
	if tp["provenanceState"] != "applied" {
		t.Errorf("resumed marker PATCH provenanceState = %v, want applied; patch=%v", tp["provenanceState"], be.patched["pg_half"])
	}
	if dv, ok := attrInt(tp["appliedDraftVersion"]); !ok || dv != 1 {
		t.Errorf("resumed marker PATCH appliedDraftVersion = %v, want 1 (the PUT-returned draft_version)", tp["appliedDraftVersion"])
	}
	if sc.homePageID != "pg_half" || sc.homeDraftVersion != 1 {
		t.Errorf("context homepage = {%q %d}, want {pg_half 1} (the reused page)", sc.homePageID, sc.homeDraftVersion)
	}
	if notes := stepNotes(sc); !strings.Contains(notes, "resuming") {
		t.Errorf("resume must emit an operator note containing %q; notes=%q", "resuming", notes)
	}
}

func TestStepPages_ConflictAfterDraftWritten(t *testing.T) {
	srv, be := newRecoveryBackend(t,
		[]map[string]any{seededPage("pg_half", "homepage", true, ourMarker("pending"))},
		map[string]int{"pg_half": 2})

	_, err := driveStepPages(t, srv.URL)
	if err == nil {
		t.Fatal("stepPages must CONFLICT on a pending page whose draft was written")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if !strings.Contains(err.Error(), `"homepage"`) || !strings.Contains(err.Error(), "draft written since our create") {
		t.Errorf("conflict error must name the page and the reason; err=%v", err)
	}
	if be.mutations != 0 {
		t.Errorf("got %d mutating request(s), want 0 — the conflict aborts before any write", be.mutations)
	}
	if be.listCalls != 1 {
		t.Errorf("got %d page-list GETs, want 1 (the loop aborts at the first page)", be.listCalls)
	}
}

func TestStepPages_NoopWhenAppliedUntouched(t *testing.T) {
	marker := ourMarker("applied")
	marker["appliedDraftVersion"] = 3
	marker["appliedTreeDigest"] = "sha256:prior"
	srv, be := newRecoveryBackend(t,
		[]map[string]any{seededPage("pg_done", "homepage", true, marker)},
		map[string]int{"pg_done": 3})

	sc, err := driveStepPages(t, srv.URL)
	if err != nil {
		t.Fatalf("an applied+untouched page must be a clean no-op: %v", err)
	}
	if !reflect.DeepEqual(be.createdSlugs, []string{"about", "faq"}) {
		t.Errorf("created slugs = %v, want [about faq] — the loop must CONTINUE past the no-op page", be.createdSlugs)
	}
	if notes := stepNotes(sc); !strings.Contains(notes, "already applied") {
		t.Errorf("no-op must emit an operator note containing %q; notes=%q", "already applied", notes)
	}
	if _, put := be.putIfMatch["pg_done"]; put {
		t.Errorf("no tree PUT may touch the untouched applied page pg_done")
	}
	if _, pub := be.pubIfMatch["pg_done"]; pub {
		t.Errorf("no publish may touch the untouched applied page pg_done")
	}
	if _, patched := be.patched["pg_done"]; patched {
		t.Errorf("no marker PATCH may touch the untouched applied page pg_done")
	}
}

func TestStepPages_ConflictWhenAppliedEdited(t *testing.T) {
	marker := ourMarker("applied")
	marker["appliedDraftVersion"] = 3
	srv, be := newRecoveryBackend(t,
		[]map[string]any{seededPage("pg_done", "homepage", true, marker)},
		map[string]int{"pg_done": 5})

	_, err := driveStepPages(t, srv.URL)
	if err == nil {
		t.Fatal("stepPages must CONFLICT on an applied page whose draft moved since our apply")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if !strings.Contains(err.Error(), "edited since our apply") {
		t.Errorf("conflict error must carry the edited-since-apply reason; err=%v", err)
	}
	if be.mutations != 0 {
		t.Errorf("got %d mutating request(s), want 0", be.mutations)
	}
}

func TestStepPages_ForeignSlugConflict(t *testing.T) {
	srv, be := newRecoveryBackend(t,
		[]map[string]any{seededPage("pg_conflict", "about", false, nil)},
		nil)

	_, err := driveStepPages(t, srv.URL)
	if err == nil {
		t.Fatal("stepPages must CONFLICT on a foreign (unmarked) page at a manifest slug")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if !strings.Contains(err.Error(), `"about"`) || !strings.Contains(err.Error(), "foreign page at slug") {
		t.Errorf("conflict error must name the slug and the foreign-page reason; err=%v", err)
	}
	if !reflect.DeepEqual(be.createdSlugs, []string{"homepage"}) {
		t.Errorf("created slugs = %v, want only [homepage] (fail-fast: nothing for about/faq)", be.createdSlugs)
	}
	if be.mutations != 5 {
		t.Errorf("got %d mutating requests, want 5 (the homepage's create+PUT+publish+PATCH+is_homepage update only)", be.mutations)
	}
	if be.listCalls != 4 {
		t.Errorf("got %d page-list GETs, want 4 (homepage walk + pre-check + set-home recheck + about walk)", be.listCalls)
	}
}

func TestStepPages_ForeignHomepageBlocksCreate(t *testing.T) {
	cases := []struct {
		name       string
		id, slug   string
		marker     map[string]any
		wantReason string
	}{
		{"foreign (unmarked) homepage", "pg_legacy", "legacy-home", nil, "is not ours"},
		{"our marker at an unexpected slug", "pg_renamed", "renamed-home", ourMarker("applied"), "unexpected slug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, be := newRecoveryBackend(t,
				[]map[string]any{seededPage(tc.id, tc.slug, true, tc.marker)},
				nil)

			_, err := driveStepPages(t, srv.URL)
			if err == nil {
				t.Fatal("stepPages must CONFLICT before creating a homepage while ANY other homepage exists")
			}
			if errs.CodeOf(err) != errs.ExitUsage {
				t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
			}
			if !strings.Contains(err.Error(), tc.id) {
				t.Errorf("error must name the existing homepage's id (%s); err=%v", tc.id, err)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("error must carry the per-case reason %q; err=%v", tc.wantReason, err)
			}
			if be.mutations != 0 {
				t.Errorf("got %d mutating request(s), want 0 — the pre-check blocks BEFORE the create", be.mutations)
			}
		})
	}
}

func TestStepPages_TokenedTitleInterpolatedAtCreate(t *testing.T) {
	srv, be := newRecoveryBackend(t, nil, nil)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	cat, ht, _ := scaffoldFixture(t)
	sc.cat, sc.hubTmpl, sc.hubName = cat, ht, "Acme Community"
	sc.pagePlan = &scaffoldPlan{pages: []plannedPage{{
		ref:     catalog.PageRef{PageTemplate: "page-x", Slug: "welcome", Title: "Welcome to {{hub_name}}"},
		rawTree: map[string]any{"id": "n1", "kind": "text", "value": "hi"},
	}}}
	if err := stepPages(sc, &sc.hubTmpl); err != nil {
		t.Fatalf("stepPages: %v", err)
	}
	got, _ := be.createdAttrs["welcome"]["title"].(string)
	if got != "Welcome to Acme Community" {
		t.Errorf("created title = %q, want %q (interpolated with the ACTUAL hub name)", got, "Welcome to Acme Community")
	}
}

func TestStepPages_HomepageIsSetAfterPublish(t *testing.T) {
	srv, be := newRecoveryBackend(t, nil, nil)
	be.enforceHomeNeedsPublish = true

	sc, err := driveStepPages(t, srv.URL)
	if err != nil {
		t.Fatalf("stepPages against a backend enforcing homepage-needs-publish: %v", err)
	}
	if be.homeRejects != 0 {
		t.Fatalf("backend answered %d x 409 homepage_page_not_published — the CLI must not ask before publish", be.homeRejects)
	}
	for _, e := range be.pageEvents {
		if strings.HasSuffix(e, "+home") {
			t.Errorf("a create carried is_homepage:true (%s); events=%v", e, be.pageEvents)
		}
	}
	pub, home := slices.Index(be.pageEvents, "publish:pg_homepage"), slices.Index(be.pageEvents, "home:pg_homepage")
	if pub < 0 || home < 0 || pub > home {
		t.Errorf("want publish:pg_homepage before home:pg_homepage; events=%v", be.pageEvents)
	}
	if n := strings.Count(strings.Join(be.pageEvents, ","), "home:"); n != 1 {
		t.Errorf("exactly one is_homepage update expected (the homepage entry only), got %d; events=%v", n, be.pageEvents)
	}
	if sc.homePageID != "pg_homepage" {
		t.Errorf("homePageID = %q, want pg_homepage", sc.homePageID)
	}
}

func TestStepPages_ResumeSetsHomepageOnAppliedButNotHome(t *testing.T) {
	marker := ourMarker("applied")
	marker["appliedDraftVersion"] = 3
	srv, be := newRecoveryBackend(t,
		[]map[string]any{seededPage("pg_done", "homepage", false, marker)},
		map[string]int{"pg_done": 3})
	be.enforceHomeNeedsPublish = true
	be.publishedPages["pg_done"] = true

	if _, err := driveStepPages(t, srv.URL); err != nil {
		t.Fatalf("resume onto applied-but-not-home homepage: %v", err)
	}
	if !slices.Contains(be.pageEvents, "home:pg_done") {
		t.Errorf("resume must set is_homepage on pg_done; events=%v", be.pageEvents)
	}
	if _, put := be.putIfMatch["pg_done"]; put {
		t.Errorf("resume must not re-PUT the tree of pg_done")
	}
	if _, pub := be.pubIfMatch["pg_done"]; pub {
		t.Errorf("resume must not re-publish pg_done")
	}
	if be.homeRejects != 0 {
		t.Errorf("backend rejected %d homepage update(s)", be.homeRejects)
	}
}

func TestStepPages_ResumeNotHomeRefusesToClobberForeignHomepage(t *testing.T) {
	marker := ourMarker("applied")
	marker["appliedDraftVersion"] = 3
	srv, be := newRecoveryBackend(t, []map[string]any{
		seededPage("pg_done", "homepage", false, marker),
		seededPage("pg_legacy", "legacy-home", true, nil),
	}, map[string]int{"pg_done": 3})

	_, err := driveStepPages(t, srv.URL)
	if err == nil || errs.CodeOf(err) != errs.ExitUsage || !strings.Contains(err.Error(), "pg_legacy") {
		t.Fatalf("want ExitUsage naming pg_legacy, got %v", err)
	}
	if slices.Contains(be.pageEvents, "home:pg_done") {
		t.Errorf("must not set is_homepage over a foreign homepage; events=%v", be.pageEvents)
	}
}

func TestStepPages_HomepageClaimedDuringPublishIsNotClobbered(t *testing.T) {
	srv, be := newRecoveryBackend(t, nil, nil)
	be.onPublish = func(be *recoveryBackend) {
		be.pages = append(be.pages, seededPage("pg_other", "other-home", true, nil))
	}

	_, err := driveStepPages(t, srv.URL)
	if err == nil || errs.CodeOf(err) != errs.ExitUsage || !strings.Contains(err.Error(), "pg_other") {
		t.Fatalf("want ExitUsage naming pg_other, got %v", err)
	}
	if slices.Contains(be.pageEvents, "home:pg_homepage") {
		t.Errorf("must not set is_homepage over pg_other; events=%v", be.pageEvents)
	}
}
