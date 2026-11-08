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

const (
	rateLimitMaxRetries = 2
	rateLimitMaxWait    = 60 * time.Second
)

const (
	contentTypeJSONAPI = "application/vnd.api+json"
	contentTypeJSON    = "application/json"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	debug   bool

	resolveCache resolveCache
}

type Option func(*Client)

func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

func WithDebug(debug bool) Option {
	return func(c *Client) { c.debug = debug }
}

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

func (c *Client) BaseURL() string { return c.baseURL }

type BodyStyle int

const (
	StyleEnvelope BodyStyle = iota
	StyleFlat
)

type envelope struct {
	Data envelopeData `json:"data"`
}

type envelopeData struct {
	Type       string         `json:"type"`
	ID         string         `json:"id,omitempty"`
	Attributes map[string]any `json:"attributes"`
}

var typeOverrides = []struct {
	suffix string
	typ    string
}{
	{"remote-connections", "remote_connections"},
	{"coupons/products", "coupon_products"},
	{"hubs/products", "hub_product_displays"},
	{"hubs/prices", "hub_price_displays"},
	{"hubs/playlists", "hub_media"},
	{"playlists/items", "playlist_items"},
	{"hubs/media", "hub_media"},
	{"files/cards", "file_cards"},
	{"files/captions", "file_captions"},
	{"files/chapters", "file_chapters"},
	{"folders/move", "folders"},
	{"products/deliverables", "product_deliverables"},
	{"contact-attributes/options", "contact_attribute_options"},
	{"hubs/contact-attributes", "contact_attribute_hub_configs"},
	{"content/reorder", "content_nodes"},
	{"segments/search", "segment_search"},
	{"contacts/tags", "tag_assignments"},
	{"drip-campaigns/steps", "drip_steps"},
	{"pages/sections", "sections"},
	{"pages/tree", "page_draft_trees"},
	{"pages/scaffold-from-template", "template_scaffolds"},
	{"hubs/from-template", "hub_scaffolds"},
	{"teams/members", "team_members"},
	{"hubs/members", "hub_memberships"},
	{"members/role", "hub_memberships"},
	{"payment-accounts/onboarding-link", "onboarding_links"},
	{"payments/refund", "refunds"},
	{"automations/enrollments", "automation_enrollments"},
	{"drip-campaigns/enrollments", "drip_enrollments"},
	{"hubs/email-settings", "hub_email_senders"},
	{"segments", "segment"},
	{"content", "content_nodes"},
	{"contacts", "team_contacts"},
	{"contact-attributes", "contact_attribute_definitions"},
	{"drip-campaigns", "drip_campaigns"},
	{"email-templates", "email_templates"},
	{"webhook-endpoints", "webhook_endpoints"},
	{"access-rules", "access_rules"},
	{"access-overrides", "access_overrides"},
	{"oauth-clients/redirect-uris", "oauth_client_redirect_uris"},
	{"oauth-clients", "oauth_clients"},
	{"external-login-providers", "external_login_providers"},
	{"verified-domains", "verified_domains"},
	{"policies/gate", "hub_policy_gate"},
	{"redirect-origins", "hub_redirect_origin_allowlists"},
	{"email-suppressions", "email_suppressions"},
	{"hubs/events", "hub_events"},
	{"events/rsvp", "event_rsvps"},
	{"events/cancel", "hub_event_cancellations"},
	{"hubs/achievements", "achievement_hubs"},
	{"members/achievements", "achievement_earns"},
	{"achievements/rule", "achievement_rules"},
	{"report-reasons", "report_reasons"},
	{"transcript", "transcripts"},
	{"replace", "file_replacements"},
	{"playlist-cover-attachments", "attachments"},
	{"file-thumbnail-attachments", "attachments"},
	{"hub-branding-attachments", "attachments"},
}

func resourceTypeFromPath(path string) string {
	segs := splitPathSegments(path)
	if len(segs) == 0 {
		return ""
	}

	for i, seg := range segs {
		if seg == "remote-connections" && i+2 == len(segs)-1 {
			switch segs[len(segs)-1] {
			case "import":
				return "remote_imports"
			case "browse":
				return "remote_recordings"
			}
		}
	}
	collections := collectionSegments(segs)
	if len(segs) == 6 && segs[0] == "api" && segs[1] == "hubs" && segs[3] == "events" && segs[5] == "cancel" {
		collections = append(collections, "cancel")
	}
	if len(collections) == 0 {
		return segs[len(segs)-1]
	}

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

var knownCollections = map[string]bool{
	"remote-connections": true,
	"teams":              true, "hubs": true, "products": true, "prices": true,
	"deliverables": true, "coupons": true, "segments": true, "search": true,
	"members": true, "contacts": true, "content": true, "children": true,
	"tags": true, "contact-attributes": true, "options": true,
	"reorder": true,
	"pages":   true, "sections": true, "tree": true, "api-keys": true, "roles": true,
	"scaffold-from-template": true,
	"from-template":          true,
	"users":                  true, "access-rules": true, "access-overrides": true,
	"drip-campaigns": true, "steps": true, "email-templates": true,
	"enrollments": true, "orders": true, "subscriptions": true,
	"payments": true, "refund": true, "attributes": true,
	"payment-accounts": true, "onboarding-link": true,
	"policies":       true,
	"email-settings": true,
	"automations":    true, "webhook-endpoints": true, "versions": true, "events": true,
	"spaces": true, "discussions": true,
	"report-reasons": true, "comments": true,
	"role":  true,
	"files": true, "folders": true, "playlists": true,
	"items": true,
	"cards": true, "captions": true, "chapters": true, "move": true, "media": true,
	"transcript":                 true,
	"replace":                    true,
	"attachments":                true,
	"playlist-cover-attachments": true,
	"file-thumbnail-attachments": true,
	"hub-branding-attachments":   true,
	"drip-enrollments":           true,
	"oauth-clients":              true, "redirect-uris": true,
	"external-login-providers": true,
	"verified-domains":         true,
	"gate":                     true, "redirect-origins": true, "email-suppressions": true,
	"rsvp":         true,
	"achievements": true,
	"rule":         true,
}

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

func (c *Client) Retrieve(ctx context.Context, path string) (*Resource, error) {
	body, err := c.do(ctx, http.MethodGet, path, nil, nil, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

func (c *Client) RetrieveWithQuery(ctx context.Context, path string, query url.Values) (*Resource, error) {
	body, err := c.do(ctx, http.MethodGet, path, query, nil, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

func (c *Client) Create(ctx context.Context, path string, attrs map[string]any) (*Resource, error) {
	return c.CreateWith(ctx, StyleEnvelope, path, attrs)
}

func (c *Client) CreateWith(ctx context.Context, style BodyStyle, path string, attrs map[string]any) (*Resource, error) {
	body, err := c.do(ctx, http.MethodPost, path, nil, buildWriteBody(style, path, attrs), contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

func (c *Client) Update(ctx context.Context, path string, attrs map[string]any) (*Resource, error) {
	return c.UpdateWith(ctx, StyleEnvelope, path, attrs)
}

func (c *Client) UpdateWith(ctx context.Context, style BodyStyle, path string, attrs map[string]any) (*Resource, error) {
	body, err := c.do(ctx, http.MethodPatch, path, nil, buildWriteBody(style, path, attrs), contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

func (c *Client) UpdateWithID(ctx context.Context, path, id string, attrs map[string]any) (*Resource, error) {
	env := envelope{Data: envelopeData{Type: resourceTypeFromPath(path), ID: id, Attributes: attrs}}
	body, err := c.do(ctx, http.MethodPatch, path, nil, env, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

func buildWriteBody(style BodyStyle, path string, attrs map[string]any) any {
	if attrs == nil {
		return nil
	}
	if style == StyleFlat {
		return attrs
	}
	return newEnvelope(path, attrs)
}

func newEnvelope(path string, attrs map[string]any) envelope {
	return envelope{Data: envelopeData{Type: resourceTypeFromPath(path), Attributes: attrs}}
}

type RawEnvelope struct {
	Data RawEnvelopeData `json:"data"`
}

type RawEnvelopeData struct {
	Type       string `json:"type"`
	Attributes any    `json:"attributes"`
}

func NewRawEnvelope(typ string, attributes any) RawEnvelope {
	return RawEnvelope{Data: RawEnvelopeData{Type: typ, Attributes: attributes}}
}

func (c *Client) Delete(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, path, nil, nil, contentTypeJSONAPI)
	return err
}

func (c *Client) DeleteWithQuery(ctx context.Context, path string, query url.Values) error {
	_, err := c.do(ctx, http.MethodDelete, path, query, nil, contentTypeJSONAPI)
	return err
}

func (c *Client) Action(ctx context.Context, method, path string, body map[string]any) (*Resource, error) {
	return c.ActionWith(ctx, StyleEnvelope, method, path, body)
}

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

func (c *Client) ActionWithType(ctx context.Context, method, path, typ string, body map[string]any) (*Resource, error) {
	var payload any
	if body != nil {
		payload = NewRawEnvelope(typ, body)
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

func (c *Client) ActionWithHeaders(ctx context.Context, style BodyStyle, method, path string, body map[string]any, extra map[string]string) (*Resource, error) {
	res, _, err := c.actionWithHeadersStatus(ctx, style, method, path, body, extra)
	return res, err
}

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

func (c *Client) doWithHeaders(ctx context.Context, method, path string, query url.Values, payload any, accept string, extra map[string]string) ([]byte, error) {
	raw, _, err := c.doWithHeadersStatus(ctx, method, path, query, payload, accept, extra)
	return raw, err
}

func (c *Client) doWithHeadersStatus(ctx context.Context, method, path string, query url.Values, payload any, accept string, extra map[string]string) ([]byte, int, error) {
	u := c.baseURL + canonicalRequestPath(path)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

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

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, resp.StatusCode, nil
		}

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

		return nil, resp.StatusCode, c.errorForResponse(resp.StatusCode, respBody)
	}
}

func retryAfterDuration(header string) time.Duration {
	if header == "" {
		return time.Second
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(header), 64)
	if err != nil || math.IsNaN(secs) || math.IsInf(secs, 0) || secs < 0 {
		return time.Second
	}
	maxSecs := float64(rateLimitMaxWait / time.Second)
	if secs > maxSecs {
		return rateLimitMaxWait
	}
	return time.Duration(math.Ceil(secs)) * time.Second
}

func (c *Client) ActionCollection(ctx context.Context, method, path string, body map[string]any) (*Collection, error) {
	var payload any
	if body != nil {
		payload = newEnvelope(path, body)
	}
	return c.actionCollectionPayload(ctx, method, path, payload)
}

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

func decodeResourceWrapped(body []byte) (*Resource, error) {
	res, derr := DecodeResource(body)
	if derr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, derr)
	}
	return res, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, payload any, accept string) ([]byte, error) {
	return c.doWithHeaders(ctx, method, path, query, payload, accept, nil)
}

func (c *Client) errorForResponse(status int, body []byte) error {
	var doc struct {
		Errors []apiError `json:"errors"`
	}
	if json.Unmarshal(body, &doc) == nil && len(doc.Errors) > 0 {
		return errs.WrapHTTP(status, newAPIErrorList(doc.Errors, body))
	}

	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return errs.NewHTTP(status, "request failed (HTTP %d): %s", status, msg)
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
