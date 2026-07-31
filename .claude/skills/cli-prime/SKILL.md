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

- **It ships its own agent skill.** The source is embedded from `cmd/skills/content/mio-skill.md`; `mio skills install` writes it into the target agent's skills directory (`--user` by default, `--project` for `./.claude` or `./.codex`, `--target claude|codex`), where an LLM executes it *literally*. Documentation defects here are runtime defects — the same weight as code.
- **Docs surfaces** (`README.md`, `AGENTS.md`, `llms.txt`, `docs/internal/api-surface.md`) are hand-maintained and **not** test-enforced. Only the catalog-derived blocks of `mio-skill.md` are generated.

Read `AGENTS.md` first — it is the densest description of actual behaviour.

## 2. The gates

```bash
export PATH="$PATH:$(go env GOPATH)/bin"   # golangci-lint is NOT on PATH by default
go build ./...
go vet ./...
gofmt -l .                                 # prints nothing when clean — `.`, not `cmd internal`: CI checks the root too
golangci-lint run ./...                    # v2.12.2, matching ci.yml; expect "0 issues."
XDG_CONFIG_HOME=$(mktemp -d) go test ./... -race -timeout 120s
go run ./internal/docsgen/cmd/skilldocs -file cmd/skills/content/mio-skill.md -check
```

The first five mirror `.github/workflows/ci.yml`. `skilldocs -check` is **not** a separate CI step — CI covers it through `TestSkillDocIsGeneratedFromCatalog` inside `go test`, and that test's message is the more useful of the two: it names *which* generated block went stale, where `-check` only reports that the file doesn't match.

CI installs golangci-lint with `go install …@v2.12.2` rather than using a release binary — prebuilt binaries refuse to lint a project targeting a newer Go.

`skilldocs -check` reports up-to-date without writing. To regenerate after a catalog re-pin: `go generate ./...` (directive at `cmd/skills.go:31`). `TestSkillDocIsGeneratedFromCatalog` turns a forgotten regen into a red test instead of silent doc rot — `go build` stays green with a stale doc, so the suite is what catches it.

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

**But never check an exit code through `go run`.** It collapses every non-zero exit to `1`, which silently destroys the contract in §1. Measured:

| invocation | `go run .` | built binary |
|---|---|---|
| unknown flag (`ExitUsage`) | **1** | **2** |
| no credentials (`ExitAuth`) | **1** | **3** |

`go run` does print `exit status 3` on stderr and the CLI still reports `meta.exit_code` in its JSON envelope, but in the ordinary `cmd >/dev/null 2>&1; echo $?` shape the real code is simply gone. Whenever the exit code is what you are testing, build first and run the binary:

```bash
BIN=$(mktemp -d)/mio
go build -o "$BIN" . && XDG_CONFIG_HOME=$(mktemp -d) "$BIN" <args>; echo "exit=$?"
```

(Verified: this returns `3` for missing credentials and `2` for an unknown flag, where `go run` returns `1` for both.)

### 3.3 Fetch before asserting any cross-repo fact

Sibling checkouts in `~/src` run days and dozens of commits stale, and are often parked on a feature branch. `git fetch origin` and read `origin/main` — never the working tree.

This produced four confidently-wrong claims in one session, including a leaf-kind count read from a mio-hub checkout 32 commits behind. `mio-page-catalog` moved 0.12.0 → 0.14.1 in about two days (0.12.0 on 2026-07-28, 0.14.0 and 0.14.1 both on 2026-07-30) — a pin verified in the morning can be a minor version stale by evening.

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

`.github/workflows/catalog-pin-staleness.yml` watches this daily at 06:15 UTC. It self-gates on `SIBLING_APP_CLIENT_ID` + `SIBLING_APP_PRIVATE_KEY`: without them, step 2 warns and exits 0 while every later step is skipped, so **the job goes green having done nothing**.

That failure mode is not theoretical — mio-backend's equivalent watcher ran that way for weeks with the credentials unprovisioned, reporting success on every run. (This repo's own watcher arrived later, as part of the fix in #79; before it there was simply nothing watching, and the pin lived only in a Go comment.) Both hazards are the same lesson: **if you touch this workflow, confirm the steps actually ran** — a green check-mark here is not evidence.

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

Releases are tag-driven: pushing a `v*` tag runs GoReleaser and publishes 5 platform archives + `checksums.txt` (darwin amd64/arm64, linux amd64/arm64, windows amd64). **The Homebrew tap is not live** — the `brews:` block in `.goreleaser.yaml` is commented out pending `Searchie-Inc/homebrew-tap`, and `README.md` says so; installation today is the curl script or a prebuilt binary. **Merging does not deploy** — cutting a release is a separate deliberate step, so check whether shipped work is actually in a user's hands before assuming it is.

## 7. Jira

```bash
acli jira workitem search \
  --jql 'project = MIO AND component = "CLI V1" AND statusCategory != Done ORDER BY created DESC' \
  --fields key,summary,status
```

Epics: **MIO-2572** scaffold · **MIO-2665** general bug-fix & hardening (non-scaffold) · **MIO-2666** hub + page templates.

Conventions: file CLI work under component **CLI V1** (id `10257`) and parent it to the right epic. Transition to **In Progress** when you pick a ticket up and **Done** when it merges. Assign before starting: most tickets are unassigned and parallel sessions have collided twice.

**Use the `create-ticket` skill to file one** — `acli jira workitem create` has no `--component` flag, and a ticket without `component = CLI V1` never reaches the board, silently. The skill carries the verified `--from-json` payload (components *and* parent stick in one call) plus the read-back and transition gotchas.

## 8. Rules and skills already in this repo

- **`.claude/skills/create-ticket/SKILL.md`** — filing a ticket that actually reaches the board.
- **`.claude/agents/cli-code-reviewer.md`** — the blind reviewer. Dispatch it by name (`subagent_type: cli-code-reviewer`) with a diff and a scope, and nothing else; the blindness contract lives in the agent, not in your brief. Note agent definitions load at **session start**, so a newly added one is not dispatchable until the session restarts.
- **`.claude/settings.json` + `.claude/hooks/`** — a `PreToolUse` hook blocks code commits with no `MIO-<n>`. Tests: `.claude/hooks/require-jira-on-code-commit.test.sh`; run it after any edit to the hook.
- **`.claude/rules/verifying-guards.md` — read it.** A guard is not verified until you have watched it fail. Any PR adding or changing a test, assertion or preflight check must break the protected behaviour, observe the named failure, restore, and **say so in the PR**. Seven unfailable guards reached or nearly reached `main` in one week; reading caught none of them.
- **`.claude/skills/codex-review/SKILL.md`** — multi-round review before merge. If Codex is unavailable, the substitute is a *blind* review (a fresh agent given the diff and primary sources, but not the PR's claims or a list of what to check) — not skipping.

## 9. Then summarize

Report: branch and open PRs · gate status (naming any failure and whether it is 3.1) · catalog pin versus backend `origin/main` · unreleased commits · open CLI V1 tickets worth picking up · anything in sections 3–5 that is currently in an unexpected state.
