# `hubs scaffold` — catalog-driven full-experience hub seeder

**Status:** Design (approved in brainstorming, 2026-07-21)
**Epic:** MIO-2512 (CLI V1: gaps, bugs & docs surfaced by CLI-only hub build)
**Author:** Marius Nicula

## Problem

Asking an agent to build a hub with the `mio` CLI today yields an empty shell: the operator must hand-run ~a dozen commands across hubs / community / media / pages, and the failure mode is *silent* — the API accepts a malformed blob or value, returns 200, and the renderer quietly drops it (observed in the Chipmunk and Leo Messi CLI-only builds). Other teams want to hand their agent "a step-by-step process" (Jake, 2026-07-20); a documented prompt is fragile because it inherits every silent-drop trap.

Separately, Legacy auto-assigned the hub creator as hub admin on create; V3 does not, so a CLI/API-created hub has no admin.

## Goal

One idempotent command — `mio hubs scaffold --template community` — that creates a hub and applies a full-experience template (branding/logo/favicon, menu/navigation, registration, discussion spaces, playlists, homepage, policies, onboarding schema) by orchestrating the CLI verbs we already have, so it stays strictly CLI-only and an agent cannot fall into the silent-drop traps. Backend-gated pieces (welcome post, auto-admin) are skipped gracefully with a clear note and wired in when their endpoints land.

## Non-goals

- Creating contacts / community population (explicitly out — unlike the mio-backend seeder).
- New rendering primitives (tracked separately: MIO-2541).
- The backend endpoints themselves (tracked: MIO-2262 welcome post, MIO-2540 auto-admin).

## Approach

Mirror the proven `pages catalog scaffold` architecture (MIO-2340): a versioned, declarative template in a catalog (live-fetch + **vendored offline fallback**, byte-parity-tested), applied by an orchestrator that **delegates each step to existing command internals** — never raw REST — so the CLI-only guarantee holds and there is no logic duplication.

### Command surface

- `mio hubs catalog templates` — list available hub templates (v1: `community`).
- `mio hubs scaffold --template <id> [--name <n> --slug <s> | --hub <id>] [--dry-run] [--publish] [overrides]`
  - Default: **creates a new hub** (`--name`/`--slug`). `--hub <id>` targets an existing hub instead.
  - `--dry-run`: print the ordered plan (each resource + effective request) without applying.
  - `--publish`: publish the hub as the final step (default **off** — review before it's live). Output echoes the public-URL reference + private/published state.
  - Overrides: `--name`, `--slug`, `--favicon-url`, `--logo-url`, `--registration-enabled`, `--offline` (force vendored catalog), `--catalog <file>`.

### What a hub template is

A declarative recipe (schema in `internal/hubcatalog`, sibling to `internal/catalog`):

- `branding` — logo_url, favicon_url, colors (→ `branding` blob)
- `navigation` — header/footer menu items (→ `navigation` blob)
- `settings` — `registration.enabled`, `discussions.enabled`, etc. (→ `settings` blob)
- `spaces[]` — discussion spaces (name, slug, description)
- `playlists[]` — playlist stubs (title, optional item `file_id`s)
- `homepage` — references a `pages catalog` page-template id
- `policies` — optional ToS/Privacy set/require
- `onboarding[]` — contact-attribute hub-config rows with `is_in_onboarding` (the default onboarding schema)
- `placeholders` — welcome post (backend-gated), marked skipped

### Apply pipeline (ordered, idempotent — upsert by slug/id)

Each step is a thin adapter over an existing command's internals:

1. **Hub** — create (`hubs create`) or resolve `--hub`.
2. **Blobs** — branding+favicon, navigation (menu), settings/registration via the whole-blob read-modify-write + key-validation path (reuses MIO-2515/2516/2517/2522). Because scaffold routes through the validated blob path, malformed keys **error** rather than silently drop.
3. **Spaces** — `community spaces create` per template space (skip-if-exists by slug).
4. **Onboarding schema** — `contact-attributes` defs + `hub-config create --in-onboarding` (reuses MIO-2497/2502 fixes).
5. **Policies** — `policies update` (optional set/require).
6. **Playlists** — `media playlists create` + `items add` (MIO-2513) + `hub-playlists publish --visibility public --published-at now`. **Depends on MIO-2536** (published-at default bug) so seeded playlists render.
7. **Homepage** — `pages catalog scaffold` → `pages tree set` (MIO-2340/2518). Node settings pass through the pre-flight validation of MIO-2537 (no silent drops).
8. **Publish** *(only with `--publish`)* — `hubs update --published`; echo URL + state.
9. **Skipped-with-note** — welcome post (MIO-2262) + auto-admin (MIO-2540): print a clear "skipped, needs backend X" line; wire in when the endpoints land.

`--dry-run` runs steps 1–8 in plan mode (compute each request, print the ordered plan, no HTTP). A normal run is re-runnable: each step upserts, so re-running converges (idempotent).

## Components & isolation

- `internal/hubcatalog/` — template schema, loader (live-fetch + vendored `hubcatalog.json` fallback), digest/parity test. One purpose: load + validate a hub template. Mirrors `internal/catalog`.
- `cmd/hubs_scaffold.go` — the `scaffold` orchestrator + `hubs catalog templates`. Owns the ordered pipeline and `--dry-run`/`--publish`/override flag wiring.
- **Step adapters** — each step calls the *existing* command's internal helper (e.g. the blob RMW builder, `spacesPath`+Create, the playlist-items path, the pages-catalog applier). No new API paths; no raw REST. If a helper isn't cleanly callable, extract a small internal function from the existing command (targeted, not a refactor).

Each unit is independently testable: the loader (template in → validated struct or error), each step adapter (template slice → the exact request(s) it would emit), the orchestrator (template → ordered plan).

## Error handling

- Template load/validation failure → `ExitUsage` before any HTTP.
- A step's underlying command error surfaces with which step failed; because steps upsert, the operator fixes and re-runs (converges). Scaffold does **not** roll back prior steps (documented) — re-run is the recovery.
- Backend-gated steps never error; they print a skip note.
- Malformed template values are caught by the same validation the individual commands use (blob keys MIO-2515, node settings MIO-2537).

## Testing

- **Unit** — loader invariants; each step adapter's emitted request shape; orchestrator plan ordering.
- **Contract** — `--dry-run` plan output is stable + covers every step; idempotency (second apply emits upserts, not duplicates); backend-gated steps emit skip notes not requests.
- **Parity** — vendored `hubcatalog.json` digest pinned (canary like `internal/catalog/parity_test.go`).
- **E2E (`:8000`/dev)** — `hubs scaffold --template community --name … --publish`, then confirm the hub renders: menu present, spaces present, playlists **visible** (needs MIO-2536), branding/favicon applied, homepage tree set.

## Dependencies / sequencing

- **Blocks a complete seeded hub:** MIO-2536 (playlist published-at → visible playlists) — should land first.
- **Improves scaffold output but not blocking:** MIO-2537 (page-tree pre-flight validation).
- **Backend-gated, wired in later:** MIO-2262 (welcome post), MIO-2540 (auto-admin-on-create).
- Reuses already-shipped: MIO-2513, 2515, 2516, 2517, 2522, 2497, 2502, 2340, 2518, 2514.

## Rollout

Ship `hubs scaffold` + the `community` template with steps 1–8 working and 9 skipped-with-note. As MIO-2262/2540 land, flip those steps on. `community` is the only template at v1; the schema supports more (`starter`, etc.) without command changes.
