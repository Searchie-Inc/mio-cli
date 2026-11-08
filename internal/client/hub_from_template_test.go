package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const hubOpOKBody = `{"data":{"id":"hub_new","type":"hub_scaffolds","attributes":{
  "hub_id":"hub_new",
  "summary":[
    {"resource":"hub","action":"created"},
    {"resource":"spaces","action":"created"},
    {"resource":"playlists","action":"skipped","reason":"template declares none"}
  ],
  "created_resource_ids":{"hubs":["hub_new"],"spaces":["sp_1","sp_2"]},
  "replayed":false}}}`

func hubOpServer(t *testing.T, status int, body string) (*httptest.Server, *string, *[]byte, *http.Header) {
	t.Helper()
	var path string
	var reqBody []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		reqBody, _ = io.ReadAll(r.Body)
		hdr = r.Header.Clone()
		if status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"detail":"Method Not Allowed"}`))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &path, &reqBody, &hdr
}

func newHubOpClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(srv.URL, "k", WithHTTPClient(srv.Client()))
}

func TestHubFromTemplate_PathBodyAndIdempotencyHeader(t *testing.T) {
	srv, gotPath, gotBody, gotHdr := hubOpServer(t, http.StatusCreated, hubOpOKBody)
	c := newHubOpClient(t, srv)

	reg := true
	logo := "https://cdn/logo.png"
	_, err := c.HubFromTemplate(context.Background(), "t_1", HubFromTemplateRequest{
		HubTemplateID:  "community",
		Name:           "Founders",
		Slug:           "founders",
		CatalogDigest:  "sha256:abc",
		Overrides:      HubFromTemplateOverrides{LogoURL: &logo, RegistrationEnabled: &reg, Publish: true},
		IdempotencyKey: "deadbeef",
	})
	if err != nil {
		t.Fatalf("HubFromTemplate: %v", err)
	}
	if want := "/api/v1/teams/t_1/hubs/from-template"; *gotPath != want {
		t.Errorf("path = %q, want %q", *gotPath, want)
	}
	if got := gotHdr.Get("Idempotency-Key"); got != "deadbeef" {
		t.Errorf("Idempotency-Key = %q, want deadbeef — the op 400s without it", got)
	}

	var env map[string]any
	if err := json.Unmarshal(*gotBody, &env); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	data, _ := env["data"].(map[string]any)
	if data["type"] != "hub_scaffolds" {
		t.Errorf("envelope type = %v, want hub_scaffolds (NOT the pages op's template_scaffolds)", data["type"])
	}
	attrs, _ := data["attributes"].(map[string]any)
	for k, want := range map[string]any{
		"hub_template_id": "community",
		"name":            "Founders",
		"slug":            "founders",
		"catalog_digest":  "sha256:abc",
	} {
		if attrs[k] != want {
			t.Errorf("attributes[%q] = %v, want %v", k, attrs[k], want)
		}
	}
	ov, _ := attrs["overrides"].(map[string]any)
	if ov["logo_url"] != "https://cdn/logo.png" || ov["registration_enabled"] != true || ov["publish"] != true {
		t.Errorf("overrides = %v, want the three supplied values", ov)
	}
	if _, present := ov["favicon_url"]; present {
		t.Errorf("favicon_url must be OMITTED when unset, not sent; overrides=%v", ov)
	}
}

func TestHubFromTemplate_DormantOpIsExitNotFound(t *testing.T) {
	srv, _, _, _ := hubOpServer(t, http.StatusMethodNotAllowed, "")
	c := newHubOpClient(t, srv)

	_, err := c.HubFromTemplate(context.Background(), "t_1", HubFromTemplateRequest{
		HubTemplateID: "community", Name: "N", Slug: "n", CatalogDigest: "d", IdempotencyKey: "k",
	})
	if err == nil {
		t.Fatal("a dormant op must return an error for the caller to classify")
	}
	if got := errs.CodeOf(err); got != errs.ExitNotFound {
		t.Errorf("exit code = %d, want %d (ExitNotFound)", got, errs.ExitNotFound)
	}
	if !errors.Is(err, ErrHubOpAbsent) {
		t.Errorf("a dormant op must carry ErrHubOpAbsent — it is what the fallback keys on; got %v", err)
	}
}

func TestHubFromTemplate_OtherErrorsDoNotLookLikeAbsence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"digest mismatch", http.StatusConflict, `{"errors":[{"code":"catalog_digest_mismatch","detail":"stale"}]}`, errs.ExitUsage},
		{"template unknown", http.StatusNotFound, `{"errors":[{"code":"template_not_found","detail":"nope"}]}`, errs.ExitNotFound},
		{"server error", http.StatusInternalServerError, `{"errors":[{"detail":"boom"}]}`, errs.ExitServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _, _ := hubOpServer(t, tc.status, tc.body)
			c := newHubOpClient(t, srv)
			_, err := c.HubFromTemplate(context.Background(), "t_1", HubFromTemplateRequest{
				HubTemplateID: "community", Name: "N", Slug: "n", CatalogDigest: "d", IdempotencyKey: "k",
			})
			if err == nil {
				t.Fatalf("status %d must be an error", tc.status)
			}
			if got := errs.CodeOf(err); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
			if errors.Is(err, ErrHubOpAbsent) {
				t.Errorf("status %d must NOT look like op-absence — falling back client-side here smears partial state against a backend that HAS the op", tc.status)
			}
		})
	}
}

func TestHubFromTemplate_ParsesResultFieldsByTheirWireNames(t *testing.T) {
	srv, _, _, _ := hubOpServer(t, http.StatusCreated, hubOpOKBody)
	c := newHubOpClient(t, srv)

	got, err := c.HubFromTemplate(context.Background(), "t_1", HubFromTemplateRequest{
		HubTemplateID: "community", Name: "N", Slug: "n", CatalogDigest: "d", IdempotencyKey: "k",
	})
	if err != nil {
		t.Fatalf("HubFromTemplate: %v", err)
	}
	if got.HubID != "hub_new" {
		t.Errorf("HubID = %q, want hub_new", got.HubID)
	}
	if got.Replayed {
		t.Errorf("Replayed = true, want false")
	}
	if len(got.Summary) != 3 {
		t.Fatalf("summary rows = %d, want 3", len(got.Summary))
	}
	if got.Summary[2].Action != "skipped" || got.Summary[2].Reason != "template declares none" {
		t.Errorf("skip row lost its reason: %+v", got.Summary[2])
	}
	if len(got.CreatedIDs["spaces"]) != 2 {
		t.Errorf("CreatedIDs[spaces] = %v, want 2 ids — read from `created_resource_ids`, NOT `created_ids`; the wrong key yields an empty map with no error", got.CreatedIDs["spaces"])
	}
	if len(got.CreatedIDs["hubs"]) != 1 {
		t.Errorf("CreatedIDs[hubs] = %v, want 1 id", got.CreatedIDs["hubs"])
	}
}

func TestHubFromTemplate_ReplayHasSummaryButNoCreatedIDs(t *testing.T) {
	const replay = `{"data":{"id":"hub_new","type":"hub_scaffolds","attributes":{
	  "hub_id":"hub_new",
	  "summary":[{"resource":"hub","action":"created"}],
	  "created_resource_ids":{},
	  "replayed":true}}}`
	srv, _, _, _ := hubOpServer(t, http.StatusCreated, replay)
	c := newHubOpClient(t, srv)

	got, err := c.HubFromTemplate(context.Background(), "t_1", HubFromTemplateRequest{
		HubTemplateID: "community", Name: "N", Slug: "n", CatalogDigest: "d", IdempotencyKey: "k",
	})
	if err != nil {
		t.Fatalf("HubFromTemplate: %v", err)
	}
	if !got.Replayed {
		t.Error("Replayed must be true — it is the ONLY way to tell a replay from a run that created nothing")
	}
	if len(got.CreatedIDs) != 0 {
		t.Errorf("a replay carries no created ids; got %v", got.CreatedIDs)
	}
	if len(got.Summary) != 1 {
		t.Errorf("a replay still carries the summary; got %d rows", len(got.Summary))
	}
}
