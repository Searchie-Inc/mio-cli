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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
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

// envelope wraps create/update attributes in a JSON:API resource document.
// Per JSON:API v1.1 a resource object MUST carry `type`; the mio backend pins
// each write schema's type to a Literal with extra="forbid", so omitting it
// yields a 422. The type is derived from the request path (see
// resourceTypeFromPath) rather than hardcoded per command.
type envelope struct {
	Data envelopeData `json:"data"`
}

type envelopeData struct {
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
}

// typeOverrides maps a request-path tail to the backend JSON:API resource
// `type` for the cases where the type is NOT simply the last collection path
// segment. The key is a "/"-joined suffix of the path's collection segments
// (most specific match wins) so context disambiguates the segments that mean
// different types in different parents (e.g. ".../products" under a coupon is
// "coupon-products" but under a hub is "hub-product-displays").
//
// Verified against mio-backend app/*/schemas.py JSON:API write resource type
// Literals (2026-06-01).
var typeOverrides = []struct {
	suffix string // collection segments, most-specific listed first below
	typ    string
}{
	// Contextual collisions — keep these BEFORE the bare-segment entries.
	{"coupons/products", "coupon-products"},
	{"hubs/products", "hub-product-displays"},
	{"hubs/prices", "hub-price-displays"},
	{"products/deliverables", "product-deliverables"},
	{"contact-attributes/options", "contact-attribute-options"},
	{"segments/search", "segment-search"},
	{"contacts/tags", "tag_assignments"},
	// Bare-segment overrides (URL segment != backend type).
	{"segments", "segment"},
	{"content", "content-nodes"},
	{"contacts", "team-contacts"},
	{"contact-attributes", "contact-attribute-definitions"},
	{"drip-campaigns", "drip_campaigns"},
	{"email-templates", "email_templates"},
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
	"pages": true, "sections": true, "api-keys": true, "roles": true,
	"users": true, "access-rules": true, "access-overrides": true,
	"drip-campaigns": true, "steps": true, "email-templates": true,
	"enrollments": true, "orders": true, "subscriptions": true,
	"payments": true, "attributes": true,
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

// Create performs a POST with the attributes wrapped in a JSON:API envelope.
// The resource `type` is derived from the path (resourceTypeFromPath).
func (c *Client) Create(ctx context.Context, path string, attrs map[string]any) (*Resource, error) {
	body, err := c.do(ctx, http.MethodPost, path, nil, newEnvelope(path, attrs), contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// Update performs a PATCH with the attributes wrapped in a JSON:API envelope.
// Per JSON:API, only the supplied fields are changed (partial update). The
// resource `type` is derived from the path (resourceTypeFromPath).
func (c *Client) Update(ctx context.Context, path string, attrs map[string]any) (*Resource, error) {
	body, err := c.do(ctx, http.MethodPatch, path, nil, newEnvelope(path, attrs), contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// newEnvelope builds a JSON:API resource document, deriving the `type` member
// from the request path so the backend's Literal-typed write schemas accept it.
func newEnvelope(path string, attrs map[string]any) envelope {
	return envelope{Data: envelopeData{Type: resourceTypeFromPath(path), Attributes: attrs}}
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
	var payload any
	if body != nil {
		payload = newEnvelope(path, body)
	}
	raw, err := c.do(ctx, method, path, nil, payload, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	return decodeResourceWrapped(raw)
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
	u := c.baseURL + ensureLeadingSlash(path)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reqBody io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("encode request body: %w", err))
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("build request: %w", err))
	}
	req.Header.Set("Accept", accept)
	if reqBody != nil {
		req.Header.Set("Content-Type", accept)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	if c.debug {
		fmt.Fprintf(stderr(), "[debug] %s %s\n", method, u)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("%s %s: %w", method, u, err))
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("read response: %w", err))
	}

	if c.debug {
		fmt.Fprintf(stderr(), "[debug] -> %d (%d bytes)\n", resp.StatusCode, len(respBody))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.errorForResponse(resp.StatusCode, respBody)
	}
	return respBody, nil
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
