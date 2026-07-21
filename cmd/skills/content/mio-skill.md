# mio — Membership.io CLI

`mio` is the Membership.io command-line interface. It is agent-first: JSON output
by default off a TTY, environment-variable auth, and stable exit codes so scripts
and agents can branch deterministically. Use it for creator-side automation and,
above all, to **build a full, render-faithful hub end to end without touching the
raw API**.

Reach for this skill whenever the task is "create/build/automate a Membership.io
hub, its pages, playlists, media, discussion spaces, branding, or menus" — or when
you hit one of the silent-drop traps below where the API returns `200` but nothing
renders.

## Setup and conventions

```bash
export MIO_API_KEY=mio_sk_live_xxxxx      # never run `mio login` in an agent (it prompts)
mio config set team team_abc123           # drop the repeated --team; or pass --team per call
# once the hub exists: mio config set hub hub_abc123
```

- **Auth resolution (first wins):** `--api-key` flag → `MIO_API_KEY` env → key stored by `mio login`. No key ⇒ exit `3`.
- **Output:** JSON when piped/non-interactive (agent default), table on a TTY. Force with `--output json`. Filter inline with `--jq '<expr>'` (no external `jq` needed), e.g. `HUB_ID=$(mio hubs list --jq '.[0].id')`. Use `--raw` for the unflattened JSON:API envelope (`meta`/`links`/`included`).
- **Exit codes (stable contract):** `0` ok · `1` error · `2` bad args (400/409/422) · `3` auth (401/403) · `4` not found · `5` needs `--yes` in a non-TTY · `6` rate limited (429) · `7` server (5xx).
- **Destructive ops** (`delete`/`cancel`/`refund`) require `--yes`/`-y` in a non-interactive shell or they exit `5`.
- Info/hints print to **stderr**, so machine-readable stdout stays clean. `--jq .id` on a create gives you the new id for the next step.

## Build a hub — the CLI-only ordered recipe

Follow this sequence. Each step is a real, verified `mio` command. **Verify the
outcome, don't trust the `200`** — the render-contract traps below cause silent
drops.

### 1. Create the hub

```bash
mio hubs create --name "Member Academy" --slug member-academy
```

`--name` maps to the hub `title`; `--slug` is the public lookup key. A new hub is
**private and unpublished** — not reachable by members yet. The public hub URL is
**not** returned by the API and cannot be derived; combine the slug with your
hub-frontend host yourself.

### 2. Branding, favicon, registration, menu

Branding / settings / meta are opaque JSONB blobs. The `--branding-json` /
`--settings-json` / `--meta-json` flags **merge** (read-modify-write, so a partial
edit never clobbers siblings) and **validate keys** (unknown key warns; add
`--strict-keys` to make it a hard error).

```bash
mio hubs update hub_abc123 \
  --favicon-url "https://cdn.example.com/favicon.png" \
  --logo-url    "https://cdn.example.com/logo.png" \
  --branding-json '{"primary":"#0F766E","background":"#ffffff","font_heading":"Inter"}' \
  --registration-enabled \
  --navigation-json '{
    "header":[{"type":"url","label":"Home","href":"/member-academy/","position":0}],
    "footer":[{"type":"url","label":"Privacy","href":"/member-academy/privacy","position":0}]
  }'
```

- **Menu items MUST be typed.** The hub frontend parser **silently drops** any
  `header`/`footer` item without a non-empty `type` (`url`, `page`, `playlist`, or
  `discussions`), so an untyped menu renders empty. `header`/`footer` are arrays of
  objects; the `mobile` bucket uses a different `{id,label,route,icon}` shape. On
  `hubs update`, `--navigation-json` **replaces** the whole navigation blob.
- **Relative menu hrefs must be hub-scoped**: a `type:"url"` href must start with
  `/<slug>/…` (e.g. `/member-academy/about`) or it escapes the hub and 404s.
  Absolute `https://` hrefs are left untouched.
- `--registration-enabled` is tri-state: set it to enable, `=false` to disable, omit
  to leave untouched. `mio hubs retrieve` surfaces a derived `registration_enabled`.
- The `-json` flags are merge-only. To **delete** a key use `--unset` with a dotted
  path whose first segment picks the blob, applied after the merges:
  `mio hubs update hub_abc123 --unset settings.registration.enabled --unset branding.gradient`.

### 3. Discussion spaces

```bash
mio community spaces create --hub hub_abc123 --name "General" --slug general
mio community spaces create --hub hub_abc123 --name "Announcements" --slug announcements \
  --posting-permission admins_only
```

`--access-level` is `public` or `restricted`; `--posting-permission` is
`any_member`, `admins_only`, or `segment` (with `--segment-id`).

### 4. Playlists → items → publish to the hub

Build a playlist, curate items, give it a cover, then publish it onto the hub.
Publishing writes the `hub_media` row that surfaces it on `/content` and the
homepage content-grid.

```bash
mio media playlists create --title "Getting Started" --hub-id hub_abc123 --visibility public
mio media playlists items add --playlist-id pl_abc --file-id file_intro
mio media playlists items add --playlist-id pl_abc --file-id file_lesson2 --position 1
mio media playlists set-cover pl_abc --file-id file_cover        # pass the FILE id; media id is resolved
mio media hub-playlists publish --hub hub_abc123 --playlist-id pl_abc \
  --visibility public
```

> **Pass `--visibility public` so anonymous visitors can see the card.**
> `--published-at` is optional: when omitted the CLI now defaults it to *now*, so
> the card publishes immediately. Pass an explicit past/future RFC3339 timestamp
> only to backdate or schedule. (A `null` `published_at` would be treated as a
> silent draft — the CLI no longer sends null, so an unset flag can't hide the card.)

`playlists items` also has `list`, `remove <item_id>`, `reorder <item_id> --position N`.
`set-cover` and `items add` take the **file id**; `items remove`/`reorder` take the
**item id** (the `id` from `items list`), not the file id.

### 5. Images in page trees — use durable URLs, never `variants`

Page-tree image nodes must reference a URL that does not expire. The `variants` map
from an upload is imgproxy-signed and **expires in ~24–48h** — inline one and the
image silently 404s a day later. Use `durable-url`:

```bash
mio media files durable-url file_hero --hub hub_abc123 --preset large-1440 --publish
```

- It joins the file's `durable_variants` entry with the **required `?hub_id=`** param
  (the command adds it) so it resolves for `--hub`.
- The URL 404s until the file is **published public to that hub** — `--publish` does
  that inline (or run `mio media hub-media publish --hub hub_abc123 --file-id file_hero --visibility public` first).
- `--preset` picks one variant (`thumbnail-160`, `medium-720`, `large-1440`,
  `webp-medium`); omit to print all. Durable URLs are **image-only**.

### 6. Build the homepage

Create the page, scaffold a node-tree from the page-builder catalog, fill in real
values, set the draft, then publish.

```bash
mio pages create --hub hub_abc123 --title "Home" --slug home --is-home
mio pages catalog scaffold --template page-homepage > tree.json   # JSON to stdout; catalog note goes to stderr
# ...edit tree.json: fill headline/text/button nodes, drop in durable image URLs...
mio pages tree set page_home123 --hub hub_abc123 --file tree.json  # first tree: --if-match defaults to 0
mio pages publish page_home123 --hub hub_abc123 --if-match 1        # --if-match REQUIRED here
```

- `pages tree set --if-match` is **optional** and defaults to `0` — omit it for the
  first tree (`pages tree get` 404s until a draft exists). The `0` default does **not**
  bypass the concurrency guard: sending `0`/stale against a page that already has a
  draft is rejected as a conflict, so you can't silently clobber. For every later
  write, pass the `draft_version` from a prior `pages tree get`.
- A `page-*` template emits a complete `{"root": …}` tree ready for `tree set`; a
  section template emits a bare subtree to drop into a root's `children`. List
  templates/section types with `mio pages catalog list`.

### 7. Publish the hub

```bash
mio hubs update hub_abc123 --published
```

Now reachable by members at your hub-frontend host + slug. **`--published`
auto-enables Registration + Moderation** as a side effect — set them deliberately
afterward if that's not what you want.

## Render-contract gotchas (silent drops — verify, don't trust the 200)

The API validates *structure*, not *renderability*. These return `200` and then
**silently drop** the node/section/card at render time:

- **`weight` must be numeric** — a number like `700`, never a CSS keyword like
  `"bold"`. A string weight is dropped.
- **A section must carry its `template`** (`"hero"`, `"carousel"`, `"row"`, …). The
  catalog scaffold sets it; a hand-built section without it will not render.
- **Button nodes need the correct `action` shape.** A malformed/missing `action`
  drops the button silently.
- **`section_count` is your "did it apply" signal.** `mio pages publish` returns a
  `page-publishes` resource with `section_count` and `gate_count`. If `section_count`
  is lower than the number of sections you authored, the renderer rejected some —
  inspect the tree; don't infer success from the `200`.
- **Homepage content-grids need STATIC cards, not a data-source binding.** The
  homepage route prefetches only `type:"playlist"` sources, so a content-grid bound
  to `dataSource:{type:"hub_playlists"}` renders **empty** on the homepage
  (`hub_playlists` feeds the `/content` browse page, not the homepage).

## The contact-id vs. contact_id trap

`mio` surfaces **two** contact identifiers; mixing them up 404s a live contact.

- The `contacts` verbs — plus `contact-attributes` and `tags` — operate on the
  **team-contact id**, surfaced as `.id`.
- The **member-shaped** verbs operate on the **global contact id**, a separate field
  surfaced as top-level `.contact_id`: `hub-memberships` (add/set-role/ban/unban/warn),
  `activity contact`, community members (ban/unban/warn/soft-ban),
  `email enrollments`, and `access-rules` overrides create.

Read the global id from the flattened output first:

```bash
CID=$(mio contacts retrieve ctt_abc123 --jq .contact_id)
mio hub-memberships add "$CID" --hub hub_abc123
```

## Command reference (compact)

Discover everything with `mio --help`, `mio <group> --help`, or the machine-readable
index at `mio gen-docs --dir ./docs`. Core groups:

- **Auth/context:** `mio whoami` · `mio config set|get|list` · `mio teams list|switch` · `mio api-keys create|list`
- **Hubs:** `mio hubs create|retrieve|update|list` · `mio hubs policies update` · `mio hubs navigation ...`
- **Pages:** `mio pages create|list` · `mio pages catalog list|scaffold` · `mio pages tree get|set` · `mio pages publish`
- **Media:** `mio media files upload|list|durable-url` · `mio media playlists create|set-cover` (+ `playlists items add|list|remove|reorder`) · `mio media hub-media publish` · `mio media hub-playlists publish` · `mio media search` · `mio media transcripts get|edit|revert`
- **Community:** `mio community spaces create|list` · `mio community moderation ...`
- **Contacts/members:** `mio contacts list|retrieve` · `mio contact-attributes ...` · `mio tags ...` · `mio hub-memberships add|set-role|ban` · `mio segments ...`
- **Commerce:** `mio products ...` · `mio coupons ...` · `mio checkout ...`
- **Automation/email:** `mio automations ...` · `mio email ...` · `mio webhook-endpoints ...`

When a member verb returns exit `4`, re-check you passed `.contact_id` (global),
not `.id` (team-contact). When a page/card doesn't render despite a `200`, re-check
the render-contract gotchas above.
