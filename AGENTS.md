# mio CLI — Agent Guide

This document is written for AI coding agents (Claude Code, Codex, etc.) that need to call `mio` programmatically. It covers auth, output format, the exit-code contract, destructive-op handling, and a compact command table.

---

## Authentication

Never run `mio login` in an agent. Use an API key instead:

```sh
export MIO_API_KEY=mio_live_xxxxxxxxxxxxx
```

Resolution order (first wins):

1. `--api-key <key>` flag on the command line
2. `MIO_API_KEY` environment variable
3. Keychain token stored by `mio login`

If no key is found the command exits with code **3** (`ExitAuth`). Always set `MIO_API_KEY` before running any resource command.

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

---

## Exit-Code Contract

Branch on these stable codes. Do not parse stderr for error detection.

| Code | Meaning | When to retry |
|------|---------|---------------|
| `0` | Success | — |
| `1` | Generic / unexpected error | Investigate before retry |
| `2` | Bad flags or missing argument | Fix the command, do not retry |
| `3` | Missing or invalid credentials | Set `MIO_API_KEY`, then retry |
| `4` | Resource not found (404) | Do not retry; resource does not exist |
| `5` | Destructive op blocked in non-interactive shell | Re-run with `--yes` |

---

## Destructive Operations

Any `delete`, `cancel`, `refund`, or `restore` command in a non-TTY shell will exit **5** unless `--yes` (or `-y`) is passed:

```sh
mio contacts delete <id> --yes
mio checkout subscriptions cancel <id> --yes
mio checkout payments refund <id> --yes
```

Always include `--yes` in agent scripts for these commands.

---

## Scope Flags

Most resources are team-scoped. Hub-scoped resources additionally need `--hub`.

Set defaults once via config so individual commands are shorter:

```sh
mio config set team <team-id>
mio config set hub  <hub-id>
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
| `logout` | _(interactive only)_ |
| `version` | _(no subcommand)_ |
| `config` | `set` `get` `list` |
| `api-keys` | `create` `list` `retrieve` `delete` |
| `teams` | `create` `list` `retrieve` `update` `delete` `switch` |
| `teams members` | `list` `add` `remove` |
| `users` | `me` `list` `retrieve` `update` |
| `roles` | `create` `list` `retrieve` `update` `delete` |
| `roles permissions` | `list` |
| `hubs` | `create` `list` `retrieve` `update` `delete` |
| `contacts` | `create` `list` `retrieve` `update` `delete` `restore` |
| `contact-attributes` | `create` `list` `retrieve` `update` `delete` |
| `contact-attributes options` | `create` `list` `update` `delete` |
| `contact-attributes hub-config` | `create` `list` `update` `delete` |
| `contact-attributes values` | `get` `set` |
| `tags` | `create` `list` `retrieve` `update` `delete` `assign` `assign-bulk` `remove` |
| `segments` | `create` `list` `retrieve` `update` `delete` `search` `members` `count` |
| `content` | `create` `list` `retrieve` `children` `update` `delete` `restore` `reorder` |
| `pages` | `create` `list` `retrieve` `update` `delete` `home` |
| `pages sections` | `create` `list` `update` `delete` `reorder` |
| `products` | `create` `list` `retrieve` `update` `delete` |
| `products prices` | `create` `list` `retrieve` `update` `delete` |
| `checkout orders` | `list` `retrieve` |
| `checkout subscriptions` | `list` `retrieve` `cancel` |
| `checkout payments` | `list` `retrieve` `refund` |
| `checkout webhooks` | `list` `retrieve` `replay` |
| `checkout accounts` | `list` `retrieve` `onboarding-link` |
| `checkout stripe-sync` | `import` `import-status` `adopt-product` |
| `email drip-campaigns` | `create` `list` `retrieve` `update` `delete` `activate` `pause` |
| `email steps` | `create` `list` `update` `delete` |
| `email templates` | `create` `list` `retrieve` `update` `delete` `preview` |
| `email config` | `set` `get` `delete` `test` |
| `email enrollments` | `list` `exit` |
| `email stats` | `get` |
| `access-rules rules` | `create` `list` `retrieve` `update` `delete` |
| `access-rules overrides` | `create` `list` `retrieve` `update` `delete` |
| `activity` | `contact` `top-engaged` |

---

## Minimal Agent Snippet

```sh
#!/usr/bin/env bash
set -euo pipefail

export MIO_API_KEY="${MIO_API_KEY:?MIO_API_KEY must be set}"

# Resolve team from config (or pass --team explicitly)
CONTACTS=$(mio contacts list --output json)

# Filter with --jq
FIRST_ID=$(mio contacts list --jq '.[0].id')

# Delete safely in a script
mio contacts delete "$FIRST_ID" --yes
```

---

## Useful Links

- [README.md](./README.md) — install, quickstart, global flags, full usage guide
- [llms.txt](./llms.txt) — machine-readable one-line-per-command index
- `mio <resource> --help` — full flag reference per resource
