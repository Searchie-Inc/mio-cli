---
name: cli-prime
description: Use at the start of a session on mio-cli to load full context — repo purpose and the agent-first contract, the gates, environmental hazards that produce false test failures and corrupt real state, the catalog pin, branch/PR/release state, and the open CLI V1 board. Run it before picking up CLI work, and cite it in subagent briefs instead of restating the hazards.
---

# CLI Prime (mio-cli session warm-up)

Run these to load context, then summarize what you found.

**If you are dispatching subagents, point them at section 3.** Every hazard there has produced a confidently-wrong claim or corrupted real state in a previous session. Restating them by hand in each brief is how they get dropped.

## 1. What this repo is

`mio-cli` is the Go/Cobra CLI for the Membership.io v3 API — **agent-first by design**, not human-first:

- **Output**: JSON off a TTY, table on a TTY (`internal/output`). `--jq` (gojq), `--raw`, `-o json|table|plain`.
- **Exit codes are a stable public contract** (`internal/errs/errs.go:19-26`). Agents and CI branch on them:

  | code | meaning | | code | meaning |
  |---|---|---|---|---|
  | 0 | ok | | 4 | not found (404) |
  | 1 | generic | | 5 | destructive needs `--yes` in a non-TTY |
  | 2 | usage / 400 / 409 / 422 | | 6 | rate limited (429) |
  | 3 | auth (401/403) | | 7 | upstream 5xx |

- **It ships its own agent skill.** `mio skills install` writes `cmd/skills/content/mio-skill.md` into an agent's skills directory, where an LLM executes it *literally*. Documentation defects here are runtime defects — the same weight as code.
- **Docs surfaces** (`README.md`, `AGENTS.md`, `llms.txt`, `docs/internal/api-surface.md`) are hand-maintained and **not** test-enforced. Only the catalog-derived blocks of `mio-skill.md` are generated.

Read `AGENTS.md` first — it is the densest description of actual behaviour.

## 2. The gates (CI parity)

```bash
export PATH="$PATH:$(go env GOPATH)/bin"   # golangci-lint is NOT on PATH by default
go build ./...
go vet ./...
gofmt -l cmd internal                      # prints nothing when clean
golangci-lint run ./...                    # v2.12.2, matching ci.yml; expect "0 issues."
XDG_CONFIG_HOME=$(mktemp -d) go test ./... -race -timeout 180s
go run ./internal/docsgen/cmd/skilldocs -file cmd/skills/content/mio-skill.md -check
```

CI installs golangci-lint with `go install …@v2.12.2` rather than using a release binary — prebuilt binaries refuse to lint a project targeting a newer Go.

`skilldocs -check` reports up-to-date without writing. To regenerate after a catalog re-pin: `go generate ./...` (directive at `cmd/skills.go:31`). `TestSkillDocIsGeneratedFromCatalog` turns a forgotten regen into a build failure instead of silent doc rot.

## 3. Environmental hazards

**Read these before writing a test result, a cross-repo claim, or a subagent brief.** Each one has already caused a wrong answer here.

### 3.1 Two tests fail from *your own credentials*, not from a regression

A bare `go test ./...` fails exactly twice on a clean tree:

```
--- FAIL: TestContract_ExitCodes_NoCredentials   contract_test.go: exit code = 4, want 3 (ExitAuth)
--- FAIL: TestWiring_SingleHubAutoDefault        resolve_wiring_test.go: content path did not use the auto-defaulted hub
```

Both read the developer's real `~/.config/mio/config.toml`. **Prefix with `XDG_CONFIG_HOME=$(mktemp -d)` and the full suite is green.** Verify before reporting either as a regression, and never "fix" them by changing the assertion.

### 3.2 Never write to `~/.config/mio/`

An agent probing UUID validation ran `mio config set current_team` against the developer's live config and destroyed it; recovery took three corroborating sources. **Never run `mio config set`, `mio auth`, or anything that writes under `~/.config/mio/`.** Isolate every invocation:

```bash
XDG_CONFIG_HOME=$(mktemp -d) go run . <args>
```

### 3.3 Fetch before asserting any cross-repo fact

Sibling checkouts in `~/src` run days and dozens of commits stale, and are often parked on a feature branch. `git fetch origin` and read `origin/main` — never the working tree.

This produced four confidently-wrong claims in one session, including "exactly eight leaf kinds" read from a mio-hub checkout 32 commits behind (`origin/main` had nine). `mio-page-catalog` moved 0.12.0 → 0.14.1 in about a day.

**`mio-backend` `origin/main` deploys straight to production on merge** — there is no staging lag. If it is on main, it is live.

### 3.4 Local backends

```bash
curl -s -o /dev/null -w '%{http_code}\n' --max-time 3 http://localhost:8000/api/v1/   # 404 = up (no route at the root)
curl -s -o /dev/null -w '%{http_code}\n' --max-time 3 http://localhost:8001/api/v1/   # 000 = down
```

`:8000` is a Docker stack whose catalog is **stale** — it predates `hubTemplates[]`, so anything template-driven fails against it. Don't kill it. `:8001` is where you run `mio-backend` `main` for current behaviour; it is usually down and you must start it yourself.

### 3.5 `--jq` alone JSON-quotes a string (MIO-2792)

`--jq` filters *before* the formatter runs (`internal/output/output.go:56-77`), so a string result goes through the JSON encoder and `$(…)` captures the quotes:

```bash
HUB_ID=$(mio hubs list --jq '.[0].id')            # WRONG — yields "019f…" with quotes; 404s downstream
HUB_ID=$(mio hubs list -o plain --jq '.[0].id')   # right
```

Numeric captures (`draft_version`, counts) are unquoted either way. Any doc or example that captures a **string** id needs `-o plain`.

### 3.6 The CLI is a conduit, not a validation layer

If the API accepts X, so does the CLI; if the API rejects X, the CLI does not pre-empt it with its own rule. The triage test for "is this a CLI bug?" is to try it against the raw API first. Client-side validation is justified only where it prevents an unrecoverable or misleading outcome, and it must mirror the server's rule rather than invent one.

## 4. Key files and the conventions that are easy to violate

| Area | Where | The rule |
|---|---|---|
| Commands | `cmd/<resource>.go`, one file per resource group | `init()` registers on the parent **and** the root; create-only flags on the create command |
| Flag → attribute | `cmd/flags.go:32-232` (`setStringFlag`, `setMappedString`, `setMappedBoolInverted`, `setMappedJSONObjectFlag`, …) | **Only flags the user `Changed()` are sent.** PATCH semantics — an unset flag must never appear in the body |
| Naming | — | CLI flags kebab-case; backend attributes snake_case |
| JSONB blobs | hub `branding` / `navigation` / `settings` / `meta` | Assigned **wholesale** server-side → update commands must read-modify-write, never forward a partial |
| JSON:API type | `internal/client/client.go:143` (`typeOverrides`), `:319` (`knownCollections`) | The envelope `type` is **derived from the URL path**. A new write resource whose last path segment ≠ its type needs an override plus a unit test |
| Destructive verbs | `cmd/root.go:429` (`confirmDestructive`), `--yes`/`-y` at `:158` | Non-TTY without `--yes` → exit 5, no request |
| Errors | `internal/errs/errs.go` | `ExitCodeForStatus` is many-to-one; carry the real status on the error when you need it back |

Contract/drift tests (`cmd/contract_test.go`, `cmd/write_path_drift_test.go`, `cmd/jake_qa_drift_test.go`, `cmd/resolve_wiring_test.go`) capture the exact method, path, request body and exit code — and assert that **no request fires** on a usage error. Keep them green.

## 5. Catalog pin

`mio-page-catalog` is the source of truth; the CLI embeds a byte-identical copy and the backend vendors the same artifact. They must agree.

```bash
cat internal/catalog/CATALOG_REF
python3 -c "import json;print(json.load(open('internal/catalog/catalog.json'))['meta']['catalogVersion'])"
(cd ../mio-backend && git fetch origin -q && git show origin/main:app/page_catalog/catalog.json) \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['meta']['catalogVersion'])"
```

Re-pin with `scripts/update_catalog_pin.sh --catalog-repo ../mio-page-catalog [--ref <sha>]` — never by hand. It moves all five artifacts together (`catalog.json`, the golden fixtures, the interpolation corpus, `CATALOG_REF`, and `pinnedDigest` in `parity_test.go`); doing it by hand is what let the CLI sit on 0.10.0 while the backend served 0.12.0. Then run `go generate ./...`.

`.github/workflows/catalog-pin-staleness.yml` watches this daily at 06:15 UTC. It self-gates on `SIBLING_APP_CLIENT_ID` + `SIBLING_APP_PRIVATE_KEY`; **without them it reports success while skipping every step**, which is precisely how the drift went unnoticed. If you touch it, confirm the steps actually ran.

## 6. State

```bash
git fetch origin -q
git status -sb
git log --oneline -8
gh pr list --state open
gh run list --limit 5
git tag --sort=-v:refname | head -3
git log --oneline $(git describe --tags --abbrev=0)..origin/main   # unreleased commits
```

Releases are tag-driven: pushing a `v*` tag runs GoReleaser (5 platform binaries + the Homebrew tap). **Merging does not deploy** — cutting a release is a separate deliberate step, so check whether shipped work is actually in a user's hands before assuming it is.

## 7. Jira

```bash
acli jira workitem search \
  --jql 'project = MIO AND component = "CLI V1" AND statusCategory != Done ORDER BY created DESC' \
  --fields key,summary,status
```

Epics: **MIO-2572** scaffold · **MIO-2665** general bug-fix & hardening (non-scaffold) · **MIO-2666** hub + page templates.

Conventions: file CLI work under component **CLI V1** (id `10257`) and parent it to the right epic. `acli jira workitem create` cannot set components — set them afterwards via the Atlassian MCP `editJiraIssue`. Transition to **In Progress** when you pick a ticket up and **Done** when it merges. Assign before starting: most tickets are unassigned and parallel sessions have collided twice.

## 8. Rules and skills already in this repo

- **`.claude/rules/verifying-guards.md` — read it.** A guard is not verified until you have watched it fail. Any PR adding or changing a test, assertion or preflight check must break the protected behaviour, observe the named failure, restore, and **say so in the PR**. Seven unfailable guards reached or nearly reached `main` in one week; reading caught none of them.
- **`.claude/skills/codex-review/SKILL.md`** — multi-round review before merge. If Codex is unavailable, the substitute is a *blind* review (a fresh agent given the diff and primary sources, but not the PR's claims or a list of what to check) — not skipping.

## 9. Then summarize

Report: branch and open PRs · gate status (naming any failure and whether it is 3.1) · catalog pin versus backend `origin/main` · unreleased commits · open CLI V1 tickets worth picking up · anything in sections 3–5 that is currently in an unexpected state.
