package cmd

// hubs_scaffold_publish_resolve_test.go — W0 (MIO-2666 / MIO-2667).
//
// Regression + contract tests proving a scaffolded hub's homepage is resolvable
// and NON-NULL through the hub renderer's two-call read flow, and that an
// absent-draft publish still resolves to a non-null (empty) tree (spec §6 W0,
// §10.3).
//
// Context: mio-cli PR #66 (4896c13) made the scaffold's homepage apply POST
// …/pages/{id}/publish unconditionally (If-Match = the PUT-returned
// draft_version), closing the "publish gap" that left a scaffolded homepage
// rendering the null-tree "No content available" fallback. W0 adds NO
// production code — the publish call already exists — it adds the regression
// that fails loudly if that publish is ever dropped, plus a contract test for
// the §10.3 absent-draft publish substitution. (MIO-2672 Task 7 replaced
// stepHomepage with the general stepPages loop; the guard now drives that.)
//
// The read flow is two GETs, addressed through the same CLI client the scaffold
// writes through:
//   1. GET …/pages/home                 → page METADATA (yields the homepage slug)
//   2. GET …/pages/{slug}?resolve=true  → the resolved PUBLISHED node tree
// The /pages/home route is metadata-only; the resolved tree comes from the
// second, slug-addressed call.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/client"
)

// homepageBackend is a stateful in-memory stand-in for the hub-pages backend. It
// models the ONE property the W0 regression guards: a page's PUBLISHED tree is
// resolvable through the two-call read flow ONLY AFTER POST …/publish runs.
// Before publish, publishedRoot is nil and the resolve read returns a JSON null
// tree — exactly the "No content available" fallback PR #66 removed — so a
// pages step that stops publishing fails the regression.
type homepageBackend struct {
	mu            sync.Mutex
	pageID        string         // id minted on create
	slug          string         // slug recorded from the create body
	draftRoot     map[string]any // tree.root captured on the draft PUT (nil until a PUT)
	publishedRoot map[string]any // tree.root exposed by the resolve read (nil until publish)
}

// newHomepageBackend starts the stateful stub and returns its server plus the
// backend it mutates. Routing is by HTTP method + path suffix/query, matching
// the convention the scaffold step tests use (the client rewrites /api/… to
// /api/v1/…, so only suffixes are stable). It models ONE page's lifecycle
// (one id, one slug) — tests drive stepPages with the plan trimmed to the
// homepage entry.
func newHomepageBackend(t *testing.T) (*httptest.Server, *homepageBackend) {
	t.Helper()
	be := &homepageBackend{pageID: "page_home"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		be.mu.Lock()
		defer be.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		// (1) Resolve read: GET …/pages/{slug}?resolve=true → resolved published tree.
		// Slug-addressed and checked FIRST: only the documented second call of the
		// read flow matches; a resolve read against any other path (wrong slug,
		// draft-tree route) falls through and fails the test loudly.
		case r.Method == http.MethodGet && r.URL.Query().Get("resolve") == "true" &&
			be.slug != "" && strings.HasSuffix(path, "/pages/"+be.slug):
			var tree any // JSON null until published
			if be.publishedRoot != nil {
				tree = map[string]any{"root": be.publishedRoot}
			}
			writePageWithTree(w, be.pageID, be.slug, tree)
		// (2) Home metadata: GET …/pages/home → the homepage page's metadata (slug only).
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/pages/home"):
			writePageMeta(w, be.pageID, be.slug)
		// existingPageBySlug() list: GET …/pages → empty on a fresh hub (forces a create).
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/pages"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		// Provenance-marker PATCH (MIO-2672 Task 7): ack — provenance is pinned
		// by TestStepPages_AppliesAllPagesWithProvenance, not here.
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/pages/"+be.pageID):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"pages","attributes":{}}}`, be.pageID)
		// Draft tree PUT: capture tree.root, bump draft_version.
		case r.Method == http.MethodPut && strings.Contains(path, "/tree"):
			body, _ := io.ReadAll(r.Body)
			if tree, ok := decodeHubAttrs(t, body)["tree"].(map[string]any); ok {
				if root, ok := tree["root"].(map[string]any); ok {
					be.draftRoot = root
				}
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		// Publish: promote the draft into the published state (THE behaviour W0
		// guards). Addressed to the created page — POST …/pages/{id}/publish — so
		// publishing a wrong/stale/empty page id promotes nothing and the resolve
		// read stays null.
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/pages/"+be.pageID+"/publish"):
			if be.draftRoot != nil {
				be.publishedRoot = be.draftRoot
			} else {
				be.publishedRoot = emptyStackRoot() // §10.3: absent-draft substitution
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pp_1","type":"page-publishes","attributes":{"section_count":1}}}`))
		// Create: record the slug and mint the page id.
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/pages"):
			body, _ := io.ReadAll(r.Body)
			be.slug, _ = decodeHubAttrs(t, body)["slug"].(string)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"pages","attributes":{"slug":%q,"is_homepage":true}}}`, be.pageID, be.slug)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, be
}

// emptyStackRoot is the spec §10.3 absent-draft substitution: the tree.root of
// {"root":{"children":[]}} — a non-null, structurally-empty stack. (Option A: the
// id-less/kind-less fallback the backend substitutes today; see the plan's
// §10.3 decision callout for the canonical-stack alternative.)
func emptyStackRoot() map[string]any {
	return map[string]any{"children": []any{}}
}

// writePageMeta writes the /pages/home metadata response (slug + is_homepage) —
// metadata only, NO tree (the /pages/home route is metadata-only, §10.3).
func writePageMeta(w http.ResponseWriter, id, slug string) {
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"pages","attributes":{"slug":%q,"is_homepage":true}}}`, id, slug)
}

// writePageWithTree writes the resolve-read response: the page plus its resolved
// published `tree` attribute (JSON null when the page was never published).
func writePageWithTree(w http.ResponseWriter, id, slug string, tree any) {
	doc := map[string]any{
		"data": map[string]any{
			"id":   id,
			"type": "pages",
			"attributes": map[string]any{
				"slug": slug,
				"tree": tree,
			},
		},
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(doc)
}

// readResolvedHomepageTree runs the spec §6 W0 / §10.3 two-call read flow and
// returns the resolved published tree (nil when the page was never published)
// plus the homepage slug the metadata call yielded. It reads through the SAME
// client type the scaffold writes through, so it observes exactly the state the
// publish produced.
func readResolvedHomepageTree(t *testing.T, cl *client.Client, teamID, hubID string) (tree map[string]any, slug string) {
	t.Helper()
	meta, err := cl.Retrieve(context.Background(), pagesBase(teamID, hubID)+"/home")
	if err != nil {
		t.Fatalf("read flow: GET /pages/home failed: %v", err)
	}
	slug, _ = meta.Attributes["slug"].(string)
	if slug == "" {
		t.Fatalf("read flow: /pages/home returned no slug; attrs=%v", meta.Attributes)
	}
	q := url.Values{}
	q.Set("resolve", "true")
	res, err := cl.RetrieveWithQuery(context.Background(), pagesBase(teamID, hubID)+"/"+slug, q)
	if err != nil {
		t.Fatalf("read flow: GET /pages/%s?resolve=true failed: %v", slug, err)
	}
	tree, _ = res.Attributes["tree"].(map[string]any)
	return tree, slug
}

// TestStepPages_PublishedHomepageResolvesNonNull is the W0 regression (spec
// §6 W0): after the pages step applies the homepage entry (create → set draft
// tree → publish), the hub renderer's two-call read flow returns a NON-NULL
// resolved homepage tree WITH content. If the publish call in the pages step
// is ever dropped, publishedRoot stays nil, the resolve read returns a null
// tree ("No content available"), and this test fails.
//
// The plan is TRIMMED to just the homepage entry: homepageBackend models a
// single page's publish lifecycle, and W0's property is about the homepage —
// the full multi-page loop is pinned by
// TestStepPages_AppliesAllPagesWithProvenance in hubs_scaffold_test.go.
func TestStepPages_PublishedHomepageResolvesNonNull(t *testing.T) {
	srv, _ := newHomepageBackend(t)
	cl := client.New(srv.URL, "k")

	sc := newStepSC(cl, "hub_1", "acme")
	cat, ht, plan := scaffoldFixture(t)
	hpOnly := &scaffoldPlan{}
	for _, pp := range plan.pages {
		if pp.ref.IsHomepage {
			hpOnly.pages = append(hpOnly.pages, pp)
		}
	}
	if len(hpOnly.pages) != 1 {
		t.Fatalf("community fixture must have exactly one homepage entry, got %d", len(hpOnly.pages))
	}
	sc.cat, sc.hubTmpl, sc.pagePlan, sc.hubName = cat, ht, hpOnly, "Acme"
	if err := stepPages(sc, &sc.hubTmpl); err != nil {
		t.Fatalf("stepPages: %v", err)
	}

	tree, slug := readResolvedHomepageTree(t, cl, "t_team1", "hub_1")
	if slug != "homepage" {
		t.Errorf("read flow slug = %q, want %q (the scaffolded homepage slug from the catalog PageRef)", slug, "homepage")
	}
	if tree == nil {
		t.Fatalf("resolve read returned a NULL tree — the published homepage is not resolvable (publish gap regressed)")
	}
	root, ok := tree["root"].(map[string]any)
	if !ok {
		t.Fatalf("resolved tree has no root object; tree=%v", tree)
	}
	if kids, ok := root["children"].([]any); !ok || len(kids) == 0 {
		t.Errorf("resolved homepage root has no children — published tree is structurally empty; root=%v", root)
	}
}

// TestPublishAbsentDraft_ResolvesEmptyStackNonNull pins spec §10.3 (CLOSED):
// publishing a page that has NO draft is supported — the backend substitutes the
// canonical empty tree {"root":{"children":[]}} — so the two-call read flow
// resolves to a NON-NULL, structurally-EMPTY tree, never the null "No content
// available" fallback. This is the contract W0's empty-tree-publish capability
// (spec §2.2) depends on; it exercises create → publish (no draft) → resolve.
func TestPublishAbsentDraft_ResolvesEmptyStackNonNull(t *testing.T) {
	srv, _ := newHomepageBackend(t)
	cl := client.New(srv.URL, "k")
	ctx := context.Background()

	// Create a page but set NO draft tree (absent-draft), then publish it.
	page, err := cl.Create(ctx, pagesPath("t_team1", "hub_1", ""), map[string]any{
		"title": "Home", "slug": "homepage", "is_homepage": true,
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	if _, err := cl.ActionWithHeaders(ctx, client.StyleEnvelope, "POST",
		pagesPath("t_team1", "hub_1", page.ID)+"/publish", nil,
		map[string]string{"If-Match": "0"}); err != nil {
		t.Fatalf("absent-draft publish: %v", err)
	}

	tree, _ := readResolvedHomepageTree(t, cl, "t_team1", "hub_1")
	if tree == nil {
		t.Fatalf("absent-draft publish must resolve to a NON-NULL empty tree, got null (§10.3)")
	}
	root, ok := tree["root"].(map[string]any)
	if !ok {
		t.Fatalf("resolved tree has no root object; tree=%v", tree)
	}
	kids, ok := root["children"].([]any)
	if !ok {
		t.Fatalf("substituted empty tree must carry a children array; root=%v", root)
	}
	if len(kids) != 0 {
		t.Errorf("absent-draft substitution must be EMPTY {\"root\":{\"children\":[]}}, got %d child(ren); root=%v", len(kids), root)
	}
}
