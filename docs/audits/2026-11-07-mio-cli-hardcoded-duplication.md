# mio-cli audit: hard-coded values and duplicated code

Epic MIO-6807. Ref audited: `origin/main` at `cb28435`. Scope: `main.go`, `envelope.go`, `cmd/`, `internal/`, excluding tests. Audit only: no code changed. Production was not queried.

Method: one read-only sweep then a judging pass. "Checked" rows were re-read at this SHA; the rest are leads to confirm before a fix. Ranks as in the backend audit (MIO-6678).

Config mechanism: `internal/config/config.go` (flags > `MIO_*` env > profile/config > `DefaultAPIBase`). `api.member.dev` is named once (`config.go:44`). No client IDs or keys are in code.

## Summary

| Area | Count | Headline |
|---|---|---|
| Hard-coded values | 6 rank A, 2 rank B, 6 rank C | The backend page-size cap `100` is typed six times and one comment about it is wrong |
| Duplication | 12 groups | `--limit` has three validators with three messages; CSV splitting has three behaviours |

## 1. Hard-coded values

### Rank A

| # | file:line | Value | Note |
|---|---|---|---|
| A1 (checked) | `internal/client/resolve.go:47`, `cmd/more_rows_note.go:28`, `cmd/analytics.go:217`, `cmd/media.go:1365`, `cmd/contactattributes.go:985,1005`, `cmd/hubs_scaffold.go:1773`, `cmd/flags.go:254` | page size cap 100 | Six copies. `resolve.go:43` calls 100 "the universal maximum"; `cmd/segments.go:396-401` allows 200 |
| A2 | `cmd/hubs_scaffold.go:584,764,1358,1770`, `hubs_scaffold_pages.go:552`, `hubs_scaffold_resume.go:582`, `contactattributes.go:986` | `maxPages = 1000` | seven copies of the stall guard |
| A3 | `internal/config/config.go:355-356` vs `:622,624` | 5 tries / 10 ms vs 4 tries / 25 ms | same job, two policies |
| A4 | `cmd/update.go:19` vs `cmd/update_native.go:58` | release repo `Searchie-Inc/mio-cli` | typed twice |
| A5 | `internal/config/config.go:868` vs `:880-888` | API base precedence | two copies of one rule |
| A6 | `internal/client/hub_from_template.go:118` vs `internal/client/client.go:206` | `hub_scaffolds` | JSON:API type twice |

### Rank B

| file:line | Value |
|---|---|
| `cmd/checkout.go:637,644,659`, `cmd/teams.go:83` | "member.dev dashboard" in user text |
| `cmd/pages_catalog.go:269`, `cmd/login.go:139-140`, `cmd/events.go:101` | env var names outside the config package |

### Rank C

| file:line | Value |
|---|---|
| `cmd/media_upload.go:48,50,52,725,742,102` | multipart threshold 100 MB, part sizes, poll 2 s, grace 30 s, timeout 5 min |
| `internal/client/client.go:78,31,35` | 60 s timeout, 2 rate-limit retries, 60 s max wait |
| `cmd/update_native.go:65,68` | 10 min, 1 MiB |
| `internal/config/config.go:223,34` | legacy passphrase and keyring service, same string |
| `internal/catalog/interpolate.go:111`, `cmd/hubs_scaffold_resume.go:109-110` | truncation 120 vs 60/57 |

Rank D: sample IDs in help text, backend claims namespace.

## 2. Duplication

| # | What | Copies | Disagree? | To |
|---|---|---|---|---|
| G1 | Cursor walk | seven loops; readers at `hubs_scaffold.go:619,1844`, `contactattributes.go:951` | Yes: one errors on a missing cursor, one returns `""`; six loops hang silently on a repeated cursor | one walker in `internal/client` |
| G2 (checked) | int to string | `cmd/flags.go:283` `itoa`, `internal/client/resolve.go:355` `itoaCap` | Yes: `0` gives "0" vs "1"; the "avoid strconv" comment is stale | `strconv.Itoa` |
| G3 | First non-empty | `cmd/flags.go:274`, `config.go:909`, `client/catalog.go:73` | no | one helper |
| G4 | `--limit` validators | `media.go:1359`, `analytics.go:212`, `flags.go:259`, `segments.go:401` | Yes: three rules, three messages | `parseLimit(flag, max)` |
| G5 | CSV split | nine sites | Yes: drop blanks, error on blanks, keep blanks (`media_remote_connections.go:133`) | one `splitCSV` with a policy |
| G6 | JSON writers | `output.go:211`, `main.go:110`, `envelope.go:224`, `childbinary_keyring.go:33` | Yes: only the last HTML-escapes | `internal/output` |
| G7 | Truncation | `interpolate.go:110`, `hubs_scaffold_resume.go:109` | Yes: limit and ellipsis | `internal/output` |
| G8 | URL checks | `cmd/config.go:37`, `hubs_scaffold_branding.go:442-474` | Yes: the branding error says https only, no code enforces it | one validator |
| G9 | UUID check | `cmd/config.go:19`, `client/resolve.go:102` | no | one |
| G10 | HTTP client and body caps | `client.go:78,834`, `upload.go:21,44`, `update_native.go:81`, `catalog.go:63` | Yes: error bodies capped at 2048 in one place, unbounded in another | one constructor |
| G11 | API base chain | same as A5 | no | `Resolve` |
| G12 | Home dir | `config.go:115`, `skills.go:7,16,40` | no | low priority |

## 3. Plan

- Fix now: A1 (one named cap, fix the wrong comment), G2, G4 (`--limit` validators), G5 (CSV blank policy), G1 (stall behaviour) if it fits the box.
- Ticket under MIO-6807, label `cleanup`: A2-A6, rank B and C rows, G3, G6-G12.
