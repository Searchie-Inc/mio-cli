#!/usr/bin/env bash
# Jira-first guard (PreToolUse on Bash).
#
# WHY: "always file the ticket before building" is an automated-behaviour rule,
# and prose does not enforce those — mio-backend proved it, where an agent kept
# an internal task list and skipped Jira entirely. A hook does enforce it.
#
# WHY HERE: MIO-2262 shipped its backend half while its CLI half never landed,
# and nothing noticed for weeks. In a repo that is one side of a cross-repo
# contract, the ticket reference is the only thread tying a CLI change to the
# backend change it depends on. A code commit with no ticket cuts that thread.
#
# WHAT: blocks a `git commit` that would land CODE when the message carries no
# MIO-<n>. Docs-only and .claude/-only commits pass through untouched.
#
# Exit 0 = allow. Exit 2 = block (stderr is fed back to the agent).
#
# TESTED BY: .claude/hooks/require-jira-on-code-commit.test.sh — run it after any
# edit to this file. Per .claude/rules/verifying-guards.md a guard is not
# verified until you have watched it fail, and the first version of this hook
# blocked `git commit -m` while waving through `git commit -am`, which is the
# guard exactly inverted.
set -uo pipefail

CODE_RE='^(cmd/|internal/|main\.go|scripts/|\.github/workflows/)'

input="$(cat)"

# Parse and classify in one python pass. Emits three lines:
#   <is-git-commit 0|1> <needs-unstaged 0|1> <-C dir or empty>
#   <command>
#   <cwd>
# Tokenising matters: a substring test for "git commit" also fires on
# `grep -rn "git commit"`, and misses `git  commit` / `git -c k=v commit`.
parsed="$(printf '%s' "$input" | python3 -c '
import sys, json, shlex

try:
    d = json.load(sys.stdin)
except Exception:
    print("ERR"); raise SystemExit(0)

cmd = ((d.get("tool_input") or {}).get("command") or "")
cwd = d.get("cwd") or ""

def segments(s):
    # Split on shell operators so `cd x && git commit` is seen.
    out, cur = [], []
    lex = shlex.shlex(s, posix=True, punctuation_chars=True)
    lex.whitespace_split = True
    try:
        toks = list(lex)
    except ValueError:
        toks = s.split()
    for t in toks:
        if t in ("&&", "||", ";", "|", "&"):
            if cur: out.append(cur); cur = []
        else:
            cur.append(t)
    if cur: out.append(cur)
    return out

is_commit, needs_unstaged, cdir = 0, 0, ""
for toks in segments(cmd):
    if not toks: continue
    exe = toks[0].rsplit("/", 1)[-1]
    if exe != "git": continue
    i, sub, d_dir = 1, None, ""
    while i < len(toks):
        t = toks[i]
        if t in ("-c", "--namespace", "--work-tree", "--git-dir", "--exec-path"):
            i += 2; continue
        if t in ("-C",):
            d_dir = toks[i+1] if i+1 < len(toks) else ""
            i += 2; continue
        if t.startswith("-"):
            i += 1; continue
        sub = t; break
    if sub != "commit": continue
    is_commit = 1
    cdir = d_dir
    rest = toks[i+1:]
    for t in rest:
        if t == "--": break
        if t in ("--all", "--amend"): needs_unstaged = 1
        elif t.startswith("--"): continue
        elif t.startswith("-") and ("a" in t[1:] or t[1:].startswith("-")):
            # short cluster like -am, -a, -av
            if "a" in t[1:]: needs_unstaged = 1
    break

print("%d %d %s" % (is_commit, needs_unstaged, cdir))
print(cmd)
print(cwd)
' 2>/dev/null)"

# Fail CLOSED only when we could not parse AND the raw input looks like a commit.
# python3 missing or a payload-schema change must not silently disarm the guard,
# but neither should it block every unrelated Bash call.
if [ -z "$parsed" ] || [ "${parsed%%$'\n'*}" = "ERR" ]; then
  if printf '%s' "$input" | grep -q 'git' && printf '%s' "$input" | grep -q 'commit'; then
    if printf '%s' "$input" | grep -qE 'MIO-[0-9]+'; then exit 0; fi
    echo "⛔ Jira-first guard could not parse its input (python3 missing or payload schema changed)." >&2
    echo "Failing closed because the command looks like a git commit. Add a (MIO-<n>) ref, or fix the hook." >&2
    exit 2
  fi
  exit 0
fi

flags="$(printf '%s\n' "$parsed" | sed -n 1p)"
HOOK_CMD="$(printf '%s\n' "$parsed" | sed -n 2p)"
HOOK_CWD="$(printf '%s\n' "$parsed" | sed -n 3p)"
IS_COMMIT="$(printf '%s' "$flags" | cut -d' ' -f1)"
NEEDS_UNSTAGED="$(printf '%s' "$flags" | cut -d' ' -f2)"
DASH_C="$(printf '%s' "$flags" | cut -d' ' -f3-)"

[ "$IS_COMMIT" = "1" ] || exit 0

# Already references a ticket? Allow.
printf '%s' "$HOOK_CMD" | grep -qE 'MIO-[0-9]+' && exit 0

# Evaluate the index of the repo the commit actually targets: `git -C <dir>`
# wins over the session cwd, which is what the commit will really use.
[ -n "$DASH_C" ] && cd "$DASH_C" 2>/dev/null || { [ -n "$HOOK_CWD" ] && cd "$HOOK_CWD" 2>/dev/null; } || true

staged="$(git diff --cached --name-only 2>/dev/null || true)"
# -a / --amend also sweep in modified tracked files that were never `git add`ed.
if [ "$NEEDS_UNSTAGED" = "1" ]; then
  staged="$staged
$(git diff --name-only 2>/dev/null || true)"
  if printf '%s' "$HOOK_CMD" | grep -qE '(^|[[:space:]])--amend([[:space:]]|$)'; then
    staged="$staged
$(git show --pretty=format: --name-only HEAD 2>/dev/null || true)"
  fi
fi

printf '%s\n' "$staged" | grep -qE "$CODE_RE" || exit 0

{
  echo "⛔ Jira-first guard: this commit would land code, but the message has no MIO-<n> reference."
  echo ""
  echo "Code = cmd/ | internal/ | main.go | scripts/ | .github/workflows/ — tests included, they live under"
  echo "cmd/ and internal/ too. Docs-only and .claude/-only commits are exempt and need no ticket."
  echo ""
  echo "File a ticket FIRST (skill: create-ticket — component CLI V1 = 10257, parent an epic:"
  echo "MIO-2572 scaffold / MIO-2665 general hardening / MIO-2666 templates), transition it to"
  echo "In Progress, then put (MIO-<n>) in the commit message."
  echo ""
  echo "Why this is enforced: MIO-2262 shipped its backend half and its CLI half never landed."
  echo "The ticket reference is what ties the two sides together."
  echo ""
  echo "If this genuinely is ticket-less infra, add the (MIO-<n>) ref or narrow the staged scope."
} >&2
exit 2
