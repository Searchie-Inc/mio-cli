package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const minimalHubBody = `{
	"data": {
		"id": "hub_new",
		"type": "hubs",
		"attributes": {
			"title": "My Community",
			"slug": "my-community",
			"is_private": false
		}
	}
}`

func captureHubRequest(t *testing.T, status int) (*httptest.Server, *string, *string, *[]byte) {
	t.Helper()
	var gotMethod, gotPath string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotMethod, &gotPath, &gotBody
}

func decodeHubAttrs(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, body)
	}
	if doc.Data.Attributes == nil {
		t.Fatalf("data.attributes is nil; body=%q", body)
	}
	return doc.Data.Attributes
}

func TestHubsCreate_NameMapsToTitle(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "My Community",
			"--slug", "my-community",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", *gotMethod)
	}
	if !strings.Contains(*gotPath, "/hubs") {
		t.Errorf("path %q does not contain /hubs", *gotPath)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	if attrs["title"] != "My Community" {
		t.Errorf("data.attributes.title = %v, want \"My Community\"", attrs["title"])
	}
	if _, hasName := attrs["name"]; hasName {
		t.Errorf("data.attributes.name must NOT be present; got attrs=%v", attrs)
	}
}

func TestHubsCreate_PublishedTrueSendsIsPrivateFalse(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Public Hub",
			"--published=true",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	if attrs["is_private"] != false {
		t.Errorf("data.attributes.is_private = %v, want false (published=true → is_private=false)", attrs["is_private"])
	}
	if _, hasPub := attrs["published"]; hasPub {
		t.Errorf("data.attributes.published must NOT be present; got attrs=%v", attrs)
	}
}

func TestHubsCreate_PublishedFalseSendsIsPrivateTrue(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Private Hub",
			"--published=false",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	if attrs["is_private"] != true {
		t.Errorf("data.attributes.is_private = %v, want true (published=false → is_private=true)", attrs["is_private"])
	}
	if _, hasPub := attrs["published"]; hasPub {
		t.Errorf("data.attributes.published must NOT be present; got attrs=%v", attrs)
	}
}

func TestHubsCreate_UnsetPublishedOmitsIsPrivate(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Hub Without Published Flag",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	if _, hasPriv := attrs["is_private"]; hasPriv {
		t.Errorf("is_private must be absent when --published is not passed; got attrs=%v", attrs)
	}
	if _, hasPub := attrs["published"]; hasPub {
		t.Errorf("published must be absent when --published is not passed; got attrs=%v", attrs)
	}
}

func TestHubsUpdate_NameMapsToTitle(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--name", "Renamed Community",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", *gotMethod)
	}
	if !strings.Contains(*gotPath, "hub_abc123") {
		t.Errorf("path %q does not contain hub_abc123", *gotPath)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	if attrs["title"] != "Renamed Community" {
		t.Errorf("data.attributes.title = %v, want \"Renamed Community\"", attrs["title"])
	}
	if _, hasName := attrs["name"]; hasName {
		t.Errorf("data.attributes.name must NOT be present; got attrs=%v", attrs)
	}
}

func TestHubsUpdate_PublishedInversionBothDirections(t *testing.T) {
	for _, tc := range []struct {
		flag        string
		wantPrivate bool
	}{
		{"--published=true", false},
		{"--published=false", true},
	} {
		tc := tc
		t.Run(tc.flag, func(t *testing.T) {
			srv, _, _, gotBody := captureHubRequest(t, http.StatusOK)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1",
					"hubs", "update", "hub_abc123",
					tc.flag,
				)...)

			if res.Code != errs.ExitOK {
				t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
			}

			attrs := decodeHubAttrs(t, *gotBody)

			if attrs["is_private"] != tc.wantPrivate {
				t.Errorf("%s: is_private = %v, want %v", tc.flag, attrs["is_private"], tc.wantPrivate)
			}
			if _, hasPub := attrs["published"]; hasPub {
				t.Errorf("%s: attributes.published must NOT be present; got attrs=%v", tc.flag, attrs)
			}
		})
	}
}

func TestHubsCreate_LogoURLNested(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Branded Hub",
			"--logo-url", "https://x/l.png",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	branding, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.branding is absent or not an object; attrs=%v", attrs)
	}
	if branding["logo_url"] != "https://x/l.png" {
		t.Errorf("branding.logo_url = %v, want \"https://x/l.png\"", branding["logo_url"])
	}

	if _, hasTop := attrs["logo_url"]; hasTop {
		t.Errorf("top-level attributes.logo_url must NOT be present (backend 422); attrs=%v", attrs)
	}
	if _, hasTop := attrs["logo-url"]; hasTop {
		t.Errorf("top-level attributes.logo-url must NOT be present; attrs=%v", attrs)
	}
}

func TestHubsCreate_UnsetLogoURLOmitsBranding(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Hub Without Logo",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	if _, hasBranding := attrs["branding"]; hasBranding {
		t.Errorf("branding must be absent when --logo-url is not passed; got attrs=%v", attrs)
	}
	if _, hasTop := attrs["logo_url"]; hasTop {
		t.Errorf("top-level logo_url must be absent; got attrs=%v", attrs)
	}
}
