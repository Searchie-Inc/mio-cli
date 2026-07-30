# Verifying Guards

This repo defends its contracts with guards: drift tests, consumption assertions, preflight checks, contract tests. A guard that cannot fail is worse than no guard, because it manufactures confidence in exactly the thing it claims to protect — and it does so silently, for as long as nobody checks.

Between 2026-07-29 and 2026-07-30 this repo shipped, or nearly shipped, **seven** guards that could not fail. Every one was written deliberately, by someone trying to close a real gap, and reviewed by someone reading the code. Reading caught none of them. Mutation caught all of them.

---

## The rule (non-negotiable)

**A guard is not verified until you have watched it fail.**

Any PR that adds or changes a test, assertion, preflight check or generator guard **MUST** break the thing it protects, observe the failure, and restore — before the PR is offered for review.

**Compliance = both, explicitly:**

1. **You ran the mutation.** Change the implementation so the protected property is false. Run the guard. It must fail, and the failure must *name the thing* — a guard that fails with an unrelated error is not a guard for this property.
2. **You said so in the PR.** State what you broke and what failed. "Added a test for X" is not evidence. "Reverted X to the pre-fix behaviour; `TestFoo/subtest_bar` failed with `<message>`" is.

**Assumption is not compliance.** "The test covers it" without a mutation does not satisfy this rule. Neither does a green suite — a green suite is the *expected* state and carries no information about whether the guard works.

---

## Where this bites hardest

**Fix-up rounds.** Every fix-up round in the MIO-2576 / MIO-2567 / MIO-2808 series found the previous round had stopped short of its own stated claim. A guard added to close a gap found by review is the single highest-risk code in the repo, because it ships with a comment asserting coverage that nobody has tested.

**Rebases.** Git can produce an unfailable test with nobody writing one. During the #88 rebase, auto-merge spliced two test helpers so a `catBody` parameter was silently dropped — it compiled, and every `welcomePost` end-to-end test would have run against a catalog that declares no `welcomePost`, passing while covering nothing. Re-run at least one mutation after any non-trivial rebase.

**Test doubles.** A stub that reproduces the wire format but not the server's *semantics* will confirm any round-trip written against it. The welcome-post idempotency bug was invisible because the stub echoed back whatever it was sent, while the real endpoint stores a stripped title. If a stub stands in for a server that transforms input, the stub must transform it too.

---

## Shapes that recur

Each of these passed review by inspection:

| Shape | Instance |
|---|---|
| Whitelist compared to whitelist, not to behaviour | `TestTemplatePolicyFields_AllConsumed` compared two hand-maintained maps; adding a key to both left the value accepted and dropped |
| Probe set smaller than the claim | `TestRenderedPropKeysActuallyRender` said "each rendered key" and covered 2 of 8; adding an unrendered key kept the suite green |
| Probing an invented case instead of the real one | `consumed_test.go` probed `deprecatedInFavourOf` — a key that can never appear — while the real `deprecated` key stayed unrendered |
| Input that satisfies both implementations | the 280-code-point cap used `strings.Repeat("é", 281)`, over the cap in code points **and** bytes, so swapping `utf8.RuneCountInString` for `len` left it passing |
| Asserting an unreachable state | `TestScaffoldResult_PolicyGateThreeStates` pinned a `false` the pipeline could no longer produce, satisfiable only by hand-building a context |
| Oracle is the code under test | the docsgen drift test byte-compares against the generator's own output, so anything `Render` silently ignored was invisible to it |

A reject-side case often **cannot** discriminate: if the property is "measured after stripping", no over-the-limit input distinguishes stripped from raw, because stripped ≤ raw always. Only an accept-side case can. Check that your case *can* tell the two implementations apart before trusting it.

---

## What good looks like

`TestTemplatePolicyFields_EachOneChangesTheRequests` (MIO-2567): for each field the catalog accepts, declare that field alone, run the real step, and require the emitted HTTP requests to differ from declaring nothing. Its oracle is **the wire**, not a second list. A reviewer broke six separate things — suppressing the gate PATCH, dropping two field assignments, adding a fifth catalog key, removing the preflight hook, skipping a plan entry — and all six failed by name.

That is the bar: the guard's oracle should be an observable the implementation cannot fake, and no edit to a list alone should be able to green it.

---

## Why this is non-negotiable

This CLI is agent-first. Its documentation is executed verbatim by LLM agents, and its guards are what stop generated documentation and machine-readable contracts from drifting away from reality. When a guard fails to guard, nothing goes red — the drift just accumulates until a human notices months later, which is how the embedded catalog pin sat two minor versions stale while asserting it was current (MIO-2741).

The cost of this rule is a few minutes per guard. The cost of skipping it, measured on this repo in one week, was five review rounds and a duplicate-creation bug that reached a fix-up commit.
