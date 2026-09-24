package cmd

// hubs_scaffold_content_nodes_test.go — MIO-4167: the client-side scaffold
// reconciles the playlists it builds into content items, the way the backend
// whole-hub op's _step_content_nodes does (mio-backend app/hub_scaffold/
// service.py, MIO-3258 T3).
//
// THE ORACLE IS THE WIRE. What matters is the reconcile REQUEST: that it fires,
// where in the pipeline it fires, and that its playlist_ids are exactly the ids
// this run created or recovered — never a bodyless POST (which asks the backend
// to derive the set from HubTemplateApplication provenance a client-side
// scaffold never has, and 422s no_playlist_provenance), and never a guess.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/docexamples"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// contentNodesWire is the traffic a full scaffold run emits that decides these
// tests: the ordered write log, and every reconcile request verbatim.
type contentNodesWire struct {
	events          []string // "playlist-create:<id>", "playlist-publish", "reconcile", "page-create"
	reconcilePaths  []string
	reconcileBodies [][]byte
	opPosts         int
}

// reconcileOK answers the reconcile POST the way mio-backend does
// (HubContentReconcileResponse): one D7 result per NodeSpec.
const reconcileOKBody = `{"data":{"id":"hub_new","type":"content_node_reconciliations","attributes":{
  "hub_id":"hub_new",
  "results":[
    {"legacy_hash":"h_c1","node_type":"container","outcome":"created","node_id":"cn_1","member_visible":true,"reason":null},
    {"legacy_hash":"h_l1","node_type":"lesson","outcome":"adopted","node_id":"cn_2","member_visible":true,"reason":null},
    {"legacy_hash":"h_c2","node_type":"container","outcome":"skipped_slug_conflict","node_id":null,"member_visible":null,"reason":"slug taken"}
  ]}}}`

// mintedPlaylistIDs are the ids contentNodesServer gives its first playlist
// creates, in creation order: deliberately neither sorted nor reverse-sorted.
var mintedPlaylistIDs = []string{"pl_c", "pl_a", "pl_b"}

func reconcileOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(reconcileOKBody))
}

// playlistsCatalogBody is the 2.1 fixture's community template carrying the
// starter template's playlist block (without documents or a page binding, so
// nothing but the playlists themselves is in play) — including the two
// playlists that share a title, which is what makes a title-based guess wrong.
func playlistsCatalogBody(t *testing.T) []byte {
	t.Helper()
	return vocabCatalogBody(t, func(ht map[string]any, _ map[string]any) {
		ht["playlists"] = []any{
			map[string]any{"key": "getting-started", "title": "Getting Started", "visibility": "public"},
			map[string]any{"key": "placeholder-2", "title": "Add another playlist", "visibility": "public"},
			map[string]any{"key": "placeholder-3", "title": "Add another playlist", "visibility": "public"},
		}
	})
}

// contentNodesServer serves a full CREATE-mode scaffold. The whole-hub op
// answers opStatus/opBody (405 = absent, so the client-side pipeline runs);
// every team playlist create mints a DISTINCT id so a dropped or reordered id
// is visible in the reconcile body; reconcile answers via the given handler.
//
// The ids are minted OUT of lexical order (mintedPlaylistIDs): creation order
// is template order, so ids that also sorted into that order could not tell
// "template order" from "sorted", and a sort.Strings on the ids would pass.
func contentNodesServer(t *testing.T, catBody []byte, opStatus int, opBody string, reconcile func(w http.ResponseWriter)) (*httptest.Server, *contentNodesWire) {
	t.Helper()
	rec := &contentNodesWire{}
	playlists := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catBody) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/hubs/from-template"):
			rec.opPosts++
			if opStatus == http.StatusMethodNotAllowed {
				w.Header().Set("Allow", "GET")
			}
			w.WriteHeader(opStatus)
			_, _ = w.Write([]byte(opBody))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/content/reconcile"):
			rec.events = append(rec.events, "reconcile")
			rec.reconcilePaths = append(rec.reconcilePaths, path)
			rec.reconcileBodies = append(rec.reconcileBodies, body)
			reconcile(w)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/scaffold-from-template"):
			w.WriteHeader(http.StatusNotFound) // the pages op is absent here
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/teams/t_team1/hubs"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/teams/t_team1/playlists"):
			playlists++
			id := fmt.Sprintf("pl_%d", playlists)
			if playlists <= len(mintedPlaylistIDs) {
				id = mintedPlaylistIDs[playlists-1]
			}
			rec.events = append(rec.events, "playlist-create:"+id)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"playlists","attributes":{}}}`, id)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/hubs/hub_new/playlists"):
			rec.events = append(rec.events, "playlist-publish")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"hm_1","type":"hub_media","attributes":{}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/pages"):
			rec.events = append(rec.events, "page-create")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"pg_new","type":"pages","attributes":{"slug":"home","is_homepage":true}}}`))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"res_new","type":"resources","attributes":{"slug":"home","is_homepage":true}}}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/hubs/hub_new"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/hubs/hub_new"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hubs","attributes":{"slug":"my-community","title":"Founders","is_private":true,"branding":{"primary":"#000"}}}}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`)) // a fresh hub: every collection is empty
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// clientPathContentNodesServer is contentNodesServer with the whole-hub op
// absent, i.e. the client-side pipeline — the path this ticket is about.
func clientPathContentNodesServer(t *testing.T, catBody []byte, reconcile func(w http.ResponseWriter)) (*httptest.Server, *contentNodesWire) {
	t.Helper()
	return contentNodesServer(t, catBody, http.StatusMethodNotAllowed, `{"detail":"Method Not Allowed"}`, reconcile)
}

func indexOfEvent(events []string, prefix string, last bool) int {
	at := -1
	for i, e := range events {
		if strings.HasPrefix(e, prefix) {
			if !last {
				return i
			}
			at = i
		}
	}
	return at
}

// TestScaffoldContentNodes_ReconcilesExactlyThePlaylistsItCreated: a
// client-side scaffold of a template with playlists POSTs the reconcile route
// ONCE, naming exactly the ids its playlists step created, in template order —
// between the last playlist publish and the first page, the position the
// backend op's _step_content_nodes takes.
//
// Before MIO-4167 no reconcile fired at all, so every client-built hub's
// playlist files had no content item (no progress tracking, My List, comments)
// until someone ran `content reconcile --playlist-id` by hand.
func TestScaffoldContentNodes_ReconcilesExactlyThePlaylistsItCreated(t *testing.T) {
	srv, rec := clientPathContentNodesServer(t, playlistsCatalogBody(t), reconcileOK)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if len(rec.reconcileBodies) != 1 {
		t.Fatalf("reconcile POSTs = %d, want exactly 1 — the client-side scaffold must reconcile the playlists it built; events=%v",
			len(rec.reconcileBodies), rec.events)
	}
	if want := "/api/v1/teams/t_team1/hubs/hub_new/content/reconcile"; rec.reconcilePaths[0] != want {
		t.Errorf("reconcile path = %q, want %q", rec.reconcilePaths[0], want)
	}
	// EXPLICIT ids, in template order. A bodyless POST would ask the backend for
	// HubTemplateApplication provenance this path never records (422
	// no_playlist_provenance); a missing or reordered id reconciles the wrong set
	// (the backend positions each container by its index in this list). The
	// stub mints pl_c, pl_a, pl_b in that order, so sorted order would differ.
	assertExactBody(t, rec.reconcileBodies[0], `{
		"data": {
			"type": "content_node_reconciliations",
			"attributes": { "playlist_ids": ["pl_c", "pl_a", "pl_b"] }
		}
	}`)

	// Between playlists and pages, like the op: after the playlists are
	// published (a lesson is created published only when its file AND playlist
	// are) and before any page is written.
	at := indexOfEvent(rec.events, "reconcile", false)
	if lastPub := indexOfEvent(rec.events, "playlist-publish", true); lastPub < 0 || at < lastPub {
		t.Errorf("reconcile must follow the last playlist publish; events=%v", rec.events)
	}
	if firstPage := indexOfEvent(rec.events, "page-create", false); firstPage < 0 || at > firstPage {
		t.Errorf("reconcile must precede the first page create; events=%v", rec.events)
	}
}

// TestScaffoldContentNodes_ResultReportsTheReconciledNodes: `-o json` reports
// what the reconcile answered, per node — the same content_nodes key the op
// path fills from its summary (TestScaffoldHubOp_ReportsTheOpsContentNodes).
func TestScaffoldContentNodes_ResultReportsTheReconciledNodes(t *testing.T) {
	srv, _ := clientPathContentNodesServer(t, playlistsCatalogBody(t), reconcileOK)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	out := decodeScaffoldJSON(t, res.Stdout)
	want := []any{
		map[string]any{"legacy_hash": "h_c1", "outcome": "created", "node_id": "cn_1"},
		map[string]any{"legacy_hash": "h_l1", "outcome": "adopted", "node_id": "cn_2"},
		map[string]any{"legacy_hash": "h_c2", "outcome": "skipped_slug_conflict", "node_id": nil},
	}
	if got := out["content_nodes"]; !reflect.DeepEqual(got, want) {
		t.Errorf("content_nodes = %v, want %v", got, want)
	}
}

// TestScaffoldContentNodes_BackendWithoutTheRouteIsANoteNotAFailure: an older
// backend has no reconcile route, so POST …/content/reconcile falls onto the
// GET|PATCH|DELETE /content/{node_id} route and answers 405 (or 404 on one with
// no content router at all). That is a stderr note and the run carries on — the
// hub is still built — and the result says the run did NOT reconcile (null),
// which is not the same as reconciling nothing.
//
// The reconcile route's OWN 404 (hub_not_found) is not an absent route: it
// surfaces, like every other failure.
func TestScaffoldContentNodes_BackendWithoutTheRouteIsANoteNotAFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter)
	}{
		{"405 from the content node route", func(w http.ResponseWriter) {
			w.Header().Set("Allow", "GET, PATCH, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"detail":"Method Not Allowed"}`))
		}},
		{"404 with no route at all", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := clientPathContentNodesServer(t, playlistsCatalogBody(t), tc.answer)

			res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0 — a backend without the route is not a failed scaffold; stderr=%q", res.Code, res.Stderr)
			}
			if indexOfEvent(rec.events, "page-create", false) < 0 {
				t.Errorf("the pipeline must carry on to the pages; events=%v", rec.events)
			}
			if !strings.Contains(res.Stderr, "content/reconcile") {
				t.Errorf("the skip must be narrated on stderr, naming the route; stderr=%q", res.Stderr)
			}
			out := decodeScaffoldJSON(t, res.Stdout)
			if v, present := out["content_nodes"]; !present || v != nil {
				t.Errorf("content_nodes = %v (present=%t), want null — the run did not reconcile", v, present)
			}
		})
	}

	t.Run("the route's own 404 hub_not_found surfaces", func(t *testing.T) {
		srv, rec := clientPathContentNodesServer(t, playlistsCatalogBody(t), func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","code":"hub_not_found","detail":"Hub 'hub_new' not found in team 't_team1'."}]}`))
		})
		res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
		if res.Code != errs.ExitNotFound {
			t.Fatalf("exit = %d, want %d — hub_not_found comes from the route itself, so the route exists; stderr=%q",
				res.Code, errs.ExitNotFound, res.Stderr)
		}
		if indexOfEvent(rec.events, "page-create", false) >= 0 {
			t.Errorf("a failed step must stop the pipeline; events=%v", rec.events)
		}
	})
}

// TestScaffoldContentNodes_OtherFailuresStopTheRunAndNameTheRecovery: any other
// failure stops the pipeline like every other step's does — but the error names
// the exact reconcile command, with the ids, because a resume skips the
// playlists step and can only recover the ids a page binding needs.
func TestScaffoldContentNodes_OtherFailuresStopTheRunAndNameTheRecovery(t *testing.T) {
	boom := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"status":"500","detail":"boom"}]}`))
	}
	srv, rec := clientPathContentNodesServer(t, playlistsCatalogBody(t), boom)
	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitServer {
		t.Fatalf("exit = %d, want %d; stderr=%q", res.Code, errs.ExitServer, res.Stderr)
	}
	if indexOfEvent(rec.events, "page-create", false) >= 0 {
		t.Errorf("a failed step must stop the pipeline; events=%v", rec.events)
	}

	// The error text is what main.go renders into the stderr envelope. A fresh
	// server, so the minted playlist ids start again from the first.
	srv2, _ := clientPathContentNodesServer(t, playlistsCatalogBody(t), boom)
	err := executeCLI(t, scaffoldEnv(t, srv2.URL), scaffoldArgs()...)
	if err == nil {
		t.Fatal("a failed reconcile must return an error")
	}
	want := "mio content reconcile --hub hub_new --team t_team1 --playlist-id pl_c --playlist-id pl_a --playlist-id pl_b"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the failure must name the recovery command with every id (%q); err=%v", want, err)
	}

	// An API detail that already ends in a period is not given a second one.
	srv3, _ := clientPathContentNodesServer(t, playlistsCatalogBody(t), func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"status":"422","detail":"Rejected by the route."}]}`))
	})
	err = executeCLI(t, scaffoldEnv(t, srv3.URL), scaffoldArgs()...)
	if err == nil || !strings.Contains(err.Error(), "Rejected by the route. The playlists exist") {
		t.Errorf("want the detail, one period, then the recovery text; err=%v", err)
	}
	if !strings.Contains(err.Error(), `step "content-nodes" failed`) {
		t.Errorf("the failure must name its step; err=%v", err)
	}
}

// TestScaffoldContentNodes_TemplateWithoutPlaylistsFiresNothing: nothing to
// reconcile means no request — and a result of [] (reconciled nothing, for
// certain), not null (did not reconcile).
func TestScaffoldContentNodes_TemplateWithoutPlaylistsFiresNothing(t *testing.T) {
	srv, rec := clientPathContentNodesServer(t, catalog21Body(t), reconcileOK)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if len(rec.reconcileBodies) != 0 {
		t.Errorf("reconcile POSTs = %d, want 0 for a template with no playlists", len(rec.reconcileBodies))
	}
	out := decodeScaffoldJSON(t, res.Stdout)
	if got, ok := out["content_nodes"].([]any); !ok || len(got) != 0 {
		t.Errorf("content_nodes = %#v, want [] — nothing to reconcile is a known outcome", out["content_nodes"])
	}
}

// TestScaffoldContentNodes_DryRunNamesTheStepAndWritesNothing: the plan names
// the reconcile and its playlists; the run stays write-free.
func TestScaffoldContentNodes_DryRunNamesTheStepAndWritesNothing(t *testing.T) {
	srv, mutated := func() (*httptest.Server, *bool) {
		s, _, m := liveCatalogScaffoldServer(t, playlistsCatalogBody(t))
		return s, m
	}()
	res := runContract(t, scaffoldEnv(t, srv.URL),
		humanScaffold(withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run"))...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if *mutated {
		t.Error("dry-run must fire no mutating request")
	}
	if !strings.Contains(res.Stdout, "content-nodes — POST /api/teams/t_team1/hubs/<hub_id>/content/reconcile") {
		t.Errorf("the plan must name the reconcile route; stdout:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "[getting-started, placeholder-2, placeholder-3]") {
		t.Errorf("the plan must name the playlists it reconciles; stdout:\n%s", res.Stdout)
	}
}

// ─── resume: never guess ───────────────────────────────────────────────────────

// resumeReconcileServer answers the playlists step's O1 gate with existing hub
// playlist rows (so the create loop is skipped), serves each playlist's title,
// and records every reconcile body. Any other POST is recorded as a write the
// resume must not make.
func resumeReconcileServer(t *testing.T, titlesByID map[string]string) (*httptest.Server, *[][]byte, *int) {
	t.Helper()
	var bodies [][]byte
	otherPosts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		if r.Method == http.MethodPost && strings.HasSuffix(path, "/content/reconcile") {
			bodies = append(bodies, body)
			reconcileOK(w)
			return
		}
		if r.Method == http.MethodPost {
			otherPosts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"unexpected","type":"x","attributes":{}}}`))
			return
		}
		if strings.Contains(path, "/playlists/") && !strings.Contains(path, "/hubs/") {
			// Scoped to hub_1: the playlists a first run created carry its hub_id.
			id := path[strings.LastIndex(path, "/")+1:]
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"playlists","attributes":{"title":%q,"hub_id":"hub_1"}}}`, id, titlesByID[id])
			return
		}
		rows := make([]string, 0, len(titlesByID))
		for _, id := range sortedKeys(titlesByID) {
			rows = append(rows, fmt.Sprintf(`{"id":"hm_%s","type":"hub_media","attributes":{"playlist_id":%q}}`, id, id))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(rows, ","))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies, &otherPosts
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestScaffoldContentNodes_ResumeNeverGuessesByTitle: on a resume the playlists
// step is skipped (the hub already has playlists), so the only ids this run has
// are the ones recoverPlaylistIDsForBindings recovered unambiguously. The step
// reconciles exactly those and NAMES the rest — the two placeholder playlists
// share a title, so any title match for them would be a coin-flip.
func TestScaffoldContentNodes_ResumeNeverGuessesByTitle(t *testing.T) {
	tmpl := playlistsTemplate()
	titles := map[string]string{
		"pl_gs":  "Getting Started",
		"pl_ph1": "Add another playlist",
		"pl_ph2": "Add another playlist",
	}

	t.Run("reconciles only the recovered id and names the rest", func(t *testing.T) {
		srv, bodies, otherPosts := resumeReconcileServer(t, titles)
		var notes strings.Builder
		sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
		sc.noteW = &notes
		sc.pagePlan = bandPagePlan() // binds getting-started, so the playlists step recovers it
		if err := stepPlaylists(sc, tmpl); err != nil {
			t.Fatalf("stepPlaylists: %v", err)
		}
		if err := stepContentNodes(sc, tmpl); err != nil {
			t.Fatalf("stepContentNodes: %v", err)
		}
		if *otherPosts != 0 {
			t.Errorf("a resume must make no other write; got %d POST(s)", *otherPosts)
		}
		if len(*bodies) != 1 {
			t.Fatalf("reconcile POSTs = %d, want 1 (for the one recovered id)", len(*bodies))
		}
		assertExactBody(t, (*bodies)[0], `{"data":{"type":"content_node_reconciliations","attributes":{"playlist_ids":["pl_gs"]}}}`)
		for _, key := range []string{"placeholder-2", "placeholder-3"} {
			if !strings.Contains(notes.String(), key) {
				t.Errorf("the note must name unrecovered key %q; notes=%q", key, notes.String())
			}
		}
	})

	t.Run("nothing recovered means no request", func(t *testing.T) {
		srv, bodies, _ := resumeReconcileServer(t, titles)
		var notes strings.Builder
		sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
		sc.noteW = &notes
		sc.pagePlan = unboundPagePlan() // no binding: recovery looks nothing up
		if err := stepPlaylists(sc, tmpl); err != nil {
			t.Fatalf("stepPlaylists: %v", err)
		}
		if err := stepContentNodes(sc, tmpl); err != nil {
			t.Fatalf("stepContentNodes: %v", err)
		}
		if len(*bodies) != 0 {
			t.Errorf("reconcile POSTs = %d, want 0 — with no id in hand the only alternatives are a bodyless POST (422 no provenance) or a guess", len(*bodies))
		}
		if !strings.Contains(notes.String(), "mio content reconcile --hub hub_1") {
			t.Errorf("the note must say how to reconcile by hand; notes=%q", notes.String())
		}
		if got := contentNodesResult(sc); got != nil {
			t.Errorf("content_nodes = %v, want null — this run did not reconcile", got)
		}
	})
}

// ─── the op path reports the same key ─────────────────────────────────────────

// hubOpContentNodesBody is a live op answer for a template with playlists: its
// _step_content_nodes rows are `content_node:<legacy_hash>`, created (with an id
// appended to created_resource_ids.content_nodes) or skipped with the D7
// outcome as the reason.
func hubOpContentNodesBody(contentIDs string) string {
	return `{"data":{"id":"hub_new","type":"hub_scaffolds","attributes":{
  "hub_id":"hub_new",
  "summary":[
    {"resource":"hub","action":"created"},
    {"resource":"playlist:getting-started","action":"created"},
    {"resource":"playlist:placeholder-2","action":"created"},
    {"resource":"playlist:placeholder-3","action":"created"},
    {"resource":"content_node:h_c1","action":"created"},
    {"resource":"content_node:h_l1","action":"skipped","reason":"adopted"},
    {"resource":"content_node:h_c2","action":"created"},
    {"resource":"page:homepage","action":"created"},
    {"resource":"publish","action":"created"}
  ],
  "created_resource_ids":{
    "hubs":["hub_new"],
    "playlists":["pl_a","pl_b","pl_c"],
    "content_nodes":` + contentIDs + `,
    "pages":["pg_home"]
  },
  "replayed":false}}}`
}

// TestScaffoldHubOp_ReportsTheOpsContentNodes: the whole-hub op already
// materialises content items; its result must reach `-o json` under the same
// key and entry shape the client path reports, paired to ids with the same
// count check every other kind gets.
func TestScaffoldHubOp_ReportsTheOpsContentNodes(t *testing.T) {
	t.Run("rows paired to their ids", func(t *testing.T) {
		srv, rec := contentNodesServer(t, playlistsCatalogBody(t), http.StatusCreated, hubOpContentNodesBody(`["cn_a","cn_b"]`), reconcileOK)
		res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
		}
		if rec.opPosts != 1 {
			t.Fatalf("op POSTs = %d, want 1 — this subtest is about the op path", rec.opPosts)
		}
		if len(rec.reconcileBodies) != 0 {
			t.Errorf("the op built the content items; the client must not reconcile on top (%d POSTs)", len(rec.reconcileBodies))
		}
		want := []any{
			map[string]any{"legacy_hash": "h_c1", "outcome": "created", "node_id": "cn_a"},
			map[string]any{"legacy_hash": "h_l1", "outcome": "adopted", "node_id": nil},
			map[string]any{"legacy_hash": "h_c2", "outcome": "created", "node_id": "cn_b"},
		}
		if got := decodeScaffoldJSON(t, res.Stdout)["content_nodes"]; !reflect.DeepEqual(got, want) {
			t.Errorf("content_nodes = %v, want %v", got, want)
		}
	})

	t.Run("a count mismatch drops the ids", func(t *testing.T) {
		srv, _ := contentNodesServer(t, playlistsCatalogBody(t), http.StatusCreated, hubOpContentNodesBody(`["cn_a"]`), reconcileOK)
		res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
		}
		nodes, _ := decodeScaffoldJSON(t, res.Stdout)["content_nodes"].([]any)
		if len(nodes) != 3 {
			t.Fatalf("content_nodes = %v, want 3 entries", nodes)
		}
		for _, raw := range nodes {
			if n := raw.(map[string]any); n["node_id"] != nil {
				t.Errorf("%v: node_id = %v, want null — with the counts disagreeing any pairing is a guess", n["legacy_hash"], n["node_id"])
			}
		}
	})

	t.Run("playlists created but no content rows is not an empty result", func(t *testing.T) {
		// A backend op that predates MIO-3258 T3 creates playlists and reports no
		// content_node rows at all. Reporting [] would claim it reconciled nothing
		// for certain; it did not reconcile.
		body := strings.Replace(hubOpContentNodesBody(`[]`), `{"resource":"content_node:h_c1","action":"created"},
    {"resource":"content_node:h_l1","action":"skipped","reason":"adopted"},
    {"resource":"content_node:h_c2","action":"created"},`, "", 1)
		srv, _ := contentNodesServer(t, playlistsCatalogBody(t), http.StatusCreated, body, reconcileOK)
		res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
		}
		if v, present := decodeScaffoldJSON(t, res.Stdout)["content_nodes"]; !present || v != nil {
			t.Errorf("content_nodes = %v (present=%t), want null", v, present)
		}
	})

	t.Run("the op's content step skipped wholesale is null and not called pre-T3", func(t *testing.T) {
		// The op reports one `content_nodes` row, skipped, when its content step
		// could not run at all (service.py _step_content_nodes, content_service
		// not wired). That is not a backend that predates MIO-3258 T3, so the
		// run must not say it is: the generic skipped-row note already says why.
		body := strings.Replace(hubOpContentNodesBody(`[]`), `{"resource":"content_node:h_c1","action":"created"},
    {"resource":"content_node:h_l1","action":"skipped","reason":"adopted"},
    {"resource":"content_node:h_c2","action":"created"},`,
			`{"resource":"content_nodes","action":"skipped","reason":"content_service was not wired onto this HubScaffoldService — no content_nodes were materialized"},`, 1)
		srv, _ := contentNodesServer(t, playlistsCatalogBody(t), http.StatusCreated, body, reconcileOK)
		res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
		}
		if v, present := decodeScaffoldJSON(t, res.Stdout)["content_nodes"]; !present || v != nil {
			t.Errorf("content_nodes = %v (present=%t), want null", v, present)
		}
		if !strings.Contains(res.Stderr, "content_service was not wired") {
			t.Errorf("the op's own skip reason must reach stderr; stderr=%q", res.Stderr)
		}
		if strings.Contains(res.Stderr, "predates") {
			t.Errorf("a skipped content step is not a backend that predates MIO-3258; stderr=%q", res.Stderr)
		}
	})
}

// TestScaffoldContentNodes_KeyIsOnBothPaths: the parity gate for this key
// specifically — the whole-object shape test compares key SETS, which a key
// missing from BOTH paths would pass.
func TestScaffoldContentNodes_KeyIsOnBothPaths(t *testing.T) {
	opSrv, _ := contentNodesServer(t, playlistsCatalogBody(t), http.StatusCreated, hubOpContentNodesBody(`["cn_a","cn_b"]`), reconcileOK)
	cliSrv, _ := clientPathContentNodesServer(t, playlistsCatalogBody(t), reconcileOK)
	for name, url := range map[string]string{"op": opSrv.URL, "client": cliSrv.URL} {
		res := runContract(t, scaffoldEnv(t, url), scaffoldArgs()...)
		if res.Code != errs.ExitOK {
			t.Fatalf("%s path exit = %d; stderr=%q", name, res.Code, res.Stderr)
		}
		nodes, ok := decodeScaffoldJSON(t, res.Stdout)["content_nodes"].([]any)
		if !ok || len(nodes) == 0 {
			t.Fatalf("%s path: content_nodes = %v, want the reconciled entries", name, nodes)
		}
		var keys []string
		for k := range nodes[0].(map[string]any) {
			keys = append(keys, k)
		}
		if got := strings.Join(sortedKeys(toSet(keys)), ","); got != "legacy_hash,node_id,outcome" {
			t.Errorf("%s path: content_nodes entry keys = %s, want legacy_hash,node_id,outcome", name, got)
		}
	}
}

func toSet(keys []string) map[string]string {
	m := map[string]string{}
	for _, k := range keys {
		m[k] = k
	}
	return m
}

// playlistsTemplate is the resolved form of playlistsCatalogBody's block.
func playlistsTemplate() *catalog.HubTemplate {
	return &catalog.HubTemplate{
		ID: "starter",
		Playlists: []catalog.TemplatePlaylist{
			{Title: "Getting Started", Key: "getting-started", Visibility: "public"},
			{Title: "Add another playlist", Key: "placeholder-2", Visibility: "public"},
			{Title: "Add another playlist", Key: "placeholder-3", Visibility: "public"},
		},
	}
}

// ─── resume: a playlist the hub merely publishes is not the hub's ─────────────

// scopedPlaylist is one playlist a resume stub serves: its title (what recovery
// joins on) and its hub_id (what the reconcile route checks). An empty hubID is
// a team-library playlist, hub_id null, that was published into the hub.
type scopedPlaylist struct{ title, hubID string }

// reconcileLikeTheBackend answers POST …/hubs/{hub}/content/reconcile with the
// backend's rule (reconcile_service.py _validated_explicit_playlist_ids): if ANY
// id does not resolve to a playlist whose hub_id is this hub, the WHOLE request
// is 422 playlist_not_in_hub; otherwise one container result per id. A stub
// that accepted any id would confirm a reconcile the real route refuses.
func reconcileLikeTheBackend(w http.ResponseWriter, hubID string, body []byte, playlists map[string]scopedPlaylist) {
	var doc struct {
		Data struct {
			Attributes struct {
				PlaylistIDs []string `json:"playlist_ids"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &doc)
	var invalid, results []string
	for _, id := range doc.Data.Attributes.PlaylistIDs {
		if p, ok := playlists[id]; !ok || p.hubID != hubID {
			invalid = append(invalid, id)
			continue
		}
		results = append(results, fmt.Sprintf(`{"legacy_hash":"h_%s","node_type":"container","outcome":"created","node_id":"cn_%s"}`, id, id))
	}
	if len(invalid) > 0 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = fmt.Fprintf(w, `{"errors":[{"status":"422","code":"playlist_not_in_hub","detail":"playlist_ids contains %d id(s) that do not belong to hub '%s'."}]}`, len(invalid), hubID)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"content_node_reconciliations","attributes":{"hub_id":%q,"results":[%s]}}}`,
		hubID, hubID, strings.Join(results, ","))
}

// bindingPlaylistsCatalogBody is playlistsCatalogBody plus a homepage whose
// root binds the getting-started playlist by key (the starter homepage's band),
// which is what makes a resume RECOVER that playlist's id.
func bindingPlaylistsCatalogBody(t *testing.T) []byte {
	t.Helper()
	return vocabCatalogBody(t, func(ht map[string]any, cat map[string]any) {
		ht["playlists"] = []any{
			map[string]any{"key": "getting-started", "title": "Getting Started", "visibility": "public"},
			map[string]any{"key": "placeholder-2", "title": "Add another playlist", "visibility": "public"},
			map[string]any{"key": "placeholder-3", "title": "Add another playlist", "visibility": "public"},
		}
		pts, _ := cat["pageTemplates"].([]any)
		for _, raw := range pts {
			pt, _ := raw.(map[string]any)
			if pt["id"] != "page-homepage-community" {
				continue
			}
			starter, _ := pt["starter"].(map[string]any)
			starter["dataSource"] = map[string]any{"type": "playlist", "id": "", "key": "getting-started"}
		}
	})
}

// scopedResumeServer serves a full `hubs scaffold --hub hub_x` run onto a hub
// that already publishes the given playlists (so the playlists step skips and
// recovers by title), answering reconcile with the backend's hub-scope rule.
func scopedResumeServer(t *testing.T, catBody []byte, playlists map[string]scopedPlaylist) (*httptest.Server, *contentNodesWire) {
	t.Helper()
	const hubID = "hub_x"
	rec := &contentNodesWire{}
	titles := make(map[string]string, len(playlists))
	for id, p := range playlists {
		titles[id] = p.title
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if catBody != nil && serveCatalogGET(w, r, catBody) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/content/reconcile"):
			rec.events = append(rec.events, "reconcile")
			rec.reconcilePaths = append(rec.reconcilePaths, path)
			rec.reconcileBodies = append(rec.reconcileBodies, body)
			reconcileLikeTheBackend(w, hubID, body, playlists)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/scaffold-from-template"):
			w.WriteHeader(http.StatusNotFound) // the pages op is absent here
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/pages"):
			rec.events = append(rec.events, "page-create")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"pg_new","type":"pages","attributes":{"slug":"homepage","is_homepage":true}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/teams/t_team1/playlists"):
			rec.events = append(rec.events, "playlist-create:unexpected")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"pl_unexpected","type":"playlists","attributes":{}}}`))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"res_new","type":"resources","attributes":{}}}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case strings.HasSuffix(path, "/hubs/"+hubID):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_x","type":"hubs","attributes":{"slug":"acme","title":"Acme","is_private":true}}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/hubs/"+hubID+"/playlists"):
			// The hub's publication rows. A row's hub_id is the hub it is
			// published INTO, never the playlist's own scope.
			rows := make([]string, 0, len(playlists))
			for _, id := range sortedKeys(titles) {
				rows = append(rows, fmt.Sprintf(`{"id":"hm_%s","type":"hub_media","attributes":{"hub_id":%q,"playlist_id":%q}}`, id, hubID, id))
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(rows, ","))
		case r.Method == http.MethodGet && strings.Contains(path, "/teams/t_team1/playlists/"):
			// PlaylistResource: hub_id is always serialized, null when unscoped.
			id := path[strings.LastIndex(path, "/")+1:]
			p := playlists[id]
			scope := "null"
			if p.hubID != "" {
				scope = fmt.Sprintf("%q", p.hubID)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"playlists","attributes":{"title":%q,"hub_id":%s}}}`, id, p.title, scope)
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func scopedResumeArgs() []string {
	return withTeam("t_team1", "hubs", "scaffold", "--template", "community", "--hub", "hub_x", "--output", "json")
}

// TestScaffoldContentNodes_ResumeReconcilesOnlyPlaylistsScopedToTheHub: the
// resume that recovers a playlist for a page binding may recover a TEAM-LIBRARY
// playlist: one published into the hub whose own hub_id is null (or another
// hub). The page binding may use it; the reconcile route may not, because it
// rejects the whole request with 422 playlist_not_in_hub when any id is not
// scoped to the hub. Sending it anyway failed the content-nodes step, which
// runs BEFORE pages, so a resume that exited 0 before MIO-4167 wrote no page at
// all, and every re-run recovered the same id and failed the same way.
//
// So the step reconciles only ids scoped to the hub and NAMES the rest. The
// oracle is the wire: no reconcile carrying the library id, and the pages
// written. The second subtest is the control: the same run with the playlist
// scoped to the hub DOES reconcile it, so the first cannot pass by never
// reconciling at all.
func TestScaffoldContentNodes_ResumeReconcilesOnlyPlaylistsScopedToTheHub(t *testing.T) {
	t.Run("a team-library playlist is named, not sent, and the pages still apply", func(t *testing.T) {
		srv, rec := scopedResumeServer(t, bindingPlaylistsCatalogBody(t), map[string]scopedPlaylist{
			"pl_team": {title: "Getting Started"}, // hub_id null: the team library
		})
		res := runContract(t, scaffoldEnv(t, srv.URL), scopedResumeArgs()...)
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want 0 — a playlist reconcile cannot take must not stop the resume; stderr=%q", res.Code, res.Stderr)
		}
		for i, b := range rec.reconcileBodies {
			if strings.Contains(string(b), "pl_team") {
				t.Errorf("reconcile #%d sent pl_team, a playlist not scoped to the hub (the route 422s playlist_not_in_hub): %s", i, b)
			}
		}
		if indexOfEvent(rec.events, "page-create", false) < 0 {
			t.Errorf("the resume must carry on to the pages; events=%v", rec.events)
		}
		for _, want := range []string{"getting-started", "pl_team", "not scoped to this hub"} {
			if !strings.Contains(res.Stderr, want) {
				t.Errorf("stderr must name %q; stderr=%q", want, res.Stderr)
			}
		}
		if v, present := decodeScaffoldJSON(t, res.Stdout)["content_nodes"]; !present || v != nil {
			t.Errorf("content_nodes = %v (present=%t), want null — this run reconciled nothing", v, present)
		}
	})

	t.Run("control: the same playlist scoped to the hub is reconciled", func(t *testing.T) {
		srv, rec := scopedResumeServer(t, bindingPlaylistsCatalogBody(t), map[string]scopedPlaylist{
			"pl_gs": {title: "Getting Started", hubID: "hub_x"},
		})
		res := runContract(t, scaffoldEnv(t, srv.URL), scopedResumeArgs()...)
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
		}
		if len(rec.reconcileBodies) != 1 {
			t.Fatalf("reconcile POSTs = %d, want 1; events=%v", len(rec.reconcileBodies), rec.events)
		}
		assertExactBody(t, rec.reconcileBodies[0], `{"data":{"type":"content_node_reconciliations","attributes":{"playlist_ids":["pl_gs"]}}}`)
		if indexOfEvent(rec.events, "page-create", false) < 0 {
			t.Errorf("the resume must carry on to the pages; events=%v", rec.events)
		}
	})
}

// TestScaffoldContentNodes_MissingKeyGuidanceYieldsIDsReconcileAccepts: the
// note for a key with no id tells the operator how to find the id. An agent
// runs that command verbatim, so the oracle is RUNNING it: against a team with
// a playlist scoped to the hub, a team-library one and another hub's (all
// three published into this hub), the command must print the scoped playlist's
// id and nothing reconcile would reject. `media hub-playlists list --hub`, the
// guidance this replaced, prints publication-row ids (hm_…) and every
// published playlist, the library one included.
func TestScaffoldContentNodes_MissingKeyGuidanceYieldsIDsReconcileAccepts(t *testing.T) {
	// 1. A resume that recovers nothing emits the guidance.
	srv, _, _ := resumeReconcileServer(t, map[string]string{"pl_gs": "Getting Started"})
	var notes strings.Builder
	sc := newStepSC(client.New(srv.URL, "k"), "hub_1", "acme")
	sc.noteW = &notes
	sc.pagePlan = unboundPagePlan()
	if err := stepPlaylists(sc, playlistsTemplate()); err != nil {
		t.Fatalf("stepPlaylists: %v", err)
	}
	if err := stepContentNodes(sc, playlistsTemplate()); err != nil {
		t.Fatalf("stepContentNodes: %v", err)
	}
	var finder []string
	for _, m := range regexp.MustCompile("`(mio [^`]+)`").FindAllStringSubmatch(notes.String(), -1) {
		if !strings.HasPrefix(m[1], "mio content reconcile") {
			res := docexamples.FromScript("note", 1, m[1])
			if len(res.Invocations) != 1 {
				t.Fatalf("could not parse the guidance %q as one invocation: %+v", m[1], res)
			}
			finder = res.Invocations[0].Args
		}
	}
	if finder == nil {
		t.Fatalf("the note must name a command that finds the ids; notes=%q", notes.String())
	}

	// 2. Run it against a team whose hub publishes three playlists, one its own.
	team := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		switch path := r.URL.Path; {
		case strings.HasSuffix(path, "/teams/t_team1/playlists"):
			_, _ = w.Write([]byte(`{"data":[
				{"id":"pl_mine","type":"playlists","attributes":{"title":"Getting Started","hub_id":"hub_1"}},
				{"id":"pl_lib","type":"playlists","attributes":{"title":"Library","hub_id":null}},
				{"id":"pl_theirs","type":"playlists","attributes":{"title":"Theirs","hub_id":"hub_other"}}]}`))
		case strings.HasSuffix(path, "/teams/t_team1/hubs/hub_1/playlists"):
			_, _ = w.Write([]byte(`{"data":[
				{"id":"hm_1","type":"hub_media","attributes":{"hub_id":"hub_1","playlist_id":"pl_mine"}},
				{"id":"hm_2","type":"hub_media","attributes":{"hub_id":"hub_1","playlist_id":"pl_lib"}},
				{"id":"hm_3","type":"hub_media","attributes":{"hub_id":"hub_1","playlist_id":"pl_theirs"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(team.Close)
	res := runContract(t, baseEnv(team.URL), finder...)
	if res.Code != errs.ExitOK {
		t.Fatalf("the guidance `mio %s` exited %d; stderr=%q", strings.Join(finder, " "), res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pl_mine") {
		t.Errorf("the guidance must print the id of the playlist scoped to the hub; ran `mio %s`, stdout=%q", strings.Join(finder, " "), res.Stdout)
	}
	for _, bad := range []string{"pl_lib", "pl_theirs", "hm_"} {
		if strings.Contains(res.Stdout, bad) {
			t.Errorf("the guidance printed %q, which reconcile rejects or which is not a playlist id; stdout=%q", bad, res.Stdout)
		}
	}
}

// twoBindingPagePlan is bandPagePlan binding each of keys, one section each.
func twoBindingPagePlan(keys ...string) *scaffoldPlan {
	p := bandPagePlan()
	kids := make([]any, 0, len(keys))
	for _, k := range keys {
		kids = append(kids, map[string]any{
			"kind":       "section",
			"dataSource": map[string]any{"type": "playlist", "id": "", "key": k},
		})
	}
	p.pages[0].rawTree["children"] = kids
	return p
}

// TestStepContentNodes_FiltersPerPlaylistNotPerRequest: with one recovered
// playlist scoped to the hub and one scoped elsewhere, the step reconciles the
// scoped one. Dropping the whole request because one id is foreign would lose
// content items the route would have made.
func TestStepContentNodes_FiltersPerPlaylistNotPerRequest(t *testing.T) {
	srv, rec := scopedResumeServer(t, nil, map[string]scopedPlaylist{
		"pl_gs":  {title: "Getting Started", hubID: "hub_x"},
		"pl_lib": {title: "Library", hubID: "hub_other"}, // another hub's playlist, published into this one
	})
	tmpl := &catalog.HubTemplate{ID: "starter", Playlists: []catalog.TemplatePlaylist{
		{Title: "Getting Started", Key: "getting-started", Visibility: "public"},
		{Title: "Library", Key: "library", Visibility: "public"},
	}}
	var notes strings.Builder
	sc := newStepSC(client.New(srv.URL, "k"), "hub_x", "acme")
	sc.noteW = &notes
	sc.pagePlan = twoBindingPagePlan("getting-started", "library")
	if err := stepPlaylists(sc, tmpl); err != nil {
		t.Fatalf("stepPlaylists: %v", err)
	}
	if sc.playlistIDsByKey["library"] != "pl_lib" {
		t.Fatalf("precondition: recovery binds library to pl_lib (the page may use it); got %v", sc.playlistIDsByKey)
	}
	if err := stepContentNodes(sc, tmpl); err != nil {
		t.Fatalf("stepContentNodes: %v", err)
	}
	if len(rec.reconcileBodies) != 1 {
		t.Fatalf("reconcile POSTs = %d, want 1 (for the playlist scoped to the hub)", len(rec.reconcileBodies))
	}
	assertExactBody(t, rec.reconcileBodies[0], `{"data":{"type":"content_node_reconciliations","attributes":{"playlist_ids":["pl_gs"]}}}`)
	for _, want := range []string{"library", "pl_lib"} {
		if !strings.Contains(notes.String(), want) {
			t.Errorf("the note must name %q; notes=%q", want, notes.String())
		}
	}
}
