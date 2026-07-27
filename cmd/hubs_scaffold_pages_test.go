package cmd

// hubs_scaffold_pages_test.go — MIO-2672 Task 8: §5.1 per-boundary crash
// recovery for the client-side pages apply.
//
// The pure decision table (TestPagesRecovery_DecisionTable) runs with NO
// server: decideRecovery is a pure function over the provenance snapshot read
// back from an existing page. The boundary tests then drive stepPages against
// a scripted backend, one test per §5.1 recovery row, re-adding the
// resume/OCC coverage the deleted TestStepHomepage_Resume* tests held (see
// the supersession note in hubs_scaffold_test.go).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestPagesRecovery_DecisionTable pins the §5.1 recovery contract as a pure
// table — every row, no HTTP. v1 narrowed claim: "untouched" is draft_version
// equality only (the raw published tree is not client-readable), and a crash
// after publish but before the marker PATCH leaves "pending"+draft-written,
// which lands on conflict (see decideRecovery's narrowing note).
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

// TestProvenanceMarkerFields_TolerantGarbage: the marker parser must degrade
// any missing/mis-shaped meta to zero values (appID "" → decideRecovery
// conflict) and NEVER panic — the §5.1 tolerant-parsing hard rule.
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

// ─── scripted backend for the per-boundary integration tests ─────────────────

// recoveryBackend is a scripted pages backend for the §5.1 boundary tests:
// seeded with the hub's existing pages (returned by every page-list GET) and
// their current draft versions (tree GET → draft_version; a page absent from
// draftVers 404s = no draft ever written). Every mutating request is recorded
// so a test can assert exactly which pages were written — the §2.2
// never-overwrite guarantee is a claim about ABSENT requests.
//
// The W2b op probe (MIO-2672 Task 9) is scripted separately: op POSTs are
// answered by opHandler (nil → 404, the op-absent default every pre-Task-9
// boundary test relies on to exercise the client-side path) and recorded in
// opPosts/opBodies/opIdemKeys — NOT in mutations, which stays a count of
// page-level writes only. Catalog GETs (the 409-refetch) serve catalogBody
// (nil → 404, failing a Mutating resolve loudly). events logs the op/catalog
// interleaving so a test can assert refetch-then-retry ordering.
type recoveryBackend struct {
	mu        sync.Mutex
	pages     []map[string]any
	draftVers map[string]int

	listCalls    int
	createdSlugs []string
	createdAttrs map[string]map[string]any // slug → create body attrs
	putIfMatch   map[string]string         // pageID → tree-PUT If-Match
	putSeq       int                       // draft_version minted per PUT: 1, 2, 3…
	pubIfMatch   map[string]string         // pageID → publish If-Match
	patched      map[string]map[string]any // pageID → PATCH body attrs
	mutations    int                       // every page-level POST/PUT/PATCH (op probes excluded)

	// W2b op-probe scripting + recording (Task 9). Configure before driving.
	opHandler   func(w http.ResponseWriter, n int) // nth (1-based) op POST; nil → 404
	opPosts     int
	opBodies    []map[string]any // per-POST decoded data.attributes
	opIdemKeys  []string         // per-POST Idempotency-Key header
	catalogBody []byte           // served on GET …/page-builder/catalog; nil → 404
	catalogGETs int
	events      []string // ordered "op" / "catalog" log
}

// pathPageID returns the {id} segment of …/pages/{id}[/tree|/publish].
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
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		be.mu.Lock()
		defer be.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/scaffold-from-template"): // W2b op probe
			be.opPosts++
			be.events = append(be.events, "op")
			body, _ := io.ReadAll(r.Body)
			be.opBodies = append(be.opBodies, decodeHubAttrs(t, body))
			be.opIdemKeys = append(be.opIdemKeys, r.Header.Get("Idempotency-Key"))
			if be.opHandler == nil {
				// Default: the op is ABSENT (dormant flag / older backend) — the
				// probe 404s and stepPages falls back to the client-side loop the
				// boundary tests exercise.
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"Not Found"}]}`))
				return
			}
			be.opHandler(w, be.opPosts)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/page-builder/catalog"): // 409-refetch resolve
			be.catalogGETs++
			be.events = append(be.events, "catalog")
			if be.catalogBody == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(be.catalogBody)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/pages"): // recovery slug walk / homepage pre-check
			be.listCalls++
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": be.pages})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/tree"): // recovery draft_version read
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
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pp_1","type":"page-publishes","attributes":{}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/pages"): // create — id minted from the slug
			be.mutations++
			body, _ := io.ReadAll(r.Body)
			attrs := decodeHubAttrs(t, body)
			slug, _ := attrs["slug"].(string)
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
			be.patched[pathPageID(path)] = decodeHubAttrs(t, body)
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

// seededPage builds one existing-page resource for the backend's list body: an
// optional §5.1 marker lands under meta.template_provenance, exactly where the
// recovery snapshot reads it.
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

// ourMarker is a §5.1 marker carrying OUR applicationId for the fixture
// identity (hub_1 + community) in the given provenance state.
func ourMarker(state string) map[string]any {
	return map[string]any{
		"applicationId":   catalog.ApplicationID("hub_1", "community"),
		"hubTemplateId":   "community",
		"pageTemplateId":  "community-home",
		"catalogRevision": 7,
		"provenanceState": state,
	}
}

// driveStepPages runs stepPages over the full community fixture plan against
// srvURL and returns the context (for homepage id/dv assertions) + the error.
// sc.noteW is a *bytes.Buffer so tests can pin the operator notes (read it
// back via stepNotes).
func driveStepPages(t *testing.T, srvURL string) (*scaffoldContext, error) {
	return driveStepPagesCfg(t, srvURL, nil)
}

// driveStepPagesCfg is driveStepPages with a pre-run context hook (op tests
// set catalogOverride etc. before the step fires).
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

// stepNotes returns the operator notes a driveStepPages run wrote.
func stepNotes(sc *scaffoldContext) string {
	b, _ := sc.noteW.(*bytes.Buffer)
	if b == nil {
		return ""
	}
	return b.String()
}

// ─── per-boundary rows ────────────────────────────────────────────────────────

// TestStepPages_ResumeAfterCreateBeforeDraft: a prior run crashed between the
// homepage create and its draft PUT — the page exists at the manifest slug
// with OUR "pending" marker and NO draft (tree GET 404). The re-run must NOT
// create: it resumes the remaining sequence on the EXISTING id — tree PUT with
// the first-set sentinel If-Match 0 (correct precisely because resumeFull only
// fires at draft_version 0), publish with the PUT-returned version, marker
// PATCH to "applied" — and the other pages proceed normally.
func TestStepPages_ResumeAfterCreateBeforeDraft(t *testing.T) {
	srv, be := newRecoveryBackend(t,
		[]map[string]any{seededPage("pg_half", "homepage", true, ourMarker("pending"))},
		nil) // pg_half absent from draftVers → tree GET 404 → no draft written

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
	// Task-8 review minor: the resume is an OPERATOR-FACING note, pinned here so
	// dropping the notef silently is a test failure, not a UX regression.
	if notes := stepNotes(sc); !strings.Contains(notes, "resuming") {
		t.Errorf("resume must emit an operator note containing %q; notes=%q", "resuming", notes)
	}
}

// TestStepPages_ConflictAfterDraftWritten: ours + "pending" but a draft EXISTS
// (draft_version 2) — a crash between draft PUT and marker PATCH is
// indistinguishable from a user's first edit (§10.8), so the run refuses with
// ExitUsage naming the page and the reason, having mutated NOTHING, and the
// loop aborts.
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

// TestStepPages_NoopWhenAppliedUntouched: ours + "applied" + the current
// draft_version equals the marker's appliedDraftVersion — the page converged
// on a prior run. The re-run mutates NOTHING for it and CONTINUES the loop:
// the remaining pages are created normally and the run exits success.
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
	// Task-8 review minor: the skip is an OPERATOR-FACING note, pinned here.
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

// TestStepPages_ConflictWhenAppliedEdited: ours + "applied" but the draft
// moved since (5 vs the marker's 3) — the user edited the page after our
// apply, so the run refuses (ExitUsage) without writing.
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

// TestStepPages_ForeignSlugConflict: a page WITHOUT our marker sits at a
// manifest slug ("about") — never overwrite (§2.2). The homepage (before it in
// plan order) applies fully; the conflict aborts the loop before "faq".
// (Supersedes Task 7's TestStepPages_ExistingSlugConflicts — same scenario,
// now the foreign-page row of decideRecovery.)
func TestStepPages_ForeignSlugConflict(t *testing.T) {
	srv, be := newRecoveryBackend(t,
		[]map[string]any{seededPage("pg_conflict", "about", false, nil)}, // unmarked
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
	if be.mutations != 4 {
		t.Errorf("got %d mutating requests, want 4 (the homepage's create+PUT+publish+PATCH only)", be.mutations)
	}
	// Slug walks: homepage + its foreign-homepage pre-check + about; the
	// conflict aborts before faq is ever listed.
	if be.listCalls != 3 {
		t.Errorf("got %d page-list GETs, want 3 (homepage walk + homepage pre-check + about walk)", be.listCalls)
	}
}

// TestStepPages_ForeignHomepageBlocksCreate: the manifest homepage slug is
// FREE, but the hub already has an is_homepage page at ANOTHER slug —
// create_page(is_homepage=true) would CLEAR it server-side, so the §5.1
// pre-check conflicts BEFORE any create fires, naming the existing homepage's
// id. This holds UNCONDITIONALLY: a foreign/unmarked homepage, and equally one
// carrying OUR marker at an unexpected slug (reachable via a user slug rename
// or a catalog-revision slug change — ApplicationID is hub+template only, so a
// marker match does not prove the page is where this run expects it).
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

// TestStepPages_TokenedTitleInterpolatedAtCreate pins title interpolation at
// the create seam (§4.3 location (b)): a PageRef title carrying {{hub_name}}
// must arrive at the create POST fully substituted. (The fixture's titles are
// token-free, so without this a dropped InterpolateTitle call would still pass
// the main provenance test.)
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
