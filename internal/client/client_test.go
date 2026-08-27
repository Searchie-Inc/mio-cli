package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// newTestClient returns a client pointed at the given test server.
func newTestClient(srv *httptest.Server, key string) *Client {
	return New(srv.URL, key, WithHTTPClient(srv.Client()))
}

func TestClient_SetsAuthAndContentHeaders(t *testing.T) {
	var gotAuth, gotAccept, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"1","type":"products","attributes":{}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "mio_sk_test_abc")
	if _, err := c.Create(context.Background(), "/api/teams/t1/products", map[string]any{"name": "X"}); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if gotAuth != "Bearer mio_sk_test_abc" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != contentTypeJSONAPI {
		t.Errorf("Accept = %q, want %q", gotAccept, contentTypeJSONAPI)
	}
	if gotCT != contentTypeJSONAPI {
		t.Errorf("Content-Type = %q, want %q", gotCT, contentTypeJSONAPI)
	}
}

// TestClient_NoBodyPostSetsContentType ensures that a no-body POST (action
// endpoint like /publish, /activate, /deactivate) still carries
// Content-Type: application/vnd.api+json as required by the backend's
// require_jsonapi_content_type middleware (MIO-1115).
func TestClient_NoBodyPostSetsContentType(t *testing.T) {
	var gotCT, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"1","type":"automations","attributes":{}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	// Action with nil body simulates /automations/{id}/publish (no request body).
	_, _ = c.ActionWithHeaders(context.Background(), StyleEnvelope, "POST", "/api/v1/teams/t1/automations/a1/publish", nil, nil)
	if gotMethod != "POST" {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotCT != contentTypeJSONAPI {
		t.Errorf("Content-Type on no-body POST = %q, want %q (MIO-1115)", gotCT, contentTypeJSONAPI)
	}
}

func TestClient_WrapsAttributesInEnvelope(t *testing.T) {
	var bodySeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		bodySeen = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"1","type":"products","attributes":{}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	_, _ = c.Create(context.Background(), "/x", map[string]any{"name": "Pro"})
	if !strings.Contains(bodySeen, `"data"`) || !strings.Contains(bodySeen, `"attributes"`) {
		t.Errorf("request body not wrapped in JSON:API envelope: %s", bodySeen)
	}
}

// TestClient_ErrorMapping verifies each HTTP status maps to the correct exit
// code and that a JSON:API errors array message is surfaced.
func TestClient_ErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int
		wantMsg  string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"errors":[{"detail":"bad key"}]}`, errs.ExitAuth, "bad key"},
		{"forbidden", http.StatusForbidden, `{"errors":[{"detail":"nope"}]}`, errs.ExitAuth, "nope"},
		{"not found", http.StatusNotFound, `{"errors":[{"detail":"missing"}]}`, errs.ExitNotFound, "missing"},
		{"rate limited", http.StatusTooManyRequests, `{"errors":[{"detail":"slow down"}]}`, errs.ExitRateLimited, "slow down"},
		{"server error", http.StatusBadGateway, `boom`, errs.ExitServer, "boom"},
		{"validation", http.StatusUnprocessableEntity, `{"errors":[{"detail":"email invalid","source":{"pointer":"/data/attributes/email"}}]}`, errs.ExitUsage, "/data/attributes/email"},
		{"conflict", http.StatusConflict, `{"errors":[{"detail":"already exists"}]}`, errs.ExitUsage, "already exists"},
		{"bad request", http.StatusBadRequest, `{"errors":[{"detail":"malformed"}]}`, errs.ExitUsage, "malformed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// For 429 set Retry-After: 0 so the retry loop does not sleep
				// between attempts and the test stays fast.
				if tc.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "0")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newTestClient(srv, "k")
			_, err := c.Retrieve(context.Background(), "/x")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if got := errs.CodeOf(err); got != tc.wantCode {
				t.Errorf("exit code = %d, want %d", got, tc.wantCode)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestClient_DeleteSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	if err := c.Delete(context.Background(), "/x/1"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
}

// TestClient_DeleteWithQuerySendsQuery verifies DeleteWithQuery URL-encodes
// and sends the query string on a body-less DELETE (the achievements revoke
// ?reason= contract, MIO-3412).
func TestClient_DeleteWithQuerySendsQuery(t *testing.T) {
	var gotQuery, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	q := url.Values{}
	q.Set("reason", "granted in error")
	if err := c.DeleteWithQuery(context.Background(), "/x/1", q); err != nil {
		t.Fatalf("DeleteWithQuery error: %v", err)
	}
	if gotQuery != "reason=granted+in+error" {
		t.Errorf("query = %q, want reason=granted+in+error", gotQuery)
	}
	if gotBody != "" {
		t.Errorf("body = %q, want empty", gotBody)
	}
}

func TestClient_ListPassesQuery(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	q := url.Values{}
	q.Set("page[size]", "10")
	if _, err := c.List(context.Background(), "/x", q); err != nil {
		t.Fatalf("List error: %v", err)
	}
	if gotQuery.Get("page[size]") != "10" {
		t.Errorf("query page[size] = %q, want 10", gotQuery.Get("page[size]"))
	}
}

func TestClient_CanonicalizesAPIRoutesOnWire(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		wantPath string
	}{
		{"unversioned api route", "/api/teams", "/api/v1/teams"},
		{"canonical api route", "/api/v1/teams", "/api/v1/teams"},
		{"future api version", "/api/v2/teams", "/api/v2/teams"},
		{"non api route", "/v1/hubs/h1/drip-campaigns", "/v1/hubs/h1/drip-campaigns"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
			}))
			defer srv.Close()

			c := newTestClient(srv, "k")
			if _, err := c.List(context.Background(), tc.path, nil); err != nil {
				t.Fatalf("List error: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("wire path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

func TestClient_LoginUsesPlainJSON(t *testing.T) {
	var gotCT, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"access_token":"jwt_abc","token_type":"bearer"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	res, err := c.Login(context.Background(), "a@example.com", "pw")
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if gotPath != "/api/v1/auth/login" {
		t.Errorf("path = %q, want /api/v1/auth/login", gotPath)
	}
	if gotCT != contentTypeJSON {
		t.Errorf("Content-Type = %q, want %q (plain JSON for auth)", gotCT, contentTypeJSON)
	}
	if res.AccessToken != "jwt_abc" {
		t.Errorf("access token = %q", res.AccessToken)
	}
}

func TestExitCodeForStatus(t *testing.T) {
	cases := map[int]int{
		200: errs.ExitGeneric, // not called on 2xx in practice; default branch
		400: errs.ExitUsage,
		401: errs.ExitAuth,
		403: errs.ExitAuth,
		404: errs.ExitNotFound,
		405: errs.ExitGeneric, // other 4xx stays generic
		409: errs.ExitUsage,
		415: errs.ExitGeneric, // other 4xx stays generic
		422: errs.ExitUsage,
		429: errs.ExitRateLimited,
		500: errs.ExitServer,
		503: errs.ExitServer,
	}
	for status, want := range cases {
		if got := errs.ExitCodeForStatus(status); got != want {
			t.Errorf("ExitCodeForStatus(%d) = %d, want %d", status, got, want)
		}
	}
}

// TestResourceTypeFromPath verifies the JSON:API `type` derivation, including
// the override cases where the backend type differs from the URL collection
// segment (verified against mio-backend schemas).
func TestResourceTypeFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// Simple: type == last collection segment.
		{"/api/teams/t1/products", "products"},
		{"/api/teams/t1/products/p1", "products"},
		{"/api/teams/t1/products/p1/prices", "prices"},
		{"/api/teams/t1/products/p1/prices/pr1", "prices"},
		{"/api/teams/t1/coupons", "coupons"},
		{"/api/teams/t1/tags", "tags"},
		{"/api/teams/t1/hubs/h1/pages", "pages"},
		{"/api/teams/t1/hubs/h1/pages/pg1/sections", "sections"},
		// W2b one-step template scaffold op (MIO-2573 §5.1): the action segment
		// maps to "template_scaffolds" via the pages/scaffold-from-template
		// override, not the bare "pages" collection.
		{"/api/teams/t1/hubs/h1/pages/scaffold-from-template", "template_scaffolds"},
		// Overrides: backend type != URL segment.
		// All type values are snake_case per MIO-636 (backend cutover 2026-06-04).
		{"/api/teams/t1/segments", "segment"},
		{"/api/teams/t1/segments/s1", "segment"},
		{"/api/teams/t1/segments/search", "segment_search"},
		{"/api/teams/t1/contacts", "team_contacts"},
		{"/api/teams/t1/contacts/c1", "team_contacts"},
		{"/api/teams/t1/hubs/h1/content", "content_nodes"},
		{"/api/teams/t1/hubs/h1/content/n1", "content_nodes"},
		{"/api/teams/t1/products/p1/deliverables", "product_deliverables"},
		// Contextual collisions on the same trailing segment.
		{"/api/teams/t1/coupons/co1/products", "coupon_products"},
		{"/api/teams/t1/hubs/h1/products", "hub_product_displays"},
		{"/api/teams/t1/hubs/h1/prices", "hub_price_displays"},
		// contact-attributes family.
		{"/api/teams/t1/contact-attributes", "contact_attribute_definitions"},
		{"/api/teams/t1/contact-attributes/d1/options", "contact_attribute_options"},
		// email family.
		{"/v1/hubs/h1/drip-campaigns", "drip_campaigns"},
		{"/v1/hubs/h1/drip-campaigns/dc1/steps", "drip_steps"},
		{"/v1/hubs/h1/drip-campaigns/dc1/steps/st1", "drip_steps"},
		{"/v1/hubs/h1/email-templates", "email_templates"},
		// tag assignment.
		{"/api/teams/t1/contacts/c1/tags", "tag_assignments"},
		// page sections under a page.
		{"/api/teams/t1/hubs/h1/pages/pg1/sections/sec1", "sections"},
		// hub-config: contact-attributes under a hub != team-level definitions.
		{"/api/teams/t1/hubs/h1/contact-attributes", "contact_attribute_hub_configs"},
		{"/api/teams/t1/hubs/h1/contact-attributes/d1", "contact_attribute_hub_configs"},
		// content reorder action keeps the content_nodes type.
		{"/api/teams/t1/hubs/h1/content/reorder", "content_nodes"},
		// checkout action routes with non-segment envelope types.
		{"/api/teams/t1/hubs/h1/payments/pay1/refund", "refunds"},
		{"/api/teams/t1/payment-accounts/onboarding-link", "onboarding_links"},
		// team members write resource.
		{"/api/teams/t1/members", "team_members"},
		// Community admin spaces + discussions (MIO-811). The discussions
		// COLLECTION row also covers the MIO-2262 admin welcome-post create
		// (`community discussions create`, MIO-2808): its backend envelope
		// DiscussionCreateResourceData pins type Literal["discussions"], which is
		// exactly what the bare segment derives — so that path needs NO
		// typeOverride, and this row is the regression guard that keeps it that way
		// (a future hubs/discussions override would break the create with a 422).
		{"/api/admin/teams/t1/hubs/h1/spaces", "spaces"},
		{"/api/admin/teams/t1/hubs/h1/spaces/sp1", "spaces"},
		{"/api/admin/teams/t1/hubs/h1/discussions", "discussions"},
		{"/api/admin/teams/t1/hubs/h1/discussions/d1", "discussions"},
		// Community moderation report-reasons (MIO-2265): hyphenated segment →
		// snake_case type "report_reasons" on both create and update paths.
		{"/api/admin/teams/t1/hubs/h1/report-reasons", "report_reasons"},
		{"/api/admin/teams/t1/hubs/h1/report-reasons/rr1", "report_reasons"},
		// Media transcript edit/revert (MIO-2289): singular "transcript" segment →
		// "transcripts" type; "revert" stays opaque so it resolves to transcript.
		{"/api/teams/t1/media/m1/transcript", "transcripts"},
		{"/api/teams/t1/media/m1/transcript/revert", "transcripts"},
		// Media file content-replace init (MIO-2423): "replace" → file_replacements.
		{"/api/teams/t1/files/f1/replace", "file_replacements"},
		// Playlist cover set (MIO-2289): hyphenated create collection → "attachments".
		{"/api/teams/t1/attachments", "attachments"},
		{"/api/teams/t1/attachments/att1", "attachments"},
		{"/api/teams/t1/playlist-cover-attachments", "attachments"},
		// Hub branding attach (MIO-3465): hyphenated create collection → "attachments".
		{"/api/teams/t1/hub-branding-attachments", "attachments"},
		// Media files, folders, playlists (MIO-811).
		{"/api/teams/t1/files", "files"},
		{"/api/teams/t1/files/f1", "files"},
		{"/api/teams/t1/folders", "folders"},
		{"/api/teams/t1/folders/fo1", "folders"},
		{"/api/teams/t1/playlists", "playlists"},
		{"/api/teams/t1/playlists/pl1", "playlists"},
		// Playlist item verbs (MIO-2513): add (POST .../items) and reorder
		// (PATCH .../items/{item_id}) derive "playlist_items" via the
		// playlists/items typeOverride, not the "playlists" parent type.
		{"/api/teams/t1/playlists/pl1/items", "playlist_items"},
		{"/api/teams/t1/playlists/pl1/items/it1", "playlist_items"},
		// Long-tail admin bundle (MIO-2269): hub policy gate, redirect-origin
		// allowlist, and hub-scoped email suppressions. Each URL segment differs
		// from its backend JSON:API type Literal.
		{"/api/teams/t1/hubs/h1/policies/gate", "hub_policy_gate"},
		{"/api/teams/t1/hubs/h1/redirect-origins", "hub_redirect_origin_allowlists"},
		{"/v1/hubs/h1/email-suppressions", "email_suppressions"},
		{"/v1/hubs/h1/email-suppressions/esp1", "email_suppressions"},
		// Achievements admin surface (MIO-3054/MIO-3412). Definitions
		// self-derive; the hub-offering attach and the earn writes carry
		// different backend Literals than the shared "achievements" URL
		// segment. The restore action path resolves through the SAME
		// members/achievements override because "restore" is deliberately
		// not a known collection token.
		{"/api/teams/t1/achievements", "achievements"},
		{"/api/teams/t1/achievements/a1", "achievements"},
		{"/api/teams/t1/hubs/h1/achievements", "achievement_hubs"},
		{"/api/teams/t1/hubs/h1/members/c1/achievements", "achievement_earns"},
		{"/api/teams/t1/hubs/h1/members/c1/achievements/a1/restore", "achievement_earns"},
		// Content nodes (MIO-3074). The reconcile heal op POSTs to
		// .../content/reconcile, but that tail is AMBIGUOUS: the backend
		// resolves a node by slug/legacy-hash as well as by id, so
		// PATCH .../content/reconcile is a legitimate update of a node slugged
		// "reconcile". Path derivation cannot see the HTTP method, so this path
		// MUST derive the ordinary node type; the reconcile command names its
		// own type explicitly (ActionWithType). A typeOverride here would 422
		// every update of a node addressed as "reconcile".
		{"/api/teams/t1/hubs/h1/content", "content_nodes"},
		{"/api/teams/t1/hubs/h1/content/cnt1", "content_nodes"},
		{"/api/teams/t1/hubs/h1/content/reconcile", "content_nodes"},
	}
	for _, tc := range cases {
		if got := resourceTypeFromPath(tc.path); got != tc.want {
			t.Errorf("resourceTypeFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestClient_CreateIncludesDerivedType verifies that Create sends data.type
// derived from the path so the backend's Literal-typed schemas accept it.
func TestClient_CreateIncludesDerivedType(t *testing.T) {
	var body struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"1","type":"segment","attributes":{}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	if _, err := c.Create(context.Background(), "/api/teams/t1/segments", map[string]any{"name": "VIP"}); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if body.Data.Type != "segment" {
		t.Errorf("data.type = %q, want segment", body.Data.Type)
	}
	if body.Data.Attributes["name"] != "VIP" {
		t.Errorf("data.attributes.name = %v, want VIP", body.Data.Attributes["name"])
	}
}

// TestMintAPIKey_SendsFlatBodyWithBearer verifies the password→mint flow posts a
// FLAT body (no `data` envelope) to the api-keys endpoint authenticated with the
// JWT access token. The backend ApiKeyCreateRequest is a flat pydantic model, so
// an envelope here 422s.
func TestMintAPIKey_SendsFlatBodyWithBearer(t *testing.T) {
	var raw map[string]any
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"apk_1","type":"api-keys","attributes":{"secret":"mio_sk_live_xyz","name":"CI"}}}`))
	}))
	defer srv.Close()

	// The outer client carries no key; MintAPIKey issues with the JWT token.
	c := newTestClient(srv, "")
	res, err := c.MintAPIKey(context.Background(), "jwt_access_token", "t1", "CI")
	if err != nil {
		t.Fatalf("MintAPIKey error: %v", err)
	}
	if gotPath != "/api/v1/teams/t1/api-keys" {
		t.Errorf("path = %q, want /api/v1/teams/t1/api-keys", gotPath)
	}
	if gotAuth != "Bearer jwt_access_token" {
		t.Errorf("Authorization = %q, want Bearer jwt_access_token", gotAuth)
	}
	if _, hasData := raw["data"]; hasData {
		t.Errorf("mint body unexpectedly carried a `data` envelope: %#v", raw)
	}
	if raw["name"] != "CI" {
		t.Errorf("mint body name = %v, want CI (top-level flat attribute)", raw["name"])
	}
	if res == nil || res.Attributes["secret"] != "mio_sk_live_xyz" {
		t.Errorf("minted resource secret not surfaced: %#v", res)
	}
}

// TestClient_FlatStyleSendsNoEnvelope verifies that a StyleFlat write sends the
// attributes map as the top-level JSON object with NO `data` wrapper and NO
// injected `type` — the shape the flat-schema backend endpoints (users, roles,
// api-keys, stripe-sync) require.
func TestClient_FlatStyleSendsNoEnvelope(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"1","type":"api-keys","attributes":{}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	if _, err := c.CreateWith(context.Background(), StyleFlat, "/api/teams/t1/api-keys", map[string]any{"name": "CI"}); err != nil {
		t.Fatalf("CreateWith(StyleFlat) error: %v", err)
	}
	if _, hasData := raw["data"]; hasData {
		t.Errorf("flat body unexpectedly carried a `data` envelope: %#v", raw)
	}
	if _, hasType := raw["type"]; hasType {
		t.Errorf("flat body unexpectedly carried a top-level `type`: %#v", raw)
	}
	if raw["name"] != "CI" {
		t.Errorf("flat body name = %v, want CI (top-level attribute)", raw["name"])
	}
}

// TestClient_EnvelopeStyleWrapsWithType verifies StyleEnvelope (the default)
// wraps attributes in {"data":{"type":<derived>,"attributes":{…}}}.
func TestClient_EnvelopeStyleWrapsWithType(t *testing.T) {
	var body struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"1","type":"tags","attributes":{}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	if _, err := c.UpdateWith(context.Background(), StyleEnvelope, "/api/teams/t1/tags/tg1", map[string]any{"name": "VIP"}); err != nil {
		t.Fatalf("UpdateWith(StyleEnvelope) error: %v", err)
	}
	if body.Data.Type != "tags" {
		t.Errorf("data.type = %q, want tags", body.Data.Type)
	}
	if body.Data.Attributes["name"] != "VIP" {
		t.Errorf("data.attributes.name = %v, want VIP", body.Data.Attributes["name"])
	}
}

// TestBuildWriteBody verifies the body-shaping helper for both styles and the
// nil-attrs (no-body) case.
func TestBuildWriteBody(t *testing.T) {
	attrs := map[string]any{"name": "X"}

	if got := buildWriteBody(StyleFlat, "/api/teams/t1/roles", attrs); got == nil {
		t.Fatal("flat body should not be nil")
	} else if m, ok := got.(map[string]any); !ok || m["name"] != "X" {
		t.Errorf("flat body = %#v, want the raw attrs map", got)
	}

	if got := buildWriteBody(StyleEnvelope, "/api/teams/t1/segments", attrs); got == nil {
		t.Fatal("envelope body should not be nil")
	} else if env, ok := got.(envelope); !ok || env.Data.Type != "segment" {
		t.Errorf("envelope body = %#v, want envelope{data.type=segment}", got)
	}

	if got := buildWriteBody(StyleFlat, "/x", nil); got != nil {
		t.Errorf("nil attrs should yield a nil body, got %#v", got)
	}
	if got := buildWriteBody(StyleEnvelope, "/x", nil); got != nil {
		t.Errorf("nil attrs should yield a nil body, got %#v", got)
	}
}

// TestNewRawEnvelope verifies the structured-attributes envelope used by segment
// search marshals to {"data":{"type":…,"attributes":<arbitrary>}}.
func TestNewRawEnvelope(t *testing.T) {
	env := NewRawEnvelope("segment_search", map[string]any{
		"conditions": map[string]any{"version": 1},
		"page":       map[string]any{"size": 50},
	})
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Data struct {
			Type       string `json:"type"`
			Attributes struct {
				Conditions map[string]any `json:"conditions"`
				Page       map[string]any `json:"page"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Data.Type != "segment_search" {
		t.Errorf("type = %q, want segment_search", got.Data.Type)
	}
	if got.Data.Attributes.Conditions["version"] != float64(1) {
		t.Errorf("conditions.version = %v, want 1", got.Data.Attributes.Conditions["version"])
	}
	if got.Data.Attributes.Page["size"] != float64(50) {
		t.Errorf("page.size = %v, want 50", got.Data.Attributes.Page["size"])
	}
}

// TestClient_ActionCollectionRawSegmentSearch verifies the exact wire body for
// segment search: an envelope with type "segment_search" whose attributes carry
// a nested conditions tree (NOT a flat attrs map).
func TestClient_ActionCollectionRawSegmentSearch(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.Header().Set("Content-Type", contentTypeJSONAPI)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"count":0}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	payload := NewRawEnvelope("segment_search", map[string]any{
		"conditions": map[string]any{
			"version": 1,
			"groups":  []any{map[string]any{"logic": "AND", "conditions": []any{}}},
		},
	})
	if _, err := c.ActionCollectionRaw(context.Background(), "POST", "/api/teams/t1/segments/search", payload); err != nil {
		t.Fatalf("ActionCollectionRaw error: %v", err)
	}
	data, ok := raw["data"].(map[string]any)
	if !ok {
		t.Fatalf("body missing data object: %#v", raw)
	}
	if data["type"] != "segment_search" {
		t.Errorf("data.type = %v, want segment_search", data["type"])
	}
	attrs, ok := data["attributes"].(map[string]any)
	if !ok {
		t.Fatalf("body missing data.attributes: %#v", data)
	}
	conds, ok := attrs["conditions"].(map[string]any)
	if !ok {
		t.Fatalf("attributes.conditions not an object: %#v", attrs)
	}
	if conds["version"] != float64(1) {
		t.Errorf("conditions.version = %v, want 1", conds["version"])
	}
	if _, hasMatch := attrs["match"]; hasMatch {
		t.Errorf("attributes unexpectedly carried a `match` field: %#v", attrs)
	}
}

// TestClient_RefundSendsRefundsEnvelope verifies that the refund action sends a
// JSON:API envelope with type "refunds" (derived from the path) and the required
// `reason` attribute — the shape the backend RefundRequest schema requires. A
// nil/empty body would 422 since the body is non-optional.
func TestClient_RefundSendsRefundsEnvelope(t *testing.T) {
	var body struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"pay_1","type":"payments","attributes":{"status":"refunded"}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	path := "/api/teams/t1/hubs/h1/payments/pay_1/refund"
	if _, err := c.Action(context.Background(), "POST", path, map[string]any{
		"reason": "requested_by_customer",
		"amount": 500,
	}); err != nil {
		t.Fatalf("Action(refund) error: %v", err)
	}
	if body.Data.Type != "refunds" {
		t.Errorf("data.type = %q, want refunds", body.Data.Type)
	}
	if body.Data.Attributes["reason"] != "requested_by_customer" {
		t.Errorf("data.attributes.reason = %v, want requested_by_customer", body.Data.Attributes["reason"])
	}
	if body.Data.Attributes["amount"] != float64(500) {
		t.Errorf("data.attributes.amount = %v, want 500", body.Data.Attributes["amount"])
	}
}

// TestClient_RefundFullOmitsAmount verifies a full refund sends reason without an
// amount key (the backend treats a missing amount as a full refund).
func TestClient_RefundFullOmitsAmount(t *testing.T) {
	var body struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"pay_1","type":"payments","attributes":{}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	path := "/api/teams/t1/hubs/h1/payments/pay_1/refund"
	if _, err := c.Action(context.Background(), "POST", path, map[string]any{
		"reason": "duplicate",
	}); err != nil {
		t.Fatalf("Action(full refund) error: %v", err)
	}
	if body.Data.Type != "refunds" {
		t.Errorf("data.type = %q, want refunds", body.Data.Type)
	}
	if body.Data.Attributes["reason"] != "duplicate" {
		t.Errorf("data.attributes.reason = %v, want duplicate", body.Data.Attributes["reason"])
	}
	if _, hasAmount := body.Data.Attributes["amount"]; hasAmount {
		t.Errorf("full refund unexpectedly carried an amount: %#v", body.Data.Attributes)
	}
}

// TestClient_ActionCollection verifies a custom action that returns a
// `data: [...]` collection is decoded into a Collection.
func TestClient_ActionCollection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentTypeJSONAPI)
		_, _ = w.Write([]byte(`{
		  "data": [
		    {"id":"tc1","type":"team-contact-ref","attributes":{"contact_id":"c1"}},
		    {"id":"tc2","type":"team-contact-ref","attributes":{"contact_id":"c2"}}
		  ],
		  "meta": {"count": 2}
		}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	col, err := c.ActionCollection(context.Background(), "POST", "/api/teams/t1/segments/search", map[string]any{"conditions": "[]"})
	if err != nil {
		t.Fatalf("ActionCollection error: %v", err)
	}
	if len(col.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(col.Data))
	}
	if col.Data[0].ID != "tc1" || col.Data[1].ID != "tc2" {
		t.Errorf("unexpected resources: %#v", col.Data)
	}
}

// isSnakeCaseType returns true if s is a valid snake_case identifier (lower-case
// letters, digits, and underscores only). Kebab-case values contain a hyphen and
// therefore fail this check. This is the same rule enforced by the backend's
// test_jsonapi_type_naming.py contract guard (MIO-636).
func isSnakeCaseType(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

// TestTypeOverrides_AllSnakeCase is a compile-time-equivalent guard that fails
// whenever a kebab-case value (or any non-snake_case string) is introduced into
// typeOverrides. It enforces the MIO-636 invariant: every JSON:API resource
// type value sent to the backend must be snake_case. This mirrors the backend's
// tests/contract/test_jsonapi_type_naming.py guard on the CLI side.
func TestTypeOverrides_AllSnakeCase(t *testing.T) {
	for _, o := range typeOverrides {
		if !isSnakeCaseType(o.typ) {
			t.Errorf("typeOverrides[%q].typ = %q is not snake_case (MIO-636: backend requires snake_case type values)", o.suffix, o.typ)
		}
	}
}

// ---------------------------------------------------------------------------
// 429 / Retry-After tests (MIO-1000, from QA record MIO-982)
// ---------------------------------------------------------------------------

// TestClient_RateLimitRetry_SucceedsOnSecondAttempt verifies that a 429
// followed by a 200 results in success, and that the client honours the
// Retry-After header value (instead of retrying immediately).
func TestClient_RateLimitRetry_SucceedsOnSecondAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0") // 0 s → immediate in tests
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"rate limited"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"1","type":"products","attributes":{}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	res, err := c.Retrieve(context.Background(), "/x")
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}
	if res == nil || res.ID != "1" {
		t.Errorf("unexpected result: %#v", res)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("server called %d times, want 2", got)
	}
}

// TestClient_RateLimitRetry_ExhaustsRetries verifies that when the server
// returns 429 on every attempt, the client stops after rateLimitMaxRetries
// extra attempts and surfaces the 429 error with ExitRateLimited.
func TestClient_RateLimitRetry_ExhaustsRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"rate limited"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	_, err := c.Retrieve(context.Background(), "/x")
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if got := errs.CodeOf(err); got != errs.ExitRateLimited {
		t.Errorf("exit code = %d, want ExitRateLimited (%d)", got, errs.ExitRateLimited)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error message %q does not mention rate limit", err.Error())
	}
	// Should have made 1 initial + rateLimitMaxRetries extra calls.
	wantCalls := int32(1 + rateLimitMaxRetries)
	if got := calls.Load(); got != wantCalls {
		t.Errorf("server called %d times, want %d (1 initial + %d retries)", got, wantCalls, rateLimitMaxRetries)
	}
}

// TestRetryAfterDuration verifies header parsing, the 60 s cap, and the
// fallback-to-1s behaviour on missing/malformed values. It also tests
// very large values (e.g. 1e10) that would overflow int64 if converted
// directly to time.Duration before the cap is applied.
func TestRetryAfterDuration(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"5", 5 * time.Second},
		{"0", 0},                 // zero is a valid "retry immediately"
		{"60", 60 * time.Second}, // exactly at cap
		{"61", 60 * time.Second}, // capped to rateLimitMaxWait
		{"120", 60 * time.Second},
		{"1e10", 60 * time.Second}, // huge value — must not overflow, must cap
		{"1e18", 60 * time.Second}, // beyond int64 max seconds — must cap
		{"", time.Second},          // absent → fallback
		{"abc", time.Second},       // malformed → fallback
		{"-1", time.Second},        // negative → fallback
		{"1.7", 2 * time.Second},   // fractional → ceiling
		{"NaN", time.Second},       // ParseFloat accepts NaN; range checks are all false → must fall back
		{"+Inf", time.Second},      // non-finite → fallback
		{"-Inf", time.Second},      // non-finite → fallback
	}
	for _, tc := range cases {
		got := retryAfterDuration(tc.header)
		if got != tc.want {
			t.Errorf("retryAfterDuration(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// TestClient_RegisterUsesPlainJSON verifies that Register posts a FLAT plain-JSON
// body to /api/v1/auth/register (the /api/auth/* family speaks application/json,
// not JSON:API), carries no Authorization header (registration is
// unauthenticated), and decodes the 201 TokenResponse's access token.
func TestClient_RegisterUsesPlainJSON(t *testing.T) {
	var gotCT, gotPath, gotAuth string
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"access_token":"jwt_reg","refresh_token":"rt","token_type":"Bearer","expires_in":900}`))
	}))
	defer srv.Close()

	// An empty key mirrors how the register command constructs its client: the
	// endpoint is unauthenticated.
	c := newTestClient(srv, "")
	res, err := c.Register(context.Background(), "a@example.com", "s3cr3tpass", "Ada", "Lovelace")
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}
	if gotPath != "/api/v1/auth/register" {
		t.Errorf("path = %q, want /api/v1/auth/register", gotPath)
	}
	if gotCT != contentTypeJSON {
		t.Errorf("Content-Type = %q, want %q (plain JSON for auth)", gotCT, contentTypeJSON)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (register is unauthenticated)", gotAuth)
	}
	// Flat body: fields at the top level, NO JSON:API `data` envelope.
	if _, hasData := raw["data"]; hasData {
		t.Errorf("register body unexpectedly carried a `data` envelope: %#v", raw)
	}
	if raw["email"] != "a@example.com" {
		t.Errorf("body.email = %v, want a@example.com", raw["email"])
	}
	if raw["password"] != "s3cr3tpass" {
		t.Errorf("body.password = %v, want s3cr3tpass", raw["password"])
	}
	if raw["first_name"] != "Ada" {
		t.Errorf("body.first_name = %v, want Ada", raw["first_name"])
	}
	if raw["last_name"] != "Lovelace" {
		t.Errorf("body.last_name = %v, want Lovelace", raw["last_name"])
	}
	if res.AccessToken != "jwt_reg" {
		t.Errorf("access token = %q, want jwt_reg", res.AccessToken)
	}
}

// TestClient_RegisterOmitsEmptyNames verifies that first_name/last_name are sent
// ONLY when non-empty — the backend fields are optional, and sending empty
// strings would differ from omission.
func TestClient_RegisterOmitsEmptyNames(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"access_token":"jwt_reg","token_type":"Bearer"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	if _, err := c.Register(context.Background(), "a@example.com", "s3cr3tpass", "", ""); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	if _, has := raw["first_name"]; has {
		t.Errorf("first_name must be omitted when empty; body: %#v", raw)
	}
	if _, has := raw["last_name"]; has {
		t.Errorf("last_name must be omitted when empty; body: %#v", raw)
	}
}

// TestClient_RegisterSurfacesConflict verifies that a 409 (email already
// registered) is surfaced as a usage-class error carrying the backend's precise
// detail message rather than being masked by a generic string.
func TestClient_RegisterSurfacesConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentTypeJSONAPI)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"errors":[{"status":"409","title":"ConflictError","detail":"Email 'a@example.com' is already registered."}]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	_, err := c.Register(context.Background(), "a@example.com", "s3cr3tpass", "", "")
	if err == nil {
		t.Fatal("Register on a 409 must return an error")
	}
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("409 exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error must surface the backend detail; got %q", err.Error())
	}
}

// TestClient_RegisterErrorsWithoutAccessToken verifies that a 2xx with no access
// token is treated as a failure (the mint flow cannot proceed without one).
func TestClient_RegisterErrorsWithoutAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	if _, err := c.Register(context.Background(), "a@example.com", "s3cr3tpass", "", ""); err == nil {
		t.Fatal("Register must error when the response carries no access token")
	}
}
