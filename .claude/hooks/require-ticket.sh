#!/bin/sh
# Go files (including tests), go.mod and go.sum are code; docs, YAML and scripts are exempt.
input=$(cat)
case "$input" in *commit*) ;; *) exit 0 ;; esac
if ! command -v python3 >/dev/null 2>&1; then
    case "$input" in *git*) echo 'Commit guard: python3 is required to check this git commit.' >&2; exit 2 ;; *) exit 0 ;; esac
fi
printf '%s' "$input" | python3 "$(dirname "$0")/require_ticket.py"
status=$?
if [ "$status" -ne 0 ] && [ "$status" -ne 2 ]; then
    case "$input" in *git*) if printf '%s' "$input" | grep -Eq '(^|[^A-Za-z0-9])MIO-[0-9]+([^A-Za-z0-9_]|$)'; then exit 0; fi; echo 'Commit guard: parser failed; cannot safely check this git commit.' >&2; exit 2 ;; *) exit 0 ;; esac
fi
exit "$status"
