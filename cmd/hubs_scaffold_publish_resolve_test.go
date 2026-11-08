package cmd

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

type homepageBackend struct {
	mu            sync.Mutex
	pageID        string
	slug          string
	draftRoot     map[string]any
	publishedRoot map[string]any
}

func newHomepageBackend(t *testing.T) (*httptest.Server, *homepageBackend) {
	t.Helper()
	be := &homepageBackend{pageID: "page_home"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		be.mu.Lock()
		defer be.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/scaffold-from-template"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Query().Get("resolve") == "true" &&
			be.slug != "" && strings.HasSuffix(path, "/pages/"+be.slug):
			var tree any
			if be.publishedRoot != nil {
				tree = map[string]any{"root": be.publishedRoot}
			}
			writePageWithTree(w, be.pageID, be.slug, tree)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/pages/home"):
			writePageMeta(w, be.pageID, be.slug)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/pages"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/pages/"+be.pageID):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"pages","attributes":{}}}`, be.pageID)
		case r.Method == http.MethodPut && strings.Contains(path, "/tree"):
			body, _ := io.ReadAll(r.Body)
			if tree, ok := decodeHubAttrs(t, body)["tree"].(map[string]any); ok {
				if root, ok := tree["root"].(map[string]any); ok {
					be.draftRoot = root
				}
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/pages/"+be.pageID+"/publish"):
			if be.draftRoot != nil {
				be.publishedRoot = be.draftRoot
			} else {
				be.publishedRoot = emptyStackRoot()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pp_1","type":"page-publishes","attributes":{"section_count":1}}}`))
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

func emptyStackRoot() map[string]any {
	return map[string]any{"children": []any{}}
}

func writePageMeta(w http.ResponseWriter, id, slug string) {
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"pages","attributes":{"slug":%q,"is_homepage":true}}}`, id, slug)
}

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

func TestPublishAbsentDraft_ResolvesEmptyStackNonNull(t *testing.T) {
	srv, _ := newHomepageBackend(t)
	cl := client.New(srv.URL, "k")
	ctx := context.Background()

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
