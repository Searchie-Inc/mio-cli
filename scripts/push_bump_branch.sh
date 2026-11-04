#!/bin/sh
# push_bump_branch.sh — force-push the catalog bump branch (MIO-2998).
#
# usage: push_bump_branch.sh <branch> <push-target> [<read-remote>]
#   push-target  "origin", or a URL (the App-token URL when the App is installed)
#   read-remote  where the branch's current tip is read from (default: origin)
#
# Two traps this script exists to close (both found by blind review of the
# watcher, neither reachable in a real run until the App is installed):
#
#  1. (URL targets only — a push to `origin` needs the persisted header.)
#     actions/checkout persists the GITHUB_TOKEN as an http extraheader in
#     .git/config. A push to https://x-access-token:APP@github.com/… would send
#     THAT header, not the URL credentials, so the push is authenticated as
#     github-actions[bot] and starts no CI. The header is blanked for the push.
#  2. --force-with-lease against a URL has no remote-tracking ref, so git expects
#     the ref to be ABSENT and rejects every update of an existing bump branch
#     ("stale info"). The lease therefore names the expected tip explicitly (read
#     with ls-remote; empty = the branch must not exist yet).
set -eu
branch=${1:?branch}
target=${2:?push target}
read_remote=${3:-origin}

expected=$(git ls-remote "$read_remote" "refs/heads/$branch" | cut -f1)

if [ "$target" = "origin" ]; then
    # Fallback path (no App token): origin IS authenticated by the checkout's
    # persisted GITHUB_TOKEN header, so it must be left alone.
    exec git push --force-with-lease="$branch:$expected" origin "$branch"
fi
exec git -c 'http.https://github.com/.extraheader=' \
    push --force-with-lease="$branch:$expected" "$target" "$branch"
