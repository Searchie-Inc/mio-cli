---
name: create-ticket
description: Use whenever a JIRA ticket should be filed for mio-cli work — "create a ticket", "file a bug", "log this", "track this", or a problem described plus an intent to track it. Encodes the acli payload shape (there is no --component flag; components and parent go through --from-json) and the MIO conventions that decide whether a ticket ever reaches the board.
---

# Create a MIO ticket (mio-cli)

The reason this skill exists: **a ticket without `component = CLI V1` never appears on the CLI board**, and `acli jira workitem create` has no `--component` flag. Getting it wrong is silent — the ticket is created, returns a URL, and is invisible.

## The one-call payload

Components, parent and assignee all stick in a single `--from-json` create. Verified on MIO-2840.

```bash
cat > /tmp/ticket.json <<'JSON'
{
  "projectKey": "MIO",
  "type": "Task",
  "summary": "<imperative, specific, names the actual defect or deliverable>",
  "parentIssueId": "MIO-2665",
  "assignee": "marius@northresults.com",
  "additionalAttributes": {
    "components": [{ "name": "CLI V1" }]
  },
  "description": {
    "type": "doc", "version": 1,
    "content": [{"type":"paragraph","content":[{"type":"text","text":"placeholder"}]}]
  }
}
JSON
acli jira workitem create --from-json /tmp/ticket.json
```

Then set the real description as **markdown** via the Atlassian MCP (see below), and transition:

```bash
acli jira workitem transition --key MIO-1234 --status "In Progress"
```

Note `--key` is required on `transition` — omitting it fails with `at least one of the flags in the group [key jql filter] is required`.

### Field notes

- `parentIssueId` takes the epic **key** (`MIO-2665`), despite the name.
- `additionalAttributes` is the passthrough to JIRA's `fields.*`. `components` and `priority` live there, **not** at top level.
- `assignee` must be a real email — the `@me` shortcut works in `acli jira workitem assign` but **not** in a `--from-json` payload, where it returns `User not found for email: @me`.
- Run `acli jira auth status` if you need the current user rather than assuming.

## Two gotchas that cost time

**1. Do not verify with `acli search`.** It does not return `components` or `parent` by default, and `--fields components` is rejected outright:

```
✗ Error: field 'components' is not allowed
```

Read back with the Atlassian MCP `getJiraIssue` (fields `["summary","components","parent","assignee"]`). A create that *did* work reads as empty through `acli search --json`, which is exactly how an earlier ticket in this repo got sent down a needless create-then-edit path.

**2. Do not set the description with `--description-file`.** Its markdown→ADF conversion escapes the markup — headings arrive as literal `\## The gap`, bullets as `\-`. Use MCP `editJiraIssue` with `contentFormat: "markdown"`; it renders properly.

## Conventions

**Component — required.** CLI V1 = `10257`. Others in this project: API/Backend `10087`, Hubs V2 `10088`, Page Catalog V1 `10258`.

**Epic — parent it.**

| epic | scope |
|---|---|
| MIO-2572 | `hubs scaffold` |
| MIO-2665 | general CLI bug-fix & hardening (non-scaffold) — the default |
| MIO-2666 | hub + page templates |

**Ownership.** If the work turns out to be backend-owned, re-component it to **API/Backend only** and take it off the CLI board — do not leave it on both. If it is a defect in another repo found from here (mio-hub, mio-page-catalog), file it with *that* repo's component and leave it **unassigned**; it is not yours to schedule.

**Assign before starting.** Most tickets are unassigned, and parallel sessions have collided twice on independently-scoped work. Transition to **In Progress** on pickup and **Done** on merge.

## What makes a good ticket here

This repo's tickets are read by agents as much as by people, so the description is the specification.

- **Lead with the observable**, not the theory: the command, the actual output, the exit code.
- **Quote real strings** — error text, flag names, file:line. A ticket saying "the flag doesn't work" costs a rediscovery; one quoting `error: unexpected argument '--sandbox' found` does not.
- **Separate measured from inferred.** If half a claim could not be tested, say which half.
- **Give acceptance criteria that can fail.** For anything adding a guard, the criteria must include the mutation — what you break, and what must go red (`.claude/rules/verifying-guards.md`).
- **State what is out of scope**, so the next person does not have to re-derive the boundary.

## When NOT to use this

- Editing an existing ticket's metadata → MCP `editJiraIssue`.
- Comments → `acli jira workitem comment create`.
- Status transitions alone → the one-liner above.
