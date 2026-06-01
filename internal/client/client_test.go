package client

import (
	"context"
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
		{"validation", http.StatusUnprocessableEntity, `{"errors":[{"detail":"email invalid","source":{"pointer":"/data/attributes/email"}}]}`, errs.ExitGeneric, "/data/attributes/email"},
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
		401: errs.ExitAuth,
		403: errs.ExitAuth,
		404: errs.ExitNotFound,
		429: errs.ExitRateLimited,
		500: errs.ExitServer,
		503: errs.ExitServer,
		422: errs.ExitGeneric,
	}
	for status, want := range cases {
		if got := errs.ExitCodeForStatus(status); got != want {
			t.Errorf("ExitCodeForStatus(%d) = %d, want %d", status, got, want)
		}
	}
}
