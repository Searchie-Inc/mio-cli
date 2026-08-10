# mio CLI — Agent Guide

This document is written for AI coding agents (Claude Code, Codex, etc.) that need to call `mio` programmatically. It covers auth, output format, the exit-code contract, destructive-op handling, and a compact command table.

> **This file tracks `main`, not the released binary.** You are probably reading it on GitHub while running a separately installed `mio`, so a behaviour described here may not have shipped yet. Run `mio version` and, when a distinction matters, prefer `mio <cmd> --help` — that is generated from the binary you actually have. Behaviours known to be newer than the latest release carry an inline version gate. The bundled agent skill (`mio skills print`) has no such skew: it is embedded in the binary, so it always describes the binary it came from.

---

## Authentication

Never run `mio login` in an agent (it prompts interactively). Use an API key instead:

```sh
export MIO_API_KEY=mio_sk_live_xxxxxxxxxxxxx
```

API keys are `mio_sk_live_…`. Resolution order (first wins):

1. `--api-key <key>` flag on the command line
2. `MIO_API_KEY` environment variable
3. Key stored in the OS keychain by `mio login`

If no key is found the command exits with code **3** (`ExitAuth`). Always set `MIO_API_KEY` before running any resource command. Pass `--anonymous` to deliberately run unauthenticated — it skips both `MIO_API_KEY` and the keychain (an explicit `--api-key` still takes effect). `--anonymous` **sends the request** with no `Authorization` header and lets the API answer, so a 401 you see under it is the server's verdict, not a local precondition (MIO-2694); `whoami` reports `key_source: "none (--anonymous)"`.

Key auth works against `https://api.member.dev` by default. Point elsewhere with `--api-base <url>` or `MIO_API_BASE_URL`.

---

## Output Format

`mio` auto-detects whether stdout is a TTY:

- **Piped / non-interactive** (agent default): JSON output, newline-delimited flattened resource objects.
- **Interactive terminal**: human-friendly table.

Force JSON explicitly to be safe:

```sh
mio contacts list --output json
```

Or rely on the implicit default when piped — both are equivalent when not on a TTY.

Use `--jq` to filter inline without shelling out to `jq`:

```sh
# Extract one field (add -o plain when CAPTURING a string — --jq alone JSON-quotes it)
mio contacts retrieve <id> --jq '.email'
EMAIL=$(mio contacts retrieve <id> -o plain --jq '.email')

# Pluck IDs from a list
mio products list --jq '.[].id'

# Capture into a variable
HUB_ID=$(mio hubs list -o plain --jq '.[0].id')   # -o plain: --jq alone JSON-quotes a string (MIO-2792)
```

Use `--raw` to get the unflattened JSON:API envelope if you need `meta`, `links`, or `included` fields.

When a resource has a business-level `type` attribute (products: `course`/`membership`/`booking`; contact-attributes: `text`/`number`/…), the flattened `.type` is THAT value, not the JSON:API document type. The document type (e.g. `"products"`) is available under `--raw` as `.data.type`.

---

## Exit-Code Contract

Branch on these stable codes. Do not parse stderr for error detection.

| Code | Meaning | When to retry |
|------|---------|---------------|
| `0` | Success | — |
| `1` | Generic / unexpected error | Investigate before retry |
| `2` | Bad flags, missing argument, or rejected input (400/409/422) | Fix the command, do not retry |
| `3` | Missing or invalid credentials (401/403) | Set `MIO_API_KEY`, then retry |
| `4` | Resource not found (404) | Do not retry; resource does not exist |
| `5` | Destructive op blocked in non-interactive shell | Re-run with `--yes` |
| `6` | Rate limited (429) | Back off, then retry |
| `7` | Upstream server error (5xx) | Transient — retry with backoff |

These codes are intentionally coarse. When you need the exact HTTP status the API returned — 403 vs 401 (re-authenticating cannot help with a 403), or 409 vs 422 (a conflict may clear, a validation rejection will not) — read `errors[0].status` from the JSON:API envelope on stderr. `errors[0].meta.exit_code` echoes the coarse code. Errors that never reached the network (bad flag, missing file, no API key) have no HTTP status, so their `status` is derived from the exit code instead.

> **Version gate (MIO-2656).** `errors[0].status` carries the API's **real** status only from the release AFTER `v0.12.1`. On `v0.12.1` and earlier it is reconstructed from the exit code, so precisely the two discriminations above are impossible there: a 403 reports `"401"`, and a 409 or 422 both report `"400"` (also 503→`"500"`, and 405/415→`"500"`). Check with `mio version` before branching on it; exit codes are unchanged either way, so `meta.exit_code` is safe on every version.

```sh
# Substitute a real id. For a 422-class rejection a post-v0.12.1 binary prints
# "422" here, while v0.12.1 and earlier print "400"; a bad id prints "404" on both.
mio contacts retrieve <id> 2>err.json || jq -r '.errors[0].status' err.json
```

---

## Destructive Operations

Any `delete`, `cancel`, `refund`, or member-moderation command (`ban` / `unban` / `warn` / `soft-ban`, and `community moderation reports resolve`) in a non-TTY shell will exit **5** unless `--yes` (or `-y`) is passed. (`restore` is the undo of a soft-delete, not destructive, so it does NOT require `--yes`.)

```sh
mio contacts delete <id> --yes
mio checkout subscriptions cancel <id> --yes
mio checkout payments refund <id> --yes
mio community members ban <contact_id> --hub <hub> --yes
```

Always include `--yes` in agent scripts for these commands.

---

## Scope Flags

Most resources are team-scoped. Hub-scoped resources additionally need `--hub`.

Set defaults once via config so individual commands are shorter. Config is written as TOML to `~/.config/mio/config.toml` (or `$XDG_CONFIG_HOME/mio/config.toml`); only `current_team`, `current_hub`, and `api_base` are writable, and the API key is never stored there. Values are validated (UUID / `http(s)` URL) at the setter.

```sh
mio config set current_team <team-id>
mio config set current_hub  <hub-id>
```

Or pass per-command:

```sh
mio contacts list --team <team-id>
mio content list --team <team-id> --hub <hub-id>
```

Missing scope exits with code **2** (`ExitUsage`).

---

## Complete Command Table

Every implemented resource and its verbs.

| Resource | Verbs |
|----------|-------|
| `login` | _(interactive only — use `MIO_API_KEY` instead)_ |
| `register` | Create an account + auto-login: `--email`/`--password` (or `MIO_EMAIL`/`MIO_PASSWORD`), optional `--first-name`/`--last-name`. Unauthenticated; stores a freshly minted key, REPLACING any current one. Agents that already have a `MIO_API_KEY` do not need this. |
| `logout` | _(interactive only)_ |
| `whoami` | _(no subcommand — prints resolved user, team, hub, api-base, profile, key source)_ |
| `version` | _(no subcommand)_ |
| `update` | _(self-update; supports `--version` and `--prefix`. macOS/Linux rerun the official release installer; Windows updates natively — Go-native download + SHA-256 verify + binary swap, no `sh`/`curl` required)_ |
| `config` | `set` `get` `list` |
| `api-keys` | `create` `list` `retrieve` `delete` |
| `teams` | `create` `list` `retrieve` `update` `delete` `switch` |
| `teams members` | `list` `add` `remove` |
| `users` | `me` `list` `retrieve` `update` |
| `roles` | `create` `list` `retrieve` `update` `delete` |
| `roles permissions` | `list` |
| `hubs` | `create` `list` `retrieve` `update` `delete` `scaffold` `templates` |
| `hubs navigation` | `list` `add` `remove` `reorder` — edit the menu item-by-item (RMW the `navigation` blob; header/footer/mobile buckets; items addressed by zero-based index). `add` takes `--item-json` (any bucket/type) or the url convenience `--type url --href --label` (header/footer only — mobile items use `--item-json`, `{id,label,route,icon}`); `remove --index`; `reorder --order 2,0,1` |
| `hubs policies` | `get` `update` `gate` — `update` writes a policy document, `gate` flips the hub-level enforcement switch (`settings.policies.enabled`). They are SEPARATE writes: content without the gate is a hub whose ToS exists but is never presented (MIO-2020, MIO-2567). **`get` (MIO-2815)** is the team-owner ADMIN read — the same path as the `update` PATCH, so the method is the only discriminator — reporting both documents and the gate **as stored**. It is not interchangeable with the member-portal read, which serves defaults and forces `enabled=true`. Returns a LIST of `{policy_type, content, version, enabled, require_acceptance}`. Always returns exactly TWO items (tos, privacy_policy) — an unconfigured document is served with rendered default text, so there is NO empty case. `enabled` is the ONE hub-level gate repeated per item. **`version` is NOT a custom-vs-default discriminator**: the backend assigns a version ONLY for tos AND ONLY when require_acceptance is set, and projects an absent one as "default-v1" — so a hand-written privacy policy reads "default-v1". `content` always has a value (yours, else the rendered default) and `require_acceptance` is tri-state (null = unknown, not false). Before a `hubs scaffold --hub` resume, READ THE CONTENT and look at it — that is the only reliable check (MIO-2818). Capture the gate with `-o plain --jq '.[0].enabled'`; a bare `--jq .enabled` EXITS 1 ("expected an object but got: array"), it does not yield null. A resume sends `content:null` for any policy its template declares no text for and reverts that document; the re-prompt only follows for a tos saved WITH `--require-acceptance` — anything else is replaced silently, with no version change to notice afterwards |
| `hubs scaffold` | Build a full-experience hub in one idempotent command from a hub template in the target backend's LIVE catalog (`--template <id>` — the CLI embeds none; `--catalog <file>` is the digest-verified escape hatch, fail-closed on mismatch, and there is no `--offline`): branding/favicon, menu, registration, discussion spaces, onboarding schema, policies, playlists, pages, and — when the template declares a `welcomePost` — a welcome discussion (MIO-2558; no shipped catalog declares one yet, so that terminal step is a plan-visible no-op today, and it is idempotent by post title — compared and posted STRIPPED, because the API stores `title.strip()` — so a resume never creates a second one; the json result reports `welcome_post_status` as `created`/`adopted`/`adopted_deleted`. **`adopted` does not guarantee the post is visible**: the admin list does not serialize `is_removed`, so a moderation-REMOVED match is indistinguishable from a live one and reports as plain `adopted`. Only `adopted_deleted` (soft-deleted) is detectable. If it matters, read the post back with `mio community discussions retrieve`). **SERVER-SIDE HUB OP (MIO-2976).** In CREATE mode the run first PROBES `POST /api/teams/{team_id}/hubs/from-template` (type `hub_scaffolds`, MIO-2926) — the probe IS the real POST, never a capability check. When it answers, the backend builds the WHOLE hub in one transaction and none of the nine steps below run. **The op ships dormant (`HUB_SCAFFOLD_ENABLED` default False), so today every run falls back** — the CLI change is safe to land before any flag flip. Fallback happens on the dormant/absent signal ONLY: a bare `405` with `Allow: GET`, which is byte-identical to a backend without the route. Note this op ALSO answers `404 template_not_found`, and 404 and 405 both derive exit 4 — so the fallback keys on a dedicated `client.ErrHubOpAbsent` sentinel, never on the exit code; every other status surfaces, because a client-side apply against a backend that HAS the op just smears partial state. ALWAYS client-side: `--hub` (the op is create-only), `--dry-run` (the op has no plan mode — probing it would create the hub), an invocation missing `--name` or `--slug` (the op requires both non-empty and will not mint a slug), any branding override (`--branding-json` or a palette flag) and `--catalog` — the op's `overrides{}` carries only `logo_url`/`favicon_url`/`registration_enabled`/`publish`, so taking it with an inexpressible flag would silently drop what was asked for. An empty (or whitespace-only) value for ANY branding key ending `_url` — however spelled: `--logo-url`, `--favicon-url`, `--social-image-url`, or `--branding-json '{"logo_url":""}'` — is a USAGE ERROR before any request (exit 2): the API rejects an empty branding `*_url` on hub create AND hub update (`validate_branding` → `_validate_absolute_https_url`, which rejects the empty string, wired to both write schemas), so NEITHER path can honour it — routing it client-side would create the hub and then 422 at the blobs PATCH, leaving a half-built hub with no rollback. To clear a branding key, scaffold without the flag and then `mio hubs update <hub_id> --unset branding.logo_url`. Every FLAG-forced skip is noted on stderr; `--dry-run` is not, since it is structural and the plan renderer emits no apply-time notes. **ALSO client-side (MIO-3065): a template that declares vocabulary the op does not apply** — `spaces[].icon`, `playlists[].documents`, or a page node binding a playlist `dataSource` by `key`. The ops model none of those (mio-backend `origin/main`, 2026-08-10), so taking one would build a hub that looks finished and is not; the skip is announced and names `MIO-3073`, the backend parity ticket. The pages op has the same gate on the binding key alone — by then the client-side spaces and playlists steps have already applied the rest. The check reads the RESOLVED template, so it self-disables for a template declaring none of it. IDEMPOTENCY: the `Idempotency-Key` is derived deterministically from team+template+name+slug, so re-running the same command converges on the stored application instead of creating a second hub (MIO-2565 on this path). The backend's fingerprint additionally covers the catalog digest and the overrides, so the same name+slug after a catalog pin move — or with different override flags — is `409 idempotency_fingerprint_mismatch`, and the CLI's error text carries that token in square brackets so an agent can branch on it: nothing was applied, and the CLI says so and names the way out (different identity, or `--catalog` to force the client path). RESULT PARITY: `-o json` reports the same keys on both paths. The op returns ids as `created_resource_ids` (per-KIND ordered lists, no slug) plus an ordered `summary[]` of `{resource, action, reason?}` rows whose `resource` IS slug-scoped (`space:<slug>`, `page:<slug>`, `playlist:<key>`, `onboarding:<slug>`, plus `hub`/`welcome_post`); the two are paired positionally, which is sound because the backend appends to both in one statement — but the pairing is CHECKED, and any kind whose created-row count disagrees with its id count is dropped to `null` rather than risking a wrong id. Skipped rows become stderr notes; a `replayed:true` response carries the summary but NO ids and is disclosed as a replay; and the finished hub is read back with a GET so the reported `hub_slug`/`hub_path`/`published` are OBSERVED (the backend mints a unique slug when the requested one is taken), degrading to the requested values with a note if that read fails. The whole plan is validated before any write (`--name` ≤255 code points; `{{hub_name}}`/`{{hub_slug}}` interpolation — unknown tokens rejected, capped post-substitution). Pages: probes the backend `scaffold-from-template` op, falling back client-side on 404/405 (op absent or path shadowed on older backends: create with a `meta.template_provenance` marker → tree set → publish → mark applied), interpolating with the hub's actual title/slug. Re-runs with `--hub <id>` resume safely; an edited or foreign page at a template slug (or any pre-existing homepage) exits 2 — never overwritten. **Playlists (MIO-3065).** Each is created SCOPED to the hub (`hub_id`) — without that its hub detail page 404s for every viewer whatever the publication row says — at the template's own `visibility` (validated against the enum the CREATE takes, `public|unlisted|private`; the hub-media enum's `members` is NOT a legal value here and used to pass preflight and 422 at apply). Its per-hub publication row is `visibility: public`, a ratified hardcode with no manifest key behind it: members-only day one is not template-expressible today. A `playlists[].documents[]` entry becomes a synthetic READY document file (`files/synthetic`, MIO-2285) at the playlist's own visibility, gets its OWN per-hub publication row — without which it is attached but discloses zero item cards to an anonymous viewer — and is attached as an item; items land `file_ids` first, then documents. Files named in `file_ids[]` are attached but NOT published: publishing media the author already owns is not the scaffold's call. **Playlist dataSource FILL CONTRACT (MIO-3065).** A page node shipping `dataSource: {"type":"playlist","id":"","key":"<playlists[].key>"}` has that `key` resolved against the playlists this run created and the id written into `id` before the tree PUT — the renderer filters on a truthy `ds.id`, and the backend compiler treats a present-but-empty id as `("container","")`, so an unfilled node renders an empty band. A key naming no playlist of the same template is an exit-2 PREFLIGHT failure, before the hub exists; a key that simply had no playlist created THIS run (the playlists step's skip-if-the-hub-already-has-any gate) is a stderr note and leaves the node as the catalog shipped it. **Policies (MIO-2567):** the step writes each policy document AND then flips the hub-level enforcement gate (`PATCH …/policies/gate`, `settings.policies.enabled`) when the template declares `enabled: true`. Enforcement is ONE flag per hub, not one per policy, so the template's per-policy `enabled` values are collapsed onto it — they must agree, and a `true` alongside a `false` is rejected in the WRITE-FREE PREFLIGHT (exit 2 naming both keys, before the hub is created). The write is ENABLE-ONLY, matching the ratified applier contract: a template that declares none, or that resolves to `false`, writes no gate and leaves the hub's setting as it is (narrated on stderr) — disabling is `mio hubs policies gate <hub_id> --enabled=false`. **RESUME CAVEAT — re-runs are NOT safe for policy CONTENT.** The policy write always sends `content`, so a template that omits it (the shipped `community` one does) RESETS that policy to the backend default on every apply; for a ToS with `require_acceptance` that also bumps the version, which RE-PROMPTS every member who had already accepted. Before this fix the gate was off so nobody saw it; now it is live. If you hand-edited a hub's ToS, re-apply it after any resume (`mio hubs policies update <hub_id> --policy-type tos --content @tos.md --require-acceptance`), or read the stored state first with `GET /api/v1/teams/{team_id}/hubs/{hub_id}/policies` (the admin policies READ, MIO-2394 — same owner credentials as `policies gate`; read it with `mio hubs policies get <hub_id>`, MIO-2815). **Version gate: the gate write and the `policy_gate` result key are NOT in `v0.13.0` or earlier** — on those binaries a scaffolded hub's ToS is written but never enforced (a fresh member reads `tos_acceptance_required:false` and `POST …/tos/accept` answers the enumeration-safe 404); repair with `mio hubs policies gate <hub_id> --enabled`. `--dry-run` previews the plan (including the palette it would apply, and the gate line); `--publish` (default off) goes live; `--name`/`--slug` create a new hub; `--favicon-url`/`--logo-url`/`--registration-enabled` override the template. **Version gate: `--primary-color` and friends, and the machine-readable `--output json` result, are NOT in `v0.12.1`** — on that binary they exit 2 with `unknown flag`. Check `mio version`. **Branding (MIO-2604):** `--primary-color`/`--secondary-color`/`--text-color`/`--background-color`/`--header-color`/`--header-accent`/`--social-image-url` (plus `--branding-json` for a whole object) all MERGE over the template's `branding` block, so a key you don't name keeps the template's value; precedence is template → `--branding-json` → scalar flags. `--primary-color` also fills `header_color` unless YOU gave a header color (`--header-color`, or a `header_color` key in `--branding-json`) — the template's own value does not suppress it. Values are passed through unvalidated (branding is opaque JSONB server-side); the KEYS are strict-checked pre-auth against the MIO-2515 allowlist, so a typo exits 2 with no request. Honors `--output` like every other command: json (the default off a TTY) returns `{hub_id, hub_slug, hub_name, hub_path, published, template_id, catalog_revision, branding_overrides, homepage_page_id, welcome_post_id, welcome_post_status, pages[], spaces[], onboarding_attributes[], playlists[], policies[], policy_gate}` — `branding_overrides` is the resolved override layer, cascade included (`{}` when none), and `policy_gate` is the enforcement gate this run WROTE — `true`, or `null` when it wrote none (no declaration, or a resolved `false`, both of which leave the hub's setting standing; the write is enable-only). `policies[]` says the document exists, `policy_gate` says whether this run turned enforcement on — so `HUB_ID=$(mio hubs scaffold … -o plain --jq .hub_id)` works (`-o plain`: `--jq` alone JSON-quotes a string, MIO-2792); all progress narration goes to stderr, and a step that fails after the hub exists names the created hub id in the error (nothing is rolled back). |
| `hubs templates` | `list` the hub templates from the target backend's catalog (needs credentials, not a team — shows exactly what a scaffold against that backend would apply). `--catalog <file>` lists a local artifact instead, digest-verified and fail-closed exactly like `hubs scaffold --catalog`, so a catalog branch's templates can be inspected before they are scaffolded (MIO-3065; before it, this command exited 2 with `unknown flag`) |
| `contacts` | `create` `list` `retrieve` `update` `delete` `restore` |
| `contact-attributes` | `create` `list` `retrieve` `update` `delete` |
| `contact-attributes options` | `create` `list` `update` `delete` |
| `contact-attributes hub-config` | `create` `list` `update` `delete` |
| `contact-attributes values` | `get` `set` |
| `tags` | `create` `list` `retrieve` `update` `delete` `assign` `assign-bulk` `remove` |
| `segments` | `create` `list` `retrieve` `update` `delete` `search` `members` `count` |
| `content` | `create` `list` `retrieve` `children` `update` `delete` `restore` `reorder` |
| `pages` | `create` (`--privacy public\|members\|private` — **defaults to `members`**, so omitting it ships a login-walled page) `list` `retrieve` (add `--tree` for raw node tree) `update` `delete` `home` `publish` |
| `pages sections` | `create` (`--type` validated against the catalog writable set) `list` `update` `delete` `reorder` |
| `pages tree` | `get` `set` (author a page's draft node-tree; `set` takes `--file` + optional `--if-match` — omit for the first tree on a draft-less page, defaults to `0`). `get` returns `{tree, draft_version}`; `set` wants `{root}` — unwrap and re-wrap (see Page-Tree Render Contract) |
| `pages catalog` | `scaffold` (`--template`/`--variant` → a node-tree for `pages tree set`) `templates` (`--page-type`) `section-types` (`--writable-only`). **There is no `pages catalog list`** |
| `media files` | `list` `retrieve` `durable-url` (non-expiring hub-scoped image URL; `--hub` `--preset` `--publish`) `update` `delete` `upload` (create → presigned PUT → finalize, auto-multipart) `replace` `finalize` `transcode` `register-synthetic` |
| `media files cards` | `get` `set` (`--cards` JSON array/@file) |
| `media files chapters` | `get` `set` (`--chapters` JSON array/@file) |
| `media folders` | `list` `create` `retrieve` `update` `delete` `move` (`--parent-id`/`--to-root`) |
| `media search` | hybrid transcript search (`--query` `--hub-id` `--limit`) |
| `media playlists` | `list` `create` `retrieve` `update` `delete` `set-cover` (`--file-id`) |
| `media playlists items` | `add` `list` `remove` `reorder` (populate a playlist: `--playlist-id` `--file-id` `--position`) |
| `media hub-media` | `publish` `list` `unpublish` (standalone files → hub; `--file-id`) |
| `media hub-playlists` | `publish` `list` `unpublish` (playlists → hub; `--playlist-id`) |
| `media attachments` | `list` `show` `update` `delete` |
| `media transcripts` | `get` `vtt` `content` `versions` `edit` `revert` |
| `community discussions` | `list` `create` `retrieve` `update` `delete` — hub-scoped admin verbs. **`create`** (`--space-id` `--title` required; `--body`; `--is-published=false` for a draft — omit it and the post publishes, the API's default) posts into a hub space; the AUTHOR is derived server-side from your credentials, so there is deliberately **no author flag** and the API 422s a smuggled one. **Version gate: `create` lands in the release AFTER `v0.13.0`** — on `v0.13.0` and earlier it exits 2 with `unknown command`. `update` sets moderation state only — `--is-pinned`/`--is-locked`/`--is-broadcast`; title and body belong to the author and are not admin-editable, so they exist on `create` and not on `update` |
| `products` | `create` `list` `retrieve` `update` `delete` |
| `products prices` | `create` `list` `retrieve` `update` `delete` |
| `checkout orders` | `list` `retrieve` |
| `checkout subscriptions` | `list` `retrieve` `cancel` |
| `checkout payments` | `list` `retrieve` `refund` |
| `checkout webhooks` | `list` `retrieve` `replay` |
| `checkout accounts` | `list` `retrieve` `onboarding-link` (web/JWT-only — always fails from this CLI, see Command Gotchas) |
| `checkout stripe-sync` | `import` `import-status` `adopt-product` |
| `email drip-campaigns` | `create` `list` `retrieve` `update` `delete` `activate` `pause` (create/update accept `--enrollment-mode` `--trigger-event-type` `--segment-id` `--segment-check-interval-minutes` `--allow-reenrollment`) |
| `email steps` | `create` `list` `update` `delete` |
| `email templates` | `create` `list` `retrieve` `update` `delete` `preview` |
| `email config` | `set` `get` `delete` `test` |
| `email enrollments` | `list` `exit` |
| `email stats` | `get` |
| `access-rules rules` | `create` `list` `retrieve` `update` `delete` |
| `access-rules overrides` | `create` `list` `retrieve` `update` `delete` |
| `activity` | `contact` `top-engaged` |

---

## Command Gotchas

- **`pages publish` requires `--if-match <draft_version>`** — read the `draft_version` attribute from a prior `pages retrieve`, then pass it as `--if-match`. The backend uses it as an optimistic-concurrency guard and will return 409 if the draft has changed since you read it.
- **`pages tree set` `--if-match` is OPTIONAL (defaults to `0`) — publish's is not.** `pages tree get` 404s on a page that has no draft yet, so the very FIRST tree set has no `draft_version` to echo back: omit `--if-match` and it defaults to `0`, which the backend accepts only while the page is still at `draft_version 0` (a fresh page starts there). For every subsequent set pass the current `draft_version`. The default does NOT bypass optimistic concurrency: a defaulted (or stale) `0` against a page that already has a draft returns a 409 conflict, so you can never silently clobber an existing draft.
- **`pages tree get` and `pages tree set` do NOT share a shape, and the fix is a RE-WRAP.** `get` answers `{"id": …, "tree": <the root NODE, bare>, "draft_version": N}` — the backend deliberately unwraps before answering (`bare_node = resolved.get("root", resolved)`; its DTO documents `tree` as "the resolved draft tree ROOT node — bare (not wrapped in `{root: ...}`)"). `set --file` rejects anything without a top-level `root` (`Tree must have a 'root' key at the top level.`). So `--jq .tree` alone produces a file `tree set` refuses: use `--jq '{root: .tree}'` and keep the `draft_version` for `--if-match`. `pages catalog scaffold` already emits the *set* shape.
- **A page created by `hubs scaffold` already has a draft.** Its `draft_version` is `1`, not `0`, so the first tree write YOU make against it needs `--if-match 1` — the `0` default is only correct for a page that has never had a draft. Always read it back: `mio pages tree get <page_id> --jq .draft_version`.
- **`pages create --privacy` defaults to `members`.** A page created without `--privacy public` is behind the login wall, which is how a hub ships looking public and isn't. `hubs scaffold` sets it (MIO-2563); the manual page path does not. Also: `home` is a reserved slug (rejected) and an omitted `--slug` fails with `Field required` — use a real slug and mark the homepage with `--is-home`.
- **`pages retrieve --tree`** returns a `page-trees` resource (raw published node tree) instead of page metadata. Use this for admin editor access.
- **Scaffold real pages via the tree door, not imperative sections.** `pages catalog scaffold --template <id>` emits the same node-tree artifact the visual builder produces (a Go port of the reference applier mints fresh UUIDv7 ids). It is emit-only — pipe a PAGE template's `{"root":…}` output straight into `pages tree set`:
  ```sh
  mio pages catalog scaffold --template page-homepage > tree.json
  # FIRST tree on a draft-less page: omit --if-match (defaults to 0). 'tree get' 404s until a draft exists.
  mio pages tree set <page_id> --file tree.json
  # SUBSEQUENT edits: read the current draft_version and pass it back for OCC.
  V=$(mio pages tree get <page_id> --jq '.draft_version')
  mio pages tree set <page_id> --if-match "$V" --file tree.json
  ```
  The catalog is live-fetched (`GET /api/page-builder/catalog`, ETag/304-cached per backend origin). The read-only `pages catalog` group also carries a digest-pinned embedded fallback, so these emit-only commands work offline (`--offline` forces the embedded copy; `--catalog <file>` overrides). The mutating `hubs scaffold` does NOT degrade: it requires a live fetch (no `--offline`; its `--catalog <file>` fails closed on a digest mismatch). `pages catalog templates --page-type <pt>` lists what's recommended per page type; `pages catalog section-types --writable-only` is the `sections create --type` allow-list.
- **`mio hubs …` verbs take the hub id as an OPTIONAL positional.** Precedence is positional → `--hub` → `current_hub` in config → single-hub auto-default, so `mio hubs retrieve --hub <id>` and a bare `mio hubs retrieve` (with `current_hub` set) both work, exactly like every other hub-scoped group. Applies to `retrieve`, `update`, `policies update`, `policies gate`, `redirect-origins get|set`, `email-settings get|update` and `navigation list|add|remove|reorder`. When no hub resolves from any source the error names all three sources (never Cobra's `accepts 1 arg(s), received 0`). A positional you actually *pass* always wins, so a **blank** one (`mio hubs update "$HUB_ID" …` with an empty `$HUB_ID`) is a usage error that fires no request — it never silently falls back to the ambient hub. **Exception: `hubs delete` still requires the id positionally** — it is irreversible, so it never takes its target from ambient context. On the `navigation` verbs the hub id shares the positional slot with the bucket; a lone `header`/`footer`/`mobile` is read as the bucket (`mio hubs navigation add header …` edits the ambient hub). (MIO-2732)
- **`hubs policies update [hub_id]`** — policy content supports the `@file` convention: `--content @tos.md`.
- **`hubs policies update --policy-type`** must be exactly `tos` or `privacy_policy`; the CLI validates this client-side. The `--require-acceptance` flag is only meaningful for `tos`; passing it with `privacy_policy` will result in a backend 422.
- **`hubs policies update` content flags** — exactly one of `--content` or `--reset-content` is required (they are mutually exclusive):
  - `--content <text|@file>` — supply the policy body inline or read it from a file.
  - `--reset-content` — revert the policy to the backend default (sends `content: null`).
  Providing both exits 2; providing neither also exits 2.
- **`hubs create` is PRIVATE by default.** Pass `--published` to publish. The response has no public-URL field (and the CLI knows only the API base, not the hub-frontend host), so no URL is echoed; output carries a derived `published` (= `!is_private`) and, in table mode, a private hub prints the slug + publish hint on stderr. `--logo-url` and `--favicon-url` merge into `branding` (`logo_url`/`favicon_url`).
- **`hubs retrieve` AND `hubs list` output carry derived booleans** (list since MIO-2991; `--raw` bypasses them on both) `registration_enabled` (= `settings.registration.enabled === true`, fail-closed) and `published` (= `!is_private`). These are read-only convenience views, never sent on a write; `--raw` bypasses them.
- **`hubs update` blob flags** — `--registration-enabled true|false` sets `settings.registration.enabled` (read-modify-write; sibling keys preserved; gated on `Changed()` so `=false` differs from unset). `--favicon-url` sets `branding.favicon_url`. `--unset <dotted.path>` DELETES a blob key — the ONLY real delete (the `*-json` flags are merge-only: a literal `null` persists as null, `{}` is a no-op). First dotted segment selects the blob (`branding`/`settings`/`meta`); nested paths, comma-lists and repeats are supported; a blank/bare-blob/unknown-blob path exits 2 and fires no request. Apply order per blob: `--*-json` merge → scalar flags (`--logo-url`/`--favicon-url`/`--registration-enabled`) → `--unset` LAST (unset wins over a same-command merge).

- **Contact name flags are kebab-case**, like every other resource: `--first-name`, `--last-name`. `--email`, `--phone`, `--status` are also available. (The legacy underscore spellings `--first_name`/`--last_name` still work as hidden, deprecated aliases for back-compat, but new scripts should use the kebab form.)
- **`products prices` takes the product id as a positional argument**, not `--product`:
  `mio products prices create <product_id> --amount 4900 --currency usd --type recurring --interval month --interval-count 1`.
  `--amount`, `--currency` and `--type` (`one_time`|`recurring`) are all required; `--interval` **and** `--interval-count` are required when `--type=recurring`. The optional label flag is `--name` (there is no `--nickname`), alongside `--description` and `--is-active`.
- **`segments search --conditions` takes the full condition tree** (the backend write shape), not a flat list. Prefix with `@` to read from a file. There is no `--match` flag.
  ```sh
  mio segments search --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":"@example.com"}]}]}'
  mio segments search --conditions @conditions.json --page-size 50 --page-after <cursor>
  ```
- **Generate the full reference** for any command set with `mio gen-docs --dir ./docs` (one Markdown file per command).
- **`checkout accounts onboarding-link` always fails (exit 3, `ExitAuth`)** — the backend rejects API-key principals on this route so a leaked team API key can't attach an attacker's Stripe payout account (MIO-2655). This CLI is API-key-only (see Authentication above — any JWT is discarded right after `mio login` mints the stored key), so the command can never succeed here; it fails fast client-side with no HTTP request. Connect a Stripe account via the member.dev dashboard instead. (MIO-2717)

---

## Page-Tree Render Contract

The API validates a page tree's **structure**, not its **renderability**. The shapes
below return `200` and then render nothing, with no error anywhere (MIO-2539,
MIO-2663, MIO-2664). The embedded agent skill (`mio skills print`) carries the full
authoring recipe; this is the reference.

**Node envelope** — `value` is a sibling of `settings`, *not* `settings.value`:

```json
{ "id": "<uuid>", "kind": "headline",
  "value": "Welcome", "settings": { "level": 1, "weight": 700 } }
```

A section node is the same envelope plus a `template` and `children`:

```json
{ "id": "<uuid>", "kind": "container", "template": "row",
  "settings": { "maxWidth": "content", "padding": 0,
                "surface": { "padding": "section", "background": { "type": "tint" } } },
  "children": [ ] }
```

- The renderer dispatches on **`kind`**, never `type`. `template` marks a node as a
  section (publish-time conversion + surface wrapping).
- `value` in `settings.value` is the highest-frequency silent drop. The sole node
  kind that legitimately reads `settings.value` is `progress-ring` (a number).
- No defaults cascade — inline every rendering value on its own node. Exactly one
  `level:1` headline per page (the rest are demoted to `<h2>`).
- `settings.weight` must be a **number** (`700`), never `"bold"`; `pages tree set`
  rejects THREE shapes client-side, before any HTTP: that, a blank/non-string `template`, and a content `value` placed under `settings` on a kind that reads the top-level one (MIO-2575).

**Value-bearing kinds** — the seven the renderer reads a top-level `value` from: `headline` `text` `image` `video` `button` `icon` `quote` — `divider` reads no value at all, and `progress-ring` reads `settings.value` (a number), which is why `pages tree set`'s misplacement check allowlists exactly seven kinds (MIO-2575).
`quote`'s `value` is an **object** (`{quote, name?, profession?, avatarUrl?,
avatarFallback?}`) and renders nothing unless `quote` is a non-empty string;
`progress-ring` is the one kind that reads `settings.value` instead. `subheadline`,
`paragraph`, `embed`, `html`, `spacer`, `stat`, `input` and `section` are **not** node
kinds — use `headline` with `level:3`, `text`, and container `gap` respectively.
`content-grid` is a *template* id, not a kind. There is **no `sidebar` kind**: a
sidebar is a `row` whose `stack` children carry `settings.width`.

**The full node-kind and settings vocabulary is generated**, not hand-listed — see
the skill (`mio skills print`), whose `<!-- catalog-gen:… -->` blocks are rendered
from the embedded catalog's `settingsSchema` by `go generate ./...` and byte-pinned
by `TestSkillDocIsGeneratedFromCatalog`. Do not transcribe those lists here; they
moved three catalog minors in a day.

**Button action** is `{"type": …, "value": …}` with `type` ∈ `url` | `page` |
`email` | `scroll` | `playlist`; `value` is canonical **for its type** — `url` takes a
FULL URL *including* the scheme (a schemeless value is passed through and does not
navigate), while `email` takes a bare address and `scroll` a bare anchor id.

**`settings.surface`** is `{"padding", "background", "gradient"}`:

| `background` | renders |
|---|---|
| `{"type":"tint"}` | light tint of branding `secondary` — the only value scaffolds emit |
| `{"type":"color","token":"primary\|secondary\|accent\|muted\|background"}` | solid theme color; `primary` also stamps `data-bg="primary"` (bold band + primary-button auto-inversion) |
| `{"type":"custom-color","value":"#rrggbb"}` | solid inline color (invalid hex → nothing) |
| `{"type":"gradient"}` | gradient, configured by the **sibling** `surface.gradient` |
| `{"type":"image","url":"<durable-url>","blur":true}` | image layer; the `secondary` scrim is **always** composed over it (not authorable), `blur` only adds a further layer |
| `{"type":"none"}` | nothing |

`thumbnail` is **not** a valid value — the catalog excludes it deliberately ("declared
as a TODO in hub types, implemented nowhere, used by no recipe"), so authoring it hits
the off-enum path below.

Two traps worth stating plainly:

1. **An off-enum `background.type` is accepted on publish and renders a transparent
   row with no error** — the renderer resolves an unknown discriminant to no class and
   no style. The catalog documents its own limitation too: the enum constrains `type`,
   but every variant field is optional and nothing checks that you supplied the one
   your `type` needs, so `{"type":"custom-color"}` with no `value` also renders nothing.
2. **The gradient config is a SIBLING of `background` (`surface.gradient`), not
   nested inside it.** Nesting is ignored and you silently get the default `split`
   gradient. `gradient.type` ∈ `monochrome` | `analogous` | `complementary` |
   `triadic` | `split` | `warm-shift` | `custom`; `custom` requires `customStart` +
   `customEnd` (6-digit hex) or it falls back to `split`.

**Vocabulary** comes from the catalog, not from this file — `mio pages catalog
templates` and `mio pages catalog section-types` print the truth for the backend you
are talking to. The skill's copies of those lists are guarded against catalog drift
by `TestSkillDocIsGeneratedFromCatalog`.

---

## Hub Branding and Navigation Contract

**Branding keys** (`--branding-json`, `hubs scaffold --*-color`) map onto the
frontend like this:

| key | paints |
|---|---|
| `primary` | buttons, links, CTAs, brand accents |
| `secondary` | **the page's ink in light mode — every heading AND all body copy (`--hub-text: var(--hub-secondary)`) — and the page background in dark mode**; also the base of a light-mode `tint` surface |
| `text` / `background` | body copy / page background — **only when the theme mode is `custom`**; in `light`/`dark` the theme layer clears both and derives them from `secondary` |
| `header_color` / `header_accent` | top-nav background + accent (emitted raw, no contrast correction) |
| `dark_mode` (bool) | **not** the theme selector — only flips the defaults for `background`/`text` when those are unset |

- **`secondary` is the classic mistake**: it is not a decorative accent, it is the
  page's ink. In `light` mode `--hub-text: var(--hub-secondary)`, so it colors every
  heading *and* all body copy, and it is the base of every `tint` surface; in `dark`
  mode it becomes the page background. Set it light and the whole page goes invisible.
- **To control `background`/`text` at all, ask for `custom`**:
  `--settings-json '{"background":{"type":"custom"}}'` alongside the branding keys. In
  `light`/`dark` the theme layer clears both and derives them from `secondary`. In
  `custom` mode `text` is AA-contrast-clamped against `background`, so a low-contrast
  pair is corrected rather than honoured exactly.
- Color values must be **6-digit hex** for all six color keys (`primary`, `secondary`,
  `background`, `text`, `header_color`, `header_accent`). The frontend parser tests
  `/^#[0-9a-fA-F]{6}$/` and silently substitutes its own default on anything else, so
  3- and 8-digit hex, named colors, `rgb()`/`hsl()` and gradients all render wrong with
  a `200` on the wire. The CLI does not validate values (conduit rule) — this is the
  frontend's render contract, not the API's.
- **Every `*_url` branding key is validated server-side** — the rule applies to ANY key
  ending `_url` (case-insensitive, present or future); on the CLI's allowlist that is
  `logo_url`, `favicon_url`, `social_image_url`, `custom_login_logo_url` and
  `custom_font_url` (MIO-2658). Each must be `null` or a string, and a string must be an
  absolute `https://` URL: plain `http://`, `data:`, `javascript:`, protocol-relative,
  relative paths, whitespace/control characters (raw or percent-decoded), backslashes,
  `user:pass@` credentials, percent-encoded hosts and bad ports are all rejected (422).
  Non-`_url` keys round-trip unchecked.
- **A hub cannot select light or dark — only `custom`.** `resolveThemeMode()` consults
  the hub's mode for exactly one value: `if (hubMode === 'custom') return 'custom'`.
  Otherwise light vs dark comes from the **viewer's** `mio-hub-theme` cookie (default
  `system`) and their OS `prefers-color-scheme`. Writing `light`/`dark` anywhere on the
  hub is a no-op, and there is **no `settings.theme` key** either (the parsed
  `theme.mode` is derived from `settings.background.type`). `custom` IS honoured
  unconditionally, via `--settings-json '{"background":{"type":"custom"}}'`, and it is
  the only mode in which `branding.background`/`branding.text` are read at all.
  `branding.dark_mode` selects nothing.

**Navigation `icon` values are two different vocabularies** (MIO-2675), neither
validated by the CLI:

- `header`/`footer` accept any id from the hub frontend's icon sprite (~205 ids;
  `ICON_NAMES` in mio-hub `src/components/ui/icon.tsx` is generated alongside
  `public/icons/sprite.svg` and is authoritative). `icon` is optional — an unknown
  name drops the icon, not the item. **`info` and `globe` do not exist** (hence the
  blank glyphs); use `information`/`information-circle` and `earth`/`global`.
- `mobile` accepts an **8-value whitelist** and `icon` is **mandatory** — an off-list
  icon drops the whole tab: `Home` `Bell` `User` `Users` `MessageSquare`
  `MessageCircle` `Search` `content` (frontend component names, not sprite ids;
  the lowercase `content` is deliberate). Under 3 valid tabs and the frontend
  replaces the list with its defaults; over 5 and it truncates.

---

## Minimal Agent Snippet

```sh
#!/usr/bin/env bash
set -euo pipefail

export MIO_API_KEY="${MIO_API_KEY:?MIO_API_KEY must be set}"   # mio_sk_live_…

# Resolve team from config (or pass --team explicitly)
CONTACTS=$(mio contacts list --output json)

# Filter with --jq
FIRST_ID=$(mio contacts list -o plain --jq '.[0].id')   # -o plain: string ids need it (MIO-2792)

# Delete safely in a script (destructive ops need --yes off a TTY)
mio contacts delete "$FIRST_ID" --yes
```

---

## Useful Links

- [README.md](./README.md) — install, quickstart, global flags, full usage guide
- [llms.txt](./llms.txt) — machine-readable one-line-per-command index
- `mio <resource> --help` — full flag reference per resource
