#!/usr/bin/env bash
# Tests for require-jira-on-code-commit.sh.
#
# A guard is not verified until you have watched it fail
# (.claude/rules/verifying-guards.md). The first version of this hook passed a
# four-case probe set and was still bypassed by `git commit -am`, blocked
# `grep "git commit"`, and disarmed itself silently when python3 was absent —
# because the probe set was smaller than the claim. Every branch the hook
# claims is exercised here, on both sides.
#
# Run: .claude/hooks/require-jira-on-code-commit.test.sh
set -uo pipefail

HOOK="$(cd "$(dirname "$0")" && pwd)/require-jira-on-code-commit.sh"
PASS=0; FAIL=0

scratch() {
  local d; d="$(mktemp -d)"
  git -C "$d" init -q .
  git -C "$d" config user.email t@t; git -C "$d" config user.name t
  mkdir -p "$d/cmd" "$d/internal" "$d/docs" "$d/.claude" "$d/scripts" "$d/.github/workflows"
  echo base > "$d/cmd/root.go"; echo base > "$d/docs/n.md"
  git -C "$d" add -A >/dev/null; git -C "$d" commit -qm "base (MIO-1)"
  printf '%s' "$d"
}

# want <0|2> <label> <cwd> <command>
want() {
  local exp="$1" label="$2" cwd="$3" cmd="$4" got
  printf '{"cwd":%s,"tool_input":{"command":%s}}' \
    "$(printf '%s' "$cwd" | python3 -c 'import sys,json;print(json.dumps(sys.stdin.read()))')" \
    "$(printf '%s' "$cmd" | python3 -c 'import sys,json;print(json.dumps(sys.stdin.read()))')" \
    | "$HOOK" >/dev/null 2>&1
  got=$?
  if [ "$got" = "$exp" ]; then PASS=$((PASS+1)); printf '  ok   %-58s exit=%s\n' "$label" "$got"
  else FAIL=$((FAIL+1)); printf '  FAIL %-58s exit=%s want=%s\n' "$label" "$got" "$exp"; fi
}

echo "== blocks a code commit with no ticket, in every invocation form =="
D=$(scratch); echo v2 > "$D/cmd/root.go"; git -C "$D" add cmd/root.go
want 2 "git commit -m"                       "$D" 'git commit -m "no ticket"'
want 2 "git  commit (double space)"          "$D" 'git  commit -m "no ticket"'
want 2 "git -c k=v commit"                   "$D" 'git -c user.name=x commit -m "no ticket"'
want 2 "cd x && git commit"                  "$D" 'cd /tmp && git commit -m "no ticket"'
want 2 "/usr/bin/git commit"                 "$D" '/usr/bin/git commit -m "no ticket"'

echo "== -a / --amend sweep in UNSTAGED tracked code (the original bypass) =="
D=$(scratch); echo v2 > "$D/cmd/root.go"     # modified, deliberately NOT staged
want 2 "git commit -am"                      "$D" 'git commit -am "no ticket"'
want 2 "git commit -a -m"                    "$D" 'git commit -a -m "no ticket"'
want 2 "git commit --all -m"                 "$D" 'git commit --all -m "no ticket"'
want 2 "git commit --amend --no-edit"        "$D" 'git commit --amend --no-edit'

echo "== allows when the message carries a ticket =="
D=$(scratch); echo v2 > "$D/cmd/root.go"; git -C "$D" add cmd/root.go
want 0 "staged code + (MIO-1234)"            "$D" 'git commit -m "feat: thing (MIO-1234)"'
D=$(scratch); echo v2 > "$D/cmd/root.go"
want 0 "-am + MIO ref"                       "$D" 'git commit -am "feat: thing (MIO-1234)"'

echo "== allows exempt paths (must not regress: most commits are this shape) =="
D=$(scratch); echo v2 > "$D/docs/n.md"; git -C "$D" add docs/n.md
want 0 "docs-only"                           "$D" 'git commit -m "docs: note"'
D=$(scratch); echo x > "$D/.claude/t.md"; git -C "$D" add .claude/t.md
want 0 ".claude-only"                        "$D" 'git commit -m "chore: skill"'
D=$(scratch); echo x > "$D/README.md"; git -C "$D" add README.md
want 0 "README-only"                         "$D" 'git commit -m "docs: readme"'

echo "== every claimed code prefix blocks =="
for p in cmd/a.go internal/b.go main.go scripts/c.sh .github/workflows/d.yml; do
  D=$(scratch); mkdir -p "$D/$(dirname "$p")"; echo x > "$D/$p"; git -C "$D" add "$p"
  want 2 "prefix $p" "$D" 'git commit -m "chore: x"'
done
D=$(scratch); echo x > "$D/cmd/t_test.go"; git -C "$D" add cmd/t_test.go
want 2 "tests are code (they live under cmd/)" "$D" 'git commit -m "test: coverage"'
D=$(scratch); echo x > "$D/cmd/a.go"; echo y > "$D/docs/n.md"; git -C "$D" add cmd/a.go docs/n.md
want 2 "mixed code+docs"                     "$D" 'git commit -m "chore: x"'

echo "== does NOT fire on non-commit commands that merely mention one =="
D=$(scratch); echo v2 > "$D/cmd/root.go"; git -C "$D" add cmd/root.go
want 0 "go test"                             "$D" 'go test ./...'
want 0 'grep -rn "git commit"'               "$D" 'grep -rn "git commit" .claude/hooks/'
want 0 "git log --grep=git commit"           "$D" 'git log --grep="git commit" --oneline'
want 0 "echo mentioning git commit"          "$D" 'echo "next: git commit the fix" >> /tmp/n.md'
want 0 "git status"                          "$D" 'git status --porcelain'

echo "== -C targets the right repo's index =="
D=$(scratch); DOC=$(scratch)
echo v2 > "$D/cmd/root.go"; git -C "$D" add cmd/root.go          # code staged in D
echo v2 > "$DOC/docs/n.md"; git -C "$DOC" add docs/n.md          # docs staged in DOC
want 0 "-C <docs-only repo> from a code cwd"  "$D" "git -C $DOC commit -m \"docs: note\""

echo "== fails CLOSED when it cannot parse a commit-shaped command =="
D=$(scratch); echo v2 > "$D/cmd/root.go"; git -C "$D" add cmd/root.go
MINB=$(mktemp -d); for b in bash cat sed cut grep git printf; do ln -sf "$(command -v $b)" "$MINB/$b" 2>/dev/null; done
run_nopy() { printf '{"cwd":"%s","tool_input":{"command":"%s"}}' "$D" "$1" \
  | env -i PATH="$MINB" HOME="$HOME" bash "$HOOK" >/dev/null 2>&1; echo $?; }
g=$(run_nopy 'git commit -m \"no ticket\"')
if [ "$g" = "2" ]; then PASS=$((PASS+1)); printf '  ok   %-58s exit=2\n' "no python3, commit-shaped"
else FAIL=$((FAIL+1)); printf '  FAIL %-58s exit=%s want=2\n' "no python3, commit-shaped" "$g"; fi
g=$(run_nopy 'go test ./...')
if [ "$g" = "0" ]; then PASS=$((PASS+1)); printf '  ok   %-58s exit=0\n' "no python3, unrelated command"
else FAIL=$((FAIL+1)); printf '  FAIL %-58s exit=%s want=0\n' "no python3, unrelated command" "$g"; fi

echo
echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
