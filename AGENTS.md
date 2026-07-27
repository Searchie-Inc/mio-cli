# mio CLI — Agent Guide

This document is written for AI coding agents (Claude Code, Codex, etc.) that need to call `mio` programmatically. It covers auth, output format, the exit-code contract, destructive-op handling, and a compact command table.

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

If no key is found the command exits with code **3** (`ExitAuth`). Always set `MIO_API_KEY` before running any resource command. Pass `--anonymous` to deliberately run unauthenticated — it skips both `MIO_API_KEY` and the keychain (an explicit `--api-key` still takes effect).

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
# Extract one field
mio contacts retrieve <id> --jq '.email'

# Pluck IDs from a list
mio products list --jq '.[].id'

# Capture into a variable
HUB_ID=$(mio hubs list --jq '.[0].id')
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
| `update` | _(self-update via the official release installer; supports `--version` and `--prefix`)_ |
| `config` | `set` `get` `list` |
| `api-keys` | `create` `list` `retrieve` `delete` |
| `teams` | `create` `list` `retrieve` `update` `delete` `switch` |
| `teams members` | `list` `add` `remove` |
| `users` | `me` `list` `retrieve` `update` |
| `roles` | `create` `list` `retrieve` `update` `delete` |
| `roles permissions` | `list` |
| `hubs` | `create` `list` `retrieve` `update` `delete` `scaffold` `templates` |
| `hubs navigation` | `list` `add` `remove` `reorder` — edit the menu item-by-item (RMW the `navigation` blob; header/footer/mobile buckets; items addressed by zero-based index). `add` takes `--item-json` (any bucket/type) or the url convenience `--type url --href --label` (header/footer only — mobile items use `--item-json`, `{id,label,route,icon}`); `remove --index`; `reorder --order 2,0,1` |
| `hubs policies` | `update` |
| `hubs scaffold` | Build a full-experience hub in one idempotent command from a hub template in the target backend's LIVE catalog (`--template <id>` — the CLI embeds none; `--catalog <file>` is the digest-verified escape hatch, fail-closed on mismatch, and there is no `--offline`): branding/favicon, menu, registration, discussion spaces, onboarding schema, policies, playlists, pages. The whole plan is validated before any write (`--name` ≤255 code points; `{{hub_name}}`/`{{hub_slug}}` interpolation — unknown tokens rejected, capped post-substitution). Pages: probes the backend `scaffold-from-template` op, falling back client-side on 404 (create with a `meta.template_provenance` marker → tree set → publish → mark applied), interpolating with the hub's actual title/slug. Re-runs with `--hub <id>` resume safely; an edited or foreign page at a template slug (or any pre-existing homepage) exits 2 — never overwritten. `--dry-run` previews the plan; `--publish` (default off) goes live; `--name`/`--slug` create a new hub; `--favicon-url`/`--logo-url`/`--registration-enabled` override the template. |
| `hubs templates` | `list` the hub templates from the target backend's catalog (needs credentials, not a team — shows exactly what a scaffold against that backend would apply) |
| `contacts` | `create` `list` `retrieve` `update` `delete` `restore` |
| `contact-attributes` | `create` `list` `retrieve` `update` `delete` |
| `contact-attributes options` | `create` `list` `update` `delete` |
| `contact-attributes hub-config` | `create` `list` `update` `delete` |
| `contact-attributes values` | `get` `set` |
| `tags` | `create` `list` `retrieve` `update` `delete` `assign` `assign-bulk` `remove` |
| `segments` | `create` `list` `retrieve` `update` `delete` `search` `members` `count` |
| `content` | `create` `list` `retrieve` `children` `update` `delete` `restore` `reorder` |
| `pages` | `create` `list` `retrieve` (add `--tree` for raw node tree) `update` `delete` `home` `publish` |
| `pages sections` | `create` (`--type` validated against the catalog writable set) `list` `update` `delete` `reorder` |
| `pages tree` | `get` `set` (author a page's draft node-tree; `set` takes `--file` + optional `--if-match` — omit for the first tree on a draft-less page, defaults to `0`) |
| `pages catalog` | `scaffold` (`--template`/`--variant` → a node-tree for `pages tree set`) `templates` (`--page-type`) `section-types` (`--writable-only`) |
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
| `products` | `create` `list` `retrieve` `update` `delete` |
| `products prices` | `create` `list` `retrieve` `update` `delete` |
| `checkout orders` | `list` `retrieve` |
| `checkout subscriptions` | `list` `retrieve` `cancel` |
| `checkout payments` | `list` `retrieve` `refund` |
| `checkout webhooks` | `list` `retrieve` `replay` |
| `checkout accounts` | `list` `retrieve` `onboarding-link` |
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
- **`hubs policies update <hub_id>`** takes the hub identifier as a positional argument, NOT the `--hub` context flag. Policy content supports the `@file` convention: `--content @tos.md`.
- **`hubs policies update --policy-type`** must be exactly `tos` or `privacy_policy`; the CLI validates this client-side. The `--require-acceptance` flag is only meaningful for `tos`; passing it with `privacy_policy` will result in a backend 422.
- **`hubs policies update` content flags** — exactly one of `--content` or `--reset-content` is required (they are mutually exclusive):
  - `--content <text|@file>` — supply the policy body inline or read it from a file.
  - `--reset-content` — revert the policy to the backend default (sends `content: null`).
  Providing both exits 2; providing neither also exits 2.
- **`hubs create` is PRIVATE by default.** Pass `--published` to publish. The response has no public-URL field (and the CLI knows only the API base, not the hub-frontend host), so no URL is echoed; output carries a derived `published` (= `!is_private`) and, in table mode, a private hub prints the slug + publish hint on stderr. `--logo-url` and `--favicon-url` merge into `branding` (`logo_url`/`favicon_url`).
- **`hubs retrieve` output carries derived booleans** `registration_enabled` (= `settings.registration.enabled === true`, fail-closed) and `published` (= `!is_private`). These are read-only convenience views, never sent on a write; `--raw` bypasses them.
- **`hubs update` blob flags** — `--registration-enabled true|false` sets `settings.registration.enabled` (read-modify-write; sibling keys preserved; gated on `Changed()` so `=false` differs from unset). `--favicon-url` sets `branding.favicon_url`. `--unset <dotted.path>` DELETES a blob key — the ONLY real delete (the `*-json` flags are merge-only: a literal `null` persists as null, `{}` is a no-op). First dotted segment selects the blob (`branding`/`settings`/`meta`); nested paths, comma-lists and repeats are supported; a blank/bare-blob/unknown-blob path exits 2 and fires no request. Apply order per blob: `--*-json` merge → scalar flags (`--logo-url`/`--favicon-url`/`--registration-enabled`) → `--unset` LAST (unset wins over a same-command merge).

- **Contact name flags are kebab-case**, like every other resource: `--first-name`, `--last-name`. `--email`, `--phone`, `--status` are also available. (The legacy underscore spellings `--first_name`/`--last_name` still work as hidden, deprecated aliases for back-compat, but new scripts should use the kebab form.)
- **`products prices` takes the product id as a positional argument**, not `--product`:
  `mio products prices create <product_id> --amount 4900 --currency usd --interval month`.
- **`segments search --conditions` takes the full condition tree** (the backend write shape), not a flat list. Prefix with `@` to read from a file. There is no `--match` flag.
  ```sh
  mio segments search --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":"@example.com"}]}]}'
  mio segments search --conditions @conditions.json --page-size 50 --page-after <cursor>
  ```
- **Generate the full reference** for any command set with `mio gen-docs --dir ./docs` (one Markdown file per command).

---

## Minimal Agent Snippet

```sh
#!/usr/bin/env bash
set -euo pipefail

export MIO_API_KEY="${MIO_API_KEY:?MIO_API_KEY must be set}"   # mio_sk_live_…

# Resolve team from config (or pass --team explicitly)
CONTACTS=$(mio contacts list --output json)

# Filter with --jq
FIRST_ID=$(mio contacts list --jq '.[0].id')

# Delete safely in a script (destructive ops need --yes off a TTY)
mio contacts delete "$FIRST_ID" --yes
```

---

## Useful Links

- [README.md](./README.md) — install, quickstart, global flags, full usage guide
- [llms.txt](./llms.txt) — machine-readable one-line-per-command index
- `mio <resource> --help` — full flag reference per resource
