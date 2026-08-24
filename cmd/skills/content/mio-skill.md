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
mio config set current_team <team-uuid>   # a UUID (see 'mio teams list'); drop the repeated --team
# once the hub exists: mio config set current_hub <hub-uuid>
```

- **The only writable config keys are `current_team`, `current_hub`, `api_base`.**
  `mio config set team …` / `hub …` exit `2` with
  `unknown config key "team" (valid: [current_team current_hub api_base])` (MIO-2568).
- **Auth resolution (first wins):** `--api-key` flag → `MIO_API_KEY` env → key stored by `mio login`. No key ⇒ exit `3`.
- **Output:** JSON when piped/non-interactive (agent default), table on a TTY. Force with `--output json`. Filter inline with `--jq '<expr>'` (no external `jq` needed). Use `--raw` for the unflattened JSON:API envelope (`meta`/`links`/`included`).
- **Capturing a STRING id? Add `-o plain` (MIO-2792).** `--jq` renders through the JSON formatter, so a string result comes back **JSON-quoted** and `$(…)` captures the quotes: `HUB_ID=$(mio hubs list --jq '.[0].id')` yields `"019f…"`, which then 404s when you pass it to the next command. `-o plain` prints the bare scalar:
  ```bash
  HUB_ID=$(mio hubs list -o plain --jq '.[0].id')     # 019f…    ← use this
  V=$(mio pages tree get "$PAGE_ID" --jq .draft_version)  # 3 — numbers are unquoted either way
  ```
  Numeric captures (`draft_version`, counts) are safe without it; every string capture needs it.
- **Exit codes (stable contract):** `0` ok · `1` error · `2` bad args (400/409/422) · `3` auth (401/403) · `4` not found · `5` needs `--yes` in a non-TTY · `6` rate limited (429) · `7` server (5xx). They are deliberately coarse; when you need the exact status the API returned (403 vs 401, 409 vs 422), read `errors[0].status` from the JSON:API envelope on stderr — it carries the real HTTP status verbatim, while `errors[0].meta.exit_code` echoes the coarse code (MIO-2656).
- **Destructive ops** (`delete`/`cancel`/`refund`) require `--yes`/`-y` in a non-interactive shell or they exit `5`.
- Info/hints print to **stderr**, so machine-readable stdout stays clean. `--jq .id` on a create gives you the new id for the next step.

## Start here: `mio hubs scaffold` (one command, whole hub)

**Do not hand-build a hub unless you have to.** One idempotent command applies a
template from the backend's live page-builder catalog — branding, favicon, a typed
navigation menu, registration, discussion spaces, an onboarding contact-attribute
schema, policies, playlists and pages (homepage included) — with the palette baked
in (MIO-2543, MIO-2604):

```bash
mio hubs templates                                   # what the target backend offers
mio hubs templates --catalog ./catalog.json          # ...or a local artifact (digest-verified)
mio hubs scaffold --template community --name "Acme" --slug acme \
  --primary-color '#B91C1C' --secondary-color '#0F172A' --text-color '#111827' \
  --logo-url https://cdn.example.com/logo.png \
  --dry-run                                          # prints the ordered plan, changes nothing
HUB_ID=$(mio hubs scaffold --template community --name "Acme" --slug acme \
  --primary-color '#B91C1C' --publish -o plain --jq .hub_id)  # --publish goes live; default is private
```

- Branding flags **merge** over the template's palette — a key you don't name keeps
  the template's value. `--primary-color` also fills `header_color` unless you gave
  one yourself. `--branding-json` takes a whole object; scalar flags win over it.
- **Re-runs are safe for PAGES**: `mio hubs scaffold --template community --hub "$HUB_ID"`
  resumes. A page you edited, or a foreign page at a template slug, exits `2` and is
  **never** overwritten. Spaces, onboarding attributes and playlists skip if they
  already exist. **Legal policy CONTENT is the exception — see the next bullet.**
- **A server-side op may build the hub in one shot (MIO-2976).** In create mode the
  CLI probes `POST …/hubs/from-template` first; if the backend has it enabled, the
  whole hub is built in ONE transaction and the nine client-side steps never run.
  It ships **dormant**, so today every run still takes the client-side path and
  nothing above changes. Two things to know when it does turn on: re-running the
  SAME command converges (deterministic idempotency key) instead of creating a
  second hub — but re-running the same `--name`/`--slug` after the backend's catalog
  pin moved, or with different override flags, exits `2` having applied **nothing**
  (the message carries the literal token `[idempotency_fingerprint_mismatch]`); and any branding
  override (`--branding-json` or a palette flag like `--primary-color`, as in the
  example above), plus `--hub`, `--dry-run`, `--catalog`, or omitting `--name`/`--slug`,
  forces the client-side path — the op cannot express them (an empty or whitespace-only value for
  any branding key ending `_url` — scalar flag or inside `--branding-json` — is
  refused outright with exit `2` before any request — the API rejects an empty
  branding `*_url` on create *and* update, so neither path can honour it; clear a
  key with `mio hubs update <hub_id> --unset branding.logo_url` instead).
  The run names the flag on stderr; `--dry-run` is silent, being structural. `-o json` is identical either way.
  A **template** can force the client path too: one declaring `spaces[].icon`,
  `playlists[].documents`, or a page node binding a playlist `dataSource` by `key`
  is applied client-side, because the op models none of those and would build a
  hub that looks finished and is not (MIO-3065; backend parity is MIO-3073). The
  skip is announced and names what would have been dropped.
- **A template's playlists arrive hub-scoped, filled, and bound (MIO-3065).** Each
  playlist is created with `hub_id` — without it its detail page 404s for everyone —
  and its per-hub publication row is `visibility: public`. A `playlists[].documents[]`
  entry becomes a synthetic READY document file (no upload, no transcode), is
  published to the hub in its own right, and is attached as a playlist item; skipping
  that publish leaves it attached but invisible to anonymous visitors. A page node
  shipping `dataSource: {"type":"playlist","id":"","key":"…"}` has the created
  playlist's id written into `id` before the tree is PUT — the hub renderer ignores a
  section whose `ds.id` is empty, so an unfilled one renders as a blank band. A `key`
  naming no playlist of the same template exits `2` in preflight, before the hub exists.
- **Legal policies come with their enforcement switch, and a resume rewrites them.**
  The scaffold writes each policy document *and* flips the hub-level gate
  (`settings.policies.enabled`) when the template declares `enabled: true`;
  `policy_gate` in the JSON result reports what it applied (`null` = none written,
  so the hub's setting stands). Writing a ToS without the gate is a hub where nobody
  is ever asked to accept it: the member endpoint reports
  `tos_acceptance_required:false` and `POST …/tos/accept` returns a 404 (an
  enumeration-safe mask, not a missing route). Enforcement is one flag per hub, not
  one per policy.
  **The catch:** the policy write always sends `content`, and the `community`
  template carries none — so **every resume reverts that hub's ToS and Privacy text
  to the backend default**, and because the ToS is acceptance-gated it also bumps the
  version, **re-prompting every member who had already accepted**. Check BEFORE a resume with
  `mio hubs policies get "$HUB_ID"` — but read the **content**, not the version:
  the backend versions only a ToS saved WITH `--require-acceptance` and projects
  everything else as `default-v1`, so custom text routinely reads as the default.
  If you customized the legal text, re-apply it after any resume:
  `mio hubs policies update "$HUB_ID" --policy-type tos --content @tos.md --require-acceptance`.
  **Not in `v0.13.0` or earlier** — on those binaries the gate is never written at
  all, so fix a scaffolded hub with `mio hubs policies gate "$HUB_ID" --enabled`.
- The public URL is not returned by the API (no domain field exists) — combine
  `hub_slug` with your hub-frontend host yourself.
- **After a scaffold, its pages already carry a draft** (`draft_version` ≥ 1, in
  practice `1`). Editing one is a *subsequent* tree write, so `--if-match` is
  **required and is not `0`** — see step 6.

Use the manual recipe below to customize a scaffolded hub, or to build one from
scratch when no template fits.

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
edit never clobbers siblings) and **validate keys** (unknown key warns and is still
sent; add `--strict-keys` to make it a hard error instead).

```bash
mio hubs update hub_abc123 \
  --favicon-url "https://cdn.example.com/favicon.png" \
  --logo-url    "https://cdn.example.com/logo.png" \
  --branding-json '{"primary":"#0F766E","secondary":"#0B1F1C","text":"#111827","background":"#ffffff","font_heading":"Inter"}' \
  --registration-enabled \
  --navigation-json '{
    "header":[{"type":"url","label":"Home","href":"/member-academy/","position":0,"icon":"home"}],
    "footer":[{"type":"url","label":"Privacy","href":"/member-academy/privacy","position":0}]
  }'
```

#### Branding key map (verified against a live rendered hub + the frontend parser)

| key | what it actually paints |
|---|---|
| `primary` | buttons, links, CTAs, brand accents |
| `secondary` | **the page's ink in light mode (all headings + body copy) and the page background in dark mode**; also the base of a `tint` surface in light mode |
| `text` | body copy — **only in `custom` theme mode** (see below) |
| `background` | page background — **only in `custom` theme mode** (see below) |
| `header_color` / `header_accent` | top-nav background + accent (emitted raw — no contrast correction) |
| `dark_mode` (bool) | **not** the theme selector — only flips the defaults for `background`/`text` when those are unset (see below) |
| `logo_url` / `favicon_url` / `social_image_url` | imagery |
| `font_heading` / `font_body` / `heading_font_size` / `body_font_size` | typography |

- **Foot-gun: `secondary` is not a decorative accent — it is the page's ink.** In
  `light` mode the frontend sets `--hub-text: var(--hub-secondary)`, so `secondary`
  colors every heading *and* all body copy, and (in light mode) it is the base a `tint`
  surface is derived from. In `dark` mode it becomes the page **background** instead.
  Set it light and every heading and paragraph goes invisible on a white page — the
  most common branding mistake by a distance. Pick a dark, high-contrast value.
- **Color values must be 6-digit hex** (`#0F766E`) — for all six color keys
  (`primary`, `secondary`, `background`, `text`, `header_color`, `header_accent`).
  The frontend's branding parser tests them against `/^#[0-9a-fA-F]{6}$/` and silently
  substitutes its own default on anything else, so 3-digit hex, 8-digit hex, named
  colors, `rgb()`/`hsl()` and gradients all render as "not what you asked for" with a
  `200` on the wire. The CLI does not validate values (conduit rule) — this is a
  render contract, not an API one.
- **Every `*_url` branding key is validated server-side — and strictly.** The rest of
  the blob is stored verbatim, but the backend applies a URL rule to **any** key whose
  name ends in `_url`, case-insensitively, present or future (MIO-2658). On the CLI's
  allowlist that is `logo_url`, `favicon_url`, `social_image_url`,
  `auth_logo_url`, `custom_login_logo_url` and `custom_font_url`. Each must be `null` or a **string**
  (a list or object raises) and, if a string, an **absolute `https://` URL**. Rejected:
  any other scheme — including plain `http://`, `data:` and `javascript:` — plus
  protocol-relative `//host/x`, relative paths, whitespace or control characters (raw
  *or* once percent-decoded), backslashes, embedded `user:pass@` credentials,
  percent-encoding anywhere in the host, and an invalid port. So a `data:` SVG logo
  fails: upload the asset and use a durable URL. This is a `422`, not a silent drop —
  the one branding write that fails loudly.

#### Light, dark and `custom` — a hub cannot choose light or dark

This is the most misunderstood part of hub theming, so read it before writing any
theme key.

- **No hub setting selects light or dark.** The frontend resolves the mode as
  `if (hubMode === 'custom') return 'custom'` and otherwise falls through to the
  **viewer's** `mio-hub-theme` cookie (default `'system'`) and their OS
  `prefers-color-scheme`. The hub's own mode is consulted for exactly one value:
  `custom`. Writing `light` or `dark` anywhere is a **no-op** — the viewer decides.
- **There is no `settings.theme` key either.** Writing one is a silent no-op; the
  frontend's parsed `theme.mode` is *derived* from `settings.background.type`, and
  nothing reads a raw `settings.theme`.
- **`custom` is the one thing a hub can force**, via `settings.background.type`
  (`background` is already on the CLI's settings-key allowlist, so this needs no
  warning suppression). It is also the **only** mode in which `branding.background`
  and `branding.text` do anything at all:

  ```bash
  mio hubs update hub_abc123 \
    --settings-json '{"background":{"type":"custom"}}' \
    --branding-json '{"background":"#FFFDF7","text":"#1A1A1A"}'
  ```

  In `light`/`dark` the theme layer explicitly clears `--hub-background`/`--hub-text`
  and derives both from `secondary` (light: white background, text `secondary`; dark:
  background `secondary`, white text) — so setting `branding.text` on a non-`custom`
  hub does nothing. In `custom` mode `text` is AA-contrast-clamped against
  `background`, so a low-contrast pair is corrected rather than honoured exactly.
- **`branding.dark_mode` selects nothing.** It only flips the *defaults* the branding
  parser uses for `background`/`text` when those are absent or invalid — values that
  `light`/`dark` mode then discards anyway. Keep it consistent with your intent for the
  benefit of backend consumers that still read it, but do not expect it to change what
  a viewer sees.

#### Menus and nav icons

- **Menu items MUST be typed.** The hub frontend parser **silently drops** any
  `header`/`footer` item without a non-empty `type` (`url`, `page`, `playlist`, or
  `discussions`), so an untyped menu renders empty. `header`/`footer` are arrays of
  objects; the `mobile` bucket uses a different `{id,label,route,icon}` shape. On
  `hubs update`, `--navigation-json` **replaces** the whole navigation blob. For
  item-by-item edits use `mio hubs navigation add|remove|reorder`.
- **Relative menu hrefs must be hub-scoped**: a `type:"url"` href must start with
  `/<slug>/…` (e.g. `/member-academy/about`) or it escapes the hub and 404s.
  Absolute `https://` hrefs are left untouched.
- **`icon` glyph names are two different vocabularies** (MIO-2675). The CLI passes
  them through unvalidated, so a wrong name is a silent drop:
  - **`header`/`footer`** accept any id in the hub frontend's icon sprite
    (`mio-hub` `public/icons/sprite.svg`, ~205 ids — the generated `ICON_NAMES`
    list in `src/components/ui/icon.tsx` is authoritative). `icon` is **optional**
    here: an unknown name drops the icon but keeps the menu item.
    Safe, verified-present picks: `home` · `content` · `chat` · `users` · `search` ·
    `star` · `link` · `earth` · `calendar` · `bell` · `play` · `video` · `settings` ·
    `email` · `folder` · `heart` · `lock` · `tag` · `podcast` · `download`.
    **`info` and `globe` do NOT exist** — that is why they render blank. Use
    `information` / `information-circle` for info and `earth` / `global` for globe.
  - **`mobile`** accepts an **8-value whitelist only**, and `icon` is **mandatory** —
    an item with a missing or off-list icon is dropped entirely. The values are
    frontend component names, **not** sprite ids, and the casing matters:
    `Home` · `Bell` · `User` · `Users` · `MessageSquare` · `MessageCircle` ·
    `Search` · `content` (that last one is deliberately lowercase).
    Two more mobile-only rules: fewer than 3 valid tabs and the whole list is
    replaced by the frontend defaults; more than 5 and it is truncated to 5.
- `--registration-enabled` is tri-state: set it to enable, `=false` to disable, omit
  to leave untouched. `mio hubs retrieve` surfaces a derived `registration_enabled`.
- The `-json` flags are merge-only. To **delete** a key use `--unset` with a dotted
  path whose first segment picks the blob, applied after the merges:
  `mio hubs update hub_abc123 --unset settings.registration.enabled --unset branding.gradient`.

### 3. Discussion spaces (and a welcome post)

```bash
mio community spaces create --hub hub_abc123 --name "General" --slug general
mio community spaces create --hub hub_abc123 --name "Announcements" --slug announcements \
  --posting-permission admins_only

SPACE_ID=$(mio community spaces list --hub hub_abc123 -o plain --jq '.[0].id')
mio community discussions create --hub hub_abc123 --space-id "$SPACE_ID" \
  --title "Welcome!" --body "Introduce yourself in the comments."
```

`--access-level` is `public` or `restricted`; `--posting-permission` is
`any_member`, `admins_only`, or `segment` (with `--segment-id`).

`discussions create` posts as **you** — the author is derived server-side from your
credentials (a team-owner key posts as the team owner's contact), and there is
deliberately no flag to author as another member. It ignores the space's
`posting-permission` (it is an admin write), publishes immediately unless you pass
`--is-published=false`, and caps the title at 280 characters. Note `update` is
moderation-only (`--is-pinned`/`--is-locked`/`--is-broadcast`): once posted, title
and body belong to the author and no admin route can edit them, so get the copy
right on `create`.

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

**Placeholders: do not point image nodes at a stub-image host.** `placehold.co` and
its lookalikes render a grey "600 × 400" card that reads as unfinished work in a
shipped hub — and it is a third-party request on every page view. Either **omit the
`image` node entirely** (every layout tolerates it — the column just collapses) or
inline a self-contained SVG data URI as the node's `value`, e.g.
`data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 9'><rect width='16' height='9' fill='%230F766E'/></svg>`.
Note the asymmetry with branding: a `data:` URI is fine as an image node `value`
(the tree is opaque JSONB), but **not** as `branding.logo_url` (server-validated).

### 6. Build a page

Create the page, scaffold a node-tree from the page-builder catalog, fill in real
values, set the draft, then publish.

```bash
PAGE_ID=$(mio pages create --hub hub_abc123 --title "Welcome" --slug welcome \
  --privacy public --is-home -o plain --jq .id)   # -o plain: a bare id, not "…" (MIO-2792)
mio pages catalog scaffold --template page-homepage > tree.json   # JSON to stdout; catalog note goes to stderr
# ...edit tree.json: fill headline/text/button VALUES, drop in durable image URLs...
mio pages tree set "$PAGE_ID" --hub hub_abc123 --file tree.json  # first tree: --if-match defaults to 0
mio pages publish "$PAGE_ID" --hub hub_abc123 --if-match 1        # --if-match REQUIRED here
```

**`--privacy public` is not optional if you want a public hub.** `pages create`
defaults to `members`, so a page created without it is login-walled — the builder
ships what looks like a public hub and every anonymous visitor hits the login
screen. `mio hubs scaffold` gets this right (MIO-2563); this manual path does not
do it for you. Valid values: `public`, `members`, `private`.

**Slugs:** `home` is reserved — `--slug home` is rejected. Omitting `--slug`
entirely fails with `Field required`. Use a real slug (`welcome`, `start`, `about`)
and mark the homepage with `--is-home`, which is what actually designates it.

#### Discovering the catalog

There is **no `mio pages catalog list`.** The three real verbs are:

```bash
mio pages catalog templates                      # every author template + its variants
mio pages catalog templates --page-type homepage # what's recommended for that page type, in order
mio pages catalog section-types --writable-only  # the 'pages sections create --type' allow-list
mio pages catalog scaffold --template <id> [--variant <v>]
```

All three are emit-only and offline-capable (`--offline` forces the embedded,
digest-pinned copy; `--catalog <file>` overrides both).

#### Composing sections

A `page-*` template emits a complete `{"root": …}` tree ready for `tree set`. A
**section** template emits a bare node to splice into a root's `children`.

**The page templates are outlines, not finished sections.** `page-homepage`'s hero
child arrives as `{"kind":"row","template":"hero","settings":{}}` — no surface, no
values. Scaffold the page for the skeleton, then scaffold each section on its own
for the real, DS-conformed recipe (correct `kind`, `settings.surface`, column
widths) and swap it in:

```bash
mio pages catalog scaffold --template page-homepage > tree.json
mio pages catalog scaffold --template hero            > hero.json
mio pages catalog scaffold --template row --variant 3eq > cols.json   # 3 equal columns
mio pages catalog scaffold --template grid            > grid.json
# splice: tree.json .root.children = [hero.json, cols.json, grid.json], then fill values
```

`row` is the unified 1–4 column section; pick the layout with `--variant`:

<!-- catalog-gen:row-variants -->
`1col` · `2eq` · `2left` · `2right` · `3eq` · `4eq` · `bound-cards` ·
`cta-band` · `faq`
<!-- /catalog-gen -->

`1col`/`2eq`/`3eq`/`4eq` are equal splits; `2left` is 2/3 + 1/3 and `2right` is
1/3 + 2/3. Three are content presets that arrive **already filled with placeholder
`value`s** — the fastest way to see the contract in practice: `cta-band` (three
tint-surfaced cards, each headline + text + button), `faq` (a full-width `accordion`
of `text` items keyed by `settings.tab_label`), and `bound-cards` (two
`content-card`s bound to a `dataSource`, plus a third column whose *button* carries
the binding — fill in every `dataSource.id`).
`hero`, `grid` and `compact` carry variants too; `mio pages catalog templates`
prints them all.

#### The tree envelope: `get` returns one shape, `set` wants another

**`get` hands back the BARE root node; `set` demands the `{"root": …}` wrapper.**
So the round trip is a **re-wrap**, not an unwrap:

```bash
# get →  {"id": …, "tree": <the root NODE, bare>, "draft_version": N}
# set ←  {"root": <the root node>}
V=$(mio pages tree get "$PAGE_ID" --hub hub_abc123 --jq .draft_version)
mio pages tree get "$PAGE_ID" --hub hub_abc123 --jq '{root: .tree}' > tree.json
# ...edit tree.json...
mio pages tree set "$PAGE_ID" --hub hub_abc123 --if-match "$V" --file tree.json
```

The backend's read path deliberately unwraps before answering (`bare_node =
resolved.get("root", resolved)` — its DTO documents `tree` as "the resolved draft
tree ROOT node — bare (not wrapped in `{root: ...}`)"), while the write path rejects
anything without a top-level `root` (`Tree must have a 'root' key at the top level.`).
`--jq .tree` alone therefore produces a file that `tree set` refuses — you must
re-wrap with `--jq '{root: .tree}'`. `pages catalog scaffold` already emits the *set*
shape, which is why the scaffold→set pipe needs no transform.

#### `--if-match`, and why the first write is the odd one out

- `pages tree set --if-match` is **optional and defaults to `0`** — omit it only for
  the **first** tree on a page that has never had a draft (`pages tree get` 404s
  until one exists). The `0` default does **not** bypass the concurrency guard:
  sending `0`/stale against a page that already has a draft is a `409`, so you can
  never silently clobber.
- **Every other write needs the real version.** A page produced by
  `mio hubs scaffold` already has a draft — `draft_version` is `1`, not `0` — so the
  first tree write *you* make against it needs `--if-match 1`. Never guess: read it
  back with `mio pages tree get <page_id> --jq .draft_version`.
- `pages publish --if-match` is **always required**, and takes the `draft_version`
  the *set* returned (a fresh page: set at `0` → publish at `1`).

#### Hand-added nodes need ids you mint yourself

`tree set` rejects a tree with `Every tree node must have an 'id' key` — required on
every node at every depth, though the backend's validator treats the **root**'s id as
optional. Nothing validates the id's FORMAT: the catalog's own starters ship
`id:"root"`, `id:"hero"` and friends, and those are accepted. The CLI has no
id-minting helper, so **prefer splicing scaffolded subtrees over hand-writing nodes**
— `pages catalog scaffold` mints a fresh UUIDv7 for every node it emits. When you do
add one by hand, generate a UUID (`uuidgen | tr 'A-Z' 'a-z'`,
`python3 -c 'import uuid;print(uuid.uuid4())'`) rather than reusing a template's
outline marker: ids must be **unique within the tree**, and splicing two scaffolded
subtrees that both call a node `"root"` is the realistic way to collide.

### 7. Publish the hub

```bash
mio hubs update hub_abc123 --published
```

Now reachable by members at your hub-frontend host + slug.

**`--published` on `hubs update` enables nothing else. Set registration explicitly.**
The registration and moderation defaults are injected by `HubService.create()` only —
`update()` has no such code — so flipping `is_private` later does **not** turn
self-registration on. Concretely:

| | `hubs create` | `hubs update --published` |
|---|---|---|
| `settings.registration.enabled` | defaulted to `true` **only when the hub is created public** (`--published` at create time), and never over an explicit value | **untouched** |
| `meta.moderation.enabled` | defaulted to `true` for **every** hub, private included — it is not a publish side-effect at all | **untouched** |

So the safe sequence for a hub you created private and are publishing now is:

```bash
mio hubs update hub_abc123 --published --registration-enabled
mio hubs retrieve hub_abc123 --jq .registration_enabled   # verify: expect true
```

> This corrects the previous version of this document, which said `--published`
> auto-enables both. MIO-2539 asked for that behaviour to be documented; **the
> ticket's premise was wrong** — the behaviour does not exist on `update`, and
> moderation was never tied to publishing. An agent that trusted the old text shipped
> a public hub with self-registration **off**.

## The page-tree render contract

The API validates a tree's **structure**, not its **renderability**. Everything in
this section is a case where a wrong shape returns `200` and renders nothing
(MIO-2539, MIO-2663, MIO-2664).

### Node envelope — `value` is TOP LEVEL, not `settings.value`

```json
{ "id": "019f…-uuid", "kind": "headline",
  "value": "Welcome to Acme",
  "settings": { "level": 1, "weight": 700 } }
```

A section node is the same envelope plus a `template` and children:

```json
{ "id": "019f…-uuid", "kind": "container", "template": "row",
  "settings": { "maxWidth": "content", "padding": 0,
                "surface": { "padding": "section", "background": { "type": "tint" } } },
  "children": [ … ] }
```

- **`value` is a sibling of `settings`.** Putting the text in `settings.value`
  is the single biggest silent-drop trap: the API stores it, the renderer reads
  `node.value`, and you get an empty heading/button/image with a `200` and no error.
  The catalog's own `page-about` starter shows the shape.
- **The renderer dispatches on `kind`**, never on `type`. `template` is a *secondary*
  annotation: it marks the node as a section for the publish-time converter and opts
  it into the surface wrapper.
- **`settings` should always be present** (`{}` minimum). There is no defaults
  cascade — every value that must render has to be inlined on its own node.
- **Hard limits the write enforces (422, not a silent drop):** the tree is capped at
  **500 elements** counting every child including malformed ones, `root` must carry a
  `children` array (even an empty one), and `children`/`settings`/`dataSource`, when
  present, must be array/object/object respectively.
- **Exactly one `level:1` headline per page.** Extra level-1 headlines are demoted to
  `<h2>` (they still render, but the page's `<h1>` is whichever came first).

### Which kinds carry a `value` — the sixteen leaf kinds

`value` is only meaningful on the frontend's `LeafKind` set. These sixteen, and
what the renderer expects in `value`:

| kind | `value` |
|---|---|
| `headline` | the heading text |
| `text` | the body text |
| `image` | the image URL |
| `video` | the video URL |
| `button` | the **label** (there is no `label` setting) |
| `icon` | the **glyph name** (not `settings.name`) |
| `divider` | — (none) |
| `progress-ring` | — uses a numeric **`settings.value`**, the one exception to the top-level rule |
| `quote` | an **object**: `{quote, name?, profession?, avatarUrl?, avatarFallback?}`. `quote` must be a non-empty string or the node renders nothing |
| `countdown` | the **ISO-8601 deadline** string (e.g. `2026-09-01T00:00:00Z`); empty/unparseable and the node renders nothing |
| `plan-card` | an **object**: `{name, price?, currency?, period?, description?, badge?, save?}`. `name` must be a non-empty string or the node renders nothing; `period` is `month`/`year` (rendered as `/mo`/`/yr`) |
| `logo` | — (none; branding-bound, configured via settings) |
| `theme-toggle` | — (none) |
| `plan-price` | — (none; subscribes to a `plan-group` via `settings.groupId`) |
| `doodle` | — (none; shape picked via `settings.variant`) |
| `featured-icon` | — (none; glyph via `settings.icon` or `settings.label`) |

> This table is **hand-maintained** — the catalog does not record which kinds take a
> `value`. `LeafKind` in mio-hub `src/lib/page-tree/types.ts` is authoritative, and
> the drift test can only check these nine against the catalog's kind list, not
> against mio-hub. Treat it as the least trustworthy table in this document.

`subheadline`, `paragraph`, `embed`, `html`, `spacer`, `stat`, `input` and `section`
**are not node kinds** — they were removed from the frontend or never existed, and
authoring one gets you a blank node. Use `headline` with `level: 3` instead of
`subheadline`, `text` instead of `paragraph`, and a container `gap` instead of
`spacer`.

**Button `action` is `{"type": …, "value": …}`**, and its `value` is always
canonical for its `type` — no `mailto:`, no leading `#`:

```json
{"kind":"button","value":"Browse the library",
 "settings":{"size":"lg","action":{"type":"url","value":"https://example.com/x"}}}
```

Per `type`: **`url` takes a FULL URL including its scheme** (`https:`, `http:`,
`tel:`, `sms:`) and is passed through untouched, so a schemeless `example.com/x` yields
a non-navigating button; `email` takes a bare address (the renderer adds `mailto:`);
`scroll` a bare anchor id (it adds the `#`); `page` a system page type, a content-page
UUID, or a `/`-prefixed hub path; `playlist` a playlist id. Optional
`action.params` is a string map merged onto the resolved href. A malformed or missing
action renders a non-navigating button. `settings.href` is a deprecated alias.

### Every kind's settings — generated from the catalog

Property names, types, enums and defaults below come straight from the embedded
catalog's `settingsSchema` (`go generate ./...` rewrites this block; a test fails the
build if it drifts). This is the pin the CLI ships; the backend
pins the catalog separately, so on an unfamiliar backend confirm with
`mio pages catalog templates` (its stderr names the source and version).
`core` settings change what the node *is*; `presentational` settings change how it
looks. An unlisted property is not read by the renderer. Properties are alphabetical
— Go maps lose the catalog's declaration order.

<!-- catalog-gen:node-settings -->
**`accordion`** — accepts children
- core: `defaultExpanded` *array* of *string* · `expansion` *string* `single|multiple`

**`banner`** — accepts children
- presentational: `appearance` *string* `tint|solid|page` default `tint` · `contentWidth` *string* `full|content` default `full` · `justify` *string* `center|between` default `center` · `reveal` *string* `always|after-scroll` default `always` · `sticky` *boolean* default `false` · `tone` *string* `warning|info` default `info`

**`button`** — no children
- core: `action` *object* {params, type, value} · `actionFromScope` *string* · `href` *string* · `labelFrom` *string*
- presentational: `compactMobile` *boolean* default `false` · `disabled` *boolean* · `fullWidthMobile` *boolean* default `false` · `icon` *string* · `iconRight` *string* · `newTab` *boolean* default `false` · `size` *string* `sm|md|lg` default `md` · `variant` *string* `primary|secondary|ghost-light|overlay-light|destructive|link|muted|ghost-dark|overlay-dark|ghost-primary` default `primary`

**`carousel`** — accepts children
- core: `loop` *boolean* default `true` · `slidesPerView` *number* default `1`
- presentational: `autoplay` *boolean* default `false` · `bleed` *boolean* · `effect` *string* `slide|blur-fade|marquee` default `slide` · `interval_ms` *number* · `marqueeSpeed` *number* default `30` · `show_dots` *boolean* default `true`

**`container`** — accepts children
- presentational: `background` *string* `default|muted|accent` · `maxWidth` *string* `content|search|4xl|6xl|7xl` · `padding` *number* `0|2|4|6|8|12|16` · `rounded` *boolean*

**`content-card`** — accepts children
- core: `actionFromScope` *string*
- presentational: `surface` *object → shared:surface*

**`countdown`** — no children
- core: `ctaAction` *object* {params, type, value} · `ctaLabel` *string* · `expiredLabel` *string* default `Registration is now closed` · `title` *string*
- presentational: `align` *string* `center|start` default `center` · `hideOnExpire` *boolean* default `false` · `size` *string* `small|large` default `large`

**`cta-slot`** — no children
- core: `name` *string*
- presentational: `variant` *string* `primary|secondary` default `primary`

**`divider`** — no children
- presentational: `inset` *number* default `0` · `opacity` *number* default `15` · `orientation` *string* `horizontal|vertical` default `horizontal` · `size` *string* `small|normal|large` default `normal` · `spacing` *number* `0|2|4|6|8` default `4` · `variant` *string* `line|wave` default `line` · `weight` *number*

**`doodle`** — no children
- presentational: `align` *string* `start|center|end` · `draw` *boolean* default `false` · `drawDelay` *number* default `0` · `flip` *boolean* default `false` · `hideOnMobile` *boolean* default `false` · `rotate` *number* `90|180|270` *(freeform — any other string is legal too)* · `size` *string* `sm|md|lg|xl` default `md` · `strokeWidth` *number* default `2` · `variant` *string* `arrow-plain|arrow-straight|underline|knot-curl` default `arrow-plain`

**`featured-icon`** — no children
- presentational: `draw` *boolean* default `false` · `icon` *string* · `label` *string* · `size` *string* `small|normal|large` default `normal` · `tone` *string* `accent|destructive|neutral` default `accent` · `variant` *string* `solid|accent-overlay|outline` default `solid`

**`field`** — no children
- core: `actionFromScope` *string* · `name` *string* · `role` *string* `title|subtitle|meta|body`
- presentational: `align` *string* `left|center|right` · `clamp` *number* · `fade` *boolean* · `icon` *string* · `marginBottom` *number* `0|1|2|3|4|6|8` · `muted` *boolean* · `optional` *boolean* · `ring` *object* {size, value} · `size` *string* `title|subtitle|body-big|body|body-small` · `tone` *string* `default|primary` · `weight` *number* `400|500|600|700`

**`file-attachments`** — no children
- core: `attachments` *array* of *object* · `contentId` *string*

**`file-player`** — no children
- core: `contentId` *string* · `fileId` *string* · `playlistId` *string*

**`grid`** — accepts children
- presentational: `cols` *number* `1|2|3|4|6|12` · `gap` *number* `1|2|3|4|6|8|12` · `variant` *string* `legacy|ds-content|responsive`

**`headline`** — no children
- core: `level` *number* `1|2|3|4|5|6` default `2`
- presentational: `align` *string* `left|center|right` default `left` · `highlight` *untyped* of *string* · `highlightStyle` *string* `accent|wash` default `accent` · `highlightUnderline` *boolean* · `size` *string* `title|large-title|xl-title` · `weight` *number* `400|500|600|700` default `400`

**`horizontal-scroll`** — accepts children
- presentational: `gap` *number* `2|4|6|8` · `itemWidth` *string* `auto|card` · `snap` *string* `none|start|center` default `start`

**`icon`** — no children
- presentational: `color` *string* `default|primary|muted` default `default` · `size` *number* `16|20|24|32|48` default `24` · `strokeWidth` *number*

**`image`** — no children
- core: `alt` *string*
- presentational: `alignX` *string* `center|start` default `center` · `aspectRatio` *string* `16:9|4:3|1:1|auto` default `16:9` · `lightbox` *boolean* · `maxWidth` *number* `352|128` · `objectFit` *string* `cover|contain` default `cover` · `outline` *boolean* · `radius` *string* `control|control-l|m`

**`logo`** — no children
- presentational: `height` *string* `normal|hero` default `normal`

**`media-slot`** — no children
- core: `alt` *string* · `name` *string* · `preset` *string* `thumbnail-160|medium-720|large-1440|webp-medium`
- presentational: `aspectRatio` *string* `16:9|4:3|1:1|auto` · `objectFit` *string* `cover|contain` · `outline` *boolean* · `progressBar` *boolean* · `radius` *string* `control|control-l|m` · `width` *number*

**`plan-card`** — no children
- presentational: `defaultSelected` *boolean*

**`plan-group`** — accepts children
- presentational: `label` *string* default `Choose a plan`

**`plan-price`** — no children
- core: `groupId` *string*
- presentational: `fallback` *object* {period, price}

**`progress-ring`** — no children
- core: `label` *string* · `value` *number* · `valueFrom` *string*
- presentational: `disabled` *boolean* · `mobileSize` *number* · `size` *number* default `64` · `variant` *string* `default|white`

**`quote`** — no children
- presentational: `showAvatar` *boolean* default `true`

**`row`** — accepts children
- presentational: `align` *string* `start|center|end|stretch` · `fullWidth` *boolean* · `gap` *number* `1|1.5|2|2.5|3|4|5|6|8|12|section` · `justify` *string* `start|center|end|between|around` · `maxWidth` *number* `800` · `mobileGap` *number* `1.5|3|6` · `responsive` *boolean* · `reverse` *boolean* · `split` *boolean* · `wrap` *boolean*

**`search-bar`** — no children
- core: `placeholder` *string*

**`stack`** — accepts children
- presentational: `align` *string* `start|center|end|stretch` · `fitMobile` *boolean* default `false` · `gap` *number* `0|0.5|1|1.5|2|2.5|3|4|5|6|8|12` · `grow` *boolean* · `justify` *string* `start|center|end|between` · `mobileGap` *number* `1.5|3|4|6` · `px` *number* `0.5` · `surface` *object → shared:surface* · `width` *string* `full|1/2|1/3|1/4|2/3|3/4|fit`

**`tabs`** — accepts children
- *no settings — presentation is fully derived*

**`text`** — no children
- presentational: `align` *string* `left|center|right` default `left` · `clamp` *number* · `highlight` *untyped* of *string* · `highlightTone` *string* `wash|strong` default `wash` · `italic` *boolean* · `marginBottom` *number* `0|1|2|3|4|6|8` · `muted` *boolean* · `size` *string* `body|small|body-big` · `tone` *string* `primary` · `variant` *string* `eyebrow` · `weight` *number* `400|500|600|700`

**`theme-toggle`** — no children
- *no settings — presentation is fully derived*

**`video`** — no children
- core: `embed_type` *string* `native|iframe` default `native`
- presentational: `autoplay` *boolean* default `false` · `controls` *boolean* default `true` · `loop` *boolean* default `false` · `muted` *boolean* default `true`

<!-- /catalog-gen -->

### Containers and layout — there is no `sidebar`

`sidebar` is **not** a node kind. A sidebar layout is a `row` whose `stack` children
carry `settings.width` — see `kind:row` and `kind:stack` in the generated settings
above for the exact `width` enum and the responsive/wrap/gap/align properties.

### Sections: `template` + `surface`

A node is treated as a **section** when it carries a `template`. The catalog's
section templates are:

<!-- catalog-gen:section-templates -->
`hero` · `carousel` · `grid` · `content-grid` · `row` · `search-bar` ·
`compact` · `content-card` · `testimonials`
<!-- /catalog-gen -->

Carrying a `template` also decides whether the node gets wrapped in the surface
renderer, and that set is **narrower than the template list** — a template opts in by
declaring a `surface` property in the catalog (presence is enough; even `{}` counts).
These do:

<!-- catalog-gen:surface-templates -->
`hero` · `carousel` · `grid` · `content-grid` · `row` · `search-bar` ·
`compact` · `testimonials`
<!-- /catalog-gen -->

`content-card` is the only section template that does **not** — it is a card recipe,
not a page section. Any other `template` string lays out plain, with no surface.
(`compact` is the frontend's name for the legacy "scroll" strip; there is no `scroll`
section type — `compact`'s catalog *label* is "Scroll", its id is not.)

**A section must carry its `template`** — the CLI rejects a blank/non-string one up
front, but an *absent* one just means the node renders without its section surface.

### `settings.surface` — generated from the catalog

<!-- catalog-gen:surface-properties -->
`settings.surface` accepts these keys, all optional — an omitted key means
"no override", and this is the complete set the resolver reads:

- `background` *object → shared:background*
- `borderRadius` *string* `none|sm|md|lg|full|hub-s|hub-m` *(freeform — any other string is legal too)*
- `clip` *boolean*
- `edge` *object* {bottom, top}
- `elevate` *boolean*
- `gradient` *object → shared:gradient*
- `margin` *string* *(freeform — any other string is legal too)*
- `maxHeight` *number* `800|1000|1200`
- `minHeight` *number* `500|440`
- `minScreenHeight` *number* `60|70|80|90`
- `padding` *string* `none|sm|md|lg|xl|section|gutter|gutter-b-mobile|hero-mobile-insets|card` *(freeform — any other string is legal too)*
- `shadow` *string* `none|sm|md|lg|xl`
- `translate` *string* *(freeform — any other string is legal too)*
- `visibility` *object* {desktop, mobile}

Plus 6 key(s) the validator unions onto **every** node's settings, whatever its kind:

- `name` *string*
- `role` *string* `connect|reveal|prove|close`
- `salesMeta` *object* {compactGroup, prompt, section, shape}
- `slot` *string*
- `surface` *object → shared:surface*
- `tab_label` *string*
<!-- /catalog-gen -->

### `surface.background` — the enum, and two traps

<!-- catalog-gen:surface-background -->
| property | type | values / default |
|---|---|---|
| `blur` | *boolean* | — |
| `fade` | *string* | `down` |
| `glowSpread` | *number* | `1\|2` |
| `glowStrength` | *number* | `1\|2` |
| `glowTint` | *boolean* | — |
| `token` | *string* | `primary\|secondary\|muted\|accent\|background` |
| `tone` | *string* | `neutral\|primary` · default `neutral` |
| `type` | *string* | `none\|color\|custom-color\|tint\|image\|gradient\|gradient-glow\|gradient-tint` |
| `url` | *string* | — |
| `value` | *string* | — |
<!-- /catalog-gen -->

`tint` is the only value any scaffold emits. `type:"color"` with `token:"primary"`
also stamps `data-bg="primary"`, which is what produces the bold band **and**
auto-inverts primary buttons inside it. On `type:"image"` the secondary-tint scrim is
**always** composed over the image — that is not authorable, and `blur` only adds a
further 25px layer on top of it.

- **Trap 1 — an off-enum `background.type` is accepted on publish and renders a
  transparent row with no error.** The renderer resolves an unknown discriminant to
  no class and no style, and there is no warning in production. If a band looks
  see-through, the type string is wrong. The catalog states its own limitation here:
  the enum constrains `type`, but every variant field is optional and **nothing
  validates that you supplied the field your `type` needs** — `{"type":"custom-color"}`
  with no `value`, or `{"type":"image"}` with no `url`, passes validation and renders
  nothing.
- **Trap 2 — the gradient config is a SIBLING of `background`, not nested inside
  it.** Nesting it is silently ignored and you get the default `split` gradient:

  ```json
  "surface": {
    "padding": "section",
    "background": {"type": "gradient"},
    "gradient": {"type": "monochrome"}
  }
  ```

#### `surface.gradient`

<!-- catalog-gen:surface-gradient -->
| property | type | values / default |
|---|---|---|
| `customEnd` | *string* | — |
| `customStart` | *string* | — |
| `type` | *string* | `complementary\|analogous\|triadic\|monochrome\|split\|warm-shift\|custom` |
<!-- /catalog-gen -->

Only consulted when `background.type == "gradient"`. A non-`custom` type resolves
against the hub theme's `primary`. `custom` needs **both** `customStart` and
`customEnd` as valid hex; a missing or malformed pair falls back to `split`. An
unrecognized `background.token` renders no background at all.

### Vocabulary — generated from the catalog

Every node kind the catalog knows, split by whether it accepts children. A kind not
listed here and not a system kind (below) is unknown: the API stores it, the renderer
drops it.

**System kinds are real but not yours to author.** The frontend registry renders ten
kinds the catalog deliberately omits — `login-form`, `register-form`,
`onboarding-form`, `account-profile-form`, `account-activity-feed`,
`member-directory-list`, `discussion-list`, `notification-feed`, `achievement-list`,
`leaderboard`. They are `SystemKind`s, require `system: true`, and the **page-type
route injects them**; the catalog's own page templates hand you only the slots around
them (`page-login`'s starter is `editable-top` + `editable-bottom` with no form node
in between). The backend's tree validator checks shape, not kinds, so one WOULD be
stored — but you would be hand-authoring an undocumented contract into a page whose
route already supplies it. Scaffold the matching `page-*` template and fill the
editable slots instead.

<!-- catalog-gen:node-kinds -->
Containers (`childRules` accepts children):

`accordion` · `banner` · `carousel` · `container` · `content-card` · `grid` ·
`horizontal-scroll` · `plan-group` · `row` · `stack` · `tabs`

Childless (`childRules: "none"`):

`button` · `countdown` · `cta-slot` · `divider` · `doodle` · `featured-icon` ·
`field` · `file-attachments` · `file-player` · `headline` · `icon` · `image` ·
`logo` · `media-slot` · `plan-card` · `plan-price` · `progress-ring` · `quote` ·
`search-bar` · `text` · `theme-toggle` · `video`
<!-- /catalog-gen -->

Compiled section types (`pages sections create --type`; the writable subset is
`pages catalog section-types --writable-only`):

<!-- catalog-gen:section-types -->
`feature` · `row` · `grid` · `carousel` · `content-grid` · `search` ·
`compact` · `testimonials` · `calendar`
<!-- /catalog-gen -->

Page templates (`pages catalog scaffold --template …`):

<!-- catalog-gen:page-templates -->
`page-homepage` · `page-login` · `page-register` · `page-onboarding` ·
`page-account-activity` · `page-account-profile` · `page-members` ·
`page-file-detail` · `page-discussions-index` · `page-generic` ·
`page-homepage-community` · `page-about` · `page-faq` · `page-sales`
<!-- /catalog-gen -->

These lists track the catalog version the CLI ships; `mio pages catalog templates` /
`section-types` always print the truth for the backend you are talking to.

## Render-contract checklist — run this before you call a page done

Each item is a shape the API accepts and the renderer then fails to honour. The
failures are NOT all the same, and the difference is where you will go looking:
some DROP the node, some DISCARD one setting and render the node anyway. Three
of them `pages tree set` now rejects client-side (exit 2, before any HTTP), so
you will not reach the API with them at all.

- **Text in `settings.value` instead of the node's top-level `value`** — see above.
  This is the one that bites first. **Rejected client-side** (MIO-2575). If you
  bypass the CLI: the value is never read, and what you see depends on the kind —
  headline/text/image/video render empty, `icon` falls back to the **star** glyph,
  `quote` renders nothing at all, `button` renders with a blank label.
- **`weight` must be numeric** — a number like `700`, never a CSS keyword like
  `"bold"`. A non-numeric weight is DISCARDED, not dropped: the node still
  renders, with the kind's fallback (headline → 400/normal, text and `field` →
  no weight class — and for a `field` that also discards the weight its `role`
  would have applied), so you are hunting a wrong font weight, not a missing node.
  (`pages tree set` catches this one client-side, before any HTTP.)
- **A section must carry its `template`** (`"hero"`, `"carousel"`, `"row"`, …). The
  catalog scaffold sets it; a blank or non-string one is rejected client-side.
- **Button nodes need the correct `action` shape.** A malformed/missing `action`
  leaves a button that renders but navigates nowhere.
- **An off-enum `surface.background.type`** renders a transparent row, no error.
- **An untyped `header`/`footer` menu item**, or a `mobile` item whose `icon` is not
  one of the eight whitelisted names, is dropped by the navigation parser.
- **`section_count` is your "did it apply" signal.** `mio pages publish` returns a
  `page_publishes` resource with `section_count` and `gate_count`. If `section_count`
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
CID=$(mio contacts retrieve ctt_abc123 -o plain --jq .contact_id)
mio hub-memberships add "$CID" --hub hub_abc123
```

## Command reference (compact)

Discover everything with `mio --help`, `mio <group> --help`, or the machine-readable
index at `mio gen-docs --dir ./docs`. Core groups:

- **Auth/context:** `mio whoami` · `mio config set|get|list` · `mio teams list|switch` · `mio api-keys create|list`
- **Hubs:** `mio hubs scaffold|templates` · `mio hubs create|retrieve|update|list` · `mio hubs policies get|update|gate` · `mio hubs navigation list|add|remove|reorder`
- **Pages:** `mio pages create|list|retrieve|home` · `mio pages catalog templates|section-types|scaffold` · `mio pages tree get|set` · `mio pages publish` · `mio pages sections create|list|reorder`
- **Media:** `mio media files upload|list|durable-url` · `mio media playlists create|set-cover` (+ `playlists items add|list|remove|reorder`) · `mio media hub-media publish` · `mio media hub-playlists publish` · `mio media search` · `mio media transcripts get|edit|revert`
- **Community:** `mio community spaces create|list` · `mio community discussions create|list|update` · `mio community moderation ...`
- **Contacts/members:** `mio contacts list|retrieve` · `mio contact-attributes ...` · `mio tags ...` · `mio hub-memberships add|set-role|ban` · `mio segments ...`
- **Commerce:** `mio products ...` · `mio coupons ...` · `mio checkout ...`
- **Automation/email:** `mio automations ...` · `mio email ...` · `mio webhook-endpoints ...`

When a member verb returns exit `4`, re-check you passed `.contact_id` (global),
not `.id` (team-contact). When a page/card doesn't render despite a `200`, re-check
the render-contract section above — start with the node `value` shape.
