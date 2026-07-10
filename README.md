# mio

The official command-line interface for [Membership.io](https://membership.io).

`mio` is agent-first: JSON output by default when piped, environment-variable auth, and stable exit codes so scripts and AI agents can branch deterministically.

---

## Prerequisites

- **Prebuilt binary / curl install:** none — the binary is self-contained and statically linked.
- **`go install` or building from source:** [Go 1.25+](https://go.dev/dl/) (the module targets Go 1.25).

---

## Install

**`mio` is released.** Pick whichever you prefer.

### curl (recommended — macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/Searchie-Inc/mio-cli/main/scripts/install.sh | sh
```

Detects your OS/arch, verifies the SHA-256 checksum, and installs `mio` into `/usr/local/bin` (you'll be asked for sudo). Install somewhere you own instead with `PREFIX`:

```sh
curl -fsSL https://raw.githubusercontent.com/Searchie-Inc/mio-cli/main/scripts/install.sh | PREFIX="$HOME/.local/bin" sh
# then make sure that dir is on your PATH, e.g. add to ~/.zshrc:
#   export PATH="$HOME/.local/bin:$PATH"
```

Pin a specific version with e.g. `VERSION=0.2.0`.

### Prebuilt binary (any platform)

Download the archive for your OS/arch from the [latest release](https://github.com/Searchie-Inc/mio-cli/releases/latest), extract it, and put `mio` on your `PATH`:

- macOS / Linux: `mio_<version>_<os>_<arch>.tar.gz`
- Windows: `mio_<version>_windows_amd64.zip`

where `<version>` is the release tag, e.g. `0.2.0`.

`checksums.txt` is published alongside for verification.

### go install (Go 1.25+)

```sh
go install github.com/Searchie-Inc/mio-cli@latest
```

> **Note:** `go install` names the binary **`mio-cli`** (after the module path), not `mio`, and installs it to `$(go env GOPATH)/bin` (make sure that's on your `PATH`). Rename it if you like:
> ```sh
> mv "$(go env GOPATH)/bin/mio-cli" "$(go env GOPATH)/bin/mio"
> ```
> Or just use the curl / prebuilt-binary install above for a ready-to-go `mio`.

### Homebrew — coming soon

`brew install searchie-inc/tap/mio` isn't enabled yet (the tap is being provisioned). Use the curl installer or a prebuilt binary for now.

### From source

```sh
git clone https://github.com/Searchie-Inc/mio-cli
cd mio-cli
go build -o mio .
./mio version
```

Verify any install:

```sh
mio version   # → mio 0.2.0 (commit …, built …)
```

Update a release install later from the CLI:

```sh
mio update              # latest release
mio update --version 0.2.1
```

`mio update` reruns the official installer into the directory containing the
current executable. Use `--prefix <dir>` to install somewhere else.

---

## First run

```sh
# 1. Install (see above) — e.g. the curl one-liner.

# 2. Authenticate (pick one)
mio login                              # interactive — stores a key in your OS keychain
# …or, for scripts / CI / agents:
export MIO_API_KEY=mio_sk_live_xxxxx   # no login step needed

# 3. Confirm setup
mio whoami                             # prints resolved user, team, hub, api-base, key source

# 4. You're ready
mio contacts list
```

Auth works against `https://api.member.dev` by default. Point elsewhere with `--api-base <url>` or `MIO_API_BASE_URL` (see [Authentication](#authentication)).

If you belong to multiple teams, `mio login` will prompt you to pick one. You can switch later with `mio teams switch <team-id>`, which updates your local context. For single-team accounts the team is resolved automatically — no manual config step needed.

---

## Authentication

### `mio login` (interactive)

Run `mio login` and choose one of two paths:

1. **Paste an existing API key** — a `mio_sk_live_…` key, validated against the API and stored.
2. **Email + password** — logs you in (JWT), then auto-mints an API key named `mio-cli@<host>` on your team and stores it. **Your password is never saved** — only the minted key is kept.

The key is stored in your **OS keychain** (with an encrypted-file fallback on headless/CI environments where no keychain is available). `mio logout` deletes it.

### API key (CI / scripts / agents)

Skip `mio login` entirely — just set the environment variable:

```sh
export MIO_API_KEY=mio_sk_live_xxxxx
```

### Resolution order

The key is read in this order (first match wins):

```
--api-key <key>   →   MIO_API_KEY env   →   key stored in the OS keychain
```

### Pointing at a non-production backend

```sh
mio --api-base https://api.staging.membership.io contacts list
# or
export MIO_API_BASE_URL=https://api.staging.membership.io
```

---

## Discovering commands

`mio` is fully self-documenting:

```sh
mio --help                          # top-level: all resources + global flags
mio <resource> --help               # e.g. mio contacts --help — actions for a resource
mio <resource> <action> --help      # e.g. mio contacts create --help — flags for an action
mio gen-docs --dir ./docs           # generate the full Markdown reference (one file per command)
```

When in doubt, append `--help` to any command.

---

## Quickstart

```sh
# Confirm auth + active team
mio whoami

# Contacts
mio contacts list
mio contacts create --email alice@example.com --first-name Alice --last-name Smith
mio contacts retrieve <id>
mio contacts update <id> --last-name "Jones"
mio contacts delete <id> --yes

# Hubs
mio hubs list
mio hubs create --name "My Hub" --slug my-hub
# Author the presentation layer at create time (opaque JSONB blobs; inline JSON or @file).
# --logo-url merges into --branding-json rather than replacing it.
mio hubs create --name "My Hub" --slug my-hub \
  --branding-json '{"primary":"#6747E3","heading_font_size":32}' \
  --navigation-json @nav.json \
  --settings-json '{"policies":{"enabled":true}}' \
  --meta-json '{"discussions":{"enabled":true}}'
# Author the header/footer menu on an existing hub. Each item needs a "type"
# (url|page|playlist|discussions) — untyped items are rejected. Whole-blob replace.
mio hubs update <hub-id> --navigation-json '{"header":[{"type":"url","label":"Home","href":"/my-hub/","position":0}]}'

# Products & prices  (the product id is a positional argument, not a flag)
mio products list
mio products create --name "Pro Plan" --description "Full access"
mio products prices list <product-id>
mio products prices create <product-id> --amount 4900 --currency usd --interval month

# Segments — preview who matches a condition tree (does not save)
mio segments search --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":"@example.com"}]}]}'
mio segments members <segment-id>
```

`segments search` takes the **full condition tree** (matching the backend write shape), not a flat list. You can also read it from a file and paginate:

```sh
mio segments search --conditions @conditions.json --page-size 50 --page-after <cursor>
```

---

## Global Flags

Every command inherits these flags.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--api-key` | | env/keychain | API key. Overrides `MIO_API_KEY` and the stored key. |
| `--team` | | config | Team ID for team-scoped resources. |
| `--hub` | | config | Hub ID for hub-scoped resources. |
| `--output` | `-o` | json (piped) / table (TTY) | Output format: `json`, `table`, `plain`. |
| `--jq` | | | Filter JSON output with a [gojq](https://github.com/itchyny/gojq) expression. |
| `--raw` | | false | Emit the raw JSON:API envelope instead of the flattened resource. |
| `--yes` | `-y` | false | Skip confirmation prompts on destructive operations. |
| `--profile` | | default | Named config profile. |
| `--api-base` | | config | Override the API base URL (`MIO_API_BASE_URL`). |

---

## Output and --jq Examples

By default, `mio` flattens JSON:API responses into plain resource objects. Use `--output` to control format:

```sh
# Pipe-friendly JSON (also the auto-default when piped)
mio contacts list --output json

# Aligned table for humans
mio contacts list --output table

# key=value pairs (great for grep)
mio contacts retrieve <id> --output plain

# Raw JSON:API envelope (meta, links, included)
mio contacts list --raw

# Extract a field with jq
mio contacts retrieve <id> --jq '.email'

# Pluck IDs from a list
mio products list --jq '.[].id'

# Chain into shell variable
HUB_ID=$(mio hubs list --jq '.[0].id')
mio content list --hub "$HUB_ID"
```

---

## Exit Codes

Scripts and agents can branch on these stable codes.

| Code | Constant | Meaning |
|------|----------|---------|
| `0` | — | Success |
| `1` | `ExitGeneric` | Unexpected / generic error |
| `2` | `ExitUsage` | Bad flags, missing required argument, or rejected input (400/409/422) |
| `3` | `ExitAuth` | Missing or invalid credentials (HTTP 401/403) |
| `4` | `ExitNotFound` | Resource not found (HTTP 404) |
| `5` | `ExitNeedsConfir` | Destructive op in a non-interactive shell — pass `--yes` |
| `6` | `ExitRateLimited` | Rate limited (HTTP 429) |
| `7` | `ExitServer` | Upstream server error (HTTP 5xx) |

---

## Command Reference (by resource)

| Resource | Available actions |
|----------|-------------------|
| `login` / `logout` | Interactive auth |
| `whoami` | Print resolved identity — user, team, hub, api-base, profile, key source |
| `config` | `set`, `get`, `list` |
| `api-keys` | `create`, `list`, `retrieve`, `delete` |
| `teams` | `create`, `list`, `retrieve`, `update`, `delete`, `switch` (server-side switch + updates local context); `members list/add/remove` |
| `users` | `me`, `list`, `retrieve`, `update` |
| `roles` | `create`, `list`, `retrieve`, `update`, `delete`; `permissions list` |
| `hubs` | `create`, `list`, `retrieve`, `update`, `delete`; `policies update` |
| `contacts` | `create`, `list`, `retrieve`, `update`, `delete`, `restore` |
| `contact-attributes` | `create/list/retrieve/update/delete` defs; `options` sub-group; `hub-config` sub-group; `values get/set` |
| `tags` | `create`, `list`, `retrieve`, `update`, `delete`, `assign`, `assign-bulk`, `remove` |
| `segments` | `create`, `list`, `retrieve`, `update`, `delete`, `search`, `members`, `count` |
| `content` | `create`, `list`, `retrieve`, `children`, `update`, `delete`, `restore`, `reorder` |
| `pages` | `create`, `list`, `retrieve` (add `--tree` for raw published node tree), `update`, `delete`, `home`, `publish` (requires `--if-match <draft_version>`); `sections create/list/update/delete/reorder` |
| `products` | `create`, `list`, `retrieve`, `update`, `delete`; `prices create/list/retrieve/update/delete` |
| `checkout` | `orders list/retrieve`; `subscriptions list/retrieve/cancel`; `payments list/retrieve/refund`; `webhooks list/retrieve/replay`; `accounts list/retrieve/onboarding-link`; `stripe-sync import/import-status/adopt-product` |
| `email` | `drip-campaigns create/list/retrieve/update/delete/activate/pause` (create/update now accept `--enrollment-mode`, `--trigger-event-type`, `--segment-id`, `--segment-check-interval-minutes`, `--allow-reenrollment`); `steps create/list/update/delete`; `templates create/list/retrieve/update/delete/preview`; `config set/get/delete/test`; `enrollments list/exit`; `stats get` |
| `access-rules` | `rules create/list/retrieve/update/delete`; `overrides create/list/retrieve/update/delete` |
| `activity` | `contact`, `top-engaged` |

Run `mio <resource> --help` or `mio <resource> <action> --help` for flag details on any command, or `mio gen-docs --dir ./docs` to generate the complete reference.

---

## Config File

`mio` stores its config as **TOML** at `~/.config/mio/config.toml` (or `$XDG_CONFIG_HOME/mio/config.toml` when `XDG_CONFIG_HOME` is set). It holds non-secret context only — current team/hub, API base, and named profiles. **Your API key is never written here**; it lives in the OS keychain (managed by `mio login` / `mio logout`).

Manage it with the `config` command (writable keys: `team`, `hub`, `api-base`):

```sh
mio config set team     <team-id>
mio config set hub      <hub-id>
mio config set api-base <url>
mio config get team
mio config list            # shows all values and the config file path
```

Multiple named profiles are supported via `--profile`.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Exit code **3** (`ExitAuth`) | No key, or the key is invalid/expired | Run `mio login`, or `export MIO_API_KEY=mio_sk_live_…`. Run `mio whoami` to confirm the resolved key and active team. |
| Exit code **2** (`ExitUsage`) | Bad flag, missing argument, unknown subcommand, or rejected input | Check your flags against `mio <resource> <action> --help`. Flags are kebab-case (`--first-name`). |
| Exit code **5** (`ExitNeedsConfir`) | A destructive op (`delete`, `cancel`, `refund`) in a non-interactive shell | Re-run with `--yes` / `-y`. |
| Exit code **6** (`ExitRateLimited`) | Too many requests (HTTP 429) | Back off and retry. |
| Exit code **7** (`ExitServer`) | Upstream 5xx | Transient — retry; if it persists, the backend is down. |
| Calls fail against production | Wrong API base or stale/revoked key | Verify with `mio whoami`. Point at a different backend with `--api-base <url>` or `MIO_API_BASE_URL`. |

Errors are rendered TTY-aware: on an interactive terminal you get a friendly one-line `Error: <detail>` plus a dimmed `(exit code N)`; when stderr is piped or otherwise non-interactive you get the machine-readable JSON:API `errors` array with the exit code echoed in `meta.exit_code`. Either way the process exit code is the same, so scripts can branch on the exit code (and, off a TTY, on the body).

---

## See Also

- [AGENTS.md](./AGENTS.md) — guide for AI coding agents (non-interactive auth, exit codes, command table)
- [llms.txt](./llms.txt) — machine-readable one-line-per-command index
- `mio --help` — top-level help
- `mio <resource> --help` — resource-level help
- `mio gen-docs --dir ./docs` — generate the full Markdown reference
