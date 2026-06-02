package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	if gotPath != "/api/auth/login" {
		t.Errorf("path = %q", gotPath)
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
		// Overrides: backend type != URL segment.
		{"/api/teams/t1/segments", "segment"},
		{"/api/teams/t1/segments/s1", "segment"},
		{"/api/teams/t1/segments/search", "segment-search"},
		{"/api/teams/t1/contacts", "team-contacts"},
		{"/api/teams/t1/contacts/c1", "team-contacts"},
		{"/api/teams/t1/hubs/h1/content", "content-nodes"},
		{"/api/teams/t1/hubs/h1/content/n1", "content-nodes"},
		{"/api/teams/t1/products/p1/deliverables", "product-deliverables"},
		// Contextual collisions on the same trailing segment.
		{"/api/teams/t1/coupons/co1/products", "coupon-products"},
		{"/api/teams/t1/hubs/h1/products", "hub-product-displays"},
		{"/api/teams/t1/hubs/h1/prices", "hub-price-displays"},
		// contact-attributes family.
		{"/api/teams/t1/contact-attributes", "contact-attribute-definitions"},
		{"/api/teams/t1/contact-attributes/d1/options", "contact-attribute-options"},
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
		{"/api/teams/t1/hubs/h1/contact-attributes", "contact-attribute-hub-configs"},
		{"/api/teams/t1/hubs/h1/contact-attributes/d1", "contact-attribute-hub-configs"},
		// content reorder action keeps the content-nodes type.
		{"/api/teams/t1/hubs/h1/content/reorder", "content-nodes"},
		// checkout action routes with non-segment envelope types.
		{"/api/teams/t1/hubs/h1/payments/pay1/refund", "refunds"},
		{"/api/teams/t1/payment-accounts/onboarding-link", "onboarding_links"},
		// team members write resource.
		{"/api/teams/t1/members", "team-members"},
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
	if gotPath != "/api/teams/t1/api-keys" {
		t.Errorf("path = %q, want /api/teams/t1/api-keys", gotPath)
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
// api-keys, email-config, stripe-sync) require.
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
	env := NewRawEnvelope("segment-search", map[string]any{
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
	if got.Data.Type != "segment-search" {
		t.Errorf("type = %q, want segment-search", got.Data.Type)
	}
	if got.Data.Attributes.Conditions["version"] != float64(1) {
		t.Errorf("conditions.version = %v, want 1", got.Data.Attributes.Conditions["version"])
	}
	if got.Data.Attributes.Page["size"] != float64(50) {
		t.Errorf("page.size = %v, want 50", got.Data.Attributes.Page["size"])
	}
}

// TestClient_ActionCollectionRawSegmentSearch verifies the exact wire body for
// segment search: an envelope with type "segment-search" whose attributes carry
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
	payload := NewRawEnvelope("segment-search", map[string]any{
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
	if data["type"] != "segment-search" {
		t.Errorf("data.type = %v, want segment-search", data["type"])
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
