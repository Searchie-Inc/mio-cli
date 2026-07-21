# `hubs scaffold` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `mio hubs scaffold --template community` — one idempotent command that creates a hub and applies a full-experience template (branding/favicon, menu, registration, spaces, playlists, homepage, policies, onboarding) by orchestrating the CLI's own request-builders + `internal/client`, never re-invoking a cobra `RunE` and never raw REST.

**Architecture:** A declarative template (`internal/hubtemplate`, embedded JSON) is applied by an orchestrator (`cmd/hubs_scaffold.go`) that threads a `scaffoldContext` of server-assigned ids across ordered steps. Each step calls a **pure, data-driven builder** extracted from an existing command (both the original command and scaffold call the extracted builder — guarded by behavior-preserving regression tests). Backend-gated steps (welcome post, auto-admin) skip-with-note.

**Tech Stack:** Go 1.25, spf13/cobra, `internal/client` (JSON:API), `internal/catalog` (page-tree applier, reused), `//go:embed`.

**Spec:** `docs/superpowers/specs/2026-07-21-hubs-scaffold-design.md` · **Ticket:** MIO-2543 · **Epic:** MIO-2512

---

## Conventions for this plan

- **Go toolchain:** `export PATH=$HOME/go-sdk/go/bin:$HOME/go/bin:$PATH`. Gates after every task: `go build ./...`, `go vet ./...`, `gofmt -l cmd internal` (must print nothing), `golangci-lint run ./...` (0 issues), `go test ./cmd/... ./internal/...`. The suite has **2 known pre-existing failures** (`TestContract_ExitCodes_NoCredentials`, `TestWiring_SingleHubAutoDefault`) that reproduce on clean `main` — ignore them, do not chase.
- **Branch:** work on `MIO-2543-hubs-scaffold` (created off `main`).
- **TDD:** every task writes the failing test first, verifies RED, implements minimal, verifies GREEN, commits. Commit messages use conventional-commit + the repo footer (`Co-Authored-By:` + `Claude-Session:`).
- **Do NOT invoke any command's `RunE` from scaffold.** The orchestrator calls extracted builders + client methods directly.

## File structure

| File | Responsibility |
|---|---|
| `internal/hubtemplate/template.go` (new) | Template struct + `//go:embed hubtemplates/community.json` + `Load(id)` + schema validation |
| `internal/hubtemplate/hubtemplates/community.json` (new) | The `community` template content |
| `internal/hubtemplate/template_test.go` (new) | Loader/schema unit tests |
| `cmd/hubs_scaffold.go` (new) | `scaffoldContext`, orchestrator + ordered steps, `hubs scaffold` + `hubs templates` commands, `--dry-run` plan |
| `cmd/hubs_scaffold_test.go` (new) | Orchestrator/step/plan contract tests |
| `cmd/hubs.go` (modify) | Extract `applyHubBlobs`, `buildHubCreateAttrs`, `buildHubUpdateAttrs`, `applyHubPolicies` from RunE blocks; rewire the RunEs to call them |
| `cmd/community_spaces.go` (modify) | Extract `buildSpaceAttrs` from `setSpaceWriteAttrs` |
| `cmd/pages_write.go` (modify) | Extract `buildPageAttrs` from `setPageWriteAttrs` |
| `cmd/media.go` (modify) | Extract `buildHubMediaPublishAttrs` from `applyHubMediaOptions`; `buildPlaylistCreateAttrs` from `mediaPlaylistsCreateCmd.RunE` |
| `cmd/contactattributes.go` (modify) | Extract `buildHubConfigAttrs` from `setHubConfigBoolFlags`+def_id pattern; `buildAttrDefCreateAttrs` from `contactAttributesCreateCmd.RunE` |
| `docs/internal/api-surface.md`, `llms.txt`, `README.md`, `AGENTS.md` (modify) | Document `hubs scaffold` + `hubs templates` |

**Reused as-is (already pure, no `cmd` coupling — do NOT modify):** `deepMergeMap`/`attrMap`/`deleteAtPath` (`cmd/hubs_update_blobs.go:141,162,91`), `validateNavigationBlob`/`validateNavigationHrefs` (`cmd/hubs_navigation.go:24,67`), path builders, `catalog.InstantiateTemplate(t catalog.Template, variant string, gen catalog.IDGen)` (`internal/catalog/applier.go:32`), client `List`/`RetrieveWithQuery`/`Create`/`ActionWithHeaders` (`internal/client/client.go:400,424,436,588`).

---

## Phase 0 — Resolve the open questions (do first; they gate later tasks)

### Task 0a: Resolve O1 — playlist & item idempotency key

**Files:** none (investigation; record the decision in this plan file under "O1 decision").

- [ ] **Step 1: Determine whether `meta` (or description) is writable on playlist create.** Build the CLI and inspect against dev:
  Run: `export PATH=$HOME/go-sdk/go/bin:$PATH && go build -o /tmp/mio . && /tmp/mio media playlists create --help` and read `cmd/media.go:827` + the backend playlist create schema (`/home/ubuntu/src/mio-backend/app/media/schemas` — grep for the playlist create request model).
- [ ] **Step 2: Decide the key.** If a marker field (`meta`/description) is writable → **option (b)**: scaffold writes a stable marker (e.g. `meta.scaffold_key: "<template-id>:<playlist-slug>"`) and matches on it. Else → **option (c)**: playlists are create-only; resume skips the playlist step entirely if the hub already has ≥1 playlist.
- [ ] **Step 3: Decide item-level idempotency.** For the chosen key, confirm whether `media playlists items list` lets the step skip already-present `file_id`s on re-run (needed under (a)/(b) so resume doesn't duplicate items). Record: "items add pre-checks `items list` and skips existing file_ids" or "(c) skips the whole step".
- [ ] **Step 4: Record the O1 decision** as a short subsection appended to this plan (chosen option + the exact match field + item-level behavior). Commit the plan edit.

### O1 decision (resolved 2026-07-21)

Backend `PlaylistCreateAttributes` accepts only `{title, description, visibility}` (mio-backend `app/media/schemas/__init__.py:943`) — no `meta`, no `slug`, so there is no clean hidden marker field (option (b) would abuse the user-visible `description`). **Chosen: option (c).** The playlist step is create-only and **gated on `media hub-playlists list --hub <hub>` being empty**: on a fresh hub it creates the template playlists + items + publishes them; on resume, if the hub already has ≥1 published playlist, the **entire playlist step is skipped**. Item-level idempotency is therefore N/A (the step runs at most once per hub). Documented tradeoff: a prior run that created a team playlist but failed before publishing could leave an orphan team playlist on re-run (team playlists are team-scoped, not hub-gated); acceptable for the fallback — an operator can delete the orphan, and a future first-class idempotency key (if playlists gain a slug/meta) supersedes this. Task 17 implements this gate.

### Task 0b: O2 is handled inline

O2 (extraction blast radius) is not a separate task — **every extraction task in Phase 2 carries its own behavior-preserving regression test** (capture the original command's emitted attrs BEFORE extracting, assert identical AFTER). No code proceeds until each extraction's guard test is green on both the original command and the new builder.

---

## Phase 1 — The template package (independent; no extraction dependency)

### Task 1: `internal/hubtemplate` — struct, loader, schema validation

**Files:**
- Create: `internal/hubtemplate/template.go`
- Create: `internal/hubtemplate/hubtemplates/community.json` (minimal valid stub for now; real content in Task 12)
- Test: `internal/hubtemplate/template_test.go`

- [ ] **Step 1: Write the failing test** (`template_test.go`): `Load("community")` returns a `*Template` with a non-empty `ID`, at least one `Spaces` entry, and a `Homepage`; `Load("nope")` returns an error; a template JSON with an unknown top-level key or a space missing `slug` fails `Validate()`.

```go
func TestLoad_Community(t *testing.T) {
    tmpl, err := Load("community")
    if err != nil { t.Fatalf("Load: %v", err) }
    if tmpl.ID != "community" { t.Errorf("ID=%q", tmpl.ID) }
    if len(tmpl.Spaces) == 0 { t.Error("want >=1 space") }
}
func TestLoad_Unknown(t *testing.T) {
    if _, err := Load("nope"); err == nil { t.Fatal("want error for unknown template") }
}
func TestValidate_RejectsSpaceWithoutSlug(t *testing.T) {
    tm := &Template{ID: "x", Spaces: []Space{{Name: "General"}}}
    if err := tm.Validate(); err == nil { t.Fatal("want error: space missing slug") }
}
```

- [ ] **Step 2: Run → verify FAIL** (`go test ./internal/hubtemplate/ -run TestLoad -v` → build error / undefined).
- [ ] **Step 3: Implement `template.go`.** Define the struct mirroring the spec's template shape; embed the templates dir; `Load` reads+unmarshals+validates.

```go
package hubtemplate

import (
    "embed"
    "encoding/json"
    "fmt"
)

//go:embed hubtemplates/*.json
var templatesFS embed.FS

type Template struct {
    ID         string            `json:"id"`
    Branding   map[string]any    `json:"branding,omitempty"`   // logo_url, favicon_url, colors → branding blob
    Navigation map[string]any    `json:"navigation,omitempty"` // header/footer menu (typed items) → REPLACE
    Settings   map[string]any    `json:"settings,omitempty"`   // registration.enabled, discussions.enabled, …
    Spaces     []Space           `json:"spaces,omitempty"`
    Onboarding []AttrDef         `json:"onboarding,omitempty"` // contact-attribute defs w/ is_in_onboarding
    Policies   map[string]any    `json:"policies,omitempty"`
    Playlists  []Playlist        `json:"playlists,omitempty"`
    Homepage   *HomepageRef      `json:"homepage,omitempty"`
}
type Space struct { Name, Slug, Description string }
type AttrDef struct { Name, Slug, FieldType string; InOnboarding, Required bool `json:"-"` /* mapped in builder */ }
type Playlist struct { Title, Key string; FileIDs []string `json:"file_ids"` }
type HomepageRef struct { Template string `json:"template"`; Variant string `json:"variant"` } // an embedded catalog.Template id OR inline; see Task 11

func Load(id string) (*Template, error) {
    b, err := templatesFS.ReadFile("hubtemplates/" + id + ".json")
    if err != nil { return nil, fmt.Errorf("unknown hub template %q", id) }
    var t Template
    if err := json.Unmarshal(b, &t); err != nil { return nil, fmt.Errorf("template %q: %w", id, err) }
    if err := t.Validate(); err != nil { return nil, err }
    return &t, nil
}
func List() []string { /* read dir, strip .json */ }
func (t *Template) Validate() error {
    if t.ID == "" { return fmt.Errorf("template: missing id") }
    for i, s := range t.Spaces { if s.Slug == "" { return fmt.Errorf("template: spaces[%d] missing slug", i) } }
    // …defs need slug+field_type; playlists need title/key; homepage template non-empty…
    return nil
}
```

- [ ] **Step 4: Add a minimal-but-COMPLETE `hubtemplates/community.json`** — one instance of *every* section (id, branding w/ favicon, a typed navigation menu, `settings.registration.enabled`, one space, one onboarding def, one policy, one playlist, a homepage ref using **static cards**). This lets the Task-11 dry-run CLI test exercise all pipeline steps. Task 22 enriches it to real content; it must stay schema-valid throughout.
- [ ] **Step 5: Run → verify PASS**; gofmt/vet/lint clean.
- [ ] **Step 6: Commit** (`feat(hubtemplate): template struct + embedded loader + schema validation (MIO-2543)`).

---

## Phase 2 — Builder extractions (behavior-preserving; O2 guard on each)

**The canonical extraction pattern (Task 2 shows it in full; Tasks 3–10 follow it exactly — only the specifics in the table change).** Each extraction: (1) regression-test the ORIGINAL command's emitted attrs, (2) extract a pure `build*`/`apply*` function taking data (not `*cobra.Command`), (3) rewire the original command's flag-reading wrapper to populate a data struct and call the builder, (4) assert the original command still emits identical attrs (contract test unchanged + green), (5) commit.

### Task 2: Extract `applyHubBlobs` from `hubsUpdateCmd.RunE` (canonical example, full detail)

**Files:**
- Modify: `cmd/hubs.go` (the RMW block welded into `hubsUpdateCmd.RunE`, ~490-563)
- Test: `cmd/hubs_scaffold_builders_test.go` (new — houses builder unit tests)

- [ ] **Step 1: Write the regression + unit test.** First, confirm the existing `hubs update` contract tests (`cmd/hubs_update_blobs_test.go`, `cmd/hubs_authoring_test.go`) still pass unchanged after extraction (they are the behavior-preserving guard — do not edit them). Then add a direct unit test of the new pure function:

```go
// cmd/hubs_scaffold_builders_test.go
func TestApplyHubBlobs_MergesPreservingSiblings(t *testing.T) {
    // fake client returning a hub whose branding has {primary:"#111", logo_url:"old"}
    // applyHubBlobs(ctx, client, "hub_1", blobPatches{Branding: {"favicon_url":"f"}})
    // → PATCH body branding == {primary:"#111", logo_url:"old", favicon_url:"f"} (RMW, siblings kept)
}
```

- [ ] **Step 2: Run → verify FAIL** (`applyHubBlobs` undefined).
- [ ] **Step 3: Extract.** Lift the retrieve→deep-merge branding/settings/meta→PATCH block out of `hubsUpdateCmd.RunE` into:

```go
// blobPatches carries the three whole-blob merges + the nav REPLACE + unset paths.
type blobPatches struct {
    Branding, Settings, Meta map[string]any
    Navigation map[string]any // REPLACE (validated), nil = untouched
    Unset      []string
    Strict     bool
}
// applyHubBlobs retrieves the hub, applies patches via deepMergeMap (branding/settings/meta),
// REPLACEs navigation (validated w/ hub slug), applies unsets LAST, and PATCHes. No cobra.
func applyHubBlobs(ctx context.Context, cl *client.Client, teamID, hubID, hubSlug string, p blobPatches) (*client.Resource, error) { … }
```

  Then rewire `hubsUpdateCmd.RunE` to build a `blobPatches` from its `Changed()` flags and call `applyHubBlobs`. The nav validation (`validateNavigationBlob`/`validateNavigationHrefs`) moves inside `applyHubBlobs`. `validateBlobKeys` is called with `strict` from `p.Strict` (the update command passes `--strict-keys`; scaffold passes `true`). **Note (resolves review #3):** `validateBlobKeys(cmd, …)` takes a `*cobra.Command` only for the warn-to-stderr path; on the strict path (scaffold's) it errors before touching `cmd`, so passing `nil` is safe — but to avoid a surprising nil, give `applyHubBlobs` an `io.Writer warnW` (default `os.Stderr`) and pass a small shim, OR change `validateBlobKeys` to take an `io.Writer` instead of `cmd`. Pick one during extraction; the `io.Writer` refactor is cleaner and touches only the warn call.
- [ ] **Step 4: Run → verify GREEN** — the new unit test passes AND every existing `hubs update`/blob/authoring contract test still passes (identical emitted attrs = behavior preserved). gofmt/vet/lint clean.
- [ ] **Step 5: Commit** (`refactor(hubs): extract applyHubBlobs from hubsUpdateCmd for reuse (MIO-2543)`).

### Tasks 3–10: remaining extractions (same pattern as Task 2)

For each row: regression-guard via the original command's existing contract tests + a new direct unit test of the builder, extract the pure function, rewire the RunE/wrapper, verify identical, commit.

| Task | New builder | Extract from | File:line | Notes |
|---|---|---|---|---|
| 3 | `buildHubCreateAttrs(t *Template, ov overrides) (map[string]any, error)` | `hubsCreateCmd.RunE` | `cmd/hubs.go:184` | includes `--published`→`is_private` via `setMappedBoolInverted` logic (data-driven) |
| 4 | `buildHubUpdateAttrs(...) (map[string]any, error)` incl. published→is_private | `hubsUpdateCmd.RunE` | `cmd/hubs.go:370,384` | used by step 8 publish (`is_private:false`); `published` is NOT a writable attr |
| 5 | `applyHubPolicies(ctx, cl, teamID, hubID, pol map[string]any) error` | `hubsPoliciesUpdateCmd.RunE` | `cmd/hubs.go:680` | RunE-welded (no helper today) |
| 6 | `buildSpaceAttrs(s Space) (map[string]any, error)` | `setSpaceWriteAttrs` | `cmd/community_spaces.go:22` | **keep the enum validation** (`access_level`/`posting_permission`) — return error (resolves review #2) |
| 7 | `buildPageAttrs(...) (map[string]any, error)` | `setPageWriteAttrs` | `cmd/pages_write.go:26` | `--is-home`→`is_homepage`; **keep `privacy` enum validation** → return error |
| 8 | `buildHubMediaPublishAttrs(visibility string, publishedAt time.Time, position *int) (map[string]any, error)` | `applyHubMediaOptions` | `cmd/media.go:213` | returns `{visibility,published_at,position}`; **the caller (step 6/17) adds `playlist_id` to the BODY and POSTs to the COLLECTION path** `Create(hubPlaylistsPath(team,hub,""))` (media.go:272,281) — the id is NOT in the path (only `unpublish` DELETE is id-in-path). NO fileID arg (resolves review #4); **keep `visibility` enum validation**; scaffold sets `published_at` UNCONDITIONALLY (sidesteps MIO-2536) |
| 9 | `buildHubConfigAttrs(defID string, d AttrDef) map[string]any` + `buildAttrDefCreateAttrs(d AttrDef) (map[string]any, error)` | `setHubConfigBoolFlags` + `contactAttributesCreateCmd.RunE` | `cmd/contactattributes.go:575,156` | hub-config puts `definition_id` in the body (MIO-2502); onboarding sets `is_in_onboarding`. **NOTE (review #3-followup):** `contact-attributes create` does NOT validate `field_type` today (media→`setMappedString`, no enum check, `:190`) — adding it in `buildAttrDefCreateAttrs` is *new* validation, so a bad `--field-type` now fails client-side (`ExitUsage`) instead of a backend 422. That's an intentional behavior change; the "identical attrs" guard only covers valid input, so update the `contact-attributes create` test to expect the new error path for a bad field-type. |
| 10 | `buildPlaylistCreateAttrs(p Playlist) map[string]any` | `mediaPlaylistsCreateCmd.RunE` | `cmd/media.go:827` | include the O1 marker field per Task 0a (playlist create currently writes only `title/description/visibility/hub_id` — no `meta` — so option (b) uses `description` as the marker) |

**Enum validation (resolves review #2):** builders that validate enums today keep an `error` return and own that validation, so scaffold — calling the builder directly — gets the same checks the command does (honoring spec §Error-handling "malformed template values are caught by the same validators"). `Template.Validate()` (Task 1) additionally rejects an out-of-range enum in the template JSON as a fast fail.

Each is its own commit. After Task 10: full suite green (minus the 2 known failures), lint 0.

---

## Phase 3 — Orchestrator skeleton

### Task 11: command skeleton + `scaffoldContext` + pipeline runner + dry-run plan

**Files:**
- Create: `cmd/hubs_scaffold.go` (the `hubs scaffold` cobra command + core flags + registration on `hubsCmd` + context + runner; steps added in Phase 4)
- Test: `cmd/hubs_scaffold_test.go`

> **Wiring boundary (resolves plan-review issue #1):** this task creates the `scaffold` cobra command with its **core flags** (`--template`, `--name`, `--slug`, `--hub`, `--dry-run`) and registers it on `hubsCmd`, so the dry-run CLI test below runs here. Task 21 adds only the extras (`hubs templates`, `--publish`, `--favicon-url`/`--logo-url`/`--registration-enabled` overrides). **Phase-4 step tests call the step functions directly with in-memory `*hubtemplate.Template` values** (unit-style) — they do NOT depend on Task-22 content or full CLI wiring; only the one end-to-end dry-run test drives `--template community`.
>
> **Step registration ordering:** Task 11 registers the full **ordered step list — names + no-op/placeholder `run` funcs** (so the dry-run test can assert every step name is present and ordered). Phase 4 fills in each step's real body + its own contract test, replacing the no-op one at a time. This is why Task 11's dry-run test can name every step before Phase 4 exists.

- [ ] **Step 1: Write the failing test.** A `scaffoldContext` threads ids; the runner executes an ordered `[]scaffoldStep`; `--dry-run` collects a plan of `{step, path, attrs-with-placeholders}` and fires NO HTTP; resume mode GETs the hub to populate `hubSlug`.

```go
func TestScaffold_DryRunEmitsPlanNoHTTP(t *testing.T) {
    // firedGuardServer; run scaffold --template community --name X --slug x --dry-run
    // → exit 0, plan output names every step, *fired == false
}
func TestScaffold_ResumeGetsHubForSlug(t *testing.T) {
    // server returns hub {slug:"acme"} on GET; scaffold --hub hub_1 (resume)
    // → context.hubSlug == "acme" (assert via a step that would fail nav validation without it)
}
```

- [ ] **Step 2: Run → verify FAIL.**
- [ ] **Step 3: Implement** the context + runner + dry-run.

```go
type scaffoldContext struct {
    ctx    context.Context
    cl     *client.Client
    teamID string
    hubID, hubSlug string
    isPrivate bool
    spaceIDsBySlug, defIDsBySlug, playlistIDsByKey map[string]string
    homePageID string
    homeDraftVersion int
    dryRun bool
    plan   *[]planEntry
}
type scaffoldStep struct {
    name string
    run  func(sc *scaffoldContext, t *hubtemplate.Template) error
}
// runner: for each step, if dryRun record intent else run; on error, print hub id + resume cmd.
```

  Resume mode: after `requireHub` returns the id, **GET the hub** (`cl.Retrieve(hubsPath(teamID, hubID))`) and set `hubSlug`/`isPrivate` — because `ResolveHub` short-circuits id-shaped values (no lookup).
- [ ] **Step 4: Run → verify GREEN.** **Step 5: Commit.**

---

## Phase 4 — The steps (one task each; each: failing contract test → wire builder+client+lookup → green → commit)

Each step task follows: write a contract test asserting the step's request(s) (method/path/body) and its idempotency skip; implement the step using the Phase-2 builder + client + an **exhaustive** lookup (server-filter where available, else follow cursors — a first-page-only match is a bug); verify; commit.

- [ ] **Task 12 — Step 1 Hub:** create mode (`buildHubCreateAttrs`→`Create(hubsPath)`, capture id+slug) / resume mode (already resolved in Task 11). Test: create emits the right body; resume skips create.
- [ ] **Task 13 — Step 2 Blobs:** branding+favicon+settings+registration via `applyHubBlobs` with `Strict:true`; navigation REPLACE (validated with `hubSlug`). Test: one GET + one PATCH, siblings preserved, unknown template key → error (strict).
- [ ] **Task 14 — Step 3 Spaces:** exhaustive list spaces → skip-if-slug-exists → `buildSpaceAttrs`→`Create(spacesPath)`; record ids. Test: existing slug skipped (no create), new slug created.
- [ ] **Task 15 — Step 4 Onboarding:** per def: skip-if-slug-exists → create def (`buildAttrDefCreateAttrs`, capture def id) → `buildHubConfigAttrs(defID, …is_in_onboarding)`→`Create(hub-config collection path)`. Test: def id lands in the hub-config body (MIO-2502).
- [ ] **Task 16 — Step 5 Policies:** `applyHubPolicies` (optional). Test: policies PATCH shape; skipped when template has none.
- [ ] **Task 17 — Step 6 Playlists+items:** per O1 decision (Task 0a): match-or-create playlist (`buildPlaylistCreateAttrs`→`Create(playlistsPath)`); `items add` per file_id with item-level skip (option a/b) or skip-whole-step (option c). **Publish** the playlist to the hub: put `playlist_id` in the body + the `buildHubMediaPublishAttrs` fields (`visibility:public`, `published_at` set unconditionally), POST to the **collection** path `hubPlaylistsPath(team,hub,"")` — NOT an id-in-path route. Test: reflects the O1 decision; publish body carries `playlist_id`+`published_at`+`visibility:public` to the collection path.
- [ ] **Task 18 — Step 7 Homepage:** `buildPageAttrs`→`Create(pagesPath)` capture page_id → `catalog.InstantiateTemplate(embeddedTemplate, variant, idGen)` → `cl.ActionWithHeaders(StyleEnvelope,"PUT",pagesTreePath(pageID),{"tree":node},{"If-Match":strconv(n)})` with n=0 first-set / captured `draft_version` on resume (read via `RetrieveWithQuery`; `tree get` 404s until a draft exists → fall back to 0). Test: create→PUT chain, If-Match header present.
- [ ] **Task 19 — Step 8 Publish (`--publish` only):** `buildHubUpdateAttrs({published:true})` → emits `is_private:false` → PATCH; echo URL + state. Test: body is `{is_private:false}` (NOT `published`).
- [ ] **Task 20 — Step 9 Skip-with-note:** welcome post (MIO-2262) + auto-admin (MIO-2540) print a clear skip line, fire no request. Test: skip notes present, `*fired == false`.

---

## Phase 5 — `hubs templates` + override flags

### Task 21: add `hubs templates` + the `--publish`/override flags

**Files:** modify `cmd/hubs_scaffold.go` (the scaffold command + core flags + `hubsCmd` registration already exist from Task 11).

- [ ] Contract test: `hubs templates` lists `community`; `hubs scaffold … --publish` reaches step 8; `--favicon-url`/`--logo-url`/`--registration-enabled` overrides flow into the template. Add the `hubsTemplatesCmd` (calls `hubtemplate.List()`) and register it; add the `--publish`, `--favicon-url`, `--logo-url`, `--registration-enabled` flags to the scaffold command. Green. Commit.

---

## Phase 6 — Template content, e2e, docs

### Task 22: author `community.json`

- [ ] Fill `internal/hubtemplate/hubtemplates/community.json` with a real full-experience template (branding+favicon, typed navigation menu, `settings.registration.enabled`, 2–3 spaces, an onboarding schema, 1–2 playlists, a homepage catalog template ref, policies). Validate via `hubs scaffold --template community --dry-run`. Commit.
- **CRITICAL (resolves review #5):** the homepage template MUST use **static cards**, not a content-grid bound to `dataSource:{type:"hub_playlists"}`. Per `cmd/pages_tree.go:85–89`, the homepage route renders such a data-bound grid **empty** — exactly the silent-drop failure this whole feature exists to prevent. Verify the scaffolded homepage renders visible content in Task 23.

### Task 23: e2e on dev / :8000

- [ ] **Step 1:** build; mint a dev key (see `internal/…` / the local-test flow — dev `api.member.dev` has the backend). Run `hubs scaffold --template community --name "Scaffold E2E" --slug scaffold-e2e --publish`.
- [ ] **Step 2:** verify the hub renders: menu present, spaces present, playlists **visible** (scaffold set `published_at`), branding/favicon applied, homepage tree set, `registration_enabled` true.
- [ ] **Step 3:** re-run in `--hub <id>` mode → confirm convergence (no duplicate spaces/defs/playlists). Record the e2e transcript in the PR body. (No commit — verification only.)

### Task 24: docs parity

- [ ] Document `hubs scaffold` + `hubs templates` in `docs/internal/api-surface.md`, `llms.txt`, `README.md`, `AGENTS.md` (following the existing per-command format). Note the skip-with-note steps + the `--publish` default-off. Commit.

---

## Definition of done

- All tasks committed; `go build`/`vet`/`gofmt`/`golangci-lint` clean; full suite green (minus the 2 known pre-existing failures).
- E2E on dev produces a rendering hub; re-run converges.
- Backend-gated steps skip-with-note (wired on when MIO-2262 / MIO-2540 land).
- Codex review (via the codex-review skill) APPROVE before merge; PR opened, CI green.
