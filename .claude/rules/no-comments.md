# No comments

Code carries no comments and no docstrings. Agents read and copy what is in a file; a stale comment gets copied and believed. What code does is said by names, types and tests. Why it changed goes in the commit message and the PR body.

Allowed, and nothing else:
- tool directives: compiler directives and lint suppressions naming a real rule (`//go:embed`, `//go:generate`, `//go:build`, `//nolint:<rule>`)
- license headers
- `// Code generated ... DO NOT EDIT.` headers (generated files are left alone entirely)
- `// vendor: <url>`: one line on the behaviour of a system we cannot change, citing its source

Not allowed: doc comments on exported identifiers (this CLI is a binary and publishes no Go library API), ticket keys, review rounds, change history, TODO/FIXME (file a ticket), commented-out code. Cobra `Short`/`Long`/`Example` strings are help text, not comments, and stay.

Enforced by `TestRepoHasNoProhibitedComments` in `scripts/stripcomments`, part of `go test ./...`: a comment outside this list fails CI. Fix with `go run ./scripts/stripcomments` (idempotent). On a merge conflict with a sweep: take the other side's file, re-run the script, commit; never hand-merge comment hunks.
