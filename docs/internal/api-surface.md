# mio API Surface — CLI Command Catalog

Authoritative endpoint reference for building `mio` commands. Every command maps
to a backend route below. Backend is FastAPI; **all bodies are JSON:API v1.1**
(`Content-Type: application/vnd.api+json`) **except** `/api/auth/*` which is
plain JSON.

## Conventions

- **Team scope:** most routes are nested under `/api/teams/{team_id}/…`. The
  `{team_id}` comes from the active context: the API key's team, the `--team`
  flag, or `config current_team`. With an API key, `{team_id}` is implicit (the
  key belongs to a team) — the client should still substitute it into paths.
- **Hub scope:** many resources nest under `…/hubs/{hub_id}/…`. Supplied via a
  `--hub` flag or `config current_hub`.
- **CLI verbs:** `create`(POST) `retrieve`(GET id) `update`(PATCH) `delete`(DELETE)
  `list`(GET collection) `search`(POST/GET query) + custom actions.
- **Resource identifier flags:** create/update take `--field value` flags mapped
  to `<field>` (snake_case). For most resources the client wraps them in a JSON:API
  envelope `{"data":{"type":<derived>,"attributes":{…}}}` (`client.StyleEnvelope`,
  the default). The backend is NOT uniform, though — a handful of endpoints bind a
  **flat** pydantic model and must be sent WITHOUT an envelope (`client.StyleFlat`):
  - `users update`, `roles create/update`, `api-keys create`
  - `email config set` (PUT email-config; flat `mail_*` fields)
  - `checkout stripe-sync import` (flat `{hub_id}`) and `adopt-product`
    (flat `{stripe_product_id, hub_id}`)
  Sending the wrong shape 422s either way, so the style is declared per command.
- **Type derivation:** the envelope `type` is derived from the request path via
  `resourceTypeFromPath` + a `typeOverrides` table for the cases where the backend
  `type` literal differs from the URL segment (e.g. `segments`→`segment`,
  `contacts`→`team-contacts`, `members`→`team-members`, `hubs/contact-attributes`
  →`contact-attribute-hub-configs`, `steps`→`drip_steps`, `payments/refund`
  →`refunds`, `payment-accounts/onboarding-link`→`onboarding_links`).
- **segments search:** body is an envelope `{"data":{"type":"segment-search",
  "attributes":{"conditions":<tree>,"page":{"size","after"}}}}`. `--conditions`
  takes the full condition tree as JSON (`{"version":1,"groups":[…]}`), `--page-size`
  and `--page-after` drive pagination. There is no `--match` flag.
- Each `cmd/<resource>.go` self-registers via `func init(){ rootCmd.AddCommand(...) }`.

---

## auth (handled by login.go, not a resource command)
- `POST /api/auth/login` {email,password} → tokens (plain JSON)
- `POST /api/auth/register` {email,password,first_name?,last_name?}
- `POST /api/auth/refresh` (Bearer refresh)
- `POST /api/auth/logout`
- `GET /api/auth/me` → current user

## api-keys  (NEW backend feature — `cmd/apikeys.go`)
- `create` → POST `/api/teams/{team_id}/api-keys` {name, expires_at?} → returns secret ONCE
- `list`   → GET `/api/teams/{team_id}/api-keys`
- `retrieve`→ GET `/api/teams/{team_id}/api-keys/{id}`
- `delete` → DELETE `/api/teams/{team_id}/api-keys/{id}`  (revoke)

## teams  (`cmd/teams.go`)
- `create`   POST `/api/teams`
- `list`     GET `/api/teams`
- `retrieve` GET `/api/teams/{id}`
- `update`   PATCH `/api/teams/{id}`
- `delete`   DELETE `/api/teams/{id}`
- `switch`   POST `/api/teams/{id}/switch`
- `members list`   GET `/api/teams/{id}/members`
- `members add`    POST `/api/teams/{id}/members` {user_id, role_id?}
- `members remove` DELETE `/api/teams/{id}/members/{user_id}`

## users  (`cmd/users.go`)
- `me`       GET `/api/users/me`
- `retrieve` GET `/api/users/{id}`
- `list`     GET `/api/users`
- `update`   PATCH `/api/users/{id}`

## roles  (`cmd/roles.go`)
- `create` POST `/api/roles`
- `list`   GET `/api/roles`
- `retrieve` GET `/api/roles/{id}`
- `update` PATCH `/api/roles/{id}`
- `delete` DELETE `/api/roles/{id}`
- `permissions list` GET `/api/permissions`

## hubs  (`cmd/hubs.go`)
- `create`   POST `/api/teams/{team_id}/hubs`
- `list`     GET `/api/teams/{team_id}/hubs`
- `retrieve` GET `/api/teams/{team_id}/hubs/{id}`
- `update`   PATCH `/api/teams/{team_id}/hubs/{id}`
- `delete`   DELETE `/api/teams/{team_id}/hubs/{id}`

## contacts  (`cmd/contacts.go`) — backend module contacts_admin
- `list`     GET `/api/teams/{team_id}/contacts`  (supports filters)
- `create`   POST `/api/teams/{team_id}/contacts`
- `retrieve` GET `/api/teams/{team_id}/contacts/{id}`
- `update`   PATCH `/api/teams/{team_id}/contacts/{id}`
- `delete`   DELETE `/api/teams/{team_id}/contacts/{id}`
- `restore`  POST `/api/teams/{team_id}/contacts/{id}/restore`

## contact-attributes  (`cmd/contactattributes.go`)
- defs:    `create/list/retrieve/update/delete` `/api/teams/{team_id}/contact-attributes[/{id}]`
- options: `create/list/update/delete` `/api/teams/{team_id}/contact-attributes/{def}/options[/{id}]`
- hub-config: `create/list/update/delete` `/api/teams/{team_id}/hubs/{hub_id}/contact-attributes[/{def}]`
- values:  `get/set` GET/PATCH `/api/teams/{team_id}/contacts/{tcid}/attributes`

## tags  (`cmd/tags.go`)
- `list`     GET `/api/teams/{team_id}/tags`
- `create`   POST `/api/teams/{team_id}/tags`
- `retrieve` GET `/api/teams/{team_id}/tags/{id}`
- `update`   PATCH `/api/teams/{team_id}/tags/{id}`
- `delete`   DELETE `/api/teams/{team_id}/tags/{id}`
- `assign`   POST `/api/teams/{team_id}/contacts/{tcid}/tags`
- `assign-bulk` POST `/api/teams/{team_id}/contacts/{tcid}/tags/bulk`
- `remove`   DELETE `/api/teams/{team_id}/contacts/{tcid}/tags/{tag_id}`

## segments  (`cmd/segments.go`)
- `create`   POST `/api/teams/{team_id}/segments`
- `list`     GET `/api/teams/{team_id}/segments`
- `retrieve` GET `/api/teams/{team_id}/segments/{id}`
- `update`   PATCH `/api/teams/{team_id}/segments/{id}`
- `delete`   DELETE `/api/teams/{team_id}/segments/{id}`
- `search`   POST `/api/teams/{team_id}/segments/search` (preview conditions)
- `members`  GET `/api/teams/{team_id}/segments/{id}/members`
- `count`    GET `/api/teams/{team_id}/segments/{id}/members/count`

## content  (`cmd/content.go`)
- `create`   POST `/api/teams/{team_id}/hubs/{hub_id}/content`
- `list`     GET `/api/teams/{team_id}/hubs/{hub_id}/content` (roots)
- `retrieve` GET `/api/teams/{team_id}/hubs/{hub_id}/content/{id}`
- `children` GET `/api/teams/{team_id}/hubs/{hub_id}/content/{id}/children`
- `update`   PATCH `/api/teams/{team_id}/hubs/{hub_id}/content/{id}`
- `delete`   DELETE `/api/teams/{team_id}/hubs/{hub_id}/content/{id}`
- `restore`  POST `/api/teams/{team_id}/hubs/{hub_id}/content/{id}/restore`
- `reorder`  POST `/api/teams/{team_id}/hubs/{hub_id}/content/reorder`

## pages  (`cmd/pages.go`)
- pages:    `create/list/retrieve/update/delete` `/api/teams/{team_id}/hubs/{hub_id}/pages[/{id}]`; `home` GET `…/pages/home`
- sections: `create` POST `…/pages/{pid}/sections`; `list` GET `…/pages/{pid}/sections`;
  `update` PATCH `…/pages/{pid}/sections/{sid}`; `delete` DELETE `…/pages/{pid}/sections/{sid}`;
  `reorder` PATCH `…/pages/{pid}/sections`

## products  (`cmd/products.go`) — REFERENCE RESOURCE
- products:     `create/list/retrieve/update/delete` `/api/teams/{team_id}/products[/{id}]`
- prices:       `create/list/retrieve/update/delete` `/api/teams/{team_id}/products/{id}/prices[/{pid}]`
- deliverables: `create/list/delete` `/api/teams/{team_id}/products/{id}/deliverables[/{did}]`
- hub-attach:   `attach/list/update/detach` `/api/teams/{team_id}/hubs/{hid}/products[/{id}]`
- hub-prices:   `list/update` `/api/teams/{team_id}/hubs/{hid}/prices`
- coupons:      `create/list/retrieve/update/delete` `/api/teams/{team_id}/coupons[/{id}]`
- coupon-products: `attach/list/detach` `/api/teams/{team_id}/coupons/{id}/products[/{pid}]`

## checkout  (`cmd/checkout.go`) — admin reads + actions, team-scoped
- orders:        `list/retrieve` `/api/teams/{team_id}/hubs/{hub_id}/orders[/{id}]`
- subscriptions: `list/retrieve` `…/subscriptions[/{id}]`; `cancel` POST `…/subscriptions/{id}/cancel`
- payments:      `list/retrieve` `…/payments[/{id}]`; `refund` POST `…/payments/{id}/refund`
- webhooks:      `list/retrieve` `…/payment-webhooks[/{id}]`; `replay` POST `…/payment-webhooks/{id}/replay`
- accounts:      `list/retrieve` `/api/teams/{team_id}/payment-accounts[/{id}]`;
                 `onboarding-link` POST `…/payment-accounts/onboarding-link`
- stripe-sync:   `import` POST `/api/teams/{team_id}/checkout/sync/import-from-stripe`;
                 `import-status` GET `…/checkout/sync/import-runs/{run_id}`;
                 `adopt-product` POST `/api/teams/{team_id}/products/adopt-from-stripe`

## email  (`cmd/email.go`) — base `/v1/hubs/{hub_id}/…`
- drip-campaigns: `create/list/retrieve/update/delete`; `activate`/`pause` POST `…/{id}/activate|pause`
- steps:          `list/create/update/delete` `…/drip-campaigns/{id}/steps[/{sid}]`
- templates:      `create/list/retrieve/update/delete` `…/email-templates[/{id}]`; `preview` POST `…/email-templates/{id}/preview`
- config:         `set` PUT `…/email-config`; `get` GET; `delete` DELETE; `test` POST `…/email-config/test`
- enrollments:    `list` GET `…/drip-campaigns/{id}/enrollments`; `exit` DELETE `…/{id}/enrollments/{eid}`
- stats:          `get` GET `…/email-stats`

## access-rules  (`cmd/accessrules.go`) — base `/api/teams/{team_id}/hubs/{hub_id}/…`
- rules:     `create/list/retrieve/update/delete` `…/access-rules[/{id}]`
- overrides: `create/list/retrieve/update/delete` `…/access-overrides[/{id}]`

## activity  (`cmd/activity.go`) — admin reads
- `contact`      GET `/api/teams/{team_id}/hubs/{hub_id}/activity/contacts/{contact_id}`
- `top-engaged`  GET `/api/teams/{team_id}/hubs/{hub_id}/activity/top-engaged`

---

## Out of scope (v1)
contact_auth, hub_memberships, comments (contact routes), realtime SSE, all
portal `/me` and `/api/hub/…` routes, webhooks, JWKS, magic-link landing,
community (stub, no routes yet).
