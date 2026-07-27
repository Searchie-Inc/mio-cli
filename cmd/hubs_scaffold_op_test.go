package cmd

// hubs_scaffold_op_test.go — MIO-2672 Task 9: the W2b backend-op probe, the
// design's ONLY runtime branch (spec §0). stepPages tries the backend's
// one-step POST …/pages/scaffold-from-template by simply calling it (the probe
// IS the real POST — no capability/HEAD check); 404 falls back to the
// client-side loop. Both branches produce a real hub. These tests drive
// stepPages against the scripted recoveryBackend (hubs_scaffold_pages_test.go),
// whose op route is scripted per test via be.opHandler.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// fixtureCatalogDigest is the 2.1 catalog fixture's meta.digest — the value the
// op POST must carry as catalog_digest (the preflight-resolved catalog's pin).
const fixtureCatalogDigest = "sha256:ab30e06a03eb7040b53754d3afac4313872d1c9e8dfc0c5f9cec9d2b6903c5eb"

// opCreated201 writes the op's 201 with all three community pages, roles keyed
// like the fixture slugs (published_revision is a PUBLISHED revision — the
// context must NOT read it as a draft version).
func opCreated201(w http.ResponseWriter) {
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"data":{"id":"ts_1","type":"template_scaffolds","attributes":{` +
		`"hub_id":"hub_1","pages":[` +
		`{"role":"homepage","page_id":"pg_home","published_revision":3},` +
		`{"role":"about","page_id":"pg_about","published_revision":1},` +
		`{"role":"faq","page_id":"pg_faq","published_revision":1}]}}}`))
}

// opConflict409 writes the op's digest-mismatch rejection.
func opConflict409(w http.ResponseWriter) {
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write([]byte(`{"errors":[{"status":"409","code":"catalog_digest_mismatch",` +
		`"detail":"catalog digest does not match the server catalog"}]}`))
}

// mutatedCatalogBody returns the 2.1 fixture with meta.revision set to rev and
// meta.digest recomputed over the modified document (catalog.Digest over the
// UseNumber-parsed doc), so the resolver's digest verification accepts it as a
// NEW, self-consistent catalog artifact.
func mutatedCatalogBody(t *testing.T, rev int) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(catalog21Body(t)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("parse 2.1 catalog fixture: %v", err)
	}
	meta, ok := doc["meta"].(map[string]any)
	if !ok {
		t.Fatal("2.1 catalog fixture has no meta object")
	}
	meta["revision"] = json.Number(strconv.Itoa(rev))
	delete(meta, "digest")
	digest, err := catalog.Digest(doc)
	if err != nil {
		t.Fatalf("recompute mutated catalog digest: %v", err)
	}
	meta["digest"] = digest
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal mutated catalog: %v", err)
	}
	return out
}

// TestStepPages_OpPathSkipsClientSideWrites: where the backend implements the
// op, ONE POST applies the whole pages[] plan server-side — zero page-level
// create/PUT/publish/PATCH/list calls fire — and the request carries the
// resolved catalog's digest plus the deterministic Idempotency-Key
// (ApplicationID). The homepage id lands in the context from the role entry;
// homeDraftVersion stays 0 (published_revision is NOT a draft version).
func TestStepPages_OpPathSkipsClientSideWrites(t *testing.T) {
	srv, be := newRecoveryBackend(t, nil, nil)
	be.opHandler = func(w http.ResponseWriter, _ int) { opCreated201(w) }

	sc, err := driveStepPages(t, srv.URL)
	if err != nil {
		t.Fatalf("stepPages via the backend op: %v", err)
	}
	if be.opPosts != 1 {
		t.Fatalf("op POSTs = %d, want 1", be.opPosts)
	}
	if got := be.opBodies[0]["catalog_digest"]; got != fixtureCatalogDigest {
		t.Errorf("op catalog_digest = %v, want the resolved catalog's digest %s", got, fixtureCatalogDigest)
	}
	if want := catalog.ApplicationID("hub_1", "community"); be.opIdemKeys[0] != want {
		t.Errorf("Idempotency-Key = %q, want ApplicationID %q", be.opIdemKeys[0], want)
	}
	if be.mutations != 0 || be.listCalls != 0 || len(be.createdSlugs) != 0 {
		t.Errorf("op path must fire ZERO page-level calls; mutations=%d listCalls=%d created=%v",
			be.mutations, be.listCalls, be.createdSlugs)
	}
	if sc.homePageID != "pg_home" {
		t.Errorf("homePageID = %q, want pg_home (from the role==homepage entry)", sc.homePageID)
	}
	if sc.homeDraftVersion != 0 {
		t.Errorf("homeDraftVersion = %d, want 0 — published_revision must NOT be conflated with a draft version", sc.homeDraftVersion)
	}
}

// TestStepPages_Op404FallsBackToClientSide: the op is absent (dormant flag or
// older backend) — the probe 404s and the FULL client-side sequence fires for
// all 3 pages, at the cost of exactly ONE op POST. This is the design's only
// runtime branch: a missing op is never an error.
func TestStepPages_Op404FallsBackToClientSide(t *testing.T) {
	srv, be := newRecoveryBackend(t, nil, nil) // opHandler nil → 404

	sc, err := driveStepPages(t, srv.URL)
	if err != nil {
		t.Fatalf("stepPages client-side fallback: %v", err)
	}
	if be.opPosts != 1 {
		t.Errorf("op POSTs = %d, want exactly 1 (the probe cost)", be.opPosts)
	}
	if want := []string{"homepage", "about", "faq"}; !slices.Equal(be.createdSlugs, want) {
		t.Errorf("created slugs = %v, want %v (full client-side apply)", be.createdSlugs, want)
	}
	if be.mutations != 12 {
		t.Errorf("mutations = %d, want 12 (create+PUT+publish+PATCH × 3 pages)", be.mutations)
	}
	if notes := stepNotes(sc); !strings.Contains(notes, "applying client-side") {
		t.Errorf("the 404 fallback must emit an operator note; notes=%q", notes)
	}
}

// TestStepPages_Op409RefetchesOnceAndRetries: the op rejects the digest (the
// backend's pin moved between our preflight resolve and the POST) → the step
// re-resolves the catalog from the SAME backend, rebuilds the plan, and retries
// the op ONCE with the new digest — which succeeds. Exactly 2 op POSTs, with a
// catalog GET between them, and zero client-side page writes.
func TestStepPages_Op409RefetchesOnceAndRetries(t *testing.T) {
	t.Setenv("MIO_CATALOG_CACHE_DIR", t.TempDir())
	mutBody := mutatedCatalogBody(t, 8)
	mutCat, perr := catalog.Parse(mutBody)
	if perr != nil {
		t.Fatalf("parse mutated catalog: %v", perr)
	}
	newDigest := mutCat.Meta.Digest
	if newDigest == fixtureCatalogDigest {
		t.Fatal("mutated catalog must carry a DIFFERENT digest than the fixture")
	}

	srv, be := newRecoveryBackend(t, nil, nil)
	be.catalogBody = mutBody
	be.opHandler = func(w http.ResponseWriter, n int) {
		if n == 1 {
			opConflict409(w)
			return
		}
		opCreated201(w)
	}

	sc, err := driveStepPages(t, srv.URL)
	if err != nil {
		t.Fatalf("stepPages 409-refetch-retry: %v", err)
	}
	if be.opPosts != 2 {
		t.Fatalf("op POSTs = %d, want exactly 2 (409 then the single retry)", be.opPosts)
	}
	if be.catalogGETs < 1 {
		t.Errorf("catalog GETs = %d, want ≥1 (the refetch after the 409)", be.catalogGETs)
	}
	if want := []string{"op", "catalog", "op"}; !slices.Equal(be.events, want) {
		t.Errorf("event order = %v, want %v (refetch strictly between the two POSTs)", be.events, want)
	}
	if got := be.opBodies[1]["catalog_digest"]; got != newDigest {
		t.Errorf("retry catalog_digest = %v, want the REFETCHED digest %s", got, newDigest)
	}
	if sc.cat.Meta.Digest != newDigest || sc.cat.Meta.Revision != 8 {
		t.Errorf("context catalog = {digest %s, revision %d}, want the refetched {%s, 8}",
			sc.cat.Meta.Digest, sc.cat.Meta.Revision, newDigest)
	}
	if be.mutations != 0 {
		t.Errorf("mutations = %d, want 0 (no client-side writes on the op path)", be.mutations)
	}
	if sc.homePageID != "pg_home" {
		t.Errorf("homePageID = %q, want pg_home", sc.homePageID)
	}
}

// TestStepPages_Op409StableDigestSurfaces: the op rejects but the refetched
// catalog carries the SAME digest — the rejection was not pin staleness, so NO
// second op POST fires and the ORIGINAL error surfaces with guidance
// (ExitUsage preserved). No client-side fallback: the hub's pages state is
// unknown, and a blind client-side apply could double-write.
func TestStepPages_Op409StableDigestSurfaces(t *testing.T) {
	t.Setenv("MIO_CATALOG_CACHE_DIR", t.TempDir())
	srv, be := newRecoveryBackend(t, nil, nil)
	be.catalogBody = catalog21Body(t) // same digest as sc.cat
	be.opHandler = func(w http.ResponseWriter, _ int) { opConflict409(w) }

	_, err := driveStepPages(t, srv.URL)
	if err == nil {
		t.Fatal("a 409 with an unchanged catalog digest must surface an error")
	}
	if errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("error code = %d, want ExitUsage (%d)", errs.CodeOf(err), errs.ExitUsage)
	}
	if !strings.Contains(err.Error(), "rejected the request") ||
		!strings.Contains(err.Error(), "does not match the server catalog") {
		t.Errorf("error must carry the guidance AND the server detail; err=%v", err)
	}
	if be.opPosts != 1 {
		t.Errorf("op POSTs = %d, want 1 — an unchanged digest must NOT be retried", be.opPosts)
	}
	if be.catalogGETs < 1 {
		t.Errorf("catalog GETs = %d, want ≥1 (the digest comparison needs the refetch)", be.catalogGETs)
	}
	if be.mutations != 0 || len(be.createdSlugs) != 0 {
		t.Errorf("NO client-side fallback may fire; mutations=%d created=%v", be.mutations, be.createdSlugs)
	}
}

// TestStepPages_OpServerErrorSurfaces: a 5xx from the op means the op EXISTS
// but the backend is unhealthy — surface it (ExitServer); NEVER fall back
// client-side (a client-side apply against an unhealthy backend just smears
// partial state).
func TestStepPages_OpServerErrorSurfaces(t *testing.T) {
	srv, be := newRecoveryBackend(t, nil, nil)
	be.opHandler = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"errors":[{"status":"502","detail":"upstream unavailable"}]}`))
	}

	_, err := driveStepPages(t, srv.URL)
	if err == nil {
		t.Fatal("a 5xx from the op must surface an error")
	}
	if errs.CodeOf(err) != errs.ExitServer {
		t.Errorf("error code = %d, want ExitServer (%d)", errs.CodeOf(err), errs.ExitServer)
	}
	if be.opPosts != 1 {
		t.Errorf("op POSTs = %d, want 1", be.opPosts)
	}
	if be.mutations != 0 || be.listCalls != 0 {
		t.Errorf("a 5xx must NOT fall back client-side; mutations=%d listCalls=%d", be.mutations, be.listCalls)
	}
}

// TestStepPages_CatalogOverrideSkipsOp: --catalog is inherently client-side —
// an override catalog can never match the backend's pin digest, so the op
// would always 409. Zero op POSTs; the client-side sequence fires.
func TestStepPages_CatalogOverrideSkipsOp(t *testing.T) {
	srv, be := newRecoveryBackend(t, nil, nil)

	_, err := driveStepPagesCfg(t, srv.URL, func(sc *scaffoldContext) {
		sc.catalogOverride = "testdata/some-catalog.json" // set → escape hatch active
	})
	if err != nil {
		t.Fatalf("stepPages with --catalog override: %v", err)
	}
	if be.opPosts != 0 {
		t.Errorf("op POSTs = %d, want 0 — a --catalog override must never probe the op", be.opPosts)
	}
	if want := []string{"homepage", "about", "faq"}; !slices.Equal(be.createdSlugs, want) {
		t.Errorf("created slugs = %v, want %v (client-side apply)", be.createdSlugs, want)
	}
}

// TestStepPages_OpEmptyPagesResponseTolerated: a 201 whose body violates the
// contract (pages: []) must NOT be read as "nothing created" — the hub IS
// scaffolded (Task-4 review note). The step succeeds, homePageID stays empty,
// and a warning note tells the operator the backend returned no page listing.
func TestStepPages_OpEmptyPagesResponseTolerated(t *testing.T) {
	srv, be := newRecoveryBackend(t, nil, nil)
	be.opHandler = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"ts_1","type":"template_scaffolds","attributes":{"hub_id":"hub_1","pages":[]}}}`))
	}

	sc, err := driveStepPages(t, srv.URL)
	if err != nil {
		t.Fatalf("a 201 with empty pages must still succeed (the hub IS created): %v", err)
	}
	if sc.homePageID != "" || sc.homeDraftVersion != 0 {
		t.Errorf("context homepage = {%q %d}, want empty/zero (no listing to read it from)",
			sc.homePageID, sc.homeDraftVersion)
	}
	if be.mutations != 0 {
		t.Errorf("mutations = %d, want 0 (no client-side fallback on op success)", be.mutations)
	}
	if notes := stepNotes(sc); !strings.Contains(notes, "no page listing") {
		t.Errorf("empty pages must emit a warning note about the missing listing; notes=%q", notes)
	}
}
