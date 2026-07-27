// Package client is the mio HTTP layer: a thin JSON:API v1.1 client plus the
// plain-JSON auth helpers. It owns request construction (auth header, content
// negotiation), response decoding, and the translation of non-2xx responses
// into typed *errs.CLIError values carrying the right exit code.
//
// Resource commands depend only on the high-level verbs (List/Retrieve/Create/
// Update/Delete/Action). They never build URLs by hand beyond the path; the
// client joins the path onto the configured base.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// Retry-After / 429 backoff constants.
const (
	// rateLimitMaxRetries is the maximum number of automatic retries on a 429.
	// Two retries means three total attempts (initial + 2 retries).
	rateLimitMaxRetries = 2
	// rateLimitMaxWait caps the Retry-After value so a misbehaving server cannot
	// stall the CLI indefinitely. 60 s is the maximum rate-limit window in the
	// backend (auth login / checkout), so honouring values beyond it is pointless.
	rateLimitMaxWait = 60 * time.Second
)

// Content types. JSON:API for resource routes; plain JSON for /api/auth/*.
const (
	contentTypeJSONAPI = "application/vnd.api+json"
	contentTypeJSON    = "application/json"
)

// Client is a configured mio API client. Construct it with New; it is safe for
// sequential CLI use (one process, one command).
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	debug   bool

	// resolveCache memoizes name/slug → id lookups (see resolve.go) for the
	// lifetime of this client so repeat references in a single command/process
	// do not re-list. Its zero value is ready to use.
	resolveCache resolveCache
}

// Option customizes a Client at construction time.
type Option func(*Client)

// WithHTTPClient overrides the underlying *http.Client (used in tests).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithDebug enables verbose request/response logging to stderr.
func WithDebug(debug bool) Option {
	return func(c *Client) { c.debug = debug }
}

// New builds a Client for the given API base URL and bearer key. The apiKey may
// be empty for unauthenticated calls (e.g. auth login); the verbs that need it
// will surface a 401 from the server.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// BaseURL returns the client's API base origin, used to origin-scope the
// on-disk catalog cache.
func (c *Client) BaseURL() string { return c.baseURL }

// BodyStyle selects how a write request body is shaped on the wire.
//
// The mio backend is NOT uniform: most resources accept (or require) a JSON:API
// envelope {"data":{"type","attributes":{…}}}, but a handful of endpoints bind a
// FLAT pydantic model with the fields at the top level and no `data` wrapper
// (users, roles, api-keys, the checkout stripe-sync admin actions).
// Sending an envelope to a flat endpoint 422s (the required fields land under
// `data`, never reaching the model), and sending a flat body to an envelope
// endpoint 422s (missing `data`). The style is therefore a per-resource fact the
// command MUST declare; it is not derivable from the path.
//
// Verified against mio-backend app/*/schemas.py + router.py (2026-06-01, the
// worktree that contains the api_keys module + every resource on origin/main).
type BodyStyle int

const (
	// StyleEnvelope wraps attributes in {"data":{"type":<derived>,"attributes":{…}}}.
	// This is the default and correct for the large majority of resources.
	StyleEnvelope BodyStyle = iota
	// StyleFlat sends the attributes map directly as the top-level JSON object,
	// with NO envelope and NO injected `type`. Required for endpoints whose
	// backend request schema is a flat pydantic BaseModel.
	StyleFlat
)

// envelope wraps create/update attributes in a JSON:API resource document.
// Per JSON:API v1.1 a resource object MUST carry `type`; the mio backend pins
// each write schema's type to a Literal with extra="forbid", so omitting it
// yields a 422. The type is derived from the request path (see
// resourceTypeFromPath) rather than hardcoded per command.
type envelope struct {
	Data envelopeData `json:"data"`
}

type envelopeData struct {
	Type string `json:"type"`
	// ID is included in the write body only when set (omitempty). Most write
	// schemas take the id from the URL, but a few pin data.id in the body too
	// (e.g. AttachmentUpdateRequest → "Field required (/data/id)"); those use
	// UpdateWithID to populate it.
	ID         string         `json:"id,omitempty"`
	Attributes map[string]any `json:"attributes"`
}

// typeOverrides maps a request-path tail to the backend JSON:API resource
// `type` for the cases where the type is NOT simply the last collection path
// segment. The key is a "/"-joined suffix of the path's collection segments
// (most specific match wins) so context disambiguates the segments that mean
// different types in different parents (e.g. ".../products" under a coupon is
// "coupon_products" but under a hub is "hub_product_displays").
//
// Verified against mio-backend app/*/schemas.py JSON:API write resource type
// Literals (2026-06-01).
var typeOverrides = []struct {
	suffix string // collection segments, most-specific listed first below
	typ    string
}{
	// Contextual collisions — keep these BEFORE the bare-segment entries.
	// All type values are snake_case per MIO-636 (backend cutover 2026-06-04).
	{"coupons/products", "coupon_products"},
	{"hubs/products", "hub_product_displays"},
	{"hubs/prices", "hub_price_displays"},
	// Publishing a playlist to a hub writes a hub_media row (MIO-2259); the
	// bare "playlists" segment would derive the team-scoped "playlists" type.
	{"hubs/playlists", "hub_media"},
	// Playlist item verbs (MIO-2513): add (POST) and reorder (PATCH) on
	// .../playlists/{id}/items[/{item_id}] bind PlaylistItemAttachData /
	// PlaylistItemUpdateData whose type Literal is "playlist_items"; the bare
	// "items" segment would otherwise resolve to the "playlists" parent type.
	{"playlists/items", "playlist_items"},
	// Publishing a standalone file to a hub also writes a hub_media row
	// (MIO-2266); the bare "media" segment would derive "media".
	{"hubs/media", "hub_media"},
	// Media enrichment (MIO-2266): in-video CTA cards + authorable chapters are
	// full-list PUT replaces whose backend Literal types are the snake_case
	// plurals, not the "cards"/"chapters" URL segments.
	{"files/cards", "file_cards"},
	{"files/chapters", "file_chapters"},
	// Folder subtree move (MIO-2266): POST .../folders/{id}/move binds the
	// FolderMove schema whose type Literal is "folders" (NOT "move"). "move" is
	// a known collection token so it is not mistaken for the {id}; this override
	// then resolves the folders/move tail back to the "folders" type.
	{"folders/move", "folders"},
	{"products/deliverables", "product_deliverables"},
	{"contact-attributes/options", "contact_attribute_options"},
	// hub-config lives at /hubs/{hub}/contact-attributes — same trailing
	// segment as team-level definitions, disambiguated by the hubs parent.
	{"hubs/contact-attributes", "contact_attribute_hub_configs"},
	{"content/reorder", "content_nodes"},
	{"segments/search", "segment_search"},
	{"contacts/tags", "tag_assignments"},
	{"drip-campaigns/steps", "drip_steps"},
	{"pages/sections", "sections"},
	// Page draft node-tree authoring: PUT .../pages/{id}/tree (MIO-2258).
	{"pages/tree", "page_draft_trees"},
	// W2b one-step template scaffold (MIO-2573 §5.1): POST
	// .../hubs/{hub}/pages/scaffold-from-template binds a write schema whose
	// data.type Literal is "template_scaffolds"; the bare "pages" collection
	// would derive "pages" without this override.
	{"pages/scaffold-from-template", "template_scaffolds"},
	{"teams/members", "team_members"},
	// Hub membership authoring (MIO-2261 add, MIO-2263 set-role): the members
	// collection under a hub is the hub_memberships resource, not team_members.
	{"hubs/members", "hub_memberships"},
	{"members/role", "hub_memberships"},
	// Action routes that take an enveloped body with a non-segment type.
	{"payment-accounts/onboarding-link", "onboarding_links"},
	{"payments/refund", "refunds"},
	// Automations sub-resource enrollments carry a different type than the
	// top-level enrollments segment on other resources.
	{"automations/enrollments", "automation_enrollments"},
	// Drip-campaign enrollments: backend DripEnrollCreateRequest pins type to
	// "drip_enrollments" (Literal const); the bare "enrollments" segment would
	// not match that Literal and returns 422.
	{"drip-campaigns/enrollments", "drip_enrollments"},
	// Hub email-settings: backend HubEmailSenderUpdateEnvelope pins type to
	// "hub_email_senders"; the segment "email-settings" would not match that
	// Literal without this override (MIO-1229).
	{"hubs/email-settings", "hub_email_senders"},
	// Bare-segment overrides (URL segment != backend type).
	{"segments", "segment"},
	{"content", "content_nodes"},
	{"contacts", "team_contacts"},
	{"contact-attributes", "contact_attribute_definitions"},
	{"drip-campaigns", "drip_campaigns"},
	{"email-templates", "email_templates"},
	{"webhook-endpoints", "webhook_endpoints"},
	// access-rules sub-resources: URL segment uses hyphens but JSON:API type
	// is snake_case. Without these overrides resourceTypeFromPath returns the
	// hyphenated segment verbatim ("access-rules", "access-overrides"), which
	// the backend's Literal-typed write schemas reject with 400 (MIO-992).
	{"access-rules", "access_rules"},
	{"access-overrides", "access_overrides"},
	// OAuth client management (Hub-as-IdP SSO, 2026-06-24).
	// redirect-uris is a sub-resource of oauth-clients; two-segment override
	// takes priority and resolves to the correct backend type literal.
	{"oauth-clients/redirect-uris", "oauth_client_redirect_uris"},
	{"oauth-clients", "oauth_clients"},
	// External login provider admin commands (MIO-1513, 2026-06-25).
	// URL segment uses hyphens; JSON:API type is snake_case.
	{"external-login-providers", "external_login_providers"},
	// Verified-domain admin commands (External Login v2 verified-domain SSO,
	// MIO-1513, 2026-06-25). URL segment uses hyphens; JSON:API type is snake_case.
	{"verified-domains", "verified_domains"},
	// Long-tail admin bundle (MIO-2269).
	// Hub policy enforcement gate: PATCH .../hubs/{hub}/policies/gate binds
	// HubPolicyGateEnvelope whose data.type Literal is "hub_policy_gate"; the
	// bare "gate" segment would not match. Two-segment tail wins over "policies".
	{"policies/gate", "hub_policy_gate"},
	// Magic-link redirect-origin allowlist: PUT .../hubs/{hub}/redirect-origins
	// binds RedirectOriginsUpdateEnvelope whose data.type Literal is
	// "hub_redirect_origin_allowlists"; the hyphenated segment would not match.
	{"redirect-origins", "hub_redirect_origin_allowlists"},
	// Hub email-suppression admin-block create: POST
	// .../hubs/{hub}/email-suppressions binds HubCreateSuppressionData whose
	// type is "email_suppressions"; the hyphenated segment would not match.
	{"email-suppressions", "email_suppressions"},
	// Community moderation report-reasons (MIO-2265): the create POST derives
	// its JSON:API type from the .../report-reasons collection tail. The URL
	// segment uses hyphens; the backend ReportReasonCreateData type Literal is
	// snake_case "report_reasons", so without this override the write 422s.
	{"report-reasons", "report_reasons"},
	// Media transcript edit/revert (MIO-2289): the singular "transcript" URL
	// segment maps to the backend "transcripts" type Literal. Applies to both
	// PATCH .../media/{id}/transcript and POST .../transcript/revert ("revert" is
	// left out of knownCollections so its last collection resolves to transcript).
	{"transcript", "transcripts"},
	// Media file content-replace init (MIO-2423): .../files/{id}/replace →
	// "file_replacements" (the "replace" segment != the backend type Literal).
	{"replace", "file_replacements"},
	// Playlist cover set (MIO-2289): POST .../playlist-cover-attachments binds
	// the AttachmentCreateRequest whose type Literal is "attachments"; the
	// hyphenated segment would not match without this override.
	{"playlist-cover-attachments", "attachments"},
}

// resourceTypeFromPath returns the JSON:API resource `type` for a write to the
// given path. The default is the last *collection* path segment (the segment
// before a trailing /{id} when present); typeOverrides handles the resources
// whose backend type differs from that segment.
//
// A "collection" segment is a static route segment (products, prices, …). The
// trailing id, if any, is the segment after the final collection segment; we
// detect it heuristically as a non-collection last segment, but since both
// create (no id) and update (trailing id) need the SAME type, we simply take
// the last two segments and treat the deeper one that matches a known/likely
// collection as the type source. In practice: split, drop a trailing id if the
// path has an even history of collection/{id} pairs, then match overrides on
// the collection tail.
func resourceTypeFromPath(path string) string {
	segs := splitPathSegments(path)
	if len(segs) == 0 {
		return ""
	}

	// Build the ordered list of *collection* segments by walking the path and
	// treating it as alternating collection/{id} pairs after the team/hub
	// prefix. Rather than parse ids (opaque), we collect every segment and use
	// the last segment as the collection when the path ends in a collection
	// (create/list/search/action) or the second-to-last when it ends in an id
	// (retrieve/update/delete). We disambiguate by checking whether the last
	// segment matches a known collection token; ids never do.
	collections := collectionSegments(segs)
	if len(collections) == 0 {
		// Fallback: last segment.
		return segs[len(segs)-1]
	}

	// Try suffix overrides from most-specific (2-segment) to least (1-segment).
	if len(collections) >= 2 {
		twoTail := collections[len(collections)-2] + "/" + collections[len(collections)-1]
		for _, o := range typeOverrides {
			if o.suffix == twoTail {
				return o.typ
			}
		}
	}
	lastColl := collections[len(collections)-1]
	for _, o := range typeOverrides {
		if o.suffix == lastColl {
			return o.typ
		}
	}
	return lastColl
}

// knownCollections is the set of static collection tokens used in mio routes.
// Used to tell a collection segment from an opaque resource id when deriving
// the resource type from a path.
var knownCollections = map[string]bool{
	"teams": true, "hubs": true, "products": true, "prices": true,
	"deliverables": true, "coupons": true, "segments": true, "search": true,
	"members": true, "contacts": true, "content": true, "children": true,
	"tags": true, "contact-attributes": true, "options": true,
	"reorder": true,
	"pages":   true, "sections": true, "tree": true, "api-keys": true, "roles": true,
	// W2b one-step template scaffold op (MIO-2573 §5.1); maps to
	// "template_scaffolds" via the pages/scaffold-from-template override.
	"scaffold-from-template": true,
	"users":                  true, "access-rules": true, "access-overrides": true,
	"drip-campaigns": true, "steps": true, "email-templates": true,
	"enrollments": true, "orders": true, "subscriptions": true,
	"payments": true, "refund": true, "attributes": true,
	"payment-accounts": true, "onboarding-link": true,
	"policies": true,
	// Hub per-sender email identity (MIO-1229).
	"email-settings": true,
	// Automations + webhook-endpoints (2026-06-09).
	"automations": true, "webhook-endpoints": true, "versions": true, "events": true,
	// Community admin (2026-06-09): spaces, discussions wired in community.go.
	"spaces": true, "discussions": true,
	// Community moderation console (MIO-2265): report-reasons CRUD write path
	// (create derives type "report_reasons" via the typeOverride above) and the
	// admin comments list/delete collection segment.
	"report-reasons": true, "comments": true,
	// Hub membership role sub-resource (MIO-2263).
	"role": true,
	// Media (2026-06-09): files, folders, playlists wired in media.go.
	"files": true, "folders": true, "playlists": true,
	// Playlist item verbs (MIO-2513): add/list/remove/reorder items on a
	// playlist (.../playlists/{id}/items[/{item_id}]). Without "items" as a known
	// token the write path's last collection resolves to "playlists" and the
	// backend 422s on the wrong type; the playlists/items typeOverride below maps
	// the two-segment tail to "playlist_items".
	"items": true,
	// Media enrichment (MIO-2266): in-video cards, authorable chapters, folder
	// subtree move, and standalone-file hub publishing (hubs/{hub}/media).
	"cards": true, "chapters": true, "move": true, "media": true,
	// Media transcripts (MIO-2289): edit (PATCH .../media/{id}/transcript) +
	// revert (POST .../transcript/revert) derive type "transcripts" via the
	// bare-segment override. "revert" is deliberately NOT a known token so the
	// revert path's last collection resolves to "transcript".
	"transcript": true,
	// Media file content-replace init (MIO-2423): POST .../files/{id}/replace
	// binds FileReplacementInitRequest (type "file_replacements").
	"replace": true,
	// Attachments admin (MIO-2289): list/show/update/delete. Bare "attachments"
	// self-derives the JSON:API type "attachments". The hyphenated
	// playlist-cover-attachments create collection maps to "attachments" too
	// (via the typeOverride below).
	"attachments":                true,
	"playlist-cover-attachments": true,
	// Contact-scoped drip enrollment reader (email enrollments list-by-contact).
	"drip-enrollments": true,
	// OAuth client management (Hub-as-IdP SSO, 2026-06-24).
	"oauth-clients": true, "redirect-uris": true,
	// External login provider admin commands (MIO-1513, 2026-06-25).
	"external-login-providers": true,
	// Verified-domain admin commands (External Login v2, MIO-1513, 2026-06-25).
	"verified-domains": true,
	// Long-tail admin bundle (MIO-2269): hub policy gate, redirect-origin
	// allowlist, and hub-scoped email-suppression admin surfaces.
	"gate": true, "redirect-origins": true, "email-suppressions": true,
}

// collectionSegments returns the path segments that are static collection
// tokens (skipping the /api prefix, the {id} args, and the team/hub ids).
func collectionSegments(segs []string) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if s == "api" {
			continue
		}
		if knownCollections[s] {
			out = append(out, s)
		}
	}
	return out
}

func splitPathSegments(path string) []string {
	out := make([]string, 0, 8)
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// List performs a GET against a collection route and decodes the JSON:API
// collection document, including the top-level meta (pagination cursors).
func (c *Client) List(ctx context.Context, path string, query url.Values) (*Collection, error) {
	body, err := c.do(ctx, http.MethodGet, path, query, nil, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	col, derr := DecodeCollection(body)
	if derr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, derr)
	}
	return col, nil
}

// Retrieve performs a GET against a single-resource route.
func (c *Client) Retrieve(ctx context.Context, path string) (*Resource, error) {
	body, err := c.do(ctx, http.MethodGet, path, nil, nil, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// RetrieveWithQuery performs a GET against a single-resource route with
// additional query parameters. Use this instead of string-concatenating query
// params onto the path (e.g. pages retrieve --tree passes resolve=false).
func (c *Client) RetrieveWithQuery(ctx context.Context, path string, query url.Values) (*Resource, error) {
	body, err := c.do(ctx, http.MethodGet, path, query, nil, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// Create performs a POST with the attributes wrapped in a JSON:API envelope.
// The resource `type` is derived from the path (resourceTypeFromPath). Use
// CreateWith(StyleFlat, …) for the handful of endpoints whose backend schema is
// a flat pydantic model (users/roles/api-keys/stripe-sync).
func (c *Client) Create(ctx context.Context, path string, attrs map[string]any) (*Resource, error) {
	return c.CreateWith(ctx, StyleEnvelope, path, attrs)
}

// CreateWith performs a POST shaping the body per the given BodyStyle.
func (c *Client) CreateWith(ctx context.Context, style BodyStyle, path string, attrs map[string]any) (*Resource, error) {
	body, err := c.do(ctx, http.MethodPost, path, nil, buildWriteBody(style, path, attrs), contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// Update performs a PATCH with the attributes wrapped in a JSON:API envelope.
// Per JSON:API, only the supplied fields are changed (partial update). The
// resource `type` is derived from the path (resourceTypeFromPath). Use
// UpdateWith(StyleFlat, …) for flat-schema endpoints.
func (c *Client) Update(ctx context.Context, path string, attrs map[string]any) (*Resource, error) {
	return c.UpdateWith(ctx, StyleEnvelope, path, attrs)
}

// UpdateWith performs a PATCH shaping the body per the given BodyStyle.
func (c *Client) UpdateWith(ctx context.Context, style BodyStyle, path string, attrs map[string]any) (*Resource, error) {
	body, err := c.do(ctx, http.MethodPatch, path, nil, buildWriteBody(style, path, attrs), contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// UpdateWithID performs a PATCH whose JSON:API envelope carries data.id in the
// body (in addition to the URL). Use it for write schemas that require the id in
// the body — e.g. AttachmentUpdateRequest, which 400s with "Field required
// (/data/id)" otherwise. The `type` is still derived from the path.
func (c *Client) UpdateWithID(ctx context.Context, path, id string, attrs map[string]any) (*Resource, error) {
	env := envelope{Data: envelopeData{Type: resourceTypeFromPath(path), ID: id, Attributes: attrs}}
	body, err := c.do(ctx, http.MethodPatch, path, nil, env, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// buildWriteBody shapes a write payload per the requested BodyStyle. A nil attrs
// map yields a nil payload (no body sent) regardless of style. StyleEnvelope
// wraps the attributes in a JSON:API resource document with the `type` derived
// from the path; StyleFlat returns the attributes map verbatim as the top-level
// object.
func buildWriteBody(style BodyStyle, path string, attrs map[string]any) any {
	if attrs == nil {
		return nil
	}
	if style == StyleFlat {
		return attrs
	}
	return newEnvelope(path, attrs)
}

// newEnvelope builds a JSON:API resource document, deriving the `type` member
// from the request path so the backend's Literal-typed write schemas accept it.
func newEnvelope(path string, attrs map[string]any) envelope {
	return envelope{Data: envelopeData{Type: resourceTypeFromPath(path), Attributes: attrs}}
}

// RawEnvelope is a JSON:API resource document whose `attributes` may be any
// shape (not just a flat string→any map). It exists for write endpoints whose
// attributes carry nested structure the backend validates strictly — e.g.
// segment search, whose attributes are {"conditions": <tree>, "page": <obj>}.
type RawEnvelope struct {
	Data RawEnvelopeData `json:"data"`
}

// RawEnvelopeData is the resource object inside a RawEnvelope.
type RawEnvelopeData struct {
	Type       string `json:"type"`
	Attributes any    `json:"attributes"`
}

// NewRawEnvelope builds a JSON:API resource document with an explicit `type`
// and arbitrary `attributes`. Used by commands that must send a structured
// (non-flat) attributes object the path-derived flat envelope cannot express.
func NewRawEnvelope(typ string, attributes any) RawEnvelope {
	return RawEnvelope{Data: RawEnvelopeData{Type: typ, Attributes: attributes}}
}

// Delete performs a DELETE and discards any body. A 204 (or any 2xx) is success.
func (c *Client) Delete(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, path, nil, nil, contentTypeJSONAPI)
	return err
}

// Action performs a custom action route (cancel, refund, replay, restore, …)
// that returns a SINGLE resource (or no body). body may be nil for actions that
// take no payload; when non-nil it is wrapped in the JSON:API envelope like
// create/update, with the `type` derived from the path. An empty 2xx body
// yields a nil resource and nil error.
//
// For actions whose response is a `data: [...]` collection, use
// ActionCollection instead — Action's single-resource decoder cannot read a
// list.
func (c *Client) Action(ctx context.Context, method, path string, body map[string]any) (*Resource, error) {
	return c.ActionWith(ctx, StyleEnvelope, method, path, body)
}

// ActionWith is Action with an explicit BodyStyle for the request payload. Flat
// action endpoints (the checkout stripe-sync import/adopt admin actions) pass
// StyleFlat so their fields are sent at the top level without an envelope.
// (email-config PUT is a JSON:API envelope endpoint — StyleEnvelope, MIO-2640.)
func (c *Client) ActionWith(ctx context.Context, style BodyStyle, method, path string, body map[string]any) (*Resource, error) {
	payload := buildWriteBody(style, path, body)
	raw, err := c.do(ctx, method, path, nil, payload, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	return decodeResourceWrapped(raw)
}

// ActionRaw performs a custom action route whose response is a FLAT JSON
// document — a plain object with NO JSON:API `data` member — and returns it
// decoded into a map. It exists for the handful of action endpoints that return
// a bespoke report rather than a resource/collection: e.g. POST
// .../automations/{id}/test returns {"meta":…,"trace":[…]} (DryRunResponse) and
// POST .../automations/events returns a meta-only ack {"meta":…}. Routing those
// through ActionWith's resource decoder fails with "response had no `data`
// member" on every SUCCESSFUL call (MIO-2503).
//
// The request body is shaped per the given BodyStyle (like ActionWith); an empty
// 2xx body yields a nil map and nil error so callers can print a fallback line.
func (c *Client) ActionRaw(ctx context.Context, style BodyStyle, method, path string, body map[string]any) (map[string]any, error) {
	payload := buildWriteBody(style, path, body)
	raw, err := c.do(ctx, method, path, nil, payload, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var out map[string]any
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("decode response: %w", uerr))
	}
	return out, nil
}

// ActionWithHeaders is ActionWith plus a set of extra HTTP request headers. It
// is the least-invasive way to send conditional headers (e.g. If-Match for the
// pages publish endpoint) without altering the signatures of any existing method.
// body may be nil (no request body is sent); style is applied only when body is
// non-nil. extra is merged on top of the standard Accept/Content-Type/Authorization
// headers; callers that need no extras should use Action or ActionWith instead.
func (c *Client) ActionWithHeaders(ctx context.Context, style BodyStyle, method, path string, body map[string]any, extra map[string]string) (*Resource, error) {
	res, _, err := c.actionWithHeadersStatus(ctx, style, method, path, body, extra)
	return res, err
}

// actionWithHeadersStatus is ActionWithHeaders plus visibility of the HTTP
// status code of the final response (0 when the request never produced one —
// encode/build/transport failures). It exists for PROBE-style callers that
// must classify specific statuses beyond the global exit-code mapping (e.g.
// ScaffoldFromTemplate treating 405 as op-absent) WITHOUT changing
// errorForResponse for every other caller.
func (c *Client) actionWithHeadersStatus(ctx context.Context, style BodyStyle, method, path string, body map[string]any, extra map[string]string) (*Resource, int, error) {
	payload := buildWriteBody(style, path, body)
	raw, status, err := c.doWithHeadersStatus(ctx, method, path, nil, payload, contentTypeJSONAPI, extra)
	if err != nil {
		return nil, status, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, status, nil
	}
	res, derr := decodeResourceWrapped(raw)
	return res, status, derr
}

// doWithHeaders is doWithHeadersStatus without the status return — the shape
// every non-probe caller uses.
func (c *Client) doWithHeaders(ctx context.Context, method, path string, query url.Values, payload any, accept string, extra map[string]string) ([]byte, error) {
	raw, _, err := c.doWithHeadersStatus(ctx, method, path, query, payload, accept, extra)
	return raw, err
}

// doWithHeadersStatus is the single HTTP request choke point. It is called by
// do() (via doWithHeaders, which passes a nil extra map) and by
// ActionWithHeaders (which passes extra headers for conditional requests such
// as If-Match). All client methods ultimately funnel through here. The second
// return is the HTTP status of the final response, 0 when the request never
// produced one (encode/build/transport failures).
//
// 429 handling: when the server returns HTTP 429 Too Many Requests this method
// reads the Retry-After response header (whole seconds) and sleeps for that
// duration (capped at rateLimitMaxWait) before retrying. It retries at most
// rateLimitMaxRetries times; if the server keeps returning 429 after all
// retries are exhausted it returns the 429 error with a message that includes
// the suggested wait time so the user knows how long to back off manually.
func (c *Client) doWithHeadersStatus(ctx context.Context, method, path string, query url.Values, payload any, accept string, extra map[string]string) ([]byte, int, error) {
	u := c.baseURL + canonicalRequestPath(path)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	// Marshal the payload once; on retry we reset the reader position from the
	// same buffer so we do not re-marshal on every attempt.
	var payloadBuf []byte
	if payload != nil {
		var err error
		payloadBuf, err = json.Marshal(payload)
		if err != nil {
			return nil, 0, errs.Wrap(errs.ExitGeneric, fmt.Errorf("encode request body: %w", err))
		}
	}

	for attempt := 0; ; attempt++ {
		var reqBody io.Reader
		if payloadBuf != nil {
			reqBody = bytes.NewReader(payloadBuf)
		}

		req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
		if err != nil {
			return nil, 0, errs.Wrap(errs.ExitGeneric, fmt.Errorf("build request: %w", err))
		}
		req.Header.Set("Accept", accept)
		// Set Content-Type on any write method even when the body is empty.
		// No-body action POSTs (e.g. automations /publish, /activate) require
		// Content-Type: application/vnd.api+json via require_jsonapi_content_type
		// middleware; without it the backend returns 415/500 (MIO-1115).
		if reqBody != nil || method == http.MethodPost || method == http.MethodPatch {
			req.Header.Set("Content-Type", accept)
		}
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}

		if c.debug {
			fmt.Fprintf(stderr(), "[debug] %s %s (attempt %d)\n", method, u, attempt+1)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, 0, errs.Wrap(errs.ExitGeneric, fmt.Errorf("%s %s: %w", method, u, err))
		}
		respBody, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			return nil, resp.StatusCode, errs.Wrap(errs.ExitGeneric, fmt.Errorf("read response: %w", rerr))
		}

		if c.debug {
			fmt.Fprintf(stderr(), "[debug] -> %d (%d bytes)\n", resp.StatusCode, len(respBody))
		}

		// Happy path.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, resp.StatusCode, nil
		}

		// 429: honour Retry-After if we have retries left.
		if resp.StatusCode == http.StatusTooManyRequests && attempt < rateLimitMaxRetries {
			wait := retryAfterDuration(resp.Header.Get("Retry-After"))
			if c.debug {
				fmt.Fprintf(stderr(), "[debug] rate limited; retrying in %s (attempt %d/%d)\n",
					wait.Round(time.Second), attempt+1, rateLimitMaxRetries)
			}
			select {
			case <-ctx.Done():
				return nil, resp.StatusCode, errs.Wrap(errs.ExitGeneric, ctx.Err())
			case <-time.After(wait):
			}
			continue
		}

		// All other non-2xx responses, or 429 after retries exhausted.
		return nil, resp.StatusCode, c.errorForResponse(resp.StatusCode, respBody)
	}
}

// retryAfterDuration parses a Retry-After header value (integer seconds) and
// returns a duration capped at rateLimitMaxWait. If the header is absent or
// unparseable it falls back to 1 second so the CLI still backs off rather than
// hammering the server.
//
// The float → Duration conversion can overflow for very large values (e.g.
// 1e10 seconds) before the cap is applied, producing a negative or wrapped
// duration. To prevent this we cap the float seconds value at the equivalent
// of rateLimitMaxWait BEFORE the conversion so the arithmetic stays within
// the safe range of int64.
func retryAfterDuration(header string) time.Duration {
	if header == "" {
		return time.Second
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(header), 64)
	// ParseFloat accepts "NaN" and "±Inf" without error, and NaN compares
	// false against every range check below — converting it to an int is
	// implementation-specific (can yield an invalid/negative duration). Treat
	// non-finite values exactly like unparseable input: fall back to 1 s.
	if err != nil || math.IsNaN(secs) || math.IsInf(secs, 0) || secs < 0 {
		return time.Second
	}
	// Cap seconds in float space first so the subsequent int64 arithmetic
	// cannot overflow. rateLimitMaxWait / time.Second is safe to convert to
	// float64 (it is 60, well within float64 precision).
	maxSecs := float64(rateLimitMaxWait / time.Second)
	if secs > maxSecs {
		return rateLimitMaxWait
	}
	return time.Duration(math.Ceil(secs)) * time.Second
}

// ActionCollection performs a custom action route that returns a JSON:API
// collection document (`data: [...]`), e.g. `segments search` which previews
// matching contacts. body may be nil; when non-nil it is wrapped in the
// JSON:API envelope with the `type` derived from the path. An empty 2xx body
// yields an empty collection.
func (c *Client) ActionCollection(ctx context.Context, method, path string, body map[string]any) (*Collection, error) {
	var payload any
	if body != nil {
		payload = newEnvelope(path, body)
	}
	return c.actionCollectionPayload(ctx, method, path, payload)
}

// ActionCollectionRaw is ActionCollection for endpoints whose request body is
// NOT a simple flat-attributes envelope. The caller supplies the exact JSON the
// backend expects (e.g. segment search's
// {"data":{"type":"segment_search","attributes":{"conditions":…,"page":…}}}).
// A nil payload sends no body.
func (c *Client) ActionCollectionRaw(ctx context.Context, method, path string, payload any) (*Collection, error) {
	return c.actionCollectionPayload(ctx, method, path, payload)
}

func (c *Client) actionCollectionPayload(ctx context.Context, method, path string, payload any) (*Collection, error) {
	raw, err := c.do(ctx, method, path, nil, payload, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return &Collection{Data: []Resource{}}, nil
	}
	col, derr := DecodeCollection(raw)
	if derr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, derr)
	}
	return col, nil
}

// decodeResourceWrapped decodes a single resource and tags any decode failure
// with a generic exit code.
func decodeResourceWrapped(body []byte) (*Resource, error) {
	res, derr := DecodeResource(body)
	if derr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, derr)
	}
	return res, nil
}

// do is the single request choke point. It builds the URL, sets auth and
// content negotiation headers, executes, and converts non-2xx responses into a
// typed *errs.CLIError. accept controls both the Accept and (for bodies) the
// Content-Type header, so auth helpers can request plain JSON.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, payload any, accept string) ([]byte, error) {
	return c.doWithHeaders(ctx, method, path, query, payload, accept, nil)
}

// errorForResponse maps a non-2xx response into a *errs.CLIError. If the body
// carries a JSON:API `errors` array, its joined message is used; otherwise a
// generic status-based message is returned. The exit code is derived from the
// HTTP status so it is correct regardless of body shape.
func (c *Client) errorForResponse(status int, body []byte) error {
	code := errs.ExitCodeForStatus(status)

	// Try to parse a JSON:API errors array for a precise message. We reuse the
	// collection decoder shape which already understands the errors array.
	var doc struct {
		Errors []apiError `json:"errors"`
	}
	if json.Unmarshal(body, &doc) == nil && len(doc.Errors) > 0 {
		return errs.Wrap(code, &apiErrorList{Errors: doc.Errors})
	}

	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return errs.New(code, "request failed (HTTP %d): %s", status, msg)
}

func ensureLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func canonicalRequestPath(path string) string {
	p := ensureLeadingSlash(path)
	if p == "/api" {
		return "/api/v1"
	}
	if !strings.HasPrefix(p, "/api/") {
		return p
	}
	rest := strings.TrimPrefix(p, "/api/")
	if hasAPIVersionPrefix(rest) {
		return p
	}
	return "/api/v1/" + rest
}

func hasAPIVersionPrefix(rest string) bool {
	if len(rest) < 2 || rest[0] != 'v' {
		return false
	}
	i := 1
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	return i > 1 && (i == len(rest) || rest[i] == '/')
}
