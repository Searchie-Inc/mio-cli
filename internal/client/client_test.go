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
		{"/v1/hubs/h1/email-templates", "email_templates"},
		// tag assignment.
		{"/api/teams/t1/contacts/c1/tags", "tag_assignments"},
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
