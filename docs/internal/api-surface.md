# mio API Surface — CLI Command Catalog

Authoritative endpoint reference for building `mio` commands. Every command maps
to a backend route below. Backend is FastAPI; **all bodies are JSON:API v1.1**
(`Content-Type: application/vnd.api+json`) **except** `/api/auth/*` which is
plain JSON.

## Conventions

- **Team scope:** most routes are nested under `/api/teams/{team_id}/…`. The
  `{team_id}` comes from the active context: the API key's team, the `--team`
  flag, or `config current_team`. With an API key, `{team_id}` is implicit (the
  key belongs to a team) — the client should still substitute it into paths.
- **Hub scope:** many resources nest under `…/hubs/{hub_id}/…`. Supplied via a
  `--hub` flag or `config current_hub`.
- **CLI verbs:** `create`(POST) `retrieve`(GET id) `update`(PATCH) `delete`(DELETE)
  `list`(GET collection) `search`(POST/GET query) + custom actions.
- **Resource identifier flags:** create/update take `--field value` flags mapped
  to `<field>` (snake_case). For most resources the client wraps them in a JSON:API
  envelope `{"data":{"type":<derived>,"attributes":{…}}}` (`client.StyleEnvelope`,
  the default). The backend is NOT uniform, though — a handful of endpoints bind a
  **flat** pydantic model and must be sent WITHOUT an envelope (`client.StyleFlat`):
  - `users update`, `roles create/update`, `api-keys create` (incl. the
    `mio login` password→mint flow via `MintAPIKey`, which posts a flat body
    with the JWT bearer token)
  - `email config set` (PUT email-config; JSON:API envelope, `mail_*` attributes — backend reads `data.attributes`, MIO-2640)
  - `checkout stripe-sync import` (flat `{hub_id}`) and `adopt-product`
    (flat `{stripe_product_id, hub_id}`)
  Sending the wrong shape 422s either way, so the style is declared per command.
- **No-body action endpoints:** several POST actions take NO request body — send
  `nil`, not an (enveloped) payload: `email config test` (re-tests stored
  creds, mails the authenticated user), `email templates preview` (sandbox
  render), `teams switch`, `checkout subscriptions cancel`, `checkout webhooks
  replay`, `content/contacts restore`, `email drip-campaigns activate/pause`.
- **Body-carrying action endpoints:** `checkout payments refund` (envelope
  `refunds`; body REQUIRED — `--reason` mandatory, `--amount` optional and
  omitted for a full refund), `content reorder` (envelope `content_nodes`), and
  `checkout accounts onboarding-link` (envelope `onboarding_links`, ALL of
  `--hub-id`/`--return-url`/`--refresh-url` required).
- **Type derivation:** the envelope `type` is derived from the request path via
  `resourceTypeFromPath` + a `typeOverrides` table for the cases where the backend
  `type` literal differs from the URL segment (e.g. `segments`→`segment`,
  `contacts`→`team_contacts`, `members`→`team_members`, `hubs/contact-attributes`
  →`contact_attribute_hub_configs`, `steps`→`drip_steps`, `payments/refund`
  →`refunds`, `payment-accounts/onboarding-link`→`onboarding_links`).
  All override values are `snake_case` per MIO-636 (backend cutover 2026-06-04).
- **segments search:** body is an envelope `{"data":{"type":"segment_search",
  "attributes":{"conditions":<tree>,"page":{"size","after"}}}}`. `--conditions`
  takes the full condition tree as JSON (`{"version":1,"groups":[…]}`), `--page-size`
  and `--page-after` drive pagination. There is no `--match` flag.
- Each `cmd/<resource>.go` self-registers via `func init(){ rootCmd.AddCommand(...) }`.

---

## auth (handled by login.go / register.go, not resource commands)
- `POST /api/auth/login` {email,password} → tokens (plain JSON)
- `POST /api/auth/register` {email,password,first_name?,last_name?} → 201 tokens
  (same TokenResponse as login; unauthenticated). Surfaced as `mio register`
  (`cmd/register.go`): creates the account then auto-logs-in by feeding the
  returned access token into the shared `mintAndStore` tail (resolveTeamID →
  MintAPIKey → keychain), so it REPLACES any stored key. Names sent only when
  non-empty. Backend auto-provisions a personal team, so the JWT team_id claim
  resolves the mint target with no `GET /api/teams` round-trip.
- `POST /api/auth/refresh` (Bearer refresh)
- `POST /api/auth/logout`
- `GET /api/auth/me` → current user

## api-keys  (NEW backend feature — `cmd/apikeys.go`)
- `create` → POST `/api/teams/{team_id}/api-keys` {name, expires_at?} → returns secret ONCE
- `list`   → GET `/api/teams/{team_id}/api-keys`
- `retrieve`→ GET `/api/teams/{team_id}/api-keys/{id}`
- `delete` → DELETE `/api/teams/{team_id}/api-keys/{id}`  (revoke)

## teams  (`cmd/teams.go`)
- `create`   POST `/api/teams`
- `list`     GET `/api/teams`
- `retrieve` GET `/api/teams/{id}`
- `update`   PATCH `/api/teams/{id}`
- `delete`   DELETE `/api/teams/{id}`
- `switch`   POST `/api/teams/{id}/switch`
- `members list`   GET `/api/teams/{id}/members`
- `members add`    POST `/api/teams/{id}/members` {user_id, role_id?}
- `members remove` DELETE `/api/teams/{id}/members/{user_id}`

## users  (`cmd/users.go`)
- `me`       GET `/api/users/me`
- `retrieve` GET `/api/users/{id}`
- `list`     GET `/api/users`
- `update`   PATCH `/api/users/{id}`

## roles  (`cmd/roles.go`)
- `create` POST `/api/roles`
- `list`   GET `/api/roles`
- `retrieve` GET `/api/roles/{id}`
- `update` PATCH `/api/roles/{id}`
- `delete` DELETE `/api/roles/{id}`
- `permissions list` GET `/api/permissions`
- `permissions assign` POST `/api/roles/{id}/permissions`  body: **flat** {slug} (user-JWT only; API keys 401)
- `permissions remove` DELETE `/api/roles/{id}/permissions/{slug}`  (destructive; user-JWT only)

## hubs  (`cmd/hubs.go`)
- `create`   POST `/api/teams/{team_id}/hubs`
  - presentation-blob flags (create-only): `--branding-json` `--navigation-json` `--settings-json` `--meta-json` (opaque JSONB objects; inline JSON or `@file`); `--logo-url` merges into `branding` (MIO-2254); `--favicon-url` merges into `branding.favicon_url` (MIO-2522)
  - discoverability (MIO-2521): output includes a derived `published` (= `!is_private`); in table (human) mode a private hub also prints a stderr hint with the slug + how to publish (`mio hubs update <id> --published`). No public URL is echoed — the create response carries no domain/url field and the CLI knows only the API base, not the hub-frontend host, so a URL is not derivable client-side (surfaced honestly, not fabricated)
  - `--navigation-json` header/footer `type:"url"` items with a hub-relative `href` (leading `/`) must stay within the hub — start with `/{--slug}` — else ExitUsage; absolute `http(s)://` hrefs pass as-is (MIO-2270)
  - blob-key validation (best-effort, MIO-2515): unknown keys in `--branding-json`/`--settings-json`/`--meta-json` **warn** on stderr by default and **error** (ExitUsage, no request) under `--strict-keys`. The API stores these blobs verbatim (opaque JSONB, no server schema) so a typo silently has no effect; the CLI curates a known-key allowlist from the demo-hub seeder + backend reads (the hub frontend is the authoritative render contract, so the list is not exhaustive). Accepted top-level keys — branding: `logo_url,favicon_url,social_image_url,custom_login_logo_url,custom_font_url,primary,secondary,background,text,primary_color,secondary_color,background_color,header_color,header_accent,dark_mode,gradient,font_heading,font_body,heading_font_size,body_font_size,labels`; settings: `customCss,menu,header,footer,background,appearance,policies,registration,email,auth`; meta: `memberDirectory,discussions,moderation`. Deep-validated stable settings sections: `policies{enabled,show,tos,privacy_policy}`, `registration{enabled}`, `email{from_name,reply_to}`, `auth{allowed_redirect_origins}` (branding/meta/other settings sections are FE-owned → top-level only). `navigation-json` is validated separately (typed items) and is unaffected.
- `list`     GET `/api/teams/{team_id}/hubs`
- `retrieve` GET `/api/teams/{team_id}/hubs/{id}`
  - output includes derived convenience booleans (MIO-2516/2521): `registration_enabled` (= `settings.registration.enabled === true`, fail-closed) and `published` (= `!is_private`). Derived for readability; never sent on a write; `--raw` bypasses them
- `update`   PATCH `/api/teams/{team_id}/hubs/{id}`
  - `--navigation-json` authors the header/footer menu (typed items; inline JSON or `@file`); whole-blob replace, validated client-side (untyped items rejected) (MIO-2255). Hub-relative `type:"url"` hrefs (leading `/`) must start with `/{hub.slug}` — the update retrieves the hub for its slug — else ExitUsage; absolute `http(s)://` hrefs pass as-is (MIO-2270)
  - `--branding-json` / `--settings-json` / `--meta-json` deep-merge (read-modify-write: retrieve → merge → PATCH, so sibling keys survive); `--logo-url` merges into branding (MIO-2256, unblocks MIO-901); `--favicon-url` merges into `branding.favicon_url` (MIO-2522)
  - `--registration-enabled true|false` sets `settings.registration.enabled` via RMW, preserving sibling settings/registration keys; gated on `Changed()` so `--registration-enabled=false` is distinguishable from unset (MIO-2516)
  - `--unset <dotted.path>` DELETES a key from a blob (the only real delete — the `*-json` flags are merge-only: a literal `null` persists as null, `{}` is a no-op). First segment selects the blob (`branding`/`settings`/`meta`); nested paths supported (e.g. `settings.registration.enabled`). Repeatable and comma-separated. Blank/bare-blob/unknown-blob paths are ExitUsage and fire no HTTP request. Deterministic apply ORDER on each blob: (1) `--*-json` deep-merge, (2) scalar overrides (`--logo-url`/`--favicon-url`/`--registration-enabled`), (3) `--unset` removals LAST — an unset in the same command wins over a merge (MIO-2517). NOTE: some settings sub-trees are stripped/managed elsewhere (`settings.policies.tos`/`privacy_policy` via `hubs policies update`; `settings.auth.allowed_redirect_origins` via `hubs redirect-origins set`), so unsetting those through the settings PATCH may be a no-op
  - blob-key validation (MIO-2515): same allowlist as create — unknown keys warn (stderr) / error under `--strict-keys`. Only the keys you PASS are checked, never the hub's existing stored blob, so older hubs carrying pre-allowlist keys are never flagged; `--strict-keys` validates pre-retrieve so a rejection fires no HTTP request.
- `navigation list/add/remove/reorder` (MIO-2633) — item-by-item editing of the `navigation` blob without rebuilding `--navigation-json`. Each mutating verb read-modify-writes: GET the hub, mutate ONE bucket (`header`/`footer`/`mobile`), re-validate the WHOLE menu (`validateNavigationBlob` + `validateNavigationHrefs` against the hub slug), then PATCH `navigation` as a whole-blob REPLACE (partial update, siblings untouched). Items have no stable id → addressed by zero-based INDEX (`list` prints it). `add <hub> <bucket>` takes `--item-json` (any bucket/type; inline or `@file`) OR the url convenience `--type url --href <h> --label <l>` (header/footer only — mobile items `{id,label,route,icon}` must use `--item-json`), plus `--position` to insert; `remove --index N`; `reorder --order 2,0,1` (a permutation — every index exactly once). Pre-fetch usage errors (bad bucket, missing/malformed flags) fire no HTTP; post-fetch validation errors (out-of-range index/position, bad permutation, hub-escaping href) fire the GET but no PATCH. NO optimistic-lock guard — last-write-wins, same as the `--navigation-json` replace
- `delete`   DELETE `/api/teams/{team_id}/hubs/{id}`
- `policies update` PATCH `/api/teams/{team_id}/hubs/{hub_id}/policies`  envelope `policies` {policy_type, content?, require_acceptance?}; hub id POSITIONAL
- `policies gate`   PATCH `/api/teams/{team_id}/hubs/{hub_id}/policies/gate`  envelope `hub_policy_gate` {enabled}; toggles settings.policies.enabled only (MIO-2020)
- `redirect-origins get` GET `/api/teams/{team_id}/hubs/{hub_id}/redirect-origins`  (owner-only magic-link allowlist)
- `redirect-origins set` PUT `/api/teams/{team_id}/hubs/{hub_id}/redirect-origins`  envelope `hub_redirect_origin_allowlists` {origins:[…]}; **full-replace**; `--origins` (comma-sep) or `--clear` (MIO-616)
- `scaffold` (MIO-2543, `cmd/hubs_scaffold.go`) — one idempotent command that builds a full-experience hub from an embedded template (`internal/hubtemplate`, `--template community`). Orchestrates the CLI's OWN request-builders + `internal/client` (never raw REST, never a command's cobra RunE) so template values pass the same validation as the individual commands. Ordered pipeline: (1) hub create (identity) / resume `--hub`; (2) blobs — branding+favicon+settings+registration+navigation via the shared `applyHubBlobs` RMW with strict-key validation, and hub-relative nav hrefs are scoped to `/{slug}` at apply time (`scopeNavHrefs`); (3) spaces — exhaustive skip-if-slug-exists then `community spaces create`; (4) onboarding — contact-attr def create (exhaustive skip-if-exists) + hub-config create to the COLLECTION path with `definition_id` in the body (MIO-2502), `is_in_onboarding`; (5) policies — `applyHubPolicies` per template policy; (6) playlists — O1 option-c gate (skip whole step if the hub already has published playlists) else create + `items add` + hub-playlists publish with `published_at` set unconditionally (sidesteps MIO-2536) + `visibility:public`; (7) homepage — resolve the vendored page-builder catalog template OFFLINE → `pages create` (slug `homepage`, `is_homepage:true` — `home` is a reserved slug) → `pages tree set` PUT with If-Match 0 (fresh) or the tree-get `draft_version` (resume); (8) publish — `--publish` only (default off) → PATCH `is_private:false`; (9) backend-gated welcome post (MIO-2262) + auto-admin (MIO-2540) skipped-with-note. Flags: `--template`(req) `--name`/`--slug` (create) | `--hub` (resume/target) `--dry-run` `--publish` `--favicon-url`/`--logo-url`/`--registration-enabled` (override). Output echoes hub id/slug + private/published state (host-relative URL — no domain field from the API).
- `templates` (MIO-2543) — lists the built-in scaffold templates via `hubtemplate.List()`; offline/no-creds; honors `-o json|table|plain`/`--jq`.
- `email-settings get/update` GET/PATCH `/api/teams/{team_id}/hubs/{hub_id}/email-settings`  envelope `hub_email_senders` {from_name?, reply_to?} (MIO-1229)
- NOTE: no admin policies READ — the only `/policies` GET is the hub portal route (member auth; rejects API keys), so `hubs policies get` is intentionally not shipped

## contacts  (`cmd/contacts.go`) — backend module contacts_admin
- `list`     GET `/api/teams/{team_id}/contacts`  (supports filters)
- `create`   POST `/api/teams/{team_id}/contacts`
- `retrieve` GET `/api/teams/{team_id}/contacts/{id}`
- `update`   PATCH `/api/teams/{team_id}/contacts/{id}`
- `delete`   DELETE `/api/teams/{team_id}/contacts/{id}`
- `restore`  POST `/api/teams/{team_id}/contacts/{id}/restore`
- **ID NAMESPACE TRAP (MIO-2504):** `{id}` on these routes is the TEAM-contact id
  (route param `{team_contact_id}`; surfaced as `.id`, also used by
  contact-attributes + tags). The GLOBAL contact id is a *separate* field,
  `.attributes.contact_id` (top-level `.contact_id` when flattened), and is what
  the `{contact_id}` routes below require: hub-memberships add/set-role/ban/
  unban/warn, activity contact, community members ban/unban/warn/soft-ban, email
  enrollments create, email enrollments list-by-contact, access-rules overrides
  create. Piping `.id` into those 404s a live contact — the CLI
  appends an actionable hint on exit-4 (`hintGlobalContactID`).

## contact-attributes  (`cmd/contactattributes.go`)
- defs:    `create/list/retrieve/update/delete` `/api/teams/{team_id}/contact-attributes[/{id}]`
- options: `create/list/update/delete` `/api/teams/{team_id}/contact-attributes/{def}/options[/{id}]`
- hub-config: `create/list/update/delete` `/api/teams/{team_id}/hubs/{hub_id}/contact-attributes[/{def}]`
- values:  `get/set` GET/PATCH `/api/teams/{team_id}/contacts/{tcid}/attributes`

## tags  (`cmd/tags.go`)
- `list`     GET `/api/teams/{team_id}/tags`
- `create`   POST `/api/teams/{team_id}/tags`
- `retrieve` GET `/api/teams/{team_id}/tags/{id}`
- `update`   PATCH `/api/teams/{team_id}/tags/{id}`
- `delete`   DELETE `/api/teams/{team_id}/tags/{id}`
- `assign`   POST `/api/teams/{team_id}/contacts/{tcid}/tags`
- `assign-bulk` POST `/api/teams/{team_id}/contacts/{tcid}/tags/bulk`
- `remove`   DELETE `/api/teams/{team_id}/contacts/{tcid}/tags/{tag_id}`

## segments  (`cmd/segments.go`)
- `create`   POST `/api/teams/{team_id}/segments`
- `list`     GET `/api/teams/{team_id}/segments`
- `retrieve` GET `/api/teams/{team_id}/segments/{id}`
- `update`   PATCH `/api/teams/{team_id}/segments/{id}`
- `delete`   DELETE `/api/teams/{team_id}/segments/{id}`
- `search`   POST `/api/teams/{team_id}/segments/search` (preview conditions)
- `members`  GET `/api/teams/{team_id}/segments/{id}/members`
- `count`    GET `/api/teams/{team_id}/segments/{id}/members/count`

## content  (`cmd/content.go`)
- `create`   POST `/api/teams/{team_id}/hubs/{hub_id}/content`
- `list`     GET `/api/teams/{team_id}/hubs/{hub_id}/content` (roots)
- `retrieve` GET `/api/teams/{team_id}/hubs/{hub_id}/content/{id}`
- `children` GET `/api/teams/{team_id}/hubs/{hub_id}/content/{id}/children`
- `update`   PATCH `/api/teams/{team_id}/hubs/{hub_id}/content/{id}`
- `delete`   DELETE `/api/teams/{team_id}/hubs/{hub_id}/content/{id}`
- `restore`  POST `/api/teams/{team_id}/hubs/{hub_id}/content/{id}/restore`
- `reorder`  POST `/api/teams/{team_id}/hubs/{hub_id}/content/reorder` — envelope `content_nodes`; body `{data:{type:content_nodes,attributes:{items:[{id,position}, …]}}}`. `--order` (comma-separated ids) becomes the `items` array with `position` = 0-based index. No `--parent-id`: `ReorderAttributes` is `extra="forbid"` (only `items`) and the backend derives each node's parent from item context (MIO-2500).

## pages  (`cmd/pages.go`)
- pages:    `create/list/retrieve/update/delete` `/api/teams/{team_id}/hubs/{hub_id}/pages[/{id}]`; `home` GET `…/pages/home`
  - create/update flags match PageCreate/PageUpdateAttributes: `--title` `--slug` `--type` `--privacy`(public|members|private) `--position` `--is-home`(→`is_homepage`) `--settings`/`--meta`(@file). No `--published`/`--description`/`--layout` (MIO-2257)
- publish: `publish` POST `…/pages/{id}/publish` (no body; `If-Match: <draft_version>` header, `--if-match` REQUIRED)
- tree: `get` GET `…/pages/{id}/tree?audience=author&resolve=true` (draft_version = OCC token);
  `set` PUT `…/pages/{id}/tree` — body `{data:{type:page_draft_trees,attributes:{tree}}}` (type via `pages/tree` typeOverride), `If-Match: <draft_version>` header from `--file` + `--if-match`.
  `--if-match` is OPTIONAL and defaults to `0` (unlike publish): omit it for the FIRST tree on a draft-less page — `tree get` 404s until a draft exists, and a fresh page sits at `draft_version 0`, which the backend's atomic OCC update (`WHERE draft_version = expected`) matches only while no draft has been written. A defaulted/stale `0` against a page that already has a draft → 409 `stale_draft`, never a clobber, so OCC stays intact for later sets (MIO-2518, MIO-2258)
- sections: `create` POST `…/pages/{pid}/sections`; `list` GET `…/pages/{pid}/sections`;
  `update` PATCH `…/pages/{pid}/sections/{sid}`; `delete` DELETE `…/pages/{pid}/sections/{sid}`;
  `reorder` PATCH `…/pages/{pid}/sections` — body is a bare `{data:[{id,position}]}` list (SectionReorderEnvelope), built from `--order` (MIO-2257)

## media files  (`cmd/media.go`, `cmd/media_upload.go`)
- files:    `list/retrieve/update/delete` `/api/teams/{team_id}/files[/{id}]`
  - update flags: `--title` `--description` `--visibility` `--folder-id`
- durable-url (derived) reads `durable_variants` from GET `…/files/{id}` and appends the REQUIRED `?hub_id={hub}` to each preset URL (safe to inline into a page-tree image node — non-expiring, unlike the imgproxy-signed `variants`). `--preset` filters; `--publish` POSTs a public `hub_media` row (visibility=public, published now) so the URL resolves for anon. Image-only. (MIO-2514; backend MIO-2525 / mio-backend #533)
- upload   POST `…/files` → presigned PUT (meta `upload_url`) → POST `…/files/{id}/finalize`; auto-multipart (init `…/files/multipart`, part `…/files/{id}/multipart/{upload_id}/parts/{n}`, complete `…/complete`) when large or `--multipart`; `--title` `--mime-type` `--folder-id` `--wait` `--timeout` `--part-size-mb`
- replace  single-part: POST `…/files/{id}/replace` → presigned PUT → POST `…/files/{id}/replace/{replacement_id}/finalize`. Multipart (`--multipart`/large): init `…/files/{id}/replace/multipart` → parts → POST `…/files/{id}/replace/{replacement_id}/multipart/{upload_id}/complete` — the complete is TERMINAL (relinks + returns the file; NO separate finalize — unlike upload-multipart, a finalize call 404s). `--mime-type` `--filename` `--multipart` `--part-size-mb`
- finalize POST `…/files/{id}/finalize`
- transcode POST `…/files/{id}/transcode`
- register-synthetic POST `/api/admin/teams/{team_id}/files/synthetic` (MIO-2285); `--title`(required) `--asset-kind`(document|pdf) `--visibility` `--mime-type` `--original-filename` `--description`
- cards    `get` GET / `set` PUT `…/files/{id}/cards` (type `file_cards`; `--cards` JSON array/@file)
- chapters `get` GET / `set` PUT `…/files/{id}/chapters` (type `file_chapters`; `--chapters` JSON array/@file)

## media folders  (`cmd/media.go`)
- folders: `list/create/retrieve/update/delete` `/api/teams/{team_id}/folders[/{id}]`
- move    POST `…/folders/{id}/move` (type `folders`; body `{new_parent_id: <id>|null}`; exactly one of `--parent-id`/`--to-root`)

## media search  (`cmd/media.go`)
- search  GET `/api/teams/{team_id}/search/media?q=…&hub_id=…&page[size]=…` (top-N pagination; `--query` required, `--hub-id`, `--limit` 1-100)

## media playlists  (`cmd/media.go`, `cmd/media_playlist_cover.go`)
- playlists: `list/create/retrieve/update/delete` `/api/teams/{team_id}/playlists[/{id}]`
  - create/update flags: `--title` `--description` `--visibility` `--hub-id`; update also `--podcast-feed-enabled`
- set-cover POST `/api/teams/{team_id}/playlist-cover-attachments` (type `attachments`; playlist id POSITIONAL; `--file-id` required — the CLI GETs `…/files/{file_id}`, resolves its `media_id`, and sends `media_id`+`target_type=playlist`+`role=thumbnail` [pinned by the backend], optional `--position`). Role IS `thumbnail`, not `cover`: the cover mechanism keys on `role='thumbnail'` (router 422 guard, `ck_attachment_role` CHECK, `uq_playlist_cover_attachment` partial index, `resolve_cover_url`). (MIO-2289, MIO-2519)

## media playlist items  (`cmd/media_playlist_items.go`) — MIO-2513
- add     POST   `/api/teams/{team_id}/playlists/{playlist_id}/items` (type `playlist_items`; body `file_id` [+ `position`]; negative `--position` → ExitUsage, no request)
- list    GET    `/api/teams/{team_id}/playlists/{playlist_id}/items` (cursor-paginated)
- remove  DELETE `/api/teams/{team_id}/playlists/{playlist_id}/items/{item_id}` (requires `--yes`)
- reorder PATCH  `/api/teams/{team_id}/playlists/{playlist_id}/items/{item_id}` (UpdateWithID → `data.id` in body; `position`)
  - Needs the `playlists/items` → `playlist_items` typeOverride + `items` knownCollections token (client type derivation).

## media hub-media / hub-playlists  (`cmd/media.go`) — publish to a hub
- hub-media:     `publish` POST / `list` GET `/api/teams/{team_id}/hubs/{hub_id}/media`; `unpublish` DELETE `…/media/{file_id}` (type `hub_media`; publish `--file-id`, `--visibility` `--published-at` `--position`)
- hub-playlists: `publish` POST / `list` GET `/api/teams/{team_id}/hubs/{hub_id}/playlists`; `unpublish` DELETE `…/playlists/{playlist_id}` (type `hub_media` via typeOverride; publish `--playlist-id`, `--visibility` `--published-at` `--position`)

## media attachments  (`cmd/media_attachments.go`)
- list   GET    `/api/teams/{team_id}/attachments` (`--media-id` `--target-type` `--target-id`; cursor-paginated)
- show   GET    `/api/teams/{team_id}/attachments/{id}`
- update PATCH  `/api/teams/{team_id}/attachments/{id}` (UpdateWithID → `data.id`; `--role` `--position`)
- delete DELETE `/api/teams/{team_id}/attachments/{id}` (requires `--yes`)

## media transcripts  (`cmd/media_transcripts.go`)
- get      GET  `/api/teams/{team_id}/media/{media_id}/transcript`
- vtt      GET  `…/transcript.vtt`
- content  GET  `…/transcript/content`
- versions GET  `…/transcript/versions[/{version}]`
- edit     PATCH `…/transcript` (type `transcripts`; `--words` JSON array/@file, `--language`)
- revert   POST `…/transcript/revert` (type `transcripts`; `--version` required, >= 1)

## products  (`cmd/products.go`) — REFERENCE RESOURCE
- products:     `create/list/retrieve/update/delete` `/api/teams/{team_id}/products[/{id}]`
- prices:       `create/list/retrieve/update/delete` `/api/teams/{team_id}/products/{id}/prices[/{pid}]`
- deliverables: `create/list/delete` `/api/teams/{team_id}/products/{id}/deliverables[/{did}]`
  (CLI `mio products deliverables`; type `product_deliverables`; `--type` enum: hub_access, content_enrollment, tag, file_download, community_access) (MIO-2268)
- hub-products: `attach/list/update/detach` `/api/teams/{team_id}/hubs/{hid}/products[/{did}]`
  (CLI `mio checkout hub-products`; type `hub_product_displays`; attach takes a PRODUCT id, update/detach take a display id) (MIO-2268)
- hub-prices:   `list/update` `/api/teams/{team_id}/hubs/{hid}/prices[/{did}]`
  (CLI `mio checkout hub-prices`; type `hub_price_displays`; no create/delete — auto-managed on product attach/detach) (MIO-2268)
- coupons:      `create/list/retrieve/update/delete` `/api/teams/{team_id}/coupons[/{id}]`
- coupon-products: `attach/list/detach` `/api/teams/{team_id}/coupons/{id}/products[/{pid}]`
  (CLI `mio coupons products`; type `coupon_products`; empty scope = coupon applies to every product) (MIO-2268)

## checkout  (`cmd/checkout.go`) — admin reads + actions, team-scoped
- orders:        `list/retrieve` `/api/teams/{team_id}/hubs/{hub_id}/orders[/{id}]`
- subscriptions: `list/retrieve` `…/subscriptions[/{id}]`; `cancel` POST `…/subscriptions/{id}/cancel`
- payments:      `list/retrieve` `…/payments[/{id}]`; `refund` POST `…/payments/{id}/refund` (refunds envelope; `--reason` required, `--amount` optional)
- webhooks:      `list/retrieve` `…/payment-webhooks[/{id}]`; `replay` POST `…/payment-webhooks/{id}/replay`
- accounts:      `list/retrieve` `/api/teams/{team_id}/payment-accounts[/{id}]`;
                 `onboarding-link` POST `…/payment-accounts/onboarding-link`
                 (envelope `onboarding_links`; requires `--hub-id`/`--return-url`/`--refresh-url`)
- stripe-sync:   `import` POST `/api/teams/{team_id}/checkout/sync/import-from-stripe`;
                 `import-status` GET `…/checkout/sync/import-runs/{run_id}`;
                 `adopt-product` POST `/api/teams/{team_id}/products/adopt-from-stripe`

## email  (`cmd/email.go`) — base `/v1/hubs/{hub_id}/…`
- drip-campaigns: `create/list/retrieve/update/delete`; `activate`/`pause` POST `…/{id}/activate|pause`
- steps:          `list/create/update/delete` `…/drip-campaigns/{id}/steps[/{sid}]`
- templates:      `create/list/retrieve/update/delete` `…/email-templates[/{id}]`; `preview` POST `…/email-templates/{id}/preview` (no body)
- config:         `set` PUT `…/email-config` (JSON:API envelope, `mail_*` attributes); `get` GET; `delete` DELETE; `test` POST `…/email-config/test` (no body — mails the authenticated user)
- enrollments:    `list` GET `…/drip-campaigns/{id}/enrollments`; `exit` DELETE `…/{id}/enrollments/{eid}`
- stats:          `get` GET `…/email-stats`
- suppressions:   `list` GET `…/email-suppressions`; `create` POST `…/email-suppressions` (envelope `email_suppressions` {email_address}; reason forced `admin_block`); `lift` DELETE `…/email-suppressions/{id}` (destructive). NOTE: hub routes require platform_admin (API keys → 403)

## access-rules  (`cmd/accessrules.go`) — base `/api/teams/{team_id}/hubs/{hub_id}/…`
- rules:     `create/list/retrieve/update/delete` `…/access-rules[/{id}]`
- overrides: `create/list/retrieve/update/delete` `…/access-overrides[/{id}]`

## activity  (`cmd/activity.go`) — admin reads
- `contact`      GET `/api/teams/{team_id}/hubs/{hub_id}/activity/contacts/{contact_id}`
- `top-engaged`  GET `/api/teams/{team_id}/hubs/{hub_id}/activity/top-engaged`

## automations  (`cmd/automations.go`) — hub-scoped
- `create`      POST   `/api/teams/{team_id}/hubs/{hub_id}/automations`  body: envelope `automations` {name, definition?, re_entry_mode?, settings?}
- `list`        GET    `/api/teams/{team_id}/hubs/{hub_id}/automations`  query: page[size], page[after], filter[status]
- `retrieve`    GET    `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}`
- `update`      PATCH  `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}`  partial: envelope `automations`
- `publish`     POST   `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}/publish`  (no body → 201 version snapshot)
- `activate`    POST   `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}/activate`  (no body)
- `deactivate`  POST   `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}/deactivate`  (no body)
- `versions`    GET    `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}/versions`
- `enrollments` GET    `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}/enrollments`  query: filter[status] (e.g. stuck)
- `enroll`      POST   `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}/enrollments`  body: envelope `automation_enrollments` {team_contact_id}
- `fire-event`  POST   `/api/teams/{team_id}/hubs/{hub_id}/automations/events`  body: **flat** {event_type, team_contact_id, idempotency_key?, payload?}
- `test`        POST   `/api/teams/{team_id}/hubs/{hub_id}/automations/{id}/test`  body: **flat** {team_contact_id} (dry-run, no side effects)

## webhook-endpoints  (`cmd/webhookendpoints.go`) — hub-scoped
- `create` POST   `/api/teams/{team_id}/hubs/{hub_id}/webhook-endpoints`  body: envelope `webhook_endpoints` {name, target_url, signing_secret?}
- `list`   GET    `/api/teams/{team_id}/hubs/{hub_id}/webhook-endpoints`
- `delete` DELETE `/api/teams/{team_id}/hubs/{hub_id}/webhook-endpoints/{id}`  → 204

---

## Out of scope (v1)
contact_auth, hub_memberships, comments (contact routes), realtime SSE, all
portal `/me` and `/api/hub/…` routes, webhooks, JWKS, magic-link landing,
community (stub, no routes yet).
