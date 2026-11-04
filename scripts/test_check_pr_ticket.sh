#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q
git config user.email test@example.com
git config user.name Test
printf base > README
git add .
git commit -qm base
git update-ref refs/remotes/origin/base HEAD
check() {
    name=$1 expected=$2 title=$3 branch=$4
    set +e
    output=$(PR_TITLE="$title" HEAD_REF="$branch" BASE_REF=base "$root/scripts/check_pr_ticket.sh" 2>&1)
    code=$?
    set -e
    if [ "$code" -ne "$expected" ]; then echo "FAIL $name: exit $code, expected $expected: $output" >&2; exit 1; fi
    if [ "$expected" -eq 1 ]; then case "$output" in *title*) ;; *) echo "FAIL $name: missing title guidance" >&2; exit 1 ;; esac; fi
    echo "PASS $name"
}
printf 'package x\n' > x.go
git add .
git commit -qm 'plain change'
check title 0 'MIO-1: change' topic
check no-ticket 1 change topic
check branch 0 change MIO-2-feature
git commit --amend -qm 'MIO-3: change'
check commit 0 change topic
git reset --hard -q origin/base
printf docs > README
git add .
git commit -qm docs
check docs-only 0 docs topic
git reset --hard -q origin/base
printf module > go.mod
git add .
git commit -qm dependencies
check go-mod 1 deps topic
git reset --hard -q origin/base
printf 'package x\n' > café.go
git add .
git commit -qm unicode
check unicode-go 1 unicode topic
git reset --hard -q origin/base
printf 'package x\n' > a.go
git add .
git commit -qm base-code
git update-ref refs/remotes/origin/base HEAD
git mv a.go a.txt
git commit -qm rename
check rename-go 1 rename topic
git reset --hard -q origin/base
printf 'package x\n' > b.go
git add .
git commit -qm feature
feature=$(git rev-parse HEAD)
git checkout -q --detach origin/base
printf advance > base-only
git add .
git commit -qm 'MIO-42: base advance'
git update-ref refs/remotes/origin/base HEAD
git checkout -q --detach "$feature"
check base-advance 1 feature topic
