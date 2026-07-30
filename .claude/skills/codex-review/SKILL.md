---
name: codex-review
description: Use when the user asks for a Codex review, a second-opinion review, or runs /codex-review in the mio-cli (Go/Cobra) repo. Sends the current branch diff (or a specific file/branch) to Codex gpt-5.6-sol for multi-round review, fixes findings, and resubmits until APPROVE or 3 rounds max. Falls back to a blind review when Codex is out of credits or hangs — the gate is never skipped.
---

# Codex Code Review (mio CLI)

Multi-round code review via Codex `gpt-5.6-sol` for the **mio-cli** Go/Cobra repo. Sends the diff, gets findings, fixes issues, resubmits until APPROVE or 3 rounds max. A second opinion from a different model family — complementary to `go vet`, `golangci-lint`, and the repo's contract/drift tests. When Codex is unreachable, the **blind review** below is the substitute; the gate is never skipped.

**One command, three modes** — auto-detected from the diff:

| Mode       | Triggered when                                             | Prompt depth |
| ---------- | ---------------------------------------------------------- | ------------ |
| **Commit** | 1 commit in scope (or a single file `$ARGUMENTS`)          | Light — correctness, obvious bugs, tests |
| **Phase**  | 2-9 commits sharing one `MIO-NN` ticket                    | Medium — adds design compliance, prior-fix verification |
| **Branch** | 10+ commits or mixed tickets (full branch vs main)         | Deep — architecture, cross-cutting concerns, merge readiness |

Invoke the same way every time: `/codex-review`. The mode picks itself.

## Variables

- `TARGET`: optional `$ARGUMENTS` — file path, branch name, or empty (defaults to `git diff main...HEAD`)
- `MAX_ROUNDS`: 3
- `CODEX_MODEL`: `gpt-5.6-sol`
- `CODEX_TIMEOUT`: `900` (15 min — the hard ceiling for any single call)
- `CODEX_BIN`: `$(command -v codex)` (fails loudly if codex is not on PATH)

## Codex CLI reference

Invoke via CLI (the MCP server is unreliable). Codex reviews; it never fixes — fixes are applied by Claude in this session.

```bash
printf '%s' "$PROMPT" > /tmp/codex-prompt.txt
timeout "$CODEX_TIMEOUT" codex exec -m gpt-5.6-sol -c model_reasoning_effort="xhigh" \
  --sandbox read-only -C "$(pwd)" - < /tmp/codex-prompt.txt
```

Resume the same session for follow-up rounds (preserves context, no diff re-upload):

```bash
echo "Round 2 — fixes applied: ..." | timeout "$CODEX_TIMEOUT" codex exec resume --last \
  -m gpt-5.6-sol -c model_reasoning_effort="xhigh" -
git status --porcelain   # MUST be clean — see the resume caveat below
```

| Flag | Purpose |
|---|---|
| `-m gpt-5.6-sol` | Always gpt-5.6-sol (not gpt-5.5 — repinned MIO-2836) |
| `-c model_reasoning_effort="xhigh"` | Extra High reasoning |
| `--sandbox read-only` | Review-only. `exec` already defaults to approval `never`, so this alone gives full autonomous read + command access with **no** write |
| `-C <repo>` | Working directory |
| `-` | Read the prompt from stdin (works for `exec` **and** `exec resume` on v0.145.0) |

### NEVER pass `--full-auto` (MIO-2836)

`--full-auto` is deprecated and **silently overrides `--sandbox read-only` regardless of flag order**. Measured on v0.145.0:

```
codex exec --sandbox read-only              -> approval: never  sandbox: read-only
codex exec --full-auto                      -> approval: never  sandbox: workspace-write [workdir, /tmp, $TMPDIR]
codex exec --full-auto --sandbox read-only  -> approval: never  sandbox: workspace-write [workdir, /tmp, $TMPDIR]
codex exec --sandbox read-only --full-auto  -> approval: never  sandbox: workspace-write [workdir, /tmp, $TMPDIR]
```

This skill carried `--full-auto --sandbox read-only` until MIO-2836, so every review this repo has ever run gave Codex **write access to the working tree** while claiming a read-only guarantee. `--full-auto` buys nothing — autonomy comes from `exec`'s approval policy, not from the sandbox mode. If you find yourself re-adding it because "Codex isn't reading files", the cause is a missing `bubblewrap`, not the sandbox mode (see **Reliability** below).

Confirm the header of any run you start: it must print `sandbox: read-only`.

### `exec resume` cannot be sandboxed

```
$ codex exec resume --last --sandbox read-only -
error: unexpected argument '--sandbox' found
```

Resume rounds always run **workspace-write** — the read-only guarantee is unenforceable there. So:

- Run `git status --porcelain` after **every** resume round and confirm it is clean.
- If a round must be strictly read-only, start a fresh `exec --sandbox read-only` session instead of resuming (you pay a diff re-upload).

## Reliability — timeout and quota (read before every invocation)

Neither a hang nor a quota response is a verdict. Never let either silently pass or fail the gate.

**Timeout.** Always wrap in `timeout "$CODEX_TIMEOUT"` and run in the **foreground**. Never background-and-poll — that is how a stall goes unnoticed. Exit code `124` means the call was killed for hanging, not that the code is fine.

On a `124`, the usual cause is Codex's sandbox stalling on file reads because `bubblewrap` is absent — a trivial prompt still returns, which masks it:

```bash
command -v bwrap >/dev/null || sudo apt-get install -y bubblewrap   # then retry once, keeping full autonomy
```

If it still hangs, fall back to a single pre-captured diff file (`git diff "$BASE"..HEAD > /tmp/codex-review.diff`) and tell Codex to read **only** that file. This is narrower — it sees the changed lines but not the surrounding code or tests — so **say in the report that the round saw the diff only**. If even that times out, the round is **inconclusive**: surface it, do not force-approve.

**Quota.** A quota or credit response is a retry, not a REQUEST CHANGES. Detect case-insensitively on `out of credits`, `usage limit`, `quota`, `rate limit`, `429`, `too many requests`, `try again`, `resets at`. Codex usually reports when it returns — parse that and sleep until then rather than polling blindly, then retry the same round. Echo each wait with a timestamp so the pause is visible in the transcript.

If credits are exhausted with no reset in sight, **do not skip the gate** — switch to the blind review below.

```
ERROR: Your workspace is out of credits. Ask your workspace owner to refill in order to continue.
```

is the exact string seen when the workspace is dry; it arrives instantly and is easy to mistake for a normal empty response.

## Workflow

### 1. Detect the mode

```bash
COMMITS=$(git log main..HEAD --oneline | wc -l)
TICKETS=$(git log main..HEAD --format=%s | grep -oE 'MIO-[0-9]+' | sort -u | wc -l)
# 1 commit → COMMIT; 2-9 commits & 1 ticket → PHASE; 10+ commits OR 2+ tickets → BRANCH
```
A single-file `$ARGUMENTS` forces COMMIT mode.

### 2. Gather context — and get the gates green FIRST

Codex needs a passing build to give useful signal. Before Round 1, confirm all green (this is CI parity):

```bash
go build ./...            # compiles
go vet ./...              # vet clean
gofmt -l cmd internal     # prints nothing
golangci-lint run ./...   # 0 issues (v2.12.2 — same as CI)
go test ./... -race -timeout 120s
```

Then collect: `git log <base>..HEAD --oneline`, `git diff <base>..HEAD --stat`, `git diff --name-only <base>..HEAD`, and the pass count. For PHASE/BRANCH also gather design docs (`docs/`) and reference files that show the convention this work should follow (e.g. `cmd/pages.go` for a hub-scoped resource, `cmd/products.go` as the reference resource).

### 3. Build the prompt

Prepend the **shared repo-context block** (below), then the mode-specific body, then the **output format**.

### 4. Invoke Codex (Round 1), parse verdict + findings, report to the user.

### 5. Triage & fix (if REQUEST CHANGES)

- **Critical** → fix now; write a regression test that fails against the buggy code FIRST (TDD), verify RED, then fix.
- **Important** → fix now unless there's a clear reason to defer (document it).
- **Low** → fix if cheap, else capture as a follow-up ticket. Don't auto-fix Low unless asked — keep the diff focused.

Re-run all gates from Step 2. Commit with a conventional message + this repo's footer (`Co-Authored-By:` + `Claude-Session:`), e.g. `fix(<scope>): address codex review round N findings`.

### 6. Resume (Round 2+)

```bash
echo "Round 2 — fixes applied.
Fixed:
- [Critical] <desc> — regression test at <file>:<line>, verified RED before fix
- [Important] <desc> — <what changed>
Deferred:
- [Low] <desc> — tracked as follow-up
Please re-review and issue a new verdict." | timeout "$CODEX_TIMEOUT" codex exec resume --last \
  -m gpt-5.6-sol -c model_reasoning_effort="xhigh" -
git status --porcelain   # resume runs workspace-write; confirm Codex wrote nothing
```

Loop until **APPROVE** or the 3-round cap.

### 7. Completion

- **APPROVED** — report final verdict, round count, all fixes applied.
- **Cap hit without APPROVE** — stop, surface remaining issues, ask the user to accept the risk, escalate, or rework. **Never force-approve.**

## Prompt templates

### Shared repo-context block (prepend to every prompt)

```
# Repo context (mio CLI)

- Go 1.25.7, Cobra command tree (spf13/cobra); one file per resource group under cmd/.
- HTTP layer: internal/client builds the JSON:API v1.1 envelope { data: { type, attributes } };
  the resource `type` is DERIVED from the URL path via knownCollections + typeOverrides.
- Output: internal/output — JSON off a TTY (agent default), table on a TTY; --jq / --raw / -o.
- Errors: internal/errs — STABLE exit-code contract: 0 ok, 1 generic, 2 usage/400/409/422,
  3 auth/401/403, 4 not-found/404, 5 destructive-blocked, 6 rate-limit/429, 7 upstream/5xx.
- Scope: team-scoped resources need --team; hub-scoped also need --hub.

# Repo conventions (enforce strictly)

- Flag->attribute mapping via cmd/flags.go helpers (setStringFlag / setMappedString / setBoolFlag /
  setMappedJSONObjectFlag ...). ONLY flags the user Changed() are sent — partial-update (PATCH)
  semantics: an unset flag must NEVER appear in the request body.
- CLI flags are kebab-case; backend attribute keys are snake_case.
- Whole-blob JSONB fields (hub branding/navigation/settings/meta) are assigned WHOLESALE server-side —
  update commands must read-modify-write, never forward a partial that clobbers sibling keys.
- Destructive verbs (delete/cancel/refund) require --yes in a non-TTY, else exit 5.
- A new write resource whose last path segment != its JSON:API type needs a typeOverride (and a
  knownCollections token for hyphenated/action segments) in internal/client, each with a unit test.
- Contract/drift tests (cmd/*_test.go) capture the exact method + path + request body + exit code and
  assert NO request fires on a usage error. Keep write_path_drift / jake_qa_drift / contract /
  resolve_wiring green.
- Docs parity (hand-maintained, NOT test-enforced): docs/internal/api-surface.md, llms.txt, README.md,
  AGENTS.md must be updated for any new command or flag.
```

### COMMIT mode body

```
# Single-commit code review
Commit: <SHA> <subject>
Files changed: <list>
Gates: build/vet/lint/gofmt clean; <N> tests passing.

Review for:
- Correctness — does it do what the subject says?
- Partial-update safety — are only Changed() flags serialized? Any whole-blob clobber?
- Exit-code contract — usage errors exit 2 BEFORE any HTTP call; correct code per status?
- JSON:API type derivation — does the envelope `type` resolve correctly for this path?
- Tests — contract test captures the exact body + exit code + no-request-on-error?
- Style — anything golangci-lint / go vet would flag?

Be strict on correctness, relaxed on nits. Reserve Critical for real bugs.
```

### PHASE / BRANCH mode body

Use the COMMIT review points plus the focus blocks below (pick by what changed), then for BRANCH add: architecture coherence across cmd/ + internal/client, previously-caught-bug verification, whole-suite test quality, and merge readiness (leftover TODO/FIXME, documented limitations, follow-up tickets). BRANCH is the LAST pass before merge — critical blockers only, acknowledge prior rounds, don't re-litigate.

### Output format (all modes)

```
Verdict: APPROVE / REQUEST CHANGES / REJECT

Findings:
- [Critical]  description — file:line
- [Important] description — file:line
- [Low]       description — file:line

Cross-cutting observations: 1-3 sentences.
```

## Focus areas by work type

**New command / resource group (`cmd/*.go`)**
```
- init() registers the command on its parent AND rootCmd; flags on the right cmd (create-only vs shared).
- Path helper builds the correct route; scope resolved via the shared context helper (auth + requireTeam/requireHub).
- Response rendered via c.render (respects -o/--jq/--raw).
- Only Changed() flags copied into attrs; kebab->snake key; mapped/inverted polarity correct.
- JSON-object flags validated as objects (@file supported); malformed/non-object -> ExitUsage, NO request fired.
- Whole-blob field updates read-modify-write, not partial overwrite.
```

**Client work (`internal/client`)**
```
- resourceType derived correctly (knownCollections + typeOverrides); hyphenated/action segments handled.
- Envelope { data:{ type, attributes } }; Content-Type application/vnd.api+json; headers (If-Match) forwarded.
- HTTP status -> exit code mapping correct (2/3/4/6/7); list pagination (page[size]/page[after]) built right.
```

**Tests / drift / docs**
```
- Contract test asserts method + path + EXACT body + exit code; error paths assert no HTTP request fired.
- New type derivation covered by a client unit test.
- api-surface.md / llms.txt / README / AGENTS.md updated; drift tests green.
```

## Blind review — the substitute when Codex is unavailable

When Codex is out of credits, hung past fallback, or otherwise unreachable, the gate does **not** get skipped. Run a blind review instead: a fresh agent with no prior context, whose agreement is worth something precisely because it was never told what to agree with.

This is currently the operative path — the workspace has been out of credits, and blind review is what has actually been catching defects. It found real problems on every green PR it was pointed at, including two Criticals (a silent wrong-hub write, and a `DELETE` issued against a collection rather than a member).

### What the reviewer gets

- The **diff** and the commit range.
- The **primary sources** it needs to check claims against — the relevant `mio-backend` route/serializer on `origin/main`, the catalog schema, the mio-hub consumption site. Tell it to `git fetch origin` first and read `origin/main`, never the working tree.
- The **repo conventions** block from this skill, and the exit-code contract.
- The **environmental hazards** — point it at `.claude/skills/cli-prime/SKILL.md` §3 rather than restating them. In particular: the two credential-leak test failures, and that it must never write to `~/.config/mio/` (isolate with `XDG_CONFIG_HOME=$(mktemp -d)`).

### What the reviewer must NOT get

- **The PR body, ticket description, or commit message prose.** These assert what the change accomplishes; a reviewer that reads them tends to verify the story rather than the code.
- **Your reasoning**, or any summary of what you believe the change does.
- **A list of what to check**, and above all not a pointer at the part you are unsure about. Steering it toward a known weak spot forfeits the only thing that makes its agreement worth anything: if you tell it where to look, a clean report means nothing about everywhere else.
- Prior rounds' findings, on a first pass.

State this explicitly in the brief — "you have not been told what this change is supposed to do, and that is deliberate; verify against the primary sources, not against any claim."

### What to ask for

Same output format as Codex (verdict + findings by severity + file:line). Additionally require it to:

- **Name what it could not verify** and why, rather than staying silent — an unverifiable claim is a finding.
- **Distinguish parse sites from consumption sites.** A setting being read into a struct proves nothing about whether anything acts on it; three wrong answers about hub theme mode came from stopping at the parse site.
- **Check guards by mutation, not by reading.** If the diff adds a test or preflight check, the question is not "does this look right" but "what edit to the implementation would this fail on" — and if the answer is "none", that is a Critical (`.claude/rules/verifying-guards.md`).

### After the review

Triage findings exactly as for Codex (Critical → fix now with a regression test verified RED; Important → fix or document the deferral; Low → fix if cheap or ticket it). For a second round, brief a **fresh** reviewer on the updated diff rather than resuming the first — a blind reviewer that has seen its own prior findings is no longer blind.

## When NOT to use

- Documentation-only or formatting-only changes — skip it.
- Trivial renames/refactors — skip it.
- Build/tests failing — fix those first; Codex needs green gates for useful signal.
- The user says "skip Codex" — honor it.

## Cost awareness

~100–300k tokens per round (COMMIT ~50–150k, PHASE ~150–300k, BRANCH ~250–500k). Worth it — catches bugs 100× cheaper than production.

## Rules

- **Never force-approve.** REQUEST CHANGES after round 3 → hand back to the user.
- Never skip/bypass `go vet`, `golangci-lint`, or tests to make a fix pass — fix the root cause.
- **`--sandbox read-only`, never `--full-auto`.** Codex reviews; Claude applies fixes. Confirm the run header prints `sandbox: read-only`, and `git status --porcelain` after every resume round (resume cannot be sandboxed).
- **A timeout or a quota response is not a verdict.** Retry, fall back, or switch to a blind review — never let either silently pass or fail the gate.
- Base branch is `main`, never `master`.
- **`.claude/rules/verifying-guards.md` applies to every fix round.** Any guard you add or change to close a finding must be broken and observed failing before you claim it works. A fix round is the highest-risk code in the repo: it ships with a comment asserting coverage nobody has tested. Seven unfailable guards reached (or nearly reached) `main` in a single week; reading caught none of them.
- **If Codex is unavailable, run a blind review — never skip the gate.** See the Blind review section above for the full procedure.

## Checklist before declaring a review done

- [ ] All target changes committed before Round 1
- [ ] build / vet / gofmt / golangci-lint / `go test -race` all green before Round 1
- [ ] Mode auto-detected correctly (or overridden)
- [ ] Codex invoked with `--sandbox read-only` and **no `--full-auto`**; run header confirmed `sandbox: read-only`
- [ ] Every invocation wrapped in `timeout "$CODEX_TIMEOUT"` and run in the foreground
- [ ] `git status --porcelain` clean after each resume round (resume ignores `--sandbox`)
- [ ] Critical findings fixed with a regression test verified RED against the bug
- [ ] **Every guard added or changed this round mutation-tested** — implementation broken, guard observed failing by name, mutation reverted (`.claude/rules/verifying-guards.md`)
- [ ] After any non-trivial rebase, at least one mutation re-run — auto-merge can silently produce an unfailable test with nobody writing one
- [ ] Important findings fixed or explicitly deferred with justification
- [ ] Round 2+ used `codex exec resume --last`
- [ ] Final verdict APPROVE (or escalated at the 3-round cap); fixes committed
