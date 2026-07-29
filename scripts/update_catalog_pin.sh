#!/bin/sh
# update_catalog_pin.sh — re-vendor the pinned mio-page-catalog artifact.
#
# The CLI embeds a byte-identical copy of mio-page-catalog/catalog.json plus its
# golden fixtures. Four things must move together or the parity guards fail:
#
#   internal/catalog/catalog.json               the embedded artifact
#   internal/catalog/testdata/fixtures/         TS-reference golden output
#   internal/catalog/testdata/interpolation/    §4.3 interpolation corpus
#   internal/catalog/CATALOG_REF                the upstream commit (machine-readable)
#   internal/catalog/parity_test.go             pinnedDigest (the independent 3rd value)
#
# This script does all five from a mio-page-catalog checkout, so a re-pin is one
# command instead of five hand edits — the drift that let the CLI sit on 0.10.0
# while the backend served 0.12.0 came from doing it by hand.
#
# Usage:
#   scripts/update_catalog_pin.sh --catalog-repo /path/to/mio-page-catalog [--ref <sha>]
#
# --ref defaults to the checkout's current HEAD. The ref is recorded verbatim in
# CATALOG_REF, so pass the SHA you actually vendored, not a branch name.
#
# Verify afterwards with:  go test ./internal/catalog/... ./cmd/...

set -eu

CATALOG_REPO=""
REF=""

usage() {
  echo "usage: $0 --catalog-repo <path> [--ref <sha>]" >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --catalog-repo) [ $# -ge 2 ] || usage; CATALOG_REPO="$2"; shift 2 ;;
    --ref)          [ $# -ge 2 ] || usage; REF="$2";          shift 2 ;;
    -h|--help)      usage ;;
    *)              echo "unknown argument: $1" >&2; usage ;;
  esac
done

[ -n "$CATALOG_REPO" ] || usage
[ -d "$CATALOG_REPO/.git" ] || { echo "not a git checkout: $CATALOG_REPO" >&2; exit 1; }

# Resolve the destination relative to this script so the tool works from any cwd.
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
DEST="$ROOT/internal/catalog"

[ -n "$REF" ] || REF=$(git -C "$CATALOG_REPO" rev-parse HEAD)

# Reject anything that is not a full 40-char SHA: CATALOG_REF is compared
# byte-for-byte against the GitHub API's HEAD sha by the staleness workflow, so a
# tag or short ref would make every run report stale forever.
case "$REF" in
  [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
  *) echo "--ref must be a full 40-char lowercase commit SHA, got: $REF" >&2; exit 1 ;;
esac

echo "Vendoring mio-page-catalog @ $REF"

TMP=$(mktemp -d)
# shellcheck disable=SC2064  # expand TMP now: the trap must survive variable reuse
trap "rm -rf '$TMP'" EXIT INT TERM

# Read the artifact out of the ref rather than the working tree, so a dirty or
# feature-branch checkout can never be vendored by accident.
git -C "$CATALOG_REPO" archive "$REF" catalog.json fixtures | tar -x -C "$TMP"

[ -f "$TMP/catalog.json" ] || { echo "catalog.json not present at $REF" >&2; exit 1; }

# ── 1. the embedded artifact ──────────────────────────────────────────────
cp "$TMP/catalog.json" "$DEST/catalog.json"

# ── 2. golden fixtures ────────────────────────────────────────────────────
#   Mirror exactly: fixtures that disappear upstream must disappear here too, or
#   TestGoldenFixtureSet_ExactlyMatches fails on the lingering file.
rm -f "$DEST/testdata/fixtures"/*.json
for f in "$TMP/fixtures"/*.json; do
  [ -e "$f" ] || continue
  cp "$f" "$DEST/testdata/fixtures/"
done

# ── 3. interpolation corpus (README.md is upstream-only docs) ─────────────
if [ -d "$TMP/fixtures/interpolation" ]; then
  rm -f "$DEST/testdata/interpolation"/*.json
  for f in "$TMP/fixtures/interpolation"/*.json; do
    [ -e "$f" ] || continue
    cp "$f" "$DEST/testdata/interpolation/"
  done
fi

# ── 4. the machine-readable pin ───────────────────────────────────────────
printf '%s\n' "$REF" > "$DEST/CATALOG_REF"

# ── 5. the independent digest pin ─────────────────────────────────────────
#   Taken from the vendored body's own meta.digest. The parity test still proves
#   the Go canonicalizer recomputes that same value from the bytes, which is the
#   check that actually catches a corrupt or mis-canonicalized vendor.
DIGEST=$(sed -n 's/.*"digest"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$DEST/catalog.json" | head -n 1)
[ -n "$DIGEST" ] || { echo "could not read meta.digest from the vendored catalog.json" >&2; exit 1; }

PARITY="$DEST/parity_test.go"
grep -q '^const pinnedDigest = ' "$PARITY" || {
  echo "pinnedDigest anchor not found in $PARITY — update this script alongside it" >&2
  exit 1
}
# Rewrite in place via a temp file: portable across GNU and BSD sed.
sed "s|^const pinnedDigest = .*|const pinnedDigest = \"$DIGEST\"|" "$PARITY" > "$TMP/parity_test.go"
mv "$TMP/parity_test.go" "$PARITY"

VERSION=$(sed -n 's/.*"catalogVersion"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$DEST/catalog.json" | head -n 1)

echo "  catalog.json     -> catalogVersion ${VERSION:-unknown}"
echo "  CATALOG_REF      -> $REF"
echo "  pinnedDigest     -> $DIGEST"
echo "  fixtures         -> $(find "$DEST/testdata/fixtures" -name '*.json' | wc -l | tr -d ' ') files"
echo
echo "Now run: go test ./internal/catalog/... ./cmd/..."
