#!/bin/sh
# The title becomes the squash-merge message; the other sources support other merge strategies.
set -eu
: "${BASE_REF:?BASE_REF is required}" "${HEAD_REF:?HEAD_REF is required}" "${PR_TITLE?PR_TITLE is required}"
range="origin/$BASE_REF..HEAD"
if ! git diff --no-renames -z --name-only "origin/$BASE_REF...HEAD" | python3 -c 'import sys; paths = sys.stdin.buffer.read().split(b"\0"); sys.exit(not any(p.endswith(b".go") or p.rsplit(b"/", 1)[-1] in (b"go.mod", b"go.sum") for p in paths))'; then
    exit 0
fi
if printf '%s\n' "$PR_TITLE" "$HEAD_REF" | grep -Eq 'MIO-[0-9]+'; then exit 0; fi
if git log --format=%B "$range" | grep -Eq 'MIO-[0-9]+'; then exit 0; fi
echo 'Jira ticket required: add a MIO-n key to the PR title (the squash-merge message), a commit message, or the head branch name.' >&2
exit 1
