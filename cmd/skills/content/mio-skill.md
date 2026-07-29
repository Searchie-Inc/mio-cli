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
- **Output:** JSON when piped/non-interactive (agent default), table on a TTY. Force with `--output json`. Filter inline with `--jq '<expr>'` (no external `jq` needed), e.g. `HUB_ID=$(mio hubs list --jq '.[0].id')`. Use `--raw` for the unflattened JSON:API envelope (`meta`/`links`/`included`).
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
mio hubs scaffold --template community --name "Acme" --slug acme \
  --primary-color '#B91C1C' --secondary-color '#0F172A' --text-color '#111827' \
  --logo-url https://cdn.example.com/logo.png \
  --dry-run                                          # prints the ordered plan, changes nothing
HUB_ID=$(mio hubs scaffold --template community --name "Acme" --slug acme \
  --primary-color '#B91C1C' --publish --jq .hub_id)  # --publish goes live; default is private
```

- Branding flags **merge** over the template's palette — a key you don't name keeps
  the template's value. `--primary-color` also fills `header_color` unless you gave
  one yourself. `--branding-json` takes a whole object; scalar flags win over it.
- **Re-runs are safe**: `mio hubs scaffold --template community --hub "$HUB_ID"`
  resumes. A page you edited, or a foreign page at a template slug, exits `2` and is
  **never** overwritten.
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
| `secondary` | **section headings (h1–h3) AND the base of every `surface.background:{"type":"tint"}` block** |
| `text` | body copy |
| `background` | page background |
| `header_color` / `header_accent` | top-nav background + accent (emitted raw — no contrast correction) |
| `dark_mode` (bool) | dark theme |
| `logo_url` / `favicon_url` / `social_image_url` | imagery |
| `font_heading` / `font_body` / `heading_font_size` / `body_font_size` | typography |

- **Foot-gun: `secondary` is not a decorative accent.** In light mode it resolves to
  the frontend's `--foreground`, so it colors every heading *and* tints every `tint`
  surface. Set it light and every heading goes invisible on a white page — the most
  common branding mistake. Pick a dark, high-contrast value.
- **Colors must be 6-digit hex** (`#0F766E`). The frontend's branding parser rejects
  3-digit and 8-digit hex and falls back to its own defaults, silently.
- **`logo_url`/`favicon_url` are the exception to "stored verbatim".** The rest of
  the branding blob has no server schema, but these two are validated server-side:
  an absolute `https://` URL is required, and `data:`/`javascript:` URLs are rejected
  (MIO-2658). A `data:` SVG logo fails — upload the asset and use a durable URL.
- **Dark/light:** `--branding-json '{"dark_mode":false,"background":"#ffffff"}'` is
  the reliable lever. The frontend also honours `settings.theme.mode`
  (`light`|`dark`|`custom`), and it **wins over `branding.dark_mode`** where they
  disagree — but `theme` is not on the CLI's settings-key allowlist yet, so
  `--settings-json '{"theme":{"mode":"dark"}}'` warns on stderr (and is still sent).
  Don't pair it with `--strict-keys`.

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
  --privacy public --is-home --jq .id)
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

<!-- catalog-sync:row-variants -->
`1col` · `2eq` · `2left` · `2right` · `3eq` · `4eq` · `bound-cards` · `cta-band` · `faq`
<!-- /catalog-sync -->

`1col`/`2eq`/`3eq`/`4eq` are equal splits; `2left` is 2/3 + 1/3 and `2right` is
1/3 + 2/3. Three are content presets that arrive **already filled with placeholder
`value`s** — the fastest way to see the contract in practice: `cta-band` (three
tint-surfaced cards, each headline + text + button), `faq` (a full-width `accordion`
of `text` items keyed by `settings.tab_label`), and `bound-cards` (three
`content-card`s bound to a `dataSource` — you must fill the `dataSource.id`s).
`hero`, `grid` and `compact` carry variants too; `mio pages catalog templates`
prints them all.

#### The tree envelope: `get` returns one shape, `set` wants another

```bash
# get →  {"id": …, "tree": {"root": …}, "draft_version": N}   ← the tree is NESTED
# set ←  {"root": …}                                          ← the tree itself
V=$(mio pages tree get "$PAGE_ID" --hub hub_abc123 --jq .draft_version)
mio pages tree get "$PAGE_ID" --hub hub_abc123 --jq .tree > tree.json   # unwrap
# ...edit tree.json...
mio pages tree set "$PAGE_ID" --hub hub_abc123 --if-match "$V" --file tree.json
```

Feeding a `tree get` response straight back into `tree set` is rejected: `--file`
must contain the tree object itself (`{"root": …}`), not the resource that wraps it.
`--jq .tree` is the whole unwrap. `pages catalog scaffold` already emits the `set`
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

`tree set` rejects a tree with `Every tree node must have an 'id' key`. The CLI has
no id-minting helper: `pages catalog scaffold` mints fresh UUIDv7 ids for every node
it emits, so **prefer splicing scaffolded subtrees over hand-writing nodes**. When
you must add one by hand, generate a UUID per node (`uuidgen | tr 'A-Z' 'a-z'`,
`python3 -c 'import uuid;print(uuid.uuid4())'`) — ids must be unique within the
tree, and literal outline markers like `"root"`/`"hero"` are not valid node ids.

### 7. Publish the hub

```bash
mio hubs update hub_abc123 --published
```

Now reachable by members at your hub-frontend host + slug. **`--published`
auto-enables Registration + Moderation** as a side effect (v0.9.0 behaviour — older
docs claiming they stay off are stale) — set them deliberately afterward if that's
not what you want.

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
- **Exactly one `level:1` headline per page.** Extra level-1 headlines are demoted to
  `<h2>` (they still render, but the page's `<h1>` is whichever came first).
- The **only** exception to the top-level-`value` rule is `progress-ring`, which
  reads a numeric `settings.value`.

### Leaf kinds and their settings

The renderer's leaf set is exactly these eight. `subheadline`, `paragraph`,
`embed`, `html`, `spacer`, `stat`, `input` and `section` **are not node kinds** —
they were removed from the frontend (or never existed), and authoring one gets you
a blank node. Use `headline` with `level: 3` instead of `subheadline`, `text`
instead of `paragraph`, and a `stack`/`row` `gap` instead of `spacer`.

| kind | `value` | settings that matter |
|---|---|---|
| `headline` | text | `level` 1–6 (default **2**), `weight` 400/500/600/700 (default 400 — set `700` for titles), `align` left/center/right |
| `text` | text | `size` `body`\|`small`, `weight`, `align`, `clamp` 1–6 (renders a show-more control), `marginBottom` 0/1/2/3/4/6/8 (default **2**), `muted` bool, `tone:"primary"` |
| `image` | the image URL | `alt`, `aspectRatio` `16:9`(default)\|`4:3`\|`1:1`\|`auto`, `objectFit` `cover`(default)\|`contain` (forces natural aspect), `radius` `control`\|`control-l`(default)\|`m`, `outline` bool. `width`/`height` are accepted but are **no-ops** |
| `video` | the video URL | `embed_type` `native`(default)\|`iframe` — note the snake_case; `controls`(true), `autoplay`(false), `loop`(false), `muted`(**true**). There is no `provider` setting; a disallowed iframe host renders a visible error box |
| `button` | the label | `action` (below), `variant` (default `primary`), `size` `sm`\|`md`\|`lg`, `icon`/`iconRight` (sprite names), `newTab` bool, `disabled` bool |
| `icon` | **the glyph name** (not `settings.name`) | `size` 16/20/24/32, `color` `default`\|`primary`\|`muted`, `strokeWidth`. An unknown name falls back to `star` rather than vanishing |
| `divider` | — | `spacing` 2/4/6/8 (default 4) |
| `progress-ring` | — (uses `settings.value`) | `value` 0–100, `size` 16/24/48/64/76, `variant` `default`\|`white`, `label` |

**Button `action` is `{"type": …, "value": …}`**, and `value` is always canonical —
no scheme prefix, no `#`:

```json
{"kind":"button","value":"Browse the library",
 "settings":{"size":"lg","action":{"type":"url","value":"https://example.com/x"}}}
```

`type` is `url` | `page` | `email` | `scroll` | `playlist`. `email` takes a bare
address, `scroll` a bare anchor id, `page` a hub-relative `/slug/...` path or page
id, `playlist` a playlist id. A malformed or missing action renders a non-navigating
button. The legacy `settings.href` still works but is deprecated.

### Containers and layout — there is no `sidebar`

Layout kinds: `stack` (vertical), `row` (horizontal), plus `grid`, `carousel`,
`horizontal-scroll`, `tabs`, `container`, `content-card`, `accordion`.

`sidebar` is **not** a node kind. A sidebar layout is a `row` whose `stack` children
carry `settings.width` from `full` | `1/2` | `1/3` | `2/3` | `1/4` | `3/4` — e.g.
`2/3` + `1/3`. `row` also takes `responsive` (stacks on narrow screens), `wrap`,
`gap`, `mobileGap`, `align`, `justify`; `stack` takes `gap`, `align`, `width` and
its own `surface`.

### Sections: `template` + `surface`

A node is treated as a **section** when it carries a `template`. The catalog's
section templates are:

<!-- catalog-sync:section-templates -->
`hero` · `carousel` · `grid` · `content-grid` · `row` · `search-bar` · `compact` · `content-card`
<!-- /catalog-sync -->

Of those, exactly seven — everything except `content-card`, which is a card recipe
rather than a page section — opt the node into the surface renderer:
`hero`, `carousel`, `grid`, `content-grid`, `row`, `search-bar`, `compact`. Any
other `template` string lays out plain. `compact` is the frontend name for the
legacy "scroll" strip.

**A section must carry its `template`** — the CLI rejects a blank/non-string one up
front, but an *absent* one just means the node renders without its section surface.

`settings.surface` is `{"padding": "section", "background": {...}, "gradient": {...}}`.

### `surface.background` — the enum, and two traps

| value | renders |
|---|---|
| `{"type":"tint"}` | a light tint of `secondary` — the only value any scaffold emits |
| `{"type":"color","token":"primary\|secondary\|accent\|muted\|background"}` | solid theme color. `primary` also stamps `data-bg="primary"`, which is what produces the bold band **and** auto-inverts primary buttons inside it |
| `{"type":"custom-color","value":"#rrggbb"}` | solid inline color (invalid hex → nothing) |
| `{"type":"gradient"}` | gradient — configure it via the **sibling** `surface.gradient` |
| `{"type":"image","url":"<durable-url>","blur":true}` | image layer + an automatic `secondary` scrim. Key is `url`; `overlay` is deprecated and ignored |
| `{"type":"none"}` | explicitly no background |
| `{"type":"thumbnail"}` | accepted, currently a **no-op** — renders nothing |

- **Trap 1 — an invalid `background.type` is accepted on publish and renders a
  transparent row with no error.** The renderer's switch has a `default` case that
  returns no class and no style. There is no warning in production. If a band looks
  see-through, the type string is wrong. (`image` and `thumbnail` also fall through
  that `default` — `image` paints via a separate layer; `thumbnail` genuinely does
  nothing yet.)
- **Trap 2 — the gradient config is a SIBLING of `background`, not nested inside
  it.** Nesting it is silently ignored and you get the default `split` gradient:

  ```json
  "surface": {
    "padding": "section",
    "background": {"type": "gradient"},
    "gradient": {"type": "monochrome"}
  }
  ```

  `gradient.type` is `monochrome` | `analogous` | `complementary` | `triadic` |
  `split` | `warm-shift` | `custom`. `custom` **requires** `customStart` +
  `customEnd` (6-digit hex); either missing or malformed and it falls back to
  `split`. An unknown `color` token also falls back to no background.

### Vocabulary (kept in sync with the embedded catalog by a test)

Node kinds the catalog knows — anything else is an unknown node:

<!-- catalog-sync:node-kinds -->
`accordion` · `button` · `carousel` · `container` · `content-card` · `cta-slot` ·
`divider` · `field` · `file-attachments` · `file-player` · `grid` · `headline` ·
`horizontal-scroll` · `icon` · `image` · `media-slot` · `progress-ring` · `row` ·
`search-bar` · `stack` · `tabs` · `text` · `video`
<!-- /catalog-sync -->

Compiled section types (`pages sections create --type`, writable subset via
`pages catalog section-types --writable-only`):

<!-- catalog-sync:section-types -->
`feature` · `row` · `grid` · `carousel` · `content-grid` · `search` · `compact` · `testimonials` · `calendar`
<!-- /catalog-sync -->

Page templates (`pages catalog scaffold --template …`):

<!-- catalog-sync:page-templates -->
`page-homepage` · `page-login` · `page-register` · `page-onboarding` ·
`page-account-activity` · `page-account-profile` · `page-members` ·
`page-file-detail` · `page-discussions-index` · `page-generic` ·
`page-homepage-community` · `page-about` · `page-faq`
<!-- /catalog-sync -->

These lists track the catalog version the CLI ships; `mio pages catalog templates`
/ `section-types` always print the truth for the backend you are talking to.

## Silent-drop checklist — run this before you call a page done

Every item returns `200` and then drops the node/section/card at render time. Check
each one against the tree you just published:

- **Text in `settings.value` instead of the node's top-level `value`** — see above.
  This is the one that bites first.
- **`weight` must be numeric** — a number like `700`, never a CSS keyword like
  `"bold"`. A string weight is dropped. (`pages tree set` catches this one
  client-side, before any HTTP.)
- **A section must carry its `template`** (`"hero"`, `"carousel"`, `"row"`, …). The
  catalog scaffold sets it; a blank or non-string one is rejected client-side.
- **Button nodes need the correct `action` shape.** A malformed/missing `action`
  leaves a button that renders but navigates nowhere.
- **An off-enum `surface.background.type`** renders a transparent row, no error.
- **An untyped `header`/`footer` menu item**, or a `mobile` item whose `icon` is not
  one of the eight whitelisted names, is dropped by the navigation parser.
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
- **Hubs:** `mio hubs scaffold|templates` · `mio hubs create|retrieve|update|list` · `mio hubs policies update|gate` · `mio hubs navigation list|add|remove|reorder`
- **Pages:** `mio pages create|list|retrieve|home` · `mio pages catalog templates|section-types|scaffold` · `mio pages tree get|set` · `mio pages publish` · `mio pages sections create|list|reorder`
- **Media:** `mio media files upload|list|durable-url` · `mio media playlists create|set-cover` (+ `playlists items add|list|remove|reorder`) · `mio media hub-media publish` · `mio media hub-playlists publish` · `mio media search` · `mio media transcripts get|edit|revert`
- **Community:** `mio community spaces create|list` · `mio community moderation ...`
- **Contacts/members:** `mio contacts list|retrieve` · `mio contact-attributes ...` · `mio tags ...` · `mio hub-memberships add|set-role|ban` · `mio segments ...`
- **Commerce:** `mio products ...` · `mio coupons ...` · `mio checkout ...`
- **Automation/email:** `mio automations ...` · `mio email ...` · `mio webhook-endpoints ...`

When a member verb returns exit `4`, re-check you passed `.contact_id` (global),
not `.id` (team-contact). When a page/card doesn't render despite a `200`, re-check
the render-contract section above — start with the node `value` shape.
