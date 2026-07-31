#!/usr/bin/env bash
# Jira-first guard (PreToolUse on Bash).
#
# WHY: "always file the ticket before building" is an automated-behaviour rule,
# and prose does not enforce those — mio-backend proved it, where an agent kept
# an internal task list and skipped Jira entirely. A hook does enforce it.
#
# WHY HERE SPECIFICALLY: MIO-2262 shipped its backend half while its CLI half
# never landed, and nothing noticed for weeks. In a repo whose work is one side
# of a cross-repo contract, the ticket reference is the only thread tying a CLI
# change to the backend change it depends on. A code commit with no ticket is
# how that thread gets cut.
#
# WHAT: blocks `git commit` when the staged set touches CODE and the message
# carries no MIO-<n>. Docs, .claude/ and test-only commits pass through — most
# legitimate commits in this repo are exactly that shape and must not be taxed.
#
# Exit 0 = allow. Exit 2 = block (stderr is fed back to the agent).
set -uo pipefail

input="$(cat)"

HOOK_CWD="$(printf '%s' "$input" | python3 -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d={}
print(d.get("cwd") or "")' 2>/dev/null)"

HOOK_CMD="$(printf '%s' "$input" | python3 -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d={}
print(((d.get("tool_input",{}) or {}).get("command","") or ""))' 2>/dev/null)"

# Only police git commits.
case "$HOOK_CMD" in
  *"git commit"*) ;;
  *) exit 0 ;;
esac

# Run git against the session's cwd (handles worktrees).
[ -n "$HOOK_CWD" ] && cd "$HOOK_CWD" 2>/dev/null || true

# Only code commits require a ticket. Note mio-backend's filter is
# app/|alembic/versions/ — this repo's layout is different, so the paths below
# are the mio-cli equivalents, NOT a copy.
staged="$(git diff --cached --name-only 2>/dev/null || true)"
if ! printf '%s\n' "$staged" | grep -qE '^(cmd/|internal/|main\.go|scripts/|\.github/workflows/)'; then
  exit 0
fi

# Already references a MIO ticket? Allow.
if printf '%s' "$HOOK_CMD" | grep -qE 'MIO-[0-9]+'; then
  exit 0
fi

# Block with an actionable message.
{
  echo "⛔ Jira-first guard: this commit stages code but the message has no MIO-<n> reference."
  echo ""
  echo "Staged paths matching cmd/ | internal/ | main.go | scripts/ | .github/workflows/ require a ticket."
  echo "File one FIRST (skill: create-ticket — component CLI V1 = 10257, parent an epic:"
  echo "MIO-2572 scaffold / MIO-2665 general hardening / MIO-2666 templates), transition it to"
  echo "In Progress, then put (MIO-<n>) in the commit message."
  echo ""
  echo "Why this is enforced: MIO-2262 shipped its backend half and its CLI half never landed."
  echo "The ticket reference is what ties the two sides together."
  echo ""
  echo "Docs-only, .claude/-only and test-only commits are exempt and need no ticket."
  echo "If this genuinely is ticket-less infra, add the (MIO-<n>) ref or narrow the staged scope."
} >&2
exit 2
