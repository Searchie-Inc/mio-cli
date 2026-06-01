# mio

The official command-line interface for [Membership.io](https://membership.io).

`mio` is agent-first: JSON output by default when piped, environment-variable auth, and stable exit codes so scripts and AI agents can branch deterministically.

---

## Install

**Homebrew (macOS / Linux)**
```sh
brew install searchie-inc/tap/mio
```

**curl one-liner (Linux / macOS)**
```sh
curl -fsSL https://get.membership.io/mio/install.sh | sh
```

**Go install**
```sh
go install github.com/Searchie-Inc/mio-cli@latest
```

After install, verify:
```sh
mio version
```

---

## Authentication

**Option A — interactive login (stores token in system keychain)**
```sh
mio login
```

**Option B — API key (CI / scripts / agents)**
```sh
export MIO_API_KEY=mio_live_xxxxxxxxxxxxx
```

The key is read in this order: `--api-key` flag → `MIO_API_KEY` env var → stored keychain token.

---

## Quickstart

```sh
# Set your default team (do this once)
mio config set team <team-id>

# Contacts
mio contacts list
mio contacts create --email alice@example.com --first-name Alice
mio contacts retrieve <id>
mio contacts update <id> --last-name "Smith"
mio contacts delete <id>

# Hubs
mio hubs list
mio hubs create --name "My Hub" --slug my-hub

# Products
mio products list
mio products create --name "Pro Plan" --description "Full access"
mio products prices list --product <product-id>
mio products prices create --product <product-id> --amount 4900 --currency usd --interval month

# Segments — preview who matches a condition set
mio segments search --conditions '[{"field":"tag","value":"vip"}]'
mio segments members <segment-id>
```

---

## Global Flags

Every command inherits these flags.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--api-key` | | env/keychain | API key. Overrides `MIO_API_KEY` and stored token. |
| `--team` | | config | Team ID for team-scoped resources. |
| `--hub` | | config | Hub ID for hub-scoped resources. |
| `--output` | `-o` | json (piped) / table (TTY) | Output format: `json`, `table`, `plain`. |
| `--jq` | | | Filter JSON output with a [gojq](https://github.com/itchyny/gojq) expression. |
| `--raw` | | false | Emit the raw JSON:API envelope instead of flattened resource. |
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

# Raw JSON:API envelope
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
| `2` | `ExitUsage` | Bad flags or missing required argument |
| `3` | `ExitAuth` | Missing or invalid credentials |
| `4` | `ExitNotFound` | Resource not found (HTTP 404) |
| `5` | `ExitNeedsConfir` | Destructive op in non-interactive shell — pass `--yes` |

---

## Command Reference (by resource)

| Resource | Available actions |
|----------|-------------------|
| `login` / `logout` | Interactive auth |
| `config` | `set`, `get`, `list` |
| `api-keys` | `create`, `list`, `retrieve`, `delete` |
| `teams` | `create`, `list`, `retrieve`, `update`, `delete`, `switch`; `members list/add/remove` |
| `users` | `me`, `list`, `retrieve`, `update` |
| `roles` | `create`, `list`, `retrieve`, `update`, `delete`; `permissions list` |
| `hubs` | `create`, `list`, `retrieve`, `update`, `delete` |
| `contacts` | `create`, `list`, `retrieve`, `update`, `delete`, `restore` |
| `contact-attributes` | `create/list/retrieve/update/delete` defs; `options` sub-group; `hub-config` sub-group; `values get/set` |
| `tags` | `create`, `list`, `retrieve`, `update`, `delete`, `assign`, `assign-bulk`, `remove` |
| `segments` | `create`, `list`, `retrieve`, `update`, `delete`, `search`, `members`, `count` |
| `content` | `create`, `list`, `retrieve`, `children`, `update`, `delete`, `restore`, `reorder` |
| `pages` | `create`, `list`, `retrieve`, `update`, `delete`, `home`; `sections create/list/update/delete/reorder` |
| `products` | `create`, `list`, `retrieve`, `update`, `delete`; `prices create/list/retrieve/update/delete` |
| `checkout` | `orders list/retrieve`; `subscriptions list/retrieve/cancel`; `payments list/retrieve/refund`; `webhooks list/retrieve/replay`; `accounts list/retrieve/onboarding-link`; `stripe-sync import/import-status/adopt-product` |
| `email` | `drip-campaigns create/list/retrieve/update/delete/activate/pause`; `steps create/list/update/delete`; `templates create/list/retrieve/update/delete/preview`; `config set/get/delete/test`; `enrollments list/exit`; `stats get` |
| `access-rules` | `rules create/list/retrieve/update/delete`; `overrides create/list/retrieve/update/delete` |
| `activity` | `contact`, `top-engaged` |

Run `mio <resource> --help` or `mio <resource> <action> --help` for flag details on any command.

---

## Config File

`mio` stores its config at `~/.config/mio/config.yaml` (XDG-compliant). Use the `config` command to manage it:

```sh
mio config set team  <team-id>
mio config set hub   <hub-id>
mio config get team
mio config list
```

Multiple named profiles are supported via `--profile`.

---

## See Also

- [AGENTS.md](./AGENTS.md) — guide for AI coding agents (non-interactive auth, exit codes, command table)
- `mio --help` — top-level help
- `mio <resource> --help` — resource-level help
