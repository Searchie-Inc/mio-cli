package cmd

// hubs_scaffold_starter_test.go — MIO-3065: the hubTemplate vocabulary the
// starter template is the first to exercise (spaces[].icon, playlists[].
// documents, the playlist dataSource fill contract), the hub-scoped playlist
// create, and the gate that keeps a server op from silently dropping any of it.
//
// THE ORACLE IS THE WIRE, everywhere in this file. Each of these defects was
// invisible precisely because the CLI's own state looked right: a space was
// created, a playlist was created, a page was published. What was wrong was the
// REQUEST — a missing hub_id, a missing icon, a dataSource still carrying the
// catalog's empty id — so nothing short of reading the emitted body can tell a
// fixed run from a broken one.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── spaces[].icon ────────────────────────────────────────────────────────────

// TestStepSpaces_SendsTheTemplateIcon: the icon the template declares reaches
// the space create body. Before MIO-3065 templateSpaceInput copied name/slug/
// description/access/posting only, so every scaffolded space came out icon-less
// however loudly the catalog asked for one — and buildSpaceAttrs omits an unset
// field by design, so the request simply had no `icon` key to notice.
func TestStepSpaces_SendsTheTemplateIcon(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, b)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"sp_1","type":"spaces","attributes":{}}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "starter",
		Spaces: []catalog.TemplateSpace{
			{Name: "Announcements", Slug: "announcements", AccessLevel: "public", PostingPermission: "admins_only", Icon: "megaphone"},
			{Name: "General", Slug: "general", AccessLevel: "public", PostingPermission: "any_member"},
		},
	}
	if err := stepSpaces(sc, tmpl); err != nil {
		t.Fatalf("stepSpaces: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("space creates = %d, want 2", len(bodies))
	}
	if got := decodeHubAttrs(t, bodies[0])["icon"]; got != "megaphone" {
		t.Errorf("announcements create icon = %v, want megaphone (the declared sprite name)", got)
	}
	// A space that declares no icon must send no icon key — sending "" would
	// overwrite the backend's own default with an empty string.
	if got, present := decodeHubAttrs(t, bodies[1])["icon"]; present {
		t.Errorf("general declares no icon, so the create must omit the key; got %v", got)
	}
}

// ─── playlists: hub scope + documents ─────────────────────────────────────────

// playlistWire is the traffic stepPlaylists emits, split by endpoint.
type playlistWire struct {
	creates    [][]byte // POST …/playlists             — the team playlist create
	synthetics [][]byte // POST …/files/synthetic       — one per documents[] entry
	itemFiles  []string // POST …/playlists/{id}/items  — file_id, in attach order
	publishes  [][]byte // POST …/hubs/{hub}/playlists  — the playlist's publication row
	mediaPubs  [][]byte // POST …/hubs/{hub}/media      — a document file's publication row
}

func playlistWireServer(t *testing.T) (*httptest.Server, *playlistWire) {
	t.Helper()
	w := &playlistWire{}
	syn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/vnd.api+json")
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/files/synthetic"):
			syn++
			w.synthetics = append(w.synthetics, body)
			rw.WriteHeader(http.StatusCreated)
			_, _ = rw.Write([]byte(`{"data":{"id":"file_syn` + string(rune('0'+syn)) + `","type":"files","attributes":{}}}`))
		case strings.Contains(path, "/items"):
			fid, _ := decodeHubAttrs(t, body)["file_id"].(string)
			w.itemFiles = append(w.itemFiles, fid)
			rw.WriteHeader(http.StatusCreated)
			_, _ = rw.Write([]byte(`{"data":{"id":"it_1","type":"playlist_items","attributes":{}}}`))
		case strings.Contains(path, "/hubs/") && r.Method == http.MethodGet:
			rw.WriteHeader(http.StatusOK)
			_, _ = rw.Write([]byte(`{"data":[]}`)) // O1 gate: hub has no playlists
		case strings.HasSuffix(path, "/media") && r.Method == http.MethodPost:
			w.mediaPubs = append(w.mediaPubs, body)
			rw.WriteHeader(http.StatusCreated)
			_, _ = rw.Write([]byte(`{"data":{"id":"hm_f","type":"hub_media","attributes":{}}}`))
		case strings.Contains(path, "/hubs/") && r.Method == http.MethodPost:
			w.publishes = append(w.publishes, body)
			rw.WriteHeader(http.StatusCreated)
			_, _ = rw.Write([]byte(`{"data":{"id":"hm_1","type":"hub_media","attributes":{}}}`))
		case r.Method == http.MethodPost:
			w.creates = append(w.creates, body)
			rw.WriteHeader(http.StatusCreated)
			_, _ = rw.Write([]byte(`{"data":{"id":"pl_made","type":"playlists","attributes":{}}}`))
		default:
			rw.WriteHeader(http.StatusOK)
			_, _ = rw.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, w
}

// TestStepPlaylists_ScopesTheCreateToTheHub: the playlist create carries
// hub_id. Without it the playlist is team-scoped only and its hub detail page
// 404s for every viewer no matter how the publication row is configured — the
// defect that made every starter playlist page dead on arrival, latent until
// then because the only shipped template declared `playlists: []`.
func TestStepPlaylists_ScopesTheCreateToTheHub(t *testing.T) {
	srv, w := playlistWireServer(t)
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID:        "starter",
		Playlists: []catalog.TemplatePlaylist{{Title: "Getting Started", Key: "getting-started", Visibility: "public"}},
	}
	if err := stepPlaylists(sc, tmpl); err != nil {
		t.Fatalf("stepPlaylists: %v", err)
	}
	if len(w.creates) != 1 {
		t.Fatalf("playlist creates = %d, want 1", len(w.creates))
	}
	attrs := decodeHubAttrs(t, w.creates[0])
	if attrs["hub_id"] != "hub_1" {
		t.Errorf("playlist create hub_id = %v, want hub_1 — an unscoped playlist's hub page 404s", attrs["hub_id"])
	}
	if attrs["visibility"] != "public" {
		t.Errorf("playlist create visibility = %v, want public (the template's own)", attrs["visibility"])
	}
	// The publication row's visibility is a DIFFERENT enum and is the ratified
	// constant, not the template's value.
	if len(w.publishes) != 1 {
		t.Fatalf("hub publishes = %d, want 1", len(w.publishes))
	}
	if got := decodeHubAttrs(t, w.publishes[0])["visibility"]; got != scaffoldHubPlaylistVisibility {
		t.Errorf("publication row visibility = %v, want %q", got, scaffoldHubPlaylistVisibility)
	}
}

// TestStepPlaylists_MaterialisesDocumentsAsSyntheticItems: each documents[]
// entry becomes a synthetic READY document file and then a playlist item.
// Field-verified inert before MIO-3065 — the files were never created at all,
// so the band had nothing to render.
func TestStepPlaylists_MaterialisesDocumentsAsSyntheticItems(t *testing.T) {
	srv, w := playlistWireServer(t)
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	tmpl := &catalog.HubTemplate{
		ID: "starter",
		Playlists: []catalog.TemplatePlaylist{{
			Title: "Getting Started", Key: "getting-started", Visibility: "public",
			FileIDs: []string{"file_existing"},
			Documents: []catalog.TemplateDocument{
				{Title: "Add your first lesson", Description: "A placeholder lesson."},
				{Title: "Add another lesson"},
			},
		}},
	}
	if err := stepPlaylists(sc, tmpl); err != nil {
		t.Fatalf("stepPlaylists: %v", err)
	}
	if len(w.synthetics) != 2 {
		t.Fatalf("synthetic file registers = %d, want 2 (one per documents[] entry)", len(w.synthetics))
	}
	first := decodeHubAttrs(t, w.synthetics[0])
	if first["title"] != "Add your first lesson" || first["description"] != "A placeholder lesson." {
		t.Errorf("document 1 register attrs = %v, want the declared title + description", first)
	}
	if first["asset_kind"] != "document" {
		t.Errorf("document 1 asset_kind = %v, want document (no upload/finalize/transcode)", first["asset_kind"])
	}
	// The file mirrors its playlist's visibility: a public playlist whose items
	// are private (the endpoint's default) discloses no items to an anonymous
	// viewer, quietly contradicting the template.
	if first["visibility"] != "public" {
		t.Errorf("document 1 visibility = %v, want public (mirrors the playlist)", first["visibility"])
	}
	second := decodeHubAttrs(t, w.synthetics[1])
	if _, present := second["description"]; present {
		t.Errorf("a document with no description must omit the key; got %v", second["description"])
	}
	// file_ids first, then documents — the order the items land in.
	want := []string{"file_existing", "file_syn1", "file_syn2"}
	if strings.Join(w.itemFiles, ",") != strings.Join(want, ",") {
		t.Errorf("attached file ids = %v, want %v (file_ids then documents, in order)", w.itemFiles, want)
	}

	// Each CREATED document gets a per-hub publication row. Without it the file
	// is attached and invisible: measured on dev, the item-card projection
	// returned zero rows to an anonymous viewer until the row existed.
	if len(w.mediaPubs) != 2 {
		t.Fatalf("hub-media publishes = %d, want 2 (one per created document)", len(w.mediaPubs))
	}
	pubFiles := []string{}
	for _, b := range w.mediaPubs {
		attrs := decodeHubAttrs(t, b)
		id, _ := attrs["file_id"].(string)
		pubFiles = append(pubFiles, id)
		if attrs["visibility"] != scaffoldHubPlaylistVisibility {
			t.Errorf("document publication visibility = %v, want %q (the same row visibility the playlist gets)", attrs["visibility"], scaffoldHubPlaylistVisibility)
		}
		if at, _ := attrs["published_at"].(string); at == "" {
			t.Errorf("document publication must set published_at (a null one is a draft); attrs=%v", attrs)
		}
	}
	// NOT the file_ids entry: publishing media the template author already owns
	// is a side effect the scaffold does not take on their behalf.
	if strings.Join(pubFiles, ",") != "file_syn1,file_syn2" {
		t.Errorf("published file ids = %v, want only the files this run created", pubFiles)
	}
}

// ─── the fill contract, on the wire ───────────────────────────────────────────

// bandPagePlan is a one-page plan whose tree binds a playlist by key — the
// starter homepage's Getting Started band in miniature.
func bandPagePlan() *scaffoldPlan {
	return &scaffoldPlan{pages: []plannedPage{{
		ref: catalog.PageRef{Role: "homepage", PageTemplate: "page-band", Slug: "homepage", Title: "Home", Privacy: "public", IsHomepage: true},
		rawTree: map[string]any{
			"kind": "stack",
			"children": []any{map[string]any{
				"kind":       "section",
				"dataSource": map[string]any{"type": "playlist", "id": "", "key": "getting-started"},
			}},
		},
	}}}
}

// treePUTBodies captures the draft-tree PUTs a page apply emits.
func pageApplyServer(t *testing.T, puts *[][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scaffold-from-template"):
			w.WriteHeader(http.StatusNotFound) // pages op absent
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pages"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pages"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"pg_1","type":"pages","attributes":{"slug":"homepage"}}}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/tree"):
			*puts = append(*puts, body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"x","type":"pages","attributes":{}}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// treeDataSourceIDs pulls every playlist dataSource id out of a captured tree
// PUT body — the value mio-backend's compiler turns into the section's model_id.
func treeDataSourceIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var env struct {
		Data struct {
			Attributes struct {
				Tree struct {
					Root map[string]any `json:"root"`
				} `json:"tree"`
			} `json:"attributes"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("decode tree PUT body: %v", err)
	}
	var ids []string
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		if ds, ok := n["dataSource"].(map[string]any); ok {
			if typ, _ := ds["type"].(string); typ == "playlist" {
				id, _ := ds["id"].(string)
				ids = append(ids, id)
			}
		}
		if kids, ok := n["children"].([]any); ok {
			for _, c := range kids {
				if child, ok := c.(map[string]any); ok {
					walk(child)
				}
			}
		}
	}
	walk(env.Data.Attributes.Tree.Root)
	return ids
}

// TestApplyPage_BindsThePlaylistDataSourceIntoTheTreePUT: the created playlist's
// id is in the tree body the page PUT sends. This is the whole fill contract:
// the backend compiles what it is PUT, so an id patched in anywhere later — or
// merely held in a map — leaves the section bound to the catalog's empty string.
func TestApplyPage_BindsThePlaylistDataSourceIntoTheTreePUT(t *testing.T) {
	var puts [][]byte
	srv := pageApplyServer(t, &puts)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	cat, _, _ := scaffoldFixture(t)
	sc.cat, sc.pagePlan = cat, bandPagePlan()
	sc.hubTmpl = catalog.HubTemplate{ID: "starter", Pages: []catalog.PageRef{sc.pagePlan.pages[0].ref}}
	sc.playlistIDsByKey["getting-started"] = "pl_made"

	if err := stepPages(sc, &sc.hubTmpl); err != nil {
		t.Fatalf("stepPages: %v", err)
	}
	if len(puts) != 1 {
		t.Fatalf("tree PUTs = %d, want 1", len(puts))
	}
	got := treeDataSourceIDs(t, puts[0])
	if len(got) != 1 || got[0] != "pl_made" {
		t.Errorf("playlist dataSource ids in the PUT tree = %v, want [pl_made] — the created id must be IN the body the backend compiles", got)
	}
}

// The plan's rawTree must survive the bind untouched: a resume re-interpolates
// and re-binds from it, and a tree mutated in place would carry the FIRST run's
// playlist id into the second run's page.
func TestApplyPage_BindLeavesThePlanTreePristine(t *testing.T) {
	var puts [][]byte
	srv := pageApplyServer(t, &puts)

	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	cat, _, _ := scaffoldFixture(t)
	sc.cat, sc.pagePlan = cat, bandPagePlan()
	sc.hubTmpl = catalog.HubTemplate{ID: "starter", Pages: []catalog.PageRef{sc.pagePlan.pages[0].ref}}
	sc.playlistIDsByKey["getting-started"] = "pl_made"

	if err := stepPages(sc, &sc.hubTmpl); err != nil {
		t.Fatalf("stepPages: %v", err)
	}
	if got := catalog.PlaylistDataSourceKeys(sc.pagePlan.pages[0].rawTree); len(got) != 1 {
		t.Fatalf("plan tree lost its dataSource key: %v", got)
	}
	kids, _ := sc.pagePlan.pages[0].rawTree["children"].([]any)
	ds, _ := kids[0].(map[string]any)["dataSource"].(map[string]any)
	if ds["id"] != "" {
		t.Errorf("plan rawTree dataSource id = %v, want \"\" — the bind must run on the CLONE, not the plan", ds["id"])
	}
}

// ─── the server-op gate ───────────────────────────────────────────────────────

// vocabCatalogBody rewrites the 2.1 fixture's `community` hubTemplate with
// mutate and recomputes meta.digest so the artifact still verifies.
func vocabCatalogBody(t *testing.T, mutate func(ht map[string]any, cat map[string]any)) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(catalog21Body(t)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("parse 2.1 catalog fixture: %v", err)
	}
	hts, _ := doc["hubTemplates"].([]any)
	ht, _ := hts[0].(map[string]any)
	mutate(ht, doc)
	meta, _ := doc["meta"].(map[string]any)
	delete(meta, "digest")
	digest, err := catalog.Digest(doc)
	if err != nil {
		t.Fatalf("recompute digest: %v", err)
	}
	meta["digest"] = digest
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal mutated catalog: %v", err)
	}
	return out
}

// TestScaffoldOps_SkipWhenTheTemplateDeclaresVocabularyTheyDrop: the whole-hub
// op is not even PROBED for a template declaring something it does not apply,
// and the client-side pipeline builds the hub instead.
//
// The oracle is the wire on both sides: zero op POSTs AND a client-side hub
// create. Asserting only the note would pass over a run that printed the note
// and then called the op anyway.
func TestScaffoldOps_SkipWhenTheTemplateDeclaresVocabularyTheyDrop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   string
		mutate func(ht map[string]any, cat map[string]any)
	}{
		{
			name: "spaces[].icon",
			want: "spaces[].icon",
			mutate: func(ht map[string]any, _ map[string]any) {
				spaces, _ := ht["spaces"].([]any)
				sp, _ := spaces[0].(map[string]any)
				sp["icon"] = "megaphone"
			},
		},
		{
			name: "playlists[].documents",
			want: "playlists[].documents",
			mutate: func(ht map[string]any, _ map[string]any) {
				ht["playlists"] = []any{map[string]any{
					"title": "Getting Started", "key": "getting-started", "visibility": "public",
					"documents": []any{map[string]any{"title": "Add your first lesson"}},
				}}
			},
		},
		{
			name: "a playlist dataSource key",
			want: "a playlist dataSource key",
			mutate: func(ht map[string]any, cat map[string]any) {
				ht["playlists"] = []any{map[string]any{
					"title": "Getting Started", "key": "getting-started", "visibility": "public",
				}}
				pts, _ := cat["pageTemplates"].([]any)
				for _, raw := range pts {
					pt, _ := raw.(map[string]any)
					if pt["id"] != "page-homepage-community" {
						continue
					}
					starter, _ := pt["starter"].(map[string]any)
					starter["dataSource"] = map[string]any{"type": "playlist", "id": "", "key": "getting-started"}
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := vocabCatalogBody(t, tc.mutate)
			srv, rec := hubOpScaffoldServerWithCatalog(t, http.StatusCreated, hubOpLiveBody, body)

			res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			if len(rec.opPosts) != 0 {
				t.Errorf("the whole-hub op must NOT be probed for a template it cannot fully apply; got %d POST(s)", len(rec.opPosts))
			}
			if rec.hubCreates != 1 {
				t.Errorf("client-side hub creates = %d, want 1 — the run must fall to the pipeline", rec.hubCreates)
			}
			if !strings.Contains(res.Stderr, tc.want) {
				t.Errorf("the skip must NAME what the op would drop (%q); stderr=%q", tc.want, res.Stderr)
			}
		})
	}
}

// The control: the SAME harness, the unmutated fixture. A template declaring
// none of that vocabulary must still take the op — otherwise the gate above
// proves nothing (a CLI that never probed would pass every case).
func TestScaffoldOps_PlainTemplateStillTakesTheOp(t *testing.T) {
	srv, rec := hubOpScaffoldServerWithCatalog(t, http.StatusCreated, hubOpLiveBody, catalog21Body(t))

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if len(rec.opPosts) != 1 {
		t.Fatalf("op POSTs = %d, want 1 — a plain template must still use the op", len(rec.opPosts))
	}
	if rec.hubCreates != 0 {
		t.Errorf("the op built the hub; client-side create must not fire, got %d", rec.hubCreates)
	}
}

// TestStepPages_PlaylistBindingSkipsThePagesOp: one level down, the same rule
// with a NARROWER trigger. By the time stepPages runs, the client-side spaces
// and playlists steps have applied icons and documents already — the only thing
// still at stake is the fill contract, so only a binding key skips the probe.
func TestStepPages_PlaylistBindingSkipsThePagesOp(t *testing.T) {
	for _, tc := range []struct {
		name      string
		plan      *scaffoldPlan
		wantProbe int
	}{
		{"binding page skips the probe", bandPagePlan(), 0},
		{"unbound page still probes", unboundPagePlan(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.api+json")
				_, _ = io.ReadAll(r.Body)
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scaffold-from-template"):
					probes++
					w.WriteHeader(http.StatusNotFound)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pages"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"data":[]}`))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pages"):
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"data":{"id":"pg_1","type":"pages","attributes":{"slug":"homepage"}}}`))
				case r.Method == http.MethodPut:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"data":{"id":"x","type":"pages","attributes":{}}}`))
				}
			}))
			t.Cleanup(srv.Close)

			sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
			cat, _, _ := scaffoldFixture(t)
			sc.cat, sc.pagePlan = cat, tc.plan
			sc.hubTmpl = catalog.HubTemplate{ID: "starter", Pages: []catalog.PageRef{tc.plan.pages[0].ref}}
			sc.playlistIDsByKey["getting-started"] = "pl_made"

			if err := stepPages(sc, &sc.hubTmpl); err != nil {
				t.Fatalf("stepPages: %v", err)
			}
			if probes != tc.wantProbe {
				t.Errorf("pages-op probes = %d, want %d", probes, tc.wantProbe)
			}
		})
	}
}

// unboundPagePlan is bandPagePlan without the dataSource — the control that
// keeps the gate above from passing by never probing at all.
func unboundPagePlan() *scaffoldPlan {
	p := bandPagePlan()
	kids, _ := p.pages[0].rawTree["children"].([]any)
	delete(kids[0].(map[string]any), "dataSource")
	return p
}
