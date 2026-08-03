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
  `--hub-id`/`--return-url`/`--refresh-url` required — `hub-id` must be the
  hub's canonical UUID, slugs are NOT resolved for this endpoint). **This
  command is web/JWT-only (MIO-2655) and always fails fast client-side with
  `ExitAuth` from this API-key-only CLI — see `cmd/checkout.go`.**
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
- **hub-id resolution (MIO-2732)** — every verb below takes the hub id as an OPTIONAL positional and resolves it through `cmdContext.hubTargetID`: positional → `--hub` → config `current_hub` → single-hub auto-default (`requireHub`). The positional is passed through VERBATIM (no name/slug resolution, no API call); only the fallback path resolves names, because `requireHub` already does. **"Omitted" and "supplied but blank" never collapse**: a SUPPLIED positional is always authoritative, so `mio hubs update "$HUB_ID" …` with an empty `$HUB_ID` is an ExitUsage error that fires no request — it must never fall through and silently write to the ambient hub (Codex review, round 1). Only a genuinely absent positional consults the context. When nothing resolves, `requireHub` returns the `errNoHubInContext` sentinel and `hubTargetID` widens it to an ExitUsage error naming all three sources — never Cobra's `accepts 1 arg(s), received 0`, which described an arg count for a context problem and (next to a flattened `errors` envelope) read as an empty/failed record. **`delete` deliberately opts out**: it keeps a custom `Args` func requiring the positional, because taking an irreversible whole-hub delete's target from ambient context is a foot-gun; its refusal message says so. On the `navigation` verbs the hub id shares the positional slot with the bucket — `splitNavArgs` disambiguates by value (a lone `header|footer|mobile` is the bucket), which is lossless because a bucket name could never have addressed a hub under the verbatim-passthrough rule.
- `create`   POST `/api/teams/{team_id}/hubs`
  - presentation-blob flags (create-only): `--branding-json` `--navigation-json` `--settings-json` `--meta-json` (opaque JSONB objects; inline JSON or `@file`); `--logo-url` merges into `branding` (MIO-2254); `--favicon-url` merges into `branding.favicon_url` (MIO-2522)
  - discoverability (MIO-2521): output includes a derived `published` (= `!is_private`); in table (human) mode a private hub also prints a stderr hint with the slug + how to publish (`mio hubs update <id> --published`). No public URL is echoed — the create response carries no domain/url field and the CLI knows only the API base, not the hub-frontend host, so a URL is not derivable client-side (surfaced honestly, not fabricated)
  - `--navigation-json` header/footer `type:"url"` items with a hub-relative `href` (leading `/`) must stay within the hub — start with `/{--slug}` — else ExitUsage; absolute `http(s)://` hrefs pass as-is (MIO-2270)
  - `settings.policies` on UPDATE is a SILENT NO-OP server-side (MIO-2811): `HubService.update` does `incoming.pop("policies", None)  # client can never write policies here`, while `create` accepts `policies.enabled`/`policies.show`. `policies` is a legitimate allowlisted settings key, so the MIO-2515 key check cannot catch it — it is a known key on the wrong verb. `hubs update` now warns on stderr (usage error under `--strict-keys`, before any HTTP). Deliberately NOT re-routed: the conduit rule cuts against the CLI sending a settings key to a different endpoint, and only `enabled` has another door (`policies gate`) — `show` has none, so a re-route would fix one key and leave the other just as inert. Checked against the `--settings-json` FLAG, not inside `applyHubBlobs`, because `hubs scaffold` routes the community template (whose settings carry `policies`) through the same applier and must not warn about a template internal on every run
  - blob-key validation (best-effort, MIO-2515): unknown keys in `--branding-json`/`--settings-json`/`--meta-json` **warn** on stderr by default and **error** (ExitUsage, no request) under `--strict-keys`. The API stores these blobs verbatim (opaque JSONB, no server schema) so a typo silently has no effect — **with one documented exception: EVERY branding key whose name ends in `_url` is validated server-side** (`validate_branding`, `app/hubs/validation.py`) — the rule is applied by SUFFIX, case-insensitively, to present and future keys alike, so on the CLI's allowlist that is `logo_url`, `favicon_url`, `social_image_url`, `custom_login_logo_url` and `custom_font_url`, not just the two with dedicated flags (MIO-2658). Each must be `null` or a `str` (a `list`/`dict` raises rather than being re-stringified by a client into the attack it was meant to block), and a `str` must be an absolute `https://` URL with a valid hostname: rejected are any other scheme (**including plain `http://`**, `data:` and `javascript:`), protocol-relative `//host`, relative paths, whitespace or control characters (checked raw AND once percent-decoded), backslashes, embedded `user:pass@` credentials, percent-encoding anywhere in the host, and an invalid port. So a `data:` SVG logo is a 422 rather than a silent no-op, and the `*_url` keys are the only branding writes that can fail on their VALUE — note this reaches `--social-image-url` on `hubs scaffold` too; the CLI curates a known-key allowlist from the demo-hub seeder + backend reads (the hub frontend is the authoritative render contract, so the list is not exhaustive). Accepted top-level keys — branding: `logo_url,favicon_url,social_image_url,custom_login_logo_url,custom_font_url,primary,secondary,background,text,primary_color,secondary_color,background_color,header_color,header_accent,dark_mode,gradient,font_heading,font_body,heading_font_size,body_font_size,labels`; settings: `customCss,menu,header,footer,background,appearance,policies,registration,email,auth`; meta: `memberDirectory,discussions,moderation`. Deep-validated stable settings sections: `policies{enabled,show,tos,privacy_policy}`, `registration{enabled}`, `email{from_name,reply_to}`, `auth{allowed_redirect_origins}` (branding/meta/other settings sections are FE-owned → top-level only). `navigation-json` is validated separately (typed items) and is unaffected.
- `list`     GET `/api/teams/{team_id}/hubs`
- `retrieve` GET `/api/teams/{team_id}/hubs/{id}`
  - output includes derived convenience booleans (MIO-2516/2521): `registration_enabled` (= `settings.registration.enabled === true`, fail-closed) and `published` (= `!is_private`). Derived for readability; never sent on a write; `--raw` bypasses them
- `update`   PATCH `/api/teams/{team_id}/hubs/{id}`
  - `--navigation-json` authors the header/footer menu (typed items; inline JSON or `@file`); whole-blob replace, validated client-side (untyped items rejected) (MIO-2255). Hub-relative `type:"url"` hrefs (leading `/`) must start with `/{hub.slug}` — the update retrieves the hub for its slug — else ExitUsage; absolute `http(s)://` hrefs pass as-is (MIO-2270)
  - `--branding-json` / `--settings-json` / `--meta-json` deep-merge (read-modify-write: retrieve → merge → PATCH, so sibling keys survive); `--logo-url` merges into branding (MIO-2256, unblocks MIO-901); `--favicon-url` merges into `branding.favicon_url` (MIO-2522)
  - `--registration-enabled true|false` sets `settings.registration.enabled` via RMW, preserving sibling settings/registration keys; gated on `Changed()` so `--registration-enabled=false` is distinguishable from unset (MIO-2516)
  - `--unset <dotted.path>` DELETES a key from a blob (the only real delete — the `*-json` flags are merge-only: a literal `null` persists as null, `{}` is a no-op). First segment selects the blob (`branding`/`settings`/`meta`); nested paths supported (e.g. `settings.registration.enabled`). Repeatable and comma-separated. Blank/bare-blob/unknown-blob paths are ExitUsage and fire no HTTP request. Deterministic apply ORDER on each blob: (1) `--*-json` deep-merge, (2) scalar overrides (`--logo-url`/`--favicon-url`/`--registration-enabled`), (3) `--unset` removals LAST — an unset in the same command wins over a merge (MIO-2517). NOTE: some settings sub-trees are stripped/managed elsewhere (`settings.policies.tos`/`privacy_policy` via `hubs policies update`; `settings.auth.allowed_redirect_origins` via `hubs redirect-origins set`), so unsetting those through the settings PATCH may be a no-op
  - blob-key validation (MIO-2515): same allowlist as create — unknown keys warn (stderr) / error under `--strict-keys`. Only the keys you PASS are checked, never the hub's existing stored blob, so older hubs carrying pre-allowlist keys are never flagged; `--strict-keys` validates pre-retrieve so a rejection fires no HTTP request.
- `navigation list/add/remove/reorder` (MIO-2633) — item-by-item editing of the `navigation` blob without rebuilding `--navigation-json`. Each mutating verb read-modify-writes: GET the hub, mutate ONE bucket (`header`/`footer`/`mobile`), re-validate the WHOLE menu (`validateNavigationBlob` + `validateNavigationHrefs` against the hub slug), then PATCH `navigation` as a whole-blob REPLACE (partial update, siblings untouched). Items have no stable id → addressed by zero-based INDEX (`list` prints it). `add [hub] <bucket>` takes `--item-json` (any bucket/type; inline or `@file`) OR the url convenience `--type url --href <h> --label <l>` (header/footer only — mobile items `{id,label,route,icon}` must use `--item-json`), plus `--position` to insert; `remove --index N`; `reorder --order 2,0,1` (a permutation — every index exactly once). Pre-fetch usage errors (bad bucket, missing/malformed flags) fire no HTTP; post-fetch validation errors (out-of-range index/position, bad permutation, hub-escaping href) fire the GET but no PATCH. NO optimistic-lock guard — last-write-wins, same as the `--navigation-json` replace
  - **`icon` values are NOT validated (MIO-2675, conduit rule)** and the two bucket families use DIFFERENT vocabularies, so a wrong name is a silent frontend drop with a 200 on the wire. `header`/`footer` icons are matched against the hub frontend's generated sprite id list (`ICON_NAMES`, mio-hub `src/components/ui/icon.tsx`, ~205 ids alongside `public/icons/sprite.svg`) and are OPTIONAL — an unmatched name drops the icon and keeps the item; `info` and `globe` are absent from the sprite (hence the reported blank glyphs) — `information`/`information-circle` and `earth`/`global` are the working substitutes. `mobile` icons are matched against an 8-entry frontend component registry (`src/lib/hub-shape/icon-registry.ts`) and are MANDATORY — `parseMobileItem` drops the whole tab when the icon is missing or off-list. The eight, casing significant: `Home` `Bell` `User` `Users` `MessageSquare` `MessageCircle` `Search` `content`. The frontend then normalizes: fewer than 3 surviving tabs → the default tab set replaces the list entirely; more than 5 → truncated to 5. Documenting rather than validating is deliberate — the sprite is generated in another repo and would rot in a CLI-side allowlist
- `delete`   DELETE `/api/teams/{team_id}/hubs/{id}`
- `policies get`    GET `/api/teams/{team_id}/hubs/{hub_id}/policies`  (MIO-2815) — the team-owner ADMIN read (`admin_get_hub_policies`, MIO-2394). Same path as the PATCH below, so the METHOD is the only discriminator. Returns a LIST of `policies` resources {policy_type, content, version, enabled, require_acceptance} — ALWAYS exactly two (`_project_policy_documents` loops `for policy_type in ("tos", "privacy_policy")` unconditionally; the empty-list early return is in the PORTAL read, not this one — backend test `test_absent_settings_returns_two_disabled_defaults`). `enabled` is the ONE hub-level gate repeated on every item. **`version` is NOT a custom-vs-default discriminator** — `policy["version"]` is assigned only under `if policy_type == "tos" and require_acceptance:` and an absent version projects as `default-v1` (backend test `test_no_reprompt_keeps_version_absent`), so custom text routinely reads `default-v1`; `content` falls back to the rendered default so presence proves nothing; `require_acceptance` is tri-state (null = unknown, NOT false — `_project_require_acceptance`). Compare the CONTENT. Not interchangeable with the member-portal read, which serves defaults and forces `enabled=true`. Capture with `-o plain --jq '.[0].enabled'` (a LIST, so bare `.a bare `--jq .enabled` EXITS 1 with "expected an object but got: array" (gojq member access on a list), it does NOT yield null.policies.enabled only (MIO-2020). The ONLY writer of that flag: the generic hub PATCH pops `policies` out of an incoming settings blob, so `hubs update --settings-json '{"policies":{"enabled":true}}'` is a silent no-op (hub CREATE is the exception — it accepts `policies.enabled`/`show` and strips only the `tos`/`privacy_policy` document objects). Content and gate are SEPARATE writes: a policy written without the gate is never presented — `_tos_acceptance_required` returns False when the gate is off, and `POST .../tos/accept` answers the enumeration-safe 404. `hubs scaffold` fires this write itself when its template declares `enabled: true` — ENABLE-ONLY, so a template declaring `false` (or nothing) leaves the hub's gate standing; see the `scaffold` entry below (MIO-2567)
- `redirect-origins get` GET `/api/teams/{team_id}/hubs/{hub_id}/redirect-origins`  (owner-only magic-link allowlist)
- `redirect-origins set` PUT `/api/teams/{team_id}/hubs/{hub_id}/redirect-origins`  envelope `hub_redirect_origin_allowlists` {origins:[…]}; **full-replace**; `--origins` (comma-sep) or `--clear` (MIO-616)
- `scaffold` (MIO-2543 + MIO-2672, `cmd/hubs_scaffold.go`) — one idempotent command that builds a full-experience hub from a hub template in the TARGET BACKEND's live page-builder catalog (`GET /api/page-builder/catalog`, digest-verified, origin-scoped on-disk cache) — the CLI embeds NO hub templates. `--catalog <file>` is the only escape hatch (mutating command: it fails closed on a digest mismatch; there is NO `--offline` and no stale-cache/vendored fallback); a catalog with no `hubTemplates[]` (pre-2.1 artifact, MIO-2666/W2a pin) is a clear ExitUsage error. Orchestrates the CLI's OWN request-builders + `internal/client` (never raw REST, never a command's cobra RunE) so template values pass the same validation as the individual commands. WRITE-FREE preflight before anything is created (`hubs_scaffold_preflight.go`): the 255-code-point `--name` bound, hub-template existence + invariants against the resolved catalog, page-plan instantiation, and a preliminary `{{hub_name}}`/`{{hub_slug}}` interpolation pass over the whole plan (token contract §4.3: closed two-token vocabulary, unknown/dangling tokens rejected; post-substitution caps — leaf values ≤5000 cp, page titles ≤200, nav labels ≤80). Ordered pipeline: (1) hub create (identity) / resume `--hub`; (2) blobs — branding+favicon+settings+registration+navigation via the shared `applyHubBlobs` RMW with strict-key validation; nav labels interpolated with the hub's ACTUAL title/slug, then hub-relative hrefs scoped to `/{slug}` (`scopeNavHrefs`); (3) spaces — exhaustive skip-if-slug-exists then `community spaces create`; (4) onboarding — contact-attr def create (exhaustive skip-if-exists) + hub-config create to the COLLECTION path with `definition_id` in the body (MIO-2502), `is_in_onboarding`; (5) policies — `applyHubPolicies` per template policy, then the hub-level ENFORCEMENT GATE via the shared `applyHubPolicyGate` (`PATCH .../policies/gate`, the same write `hubs policies gate` performs) when the template declares `enabled` (MIO-2567). Enforcement is ONE flag per hub (`settings.policies.enabled`, read by identity in `_policies_enabled`, written only by `update_policy_gate`; `update_policy` stores content + version and NO per-policy enabled), so the template's PER-POLICY `enabled` values are COLLAPSED onto it. A `true` beside a `false` is ExitUsage naming both keys (the collapse is lossy and no winner is inferable — silently OR-ing would enforce a policy the author declared off), raised from the WRITE-FREE PREFLIGHT: `resolveTemplatePolicies` runs in `rebuildScaffoldPlan` beside `HubTemplate.Validate`, so a contradictory template fails before the hub exists rather than at pipeline stage 5 of 9 with blobs/spaces/onboarding already written and no rollback. The write is ENABLE-ONLY: unanimous `true` enables; NO declaration, or a resolved `false`, writes nothing and leaves the hub's gate standing. That matches the ratified applier contract (mio-page-catalog `catalog.schema.json`: the applier "MUST call PATCH .../policies/gate when enabled is true", silent on false) and keeps resume safe — acting on a `false` would mean every re-run DISABLES enforcement an operator turned on by hand, the mirror of the reason an undeclared gate is left alone. Both skip cases are narrated on stderr with the reason (they are different situations), and `hubs policies gate --enabled=false` is the verb that disables. Gate LAST, after the content writes, so members are never briefly asked to accept the default document the template is about to replace. `settings.policies.*` carried in a template's settings blob is NOT a gate source — stepBlobs writes settings through the GENERIC hub PATCH, which pops `policies` wholesale, so it is inert; re-routing a settings key to a different endpoint would be the CLI second-guessing the API, and would repair `enabled` while leaving `show`/`tos`/`privacy_policy` just as inert. The whole policies block is parsed + validated in preflight (unknown key, unknown field, wrong-typed value, contradictory `enabled`), so `--dry-run` and a real run reject an identical set of templates. RESUME CAVEAT (MIO-2567 review): `applyHubPolicies` ALWAYS sends `content`, so a template that omits it — the shipped `community` one does — RESETS that policy to the backend default on every apply, and for `tos` with `require_acceptance` `update_policy` then normalizes the version to `default-v1`, RE-PROMPTING every member who had already accepted. The reset predates MIO-2567; what MIO-2567 changed is that the gate is now on, so the re-prompt is MEMBER-VISIBLE instead of inert. `hubs scaffold`'s resume is therefore safe for pages and idempotent for spaces/onboarding/playlists, but NOT content-preserving for policies — re-apply hand-edited legal text with `hubs policies update` after a resume. A skip-if-custom-version resume (using the admin policies GET below) is the obvious follow-up and is deliberately NOT in this change; (6) playlists — O1 option-c gate (skip whole step if the hub already has published playlists) else create + `items add` + hub-playlists publish with `published_at` set unconditionally (sidesteps MIO-2536) + `visibility:public`; (7) pages — PROBE the W2b backend op `POST …/hubs/{hub_id}/pages/scaffold-from-template` (the probe IS the real POST; the op ships dormant server-side); on 404/405 (op absent, or the path shadowed by a GET route on older backends) fall back to a client-side apply of EVERY template `pages[]` entry: `pages create` carrying a `meta.template_provenance` marker (`pending`) → tree PUT (If-Match 0) → publish (If-Match = the PUT's `draft_version`) → marker PATCH (`applied` + tree digest + draft version), titles/trees interpolated with the hub's actual title/slug; on an op 409/422/400 the catalog is re-resolved ONCE and the op retried once only if the pin digest actually moved; a `--catalog` override skips the probe (an override can never match the backend pin, so it is inherently client-side). Re-runs follow the §5.1 per-boundary recovery: our marker + `pending` + no draft → resume onto the existing page; our marker + `applied` + untouched `draft_version` → no-op; anything else (foreign page, edited draft, unknown state) → conflict (ExitUsage, exit 2) — NEVER overwritten; a pre-existing homepage always blocks the homepage create (the create would clear it server-side); (8) publish — `--publish` only (default off) → PATCH `is_private:false`; (9) welcome-post (MIO-2558; the step formerly named `backend-gated`) — a DECLARATIVE step that POSTs the hub template's `welcomePost` block ({space: a slug from this template's own spaces[], title, body?, is_published?}) to the MIO-2262 admin create-discussion route via the same path shape as `community discussions create`. No shipped catalog declares a `welcomePost` (community at 0.14.1 does not), so today it is a plan-visible no-op — the CLI holds no templates and must not invent post copy. **The key is not ratified catalog vocabulary — MIO-2812**: `welcomePost` is absent from mio-page-catalog's `catalog.schema.json` `$defs/hubTemplate` and from every applier in that repo, so it parses only because that object is `additionalProperties:true` — the CLI defined this vocabulary unilaterally and the schema is not in a position to disagree. Both failure modes are SILENT: if the ratified spec lands with a different name or shape (`welcome_post`, nested under `spaces[]`, `body` renamed) the parser sees no key, the step takes its no-declaration branch, and the post simply never appears with nothing erroring or warning; and `additionalProperties:true` cannot catch a TYPO either, so a template author writing `welcomepost` gets neither a validation error nor a post. The only mitigation on this side is that the no-declaration branch records a plan-VISIBLE entry rather than skipping silently. MIO-2812 also asks the catalog owners whether `title`/`body` join the §4.3 interpolation set; the CLI's position (below) is recorded there, and a yes is a follow-up on this repo, not a defect in the step. Preflight validates the block write-free against the ENDPOINT's own reject conditions — non-blank, NUL-free, and ≤280 code points measured on the STRIPPED title for the title, NUL-free body, space resolving to one of `spaces[]` — because a 422 at step 9 fails a run that has already written everything else; that mirroring is not a conduit-rule breach, which governs the user's `--title` flag on `community discussions create`, not a template constant. The cap is measured on the STRIPPED title, deliberately diverging from `discussion_text.py`, which measures the RAW one: the backend measures what it RECEIVES, preflight validates what the scaffold will SEND (the step posts the trimmed title), so a 285-raw / 275-stripped title must pass — measuring raw would be stricter than the endpoint about padding the request never carries. It is a CODE-POINT count, never bytes: Python's `len()` and `Field(max_length=280)` both count code points, so a byte-based check would reject titles the API accepts. `is_published` moves off the endpoint's default TRUE only for a real JSON bool: `null`/`"true"` are treated as absent, since coercing them yields `false` and scaffolds the invisible draft the default exists to prevent. IDEMPOTENCY is a TITLE match within the target space (that endpoint has no upsert and its request schema is extra="forbid" with no `meta`, so the pages step's provenance marker is unavailable): found ⇒ adopt that id and POST nothing, so a resume never creates a second welcome post. The title is compared — and POSTED — **stripped**, because `discussion_text.py::normalize_discussion_title` returns `title.strip()` as the stored form: matching the template's raw string against the stored one means a padded title never matches its own earlier post and every resume duplicates. The scan reads the admin list **unfiltered** and matches `space_id` client-side rather than passing `filter[space_id]`, which looks more expensive and is the correct call — the filtered branch runs `list_for_space`, which appends `Discussion.is_removed.is_(False)` UNCONDITIONALLY (`repositories/discussions.py:534`), so a moderation-removed welcome post is invisible to it and the resume duplicates; `list_for_hub` filters only `deleted_at` and the router passes `include_deleted=True`, making it the one view carrying drafts, soft-deleted AND removed rows. A soft-deleted or removed match is still adopted (never resurrect what the operator deleted), and the run reports WHICH outcome it got via `welcome_post_status` — `created` / `adopted` / `adopted_deleted`. Note `adopted` therefore carries a REMOVED match too, indistinguishably — the agent-facing docs (`AGENTS.md`, `llms.txt`) say so explicitly, since an agent reading only the three status values would otherwise conclude `adopted` implies visible. There is deliberately no `adopted_removed`: `_discussion_to_resource` serializes `deleted_at` but never `is_removed`, and its computed `status` is only published/scheduled/draft, so removal is not observable over this endpoint — the unfiltered scan still SEES the row (which is what stops the duplicate), it just cannot label it. The space SLUG resolves to the hub's real space id from what stepSpaces created, falling back to an exhaustive spaces listing on a resume. That listing walk is `existingSpacesBySlug` (slug→id, shared with step 3); the title lookup uses this endpoint's OWN cursor envelope — a bare `meta.next_cursor`/`meta.has_more`, verified against `discussions_admin.py::_list_response`, NOT the `meta.page.*` shape `nextPageCursor` reads for spaces/defs. Title/body are LITERAL: {{hub_name}}/{{hub_slug}} interpolation has an exhaustively-specified location set (MIO-2573 §4.3 — leaf values, page titles, nav labels, "nothing else") shared with the TS/Python appliers, and widening it is a catalog-spec change. The step's OTHER former deferral, auto-admin (MIO-2540, backend 6565362d), is server-side only — the backend assigns the creator as owner-admin on hub create — so nothing is wired and nothing is noted. Flags: `--template`(req) `--name`/`--slug` (create) | `--hub` (resume/target) `--catalog <file>` `--dry-run` `--publish` `--favicon-url`/`--logo-url`/`--registration-enabled` (override) `--primary-color`/`--secondary-color`/`--text-color`/`--background-color`/`--header-color`/`--header-accent`/`--social-image-url`/`--branding-json` (branding override layer, MIO-2604). BRANDING OVERRIDES (MIO-2604, `cmd/hubs_scaffold_branding.go`): three layers, lowest first — the hub template's `branding` block, then `--branding-json` deep-merged over it, then the scalar flags written over that; the result is handed to `applyHubBlobs` as the branding patch, which deep-merges it onto the hub's CURRENT branding and applies `--logo-url`/`--favicon-url` last (disjoint keys). So the flags MERGE, never replace: a template branding key no flag names survives. Flag NAME ≠ blob KEY: `--primary-color`→`primary`, `--secondary-color`→`secondary`, `--text-color`→`text`, `--background-color`→`background` (the FE Epic 2 SHORT form the catalog's `community` template actually sets — writing the legacy `primary_color`/`secondary_color`/`background_color` would land beside the template's value instead of overriding it), `--header-color`→`header_color`, `--header-accent`→`header_accent`, `--social-image-url`→`social_image_url`. CASCADE: `--primary-color` also fills `header_color` when the OPERATOR gave no header color (`--header-color`, or a `header_color` key in `--branding-json`); the TEMPLATE's own `header_color` does not suppress it — `community` sets `header_color` == `primary` (#4F46E5), so yielding to it would mean the cascade never fires and a recolored hub keeps the template's header. Escape hatch: pass `--header-color` explicitly. VALIDATION: color values are passed through UNVALIDATED (conduit rule — branding is opaque JSONB server-side, `dict[str, Any] | None`, so there is no format contract to mirror, and the sibling `--logo-url`/`--favicon-url` ship no scheme check either). CORRECTION (MIO-2576, verified against mio-hub `src/lib/hub-shape/branding.ts`): the earlier justification here claimed the frontend "accepts named colors/`rgb()`/gradients" — it does NOT. `parseBranding` tests every color key (`primary`, `secondary`, `background`, `text`, `header_color`, `header_accent`) against `/^#[0-9a-fA-F]{6}$/` and silently substitutes its own default on anything else. The conduit conclusion is unchanged — that is the FRONTEND's render contract, not the API's, and mirroring another repo's regex client-side would reject values the API accepts and pin the CLI to a contract it does not own — but the docs must say "pass 6-digit hex" rather than promise a permissiveness the renderer does not have. What IS checked is the CLI's own interface: `--branding-json` is parsed + key-validated PRE-AUTH in STRICT mode against the MIO-2515 `brandingKeys` allowlist (no second validator — same `parseJSONObjectFlag` + `validateBlobKeys` as `hubs update`), so malformed JSON / a non-object / a misspelled key exits ExitUsage with NO HTTP, in `--dry-run` too; every scalar flag's key is on that allowlist by construction (test-pinned). STRICT-KEY MESSAGE: the shared rejection text (`strictKeyDropHint`, `cmd/hubs_blob_keys.go`) ends "drop `--strict-keys`" and opens by naming `--<blob>-json` — both right on `hubs create`/`hubs update`, both dead ends on `hubs scaffold`, which has neither `--strict-keys` nor `--settings-json`/`--meta-json`. `scaffoldStrictKeyErr` swaps that tail for the hint matching the key's ORIGIN: `scaffoldFlagStrictKeyHint` for the operator's own `--branding-json`, `scaffoldTemplateStrictKeyHint` (fix the template, or `--catalog <file>` with a corrected copy) for a template blob key rejected by `stepBlobs`. The swap is a strict no-op for every other error — the blobs PATCH's own failures keep their text AND their exit code (`errs.CodeOf`, never a hard-coded ExitUsage) — and is anchored on the shared const, so a reworded hint stops matching instead of emitting stale advice. `hubs create`/`hubs update` messages are unchanged. The dry-run `blobs` plan entry appends `[branding overrides: …]` (sorted, cascade annotated), the prose summary gains a `Branding overrides:` line only when there are some (the override-free golden is unchanged), and the resume command echoes the flags the operator passed (cascaded keys omitted — the resume re-derives them). Output is FORMAT-DRIVEN like every other command (MIO-2574): `-o table` (the TTY default) prints the human summary — hub id/slug + private/published state + shape recap, host-relative URL since the API returns no domain field; `-o json`/`-o plain` (json is the default off a TTY, per the AGENTS.md piped contract) render a machine-readable result through the shared output layer — `hub_id`, `hub_slug`, `hub_name`, `hub_path`, `published`, `template_id`, `catalog_revision`, `branding_overrides` (the resolved MIO-2604 override layer, cascade included; `{}` when none — the override LAYER, not the hub's final branding), `homepage_page_id`, `welcome_post_id` + `welcome_post_status` (MIO-2558: the id, and whether this run `created` it / `adopted` one already there / adopted a soft-deleted one (`adopted_deleted`); both `null` when the template declares no `welcomePost`), `policy_gate` (the enforcement gate this run WROTE — `true`, or `null` when it wrote none: no declaration, or a resolved `false`, since the write is enable-only; `policies[]` says the document exists, `policy_gate` says whether anyone is asked to accept it — the pair QA could not tell apart, MIO-2567), plus `pages`/`spaces`/`onboarding_attributes`/`playlists`/`policies` in template order with the id this run created or recovered (unknown ids are `null`, never `""`), and `--dry-run` renders the ordered plan as `{dry_run, template_id, steps[]}`. All narration (step notes, catalog provenance, resolver warnings, the resume hint) goes to STDERR, so a json stdout is a single parseable value; a step failure after the hub exists names the created hub id in the error itself (and therefore in main.go's stderr error envelope) because the scaffold never rolls back.
- `templates` (MIO-2543 + MIO-2672) — lists the hub templates from the TARGET BACKEND's live catalog, LIVE-OR-FAIL (same Mutating resolve semantics as the scaffold preflight: a fetch failure surfaces with its typed exit code — 401/403→3, 404→4, 5xx→7 — never a silent stale-cache/vendored fallback listing; a 304-validated cache read is fine; the provenance line on stderr says which source answered); needs credentials (`requireAuth`), no team scope — the catalog route is not team-nested; honors `-o json|table|plain`/`--jq`.
- `email-settings get/update` GET/PATCH `/api/teams/{team_id}/hubs/{hub_id}/email-settings`  envelope `hub_email_senders` {from_name?, reply_to?} (MIO-1229)
- HISTORY (MIO-2567 -> MIO-2815): an admin policies READ **does** exist, and is now wrapped as `policies get` above — `GET /api/v1/teams/{team_id}/hubs/{identifier}/policies` (`admin_get_hub_policies`, MIO-2394), owner-only and tenant-scoped, reporting BOTH documents with the ACTUAL stored gate state (unlike the member-portal GET, which forces `enabled=true` for public display). This file previously said there was none, and that the only `/policies` GET was the member-portal route. It takes the SAME owner credentials as `policies gate` (identical `get_current_user` + `require_team_owner`). Now wrapped as `hubs policies get` (**MIO-2815**), which is how an operator checks whether a scaffold resume reverted their hand-edited ToS — but see the caveat on that row: `version` is NOT a custom-vs-default discriminator

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
  - create/update flags match PageCreate/PageUpdateAttributes: `--title` `--slug` `--type` `--privacy`(public|members|private) `--position` `--is-home`(→`is_homepage`) `--settings`/`--meta`(@file). No `--published`/`--description`/`--layout` (MIO-2257). **`privacy` defaults to `members` server-side**, so a page created without `--privacy public` is login-walled — the single most damaging omission in a hand-built public hub (`hubs scaffold` sets it, MIO-2563; the manual path does not). `home` is a RESERVED slug (rejected) and an omitted `--slug` 422s with `Field required`: author a real slug and designate the homepage with `--is-home` (MIO-2576)
- publish: `publish` POST `…/pages/{id}/publish` (no body; `If-Match: <draft_version>` header, `--if-match` REQUIRED)
- tree: `get` GET `…/pages/{id}/tree?audience=author&resolve=true` (draft_version = OCC token);
  `set` PUT `…/pages/{id}/tree` — body `{data:{type:page_draft_trees,attributes:{tree}}}` (type via `pages/tree` typeOverride), `If-Match: <draft_version>` header from `--file` + `--if-match`.
  `--if-match` is OPTIONAL and defaults to `0` (unlike publish): omit it for the FIRST tree on a draft-less page — `tree get` 404s until a draft exists, and a fresh page sits at `draft_version 0`, which the backend's atomic OCC update (`WHERE draft_version = expected`) matches only while no draft has been written. A defaulted/stale `0` against a page that already has a draft → 409 `stale_draft`, never a clobber, so OCC stays intact for later sets (MIO-2518, MIO-2258)
  - **READ SHAPE ≠ WRITE SHAPE (MIO-2576):** `get` answers a `page-trees` resource whose flattened form is `{tree:<the root NODE, BARE>, draft_version:N}`; `set --file` reads a `{root:…}` WRAPPER. The read path deliberately hands back the BARE root node (`bare_node = resolved.get("root", resolved)`, `app/pages/service.py`; the `AuthorDraftTreeResult` DTO documents `tree` as "the resolved draft tree ROOT node — bare (not wrapped in ``{root: ...}``)"), while the write path rejects anything without a top-level `root` (`Tree must have a 'root' key at the top level.`). So the round trip is a RE-WRAP, not an unwrap: `--jq '{root: .tree}'`. Feeding a `get` response back verbatim is rejected. `pages catalog scaffold` emits the WRITE shape directly for a `page-*` template, which is why the scaffold→set pipe works without a transform. Corollary for `hubs scaffold`-built pages: the scaffold's own tree PUT already moved them to `draft_version 1`, so an operator's first `tree set` against one needs `--if-match 1` — the `0` default only ever applies to a page that has never had a draft.
  - CLIENT-SIDE PRE-FLIGHT (`cmd/pages_tree_validate.go`, MIO-2537): a pre-order walk rejects the three render-fatal shapes the API accepts with a 200 — a non-numeric `settings.weight` and a blank/non-string `template` — with ExitUsage and no HTTP. It is deliberately conservative; the rest of the render contract (the `surface.background` enum, the value-bearing kind vocabulary, `surface.gradient` as a SIBLING of `background`) is documented rather than validated, because the frontend is the authoritative reader and a guessed shape would reject valid trees. See `cmd/skills/content/mio-skill.md` § "The page-tree render contract" and AGENTS.md § "Page-Tree Render Contract"; the catalog-derived vocabulary lists in the skill are pinned against the embedded catalog by `TestSkillDocIsGeneratedFromCatalog` (MIO-2539/2663/2664).
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
                 (envelope `onboarding_links`; requires `--hub-id` [canonical UUID,
                 slugs not resolved]/`--return-url`/`--refresh-url`) — **web/JWT-only
                 (MIO-2655): the backend 403s API-key principals, so this API-key-only
                 CLI always fails fast client-side (`ExitAuth`); MIO-2717**
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

## community  (`cmd/community.go`) — admin, hub-scoped, base `/api/admin/teams/{team_id}/hubs/{hub_id}/…`
- spaces:      `list/create/retrieve/update/delete/reorder` `/spaces[/{space_id}]`  body: envelope `spaces`
- discussions: `list` GET `/discussions` (admin view; cursor pagination in a BARE `meta.next_cursor`/`meta.has_more`, NOT the `meta.page.*` envelope the other lists use). The two branches differ in WHAT THEY SHOW: unfiltered → `list_for_hub` (ordered id DESC, `include_deleted=True`, no `is_removed` predicate) shows drafts, soft-deleted AND moderation-removed; with `filter[space_id]` → `list_for_space`, which appends `Discussion.is_removed.is_(False)` unconditionally, so REMOVED rows vanish. Accepted filters are `filter[space_id]` and `filter[is_broadcast]` only — the CLI's `--filter-status` sends `filter[status]` (MIO-2816), which the router does not declare and FastAPI silently ignores (a no-op returning the unfiltered list, not an error); `filter[is_broadcast]` has no flag
- discussions: `create` POST `/discussions`  body: envelope `discussions` {space_id, title, body?, is_published?} (MIO-2262 / CLI MIO-2808)
- discussions: `retrieve/update/delete` `/discussions/{id}` — `update` is MODERATION ONLY (is_pinned/is_locked/is_broadcast); title/body are author-owned and the admin PATCH cannot set them
- members:     `ban/unban/warn` POST `/members/{contact_id}/{action}`  body: **flat** {notes?}; `{contact_id}` is the GLOBAL contact id
- moderation:  see `cmd/community_moderation.go` (report-reasons, comments, queue/counts/audit-log/banned/removed, content view/remove/restore, reports) — not enumerated here yet
- **AUTHOR IS SERVER-DERIVED on `discussions create`.** There is no `author_contact_id` request field: a contact JWT authors as itself, a user JWT / team-bound API key authors as the team owner's contact (`resolve_team_owner_contact`), and the schema is `extra="forbid"` so a smuggled author field is a 422, not a silent ignore. v1 of this endpoint took an author id and was reverted for exactly that impersonation surface, so the CLI ships no `--author-contact-id` flag either. `is_admin=True` server-side means the post lands regardless of the space's posting_permission/segment ACL, but the AUTHOR's active-hub-membership gate still runs (422 `author_not_active_member`). Blank/NUL/over-280 titles are rejected server-side by the shared `discussion_text.py` validator — the CLI checks flag PRESENCE only (conduit rule), so `--title ""` is a 422.

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
portal `/me` and `/api/hub/…` routes, webhooks, JWKS, magic-link landing.
(The community ADMIN routes above are in scope and implemented; the member-facing
community portal routes are not.)
