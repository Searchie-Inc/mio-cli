# `hubs scaffold` — template-driven full-experience hub seeder

**Status:** Design (approved in brainstorming 2026-07-21; revised after spec review)
**Epic:** MIO-2512 (CLI V1) · **Story:** MIO-2543
**Author:** Marius Nicula

## Problem

Building a hub via the `mio` CLI today means hand-running ~a dozen commands across hubs / community / media / pages, and the failure mode is *silent* — the API accepts a malformed blob or value, returns 200, and the renderer quietly drops it (observed in the Chipmunk and Leo Messi CLI-only builds). Other teams want to hand their agent a repeatable process (Jake, 2026-07-20); a documented prompt is fragile because it inherits every silent-drop trap. Separately, Legacy auto-assigned the hub creator as hub admin on create; V3 does not.

## Goal

One command — `mio hubs scaffold --template community` — that creates a hub and applies a full-experience template (branding/logo/favicon, menu/navigation, registration, discussion spaces, playlists, homepage, policies, onboarding schema) by orchestrating the CLI's own request-building + client layer, so it stays strictly CLI-only. Re-runnable to resume a partially-built hub. Backend-gated pieces (welcome post, auto-admin) are skipped gracefully and wired in when their endpoints land.

## Non-goals

- Creating contacts / community population (out — unlike the mio-backend seeder).
- New rendering primitives (MIO-2541) or backend endpoints (MIO-2262 welcome post, MIO-2540 auto-admin).
- A fetchable cross-repo template catalog (see "Template source" — the template is authored in-repo; there is no upstream to mirror).

## Approach

A declarative hub template applied by an orchestrator that **builds each request from the same attribute-builders + validators + path-builders + `internal/client` calls the individual commands use** — never re-invoking a command's cobra `RunE`, and never raw REST. This keeps the CLI-only guarantee and avoids logic duplication, but (per the spec review) it requires a real extraction step, not just "call a helper" (see Components).

### Template source

The `community` template ships **embedded in the binary** (`//go:embed hubtemplates/community.json`) with a Go struct + schema validation. It is authored in this repo — there is **no backend endpoint to fetch it from and no independent source it can drift from**, so it deliberately does **not** copy `internal/catalog`'s live-fetch / ETag cache / digest-verification / byte-parity apparatus (that exists only because the *page-builder* catalog vendors a cross-repo artifact served by a real endpoint and parity-tested against a TS reference applier — none of which applies here). What transfers from that pattern: a versioned declarative struct, `//go:embed`, and schema-validation unit tests. No `--offline`/`--catalog` flags.

### Command surface

- `mio hubs templates` — list available templates (v1: `community`). (Not `hubs catalog templates` — there is no fetchable catalog.)
- `mio hubs scaffold --template <id> [--name <n> --slug <s> | --hub <id>] [--dry-run] [--publish] [overrides]`
  - **Create mode** (default): `--name`/`--slug` create a new hub.
  - **Resume/target mode**: `--hub <id>` applies onto an existing hub (also how you resume after a mid-pipeline failure — see Recovery).
  - `--dry-run`: print the ordered plan (intent + static attrs; **placeholder ids** for resources not yet created — see Dry-run fidelity). No HTTP.
  - `--publish`: publish the hub as the final step (default **off**). Output echoes the URL reference + private/published state.
  - Overrides: `--name`, `--slug`, `--favicon-url`, `--logo-url`, `--registration-enabled`.

### Resolved-id context (the data-flow spine)

Almost every step consumes a server-assigned id minted by an earlier step (hub id → all; contact-attr definition id → `hub-config create` body; playlist id → `items add` + `hub-playlists publish`; page id → `tree set`). The orchestrator threads a mutable **`scaffoldContext`**: `{client, teamID, hubID, hubSlug, spaceIDsBySlug, defIDsBySlug, playlistIDsByKey, homePageID, homeDraftVersion}`. It resolves auth/team **once** (not per step) and fills the hub id at step 1; each later step reads what it needs and writes back the ids it mints. This context, not "each step in isolation," is what makes the pipeline runnable — steps 2-8 cannot form their request without it.

### Idempotency & recovery (revised — no blanket "upsert" claim)

Each step declares a **lookup key** and does *pre-check → skip-or-create*, but only where a real key exists:

| Step | Idempotency key | Re-run behavior |
|---|---|---|
| Hub (1) | none on create; `--hub` in resume mode | Create mode re-run **would 422 on the duplicate slug** → so on any failure we print the created hub id + the exact `--hub <id>` resume command, and resume mode skips creation. |
| Blobs (2) | whole-hub PATCH (naturally idempotent) | Safe to re-apply. |
| Spaces (3) | `slug` (list spaces, skip if slug exists) | Skip-if-exists. |
| Onboarding defs (4) | attribute `slug` (list defs, skip if exists); hub-config by def id | Skip-if-exists. |
| Policies (5) | whole-hub policies (idempotent) | Safe to re-apply. |
| Playlists (6) | **template playlist key stored in the playlist's `meta`/title convention** — pre-list hub playlists and match; if unmatched, create | Documented: playlists have no server slug, so we match by a scaffold-written marker (see Open question O1). |
| Homepage (7) | page `slug`; tree via `draft_version` | If the page exists, reuse its id + current `draft_version` for `tree set` (not a blind `--if-match 0`). |

**Recovery contract:** scaffold does **not** roll back. On any step failure it prints (a) the hub id, (b) `mio hubs scaffold --hub <id> --template <t>` to resume. Because every step pre-checks its key, resume converges. This replaces the earlier (incorrect) "re-run just converges" claim, which failed at step 1's non-idempotent create.

## Apply pipeline (ordered)

Each step calls **extracted data-driven builders** (see Components), threading `scaffoldContext`:

1. **Hub** — create mode: `buildHubCreateAttrs(tmpl, overrides)` → `client.Create(hubsPath)`; capture `hub.id` + `hub.slug` into context. Resume mode: resolve `--hub`.
2. **Blobs** — branding+favicon, settings/registration via the extracted **RMW helper** `applyHubBlobs(ctx, hubID, patches)` (retrieve → deep-merge branding/settings/meta → PATCH); **navigation is a whole-blob REPLACE** (not RMW), validated by `validateNavigationBlob` + `validateNavigationHrefs` (which needs the hub slug — in context). Blob-key validation runs in **strict mode** here (scaffold forces the equivalent of `--strict-keys`) so a bad template key errors rather than warns.
3. **Spaces** — `buildSpaceAttrs(templateSpace)` → skip-if-slug-exists → `client.Create(spacesPath)`; record space ids.
4. **Onboarding schema** — per attribute: skip-if-slug-exists → `client.Create` the def (capture def id) → `buildHubConfigAttrs(def_id, {is_in_onboarding:true,…})` → `client.Create(hub-config collection path)` (the MIO-2502 fix: POST to the collection with def id in the body).
5. **Policies** — `applyHubPolicies(ctx, tmpl.policies)` (optional set/require via the policies path).
6. **Playlists** — per playlist: pre-list + match (O1) or `client.Create(playlistsPath)`; `items add` per `file_id` (MIO-2513 path); then `buildHubMediaPublishAttrs(...)` with **`published_at` set to now unconditionally** (scaffold sets it directly — this sidesteps MIO-2536 rather than depending on the flag-coupled `applyHubMediaOptions`), `visibility: public`.
7. **Homepage** — `pages create` (`buildPageAttrs`, capture `page_id`) → `catalog.InstantiateTemplate(tmpl.homepage, variant, idGen)` where `tmpl.homepage` is an **embedded `catalog.Template`** (not a catalog id — so no page-builder-catalog resolution / no fetch machinery is pulled in) → tree set via `client.ActionWithHeaders(StyleEnvelope, "PUT", pagesTreePath(page_id), {"tree":…}, {"If-Match": n})`. `n` is `0` on the first set; on resume, `tree get` **404s until a draft exists**, so resume reads `draft_version` (via `RetrieveWithQuery`) when a draft is present and otherwise falls back to `0` (the `homeDraftVersion` context default). Node settings run through the MIO-2537 pre-flight validation once it lands.
8. **Publish** *(only with `--publish`)* — `applyHubBlobs`/update `published:true`; echo URL + state.
9. **Skipped-with-note** — welcome post (MIO-2262) + auto-admin (MIO-2540): print a clear "skipped — needs backend X" line; never error. Wired in when the endpoints land.

### Dry-run fidelity (scoped honestly)

`--dry-run` prints the **ordered intent**: each step, its target path template, and its static attributes. For steps whose request depends on a not-yet-created id (hub id, def id, playlist id, page id) it prints a **placeholder** (`<hub_id>`, `<def_id:onboarding.company>`) — it does **not** claim byte-exact requests, because those ids only exist after HTTP. This is the honest limit of a no-HTTP plan; the plan is a review aid, not a replayable script.

## Components & isolation

- `internal/hubtemplate/` — template struct, `//go:embed` loader, schema validation. One purpose: load+validate a template. (No fetch/cache/digest/parity — see Template source.)
- `cmd/hubs_scaffold.go` — the orchestrator (`scaffoldContext`, ordered pipeline, `--dry-run`/`--publish`/overrides) + `hubs templates`.
- **Extracted builders** — the real work of I1. Today the write logic is welded into cobra `RunE`s and shaped `(cmd, attrs)` reading flags; it is **not** callable from scaffold. The plan extracts pure, data-driven functions and has both the original command and scaffold call them:
  - `applyHubBlobs(ctx, client, hubID, patches) error` — extract the RMW block welded into `hubsUpdateCmd.RunE` (`cmd/hubs.go`, ~60 lines coupled to flag-derived locals).
  - `buildSpaceAttrs`←`setSpaceWriteAttrs`, `buildHubConfigAttrs`←`setHubConfigBoolFlags`+the MIO-2502 `definition_id`-in-body pattern, `buildHubMediaPublishAttrs`←`applyHubMediaOptions`, `buildPageAttrs`←`setPageWriteAttrs` — data-driven functions taking template data instead of a `*cobra.Command`.
  - **RunE-welded extractions** (no existing helper — logic is inline in a `RunE`, so these are true extractions, not renames; both feed O2's blast-radius count): `buildHubCreateAttrs` from `hubsCreateCmd.RunE`, and `applyHubPolicies` from `hubsPoliciesUpdateCmd.RunE`.
  - Reuse as-is (already pure/callable): path builders (`hubsPath`, `spacesPath`, `hubPlaylistsPath`, `playlistItemsPath`, `pagesPath`, `pagesTreePath`), `deepMergeMap`/`attrMap`/`deleteAtPath`, `validateNavigationBlob`/`validateNavigationHrefs`, `validateBlobKeys` (strict mode), `catalog.InstantiateTemplate`, and the client methods `Create/Update/Retrieve/List/RetrieveWithQuery/ActionWithHeaders` (the last two are needed for the homepage tree read/PUT).
  - **Hard rule:** the orchestrator MUST NOT invoke any command's `RunE` (they build their own context, re-auth, and render to stdout per call). It calls the extracted builders + client directly, threading one context.

Each unit stays independently testable: the loader (template → struct/err), each builder (template slice → attrs), the orchestrator (template + a fake id-resolver → ordered plan).

## Error handling

- Template load/validation → `ExitUsage` before any HTTP.
- A step's client error surfaces **which step** failed + the Recovery contract (hub id + `--hub` resume command). No rollback.
- Backend-gated steps never error (skip note).
- Malformed template values are caught by the same validators the individual commands use (blob keys in strict mode; nav validators; MIO-2537 node validation once landed).

## Testing

- **Unit** — loader/schema; each extracted builder's attrs (and that the *original* command still produces identical attrs after extraction — a regression guard); orchestrator plan ordering with a fake id-resolver.
- **Contract** — `--dry-run` plan is stable + covers every step with placeholders for unresolved ids; resume mode with an existing hub skips create; skipped steps emit notes not requests; the playlist/spaces/def pre-check skips on an existing key.
- **E2E (dev / :8000)** — `hubs scaffold --template community --name … --slug … --publish`, then confirm the hub renders: menu, spaces, **visible** playlists (scaffold sets `published_at` itself), branding/favicon, homepage tree. Then re-run in `--hub` mode and confirm convergence (no duplicate spaces/playlists/defs).

## Dependencies / sequencing

- **Not a hard blocker (revised):** MIO-2536 — scaffold sets `published_at` directly, so seeded playlists are visible regardless. 2536 remains the right fix for the *standalone* `hub-playlists publish` command.
- **Improves output, not blocking:** MIO-2537 (page-tree node validation) — scaffold's homepage step benefits once it lands.
- **Backend-gated, wired in later:** MIO-2262 (welcome post), MIO-2540 (auto-admin-on-create).
- **Reuses shipped:** MIO-2513/2515/2516/2517/2522/2497/2502/2340/2518/2514.

## Open questions

- **O1 — playlist idempotency key (+ item-level).** Playlists have no server slug. Options: (a) match by exact title within the hub (title collisions possible), (b) write a scaffold marker into the playlist `meta`/description and match on it, (c) playlists are create-only and resume skips the playlist step if any playlist already exists on the hub. Leaning (b) if `meta` is writable on playlist create, else (c). **Item-level:** under (a)/(b) a *matched* playlist would still hit the `items add` sub-step — so item add must itself pre-check (list items, skip existing `file_id`s) or the resume duplicates items. Resolve both during planning.
- **O2 — extraction blast radius.** Extracting `applyHubBlobs` + five attr-builders touches ~6 existing commands. Confirm each extraction is behavior-preserving via the "original command still emits identical attrs" regression tests before scaffold consumes them.

## Rollout

Ship `hubs scaffold` + the `community` template with steps 1-8 and 9 skipped-with-note. Flip 2262/2540 steps on as they land. `community` is the only v1 template; the struct supports more without command changes.
