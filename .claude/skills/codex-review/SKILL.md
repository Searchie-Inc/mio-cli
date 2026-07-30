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
- `CODEX_TIMEOUT`: `900` (15 min — the hard ceiling for any single call). **Assign it in the shell you are running**; `timeout "$CODEX_TIMEOUT"` with it unset fails `timeout: invalid time interval ''` at exit 125, which the Reliability section does not otherwise cover.
- Confirm codex is present before Round 1 — `command -v codex` alone does not abort a script without `set -e`:

```bash
command -v codex >/dev/null || { echo "ABORT: codex not on PATH"; exit 1; }
CODEX_TIMEOUT=900
```

## Codex CLI reference

Invoke via CLI (the MCP server is unreliable). Codex reviews; it never fixes — fixes are applied by Claude in this session.

```bash
BASE=main                                  # or the merge-base you are reviewing against
PROMPT_FILE=$(mktemp -t codex-prompt.XXXXXX)   # never a fixed /tmp path — concurrent runs collide
build_prompt > "$PROMPT_FILE"              # you assemble this: repo-context block + mode body + output format

[ -s "$PROMPT_FILE" ] || { echo "ABORT: empty prompt — not a verdict"; exit 1; }

timeout "$CODEX_TIMEOUT" codex exec -m gpt-5.6-sol -c model_reasoning_effort="xhigh" \
  --sandbox read-only -C "$(pwd)" - < "$PROMPT_FILE" 2>&1
```

**Prove the prompt is non-empty before the call.** An empty `PROMPT_FILE` is caught by Codex itself — it prints `No prompt provided via stdin.` and exits **1** (measured on v0.145.0) — so this guard buys a clear abort rather than a rescue. The guard that genuinely matters is the one on the **diff file** in the fallback path below, where the prompt is valid and only the *content* is empty: Codex has nothing to object to, reviews nothing, and returns an APPROVE.

Resume the same session for follow-up rounds (preserves context, no diff re-upload). **Run it from the repo root, and check the `workdir:` line in the run header.** `resume` takes no `-C` (`error: unexpected argument '-C' found`) and it does **not** refuse an unexpected directory — measured from a temp dir, it starts the session with `workdir: /tmp/tmp.XXXX` and proceeds. Codex's relative paths and `git` commands then resolve inside that empty directory, so it explores nothing and reports on nothing, with no loud failure. (Reads are not themselves confined to the workdir under `read-only` — the damage is the wrong working directory, not a read barrier.) There is no guard for this but your own eyes on the header — which applies equally to the `-C "$(pwd)"` on the Round-1 call above:

```bash
echo "Round 2 — fixes applied: ..." | timeout "$CODEX_TIMEOUT" codex exec resume --last \
  -m gpt-5.6-sol -c model_reasoning_effort="xhigh" -c sandbox_mode="read-only" - 2>&1
git status --porcelain   # confirm Codex wrote nothing
```

Capture **stderr** on every call (`2>&1`): the run header, the quota banner and the credits error all go to stderr, and stdout can be entirely empty on a failed round. Reading stdout alone makes a hard failure look like a quiet success.

| Flag | Purpose |
|---|---|
| `-m gpt-5.6-sol` | Always gpt-5.6-sol (not gpt-5.5 — repinned MIO-2836) |
| `-c model_reasoning_effort="xhigh"` | Extra High reasoning |
| `--sandbox read-only` | Review-only. `exec` already defaults to approval `never`, so this alone gives full autonomous read + command access with **no** write |
| `-C <repo>` | Working directory |
| `-` | Read the prompt from stdin (works for `exec` **and** `exec resume` on v0.145.0) |

### NEVER pass `--full-auto` (MIO-2836)

`--full-auto` **overrides an explicit `--sandbox read-only`, regardless of flag order**. Measured on v0.145.0:

```
codex exec --sandbox read-only              -> approval: never  sandbox: read-only
codex exec --full-auto                      -> approval: never  sandbox: workspace-write [workdir, /tmp, $TMPDIR]
codex exec --full-auto --sandbox read-only  -> approval: never  sandbox: workspace-write [workdir, /tmp, $TMPDIR]
codex exec --sandbox read-only --full-auto  -> approval: never  sandbox: workspace-write [workdir, /tmp, $TMPDIR]
```

It does print a deprecation warning — ``warning: `--full-auto` is deprecated; use `--sandbox workspace-write` instead.`` — but that warning says nothing about it beating an *explicit* `--sandbox read-only`, which is the part that matters and the part that is easy to read past.

This skill carried `--full-auto --sandbox read-only` until MIO-2836, so every review this repo has ever run gave Codex **write access to the working tree** while claiming a read-only guarantee. `--full-auto` buys nothing — autonomy comes from `exec`'s approval policy (`never`), not from the sandbox mode. If you find yourself tempted to re-add it because "Codex isn't reading files", diagnose the actual stall first (see **Reliability** below); write access is never the fix for a read problem.

Confirm the header of any run you start: it must print `sandbox: read-only`.

### `exec resume` rejects `--sandbox` — use `-c sandbox_mode` instead

The flag is not accepted on resume:

```
$ codex exec resume --last --sandbox read-only -
error: unexpected argument '--sandbox' found
```

But the underlying config key **is**, and resume already takes `-c`:

```
codex exec resume --last -m gpt-5.6-sol -c model_reasoning_effort='xhigh' -c sandbox_mode='read-only' -
  -> approval: never   sandbox: read-only

codex exec resume --last -m gpt-5.6-sol -c model_reasoning_effort='xhigh' -
  -> approval: never   sandbox: workspace-write [workdir, /tmp, $TMPDIR]
```

So **pass `-c sandbox_mode="read-only"` on every resume round.** Without it the round silently runs workspace-write and the read-only guarantee applies only to round 1 — half a fix.

Note the workspace-write default here is environment-dependent, not universal: it comes from `trust_level = "trusted"` for this repo in `~/.codex/config.toml`. A plain `codex exec -C /home/ubuntu/src/mio-cli` with no sandbox flag prints `workspace-write` for the same reason, and `--ignore-user-config` prints `read-only`. Don't rely on the ambient default either way — set it explicitly on every call.

Still run `git status --porcelain` after each resume round. It costs nothing and it is the only check that catches a config change out from under you.

## Reliability — timeout and quota (read before every invocation)

Neither a hang nor a quota response is a verdict. Never let either silently pass or fail the gate.

**Timeout.** Always wrap in `timeout "$CODEX_TIMEOUT"` and run in the **foreground**. Never background-and-poll — that is how a stall goes unnoticed. Exit code `124` means the call was killed for hanging, not that the code is fine.

On a `124`, first retry once — a hang is usually the sandbox doing many file reads, not a slow model.

> A `bubblewrap`-absence diagnosis circulates for this (it is in mio-platform's copy, which predates the current CLI). It does **not** apply to codex-cli 0.145.0, which **bundles its own** static `bwrap` under `@openai/codex-linux-x64/vendor/…/codex-resources/bwrap`. Check `command -v bwrap` if you like, but do not `sudo apt-get install` on that theory — verify the sandbox is actually the stall point first.

If it still hangs, fall back to a single pre-captured diff file and tell Codex to read **only** that file:

```bash
BASE=${BASE:?set BASE to the base ref first}          # unset -> "...HEAD" resolves to HEAD...HEAD -> EMPTY diff
DIFF_FILE=$(mktemp -t codex-review.XXXXXX.diff)
git diff "$BASE"...HEAD > "$DIFF_FILE"
[ -s "$DIFF_FILE" ] || { echo "ABORT: empty diff — inconclusive, NOT an approval"; exit 1; }
```

**Both guards are load-bearing, and this is the dangerous path.** With `BASE` unset or empty, `git diff "$BASE"...HEAD` collapses to `HEAD...HEAD` and writes a **0-byte file, exit 0** — measured for all four combinations of unset/empty × `..`/`...`. Codex then receives a perfectly valid prompt pointing at an empty file. Unlike an empty *prompt*, which Codex rejects outright with exit 1, an empty *diff* gives it nothing to object to: the expected result is a confident APPROVE over no content. That is a gate that cannot fail, in the fallback path taken exactly when the gate is under most stress.

(The 0-byte-and-exit-0 half is measured. That Codex then returns APPROVE is inference — the workspace is out of credits, so no round can complete to confirm it. The guard is warranted either way: an empty diff is inconclusive, never an approval.)

This mode is narrower — Codex sees the changed lines but not the surrounding code or tests — so **say in the report that the round saw the diff only**. If even that times out, the round is **inconclusive**: surface it, do not force-approve.

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

Codex needs a passing build to give useful signal. Before Round 1, confirm all green. This mirrors `cli-prime` §2 (which additionally runs the `skilldocs -check`, covered here by `go test`), and the two prefixes are not optional:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"   # golangci-lint is NOT on PATH by default
go build ./...            # compiles
go vet ./...              # vet clean
gofmt -l .                # prints nothing — `.`, not `cmd internal`: CI checks root main.go too
golangci-lint run ./...   # 0 issues (v2.12.2 — same as CI)
XDG_CONFIG_HOME=$(mktemp -d) go test ./... -race -timeout 120s
```

**Without `XDG_CONFIG_HOME`, two tests fail from the developer's real credentials** — `TestContract_ExitCodes_NoCredentials` and `TestWiring_SingleHubAutoDefault`. They are environmental, not regressions. Do not "fix those first" and above all do not change their assertions; isolate and they are green. See `cli-prime` §3.1.

Then collect: `git log <base>..HEAD --oneline`, `git diff <base>...HEAD --stat`, `git diff --name-only <base>...HEAD`, and the pass count.

**Note the deliberate pairing: two dots for `log`, three for `diff`.** The token means different things to the two commands, so "be consistent" is exactly the wrong instinct:

| | `A..B` | `A...B` |
|---|---|---|
| `git log` | commits on B not on A ← **what you want** | *symmetric difference* — also lists commits on A not on B |
| `git diff` | A's tip vs B's tip | merge-base(A,B) vs B ← **what you want** |

Once `main` advances past the branch point, `git log main...HEAD` starts listing unrelated `main` commits. That inflates both the commit count and the distinct-ticket count in Step 1, which silently promotes a PHASE review to BRANCH. Step 1's counters use `..` for exactly this reason — leave them alone.

For PHASE/BRANCH also gather design docs (`docs/`) and reference files that show the convention this work should follow (e.g. `cmd/pages.go` for a hub-scoped resource, `cmd/products.go` as the reference resource).

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
  -m gpt-5.6-sol -c model_reasoning_effort="xhigh" -c sandbox_mode="read-only" - 2>&1
git status --porcelain   # belt-and-braces; must be clean
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

- Prose-only or formatting-only changes — skip it. **But `AGENTS.md`, `llms.txt`, `cmd/skills/content/mio-skill.md` and `.claude/skills/**` are not prose** — agents execute them literally, so a wrong command or a false claim in them is a runtime defect and gets the full gate. This is not hypothetical: the review of *this very skill* found a fallback command that silently handed the reviewer an empty diff, and a resume invocation that ran with write access while the file three sections up asserted read-only. Both would have shipped under a docs-only exemption.
- Trivial renames/refactors — skip it.
- Build/tests genuinely failing — fix those first; Codex needs green gates for useful signal. (First confirm they are genuine: the two credential-leak failures in Step 2 are not.)
- The user says "skip Codex" — honor it.

## Cost awareness

~100–300k tokens per round (COMMIT ~50–150k, PHASE ~150–300k, BRANCH ~250–500k). Worth it — catches bugs 100× cheaper than production.

## Rules

- **Never force-approve.** REQUEST CHANGES after round 3 → hand back to the user.
- Never skip/bypass `go vet`, `golangci-lint`, or tests to make a fix pass — fix the root cause.
- **`--sandbox read-only`, never `--full-auto`** on `exec`; **`-c sandbox_mode="read-only"`** on `resume` (which rejects the flag). Codex reviews; Claude applies fixes. Confirm the run header prints `sandbox: read-only`.
- **An empty input is not a clean review.** Prove any diff file is non-empty before the call: an unset `$BASE` yields a 0-byte diff with exit 0, and Codex will happily review it and APPROVE. (An empty *prompt* is safer — Codex rejects it with exit 1 — but guard both.)
- **A timeout or a quota response is not a verdict.** Retry, fall back, or switch to a blind review — never let either silently pass or fail the gate.
- Base branch is `main`, never `master`.
- **`.claude/rules/verifying-guards.md` applies to every fix round.** Any guard you add or change to close a finding must be broken and observed failing before you claim it works. A fix round is the highest-risk code in the repo: it ships with a comment asserting coverage nobody has tested. Seven unfailable guards reached (or nearly reached) `main` in a single week; reading caught none of them.
- **If Codex is unavailable, run a blind review — never skip the gate.** See the Blind review section above for the full procedure.

## Checklist before declaring a review done

- [ ] All target changes committed before Round 1
- [ ] build / vet / gofmt / golangci-lint / `go test -race` all green before Round 1
- [ ] Mode auto-detected correctly (or overridden)
- [ ] Codex invoked with `--sandbox read-only` and **no `--full-auto`**; run header confirmed `sandbox: read-only`
- [ ] **Diff file proven non-empty** in fallback mode — an empty one is reviewed silently and comes back APPROVE
- [ ] `BASE` explicitly set before any `git diff "$BASE"…` — unset yields a 0-byte diff with exit 0
- [ ] Every invocation wrapped in `timeout "$CODEX_TIMEOUT"` and run in the foreground
- [ ] Resume rounds passed `-c sandbox_mode="read-only"`; `git status --porcelain` clean afterwards
- [ ] Critical findings fixed with a regression test verified RED against the bug
- [ ] **Every guard added or changed this round mutation-tested** — implementation broken, guard observed failing by name, mutation reverted (`.claude/rules/verifying-guards.md`)
- [ ] After any non-trivial rebase, at least one mutation re-run — auto-merge can silently produce an unfailable test with nobody writing one
- [ ] Important findings fixed or explicitly deferred with justification
- [ ] Round 2+ used `codex exec resume --last` **with `-c sandbox_mode="read-only"`**, run from the repo root, stderr captured
- [ ] Final verdict APPROVE (or escalated at the 3-round cap); fixes committed
