package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func decodeDataIDTypeAttrs(t *testing.T, body []byte) (id, typ string, attrs map[string]any) {
	t.Helper()
	var doc struct {
		Data struct {
			ID         string         `json:"id"`
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body not JSON: %v; body=%s", err, body)
	}
	return doc.Data.ID, doc.Data.Type, doc.Data.Attributes
}

func TestBatchA_FoldersUpdate_IncludesDataID(t *testing.T) {
	const resp = `{"data":{"id":"folder_x","type":"folders","attributes":{"name":"Renamed"}}}`
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, resp)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "folders", "update", "folder_x", "--name", "Renamed")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPatch {
		t.Errorf("method=%q want PATCH", *method)
	}
	if want := "/api/v1/teams/t_team1/folders/folder_x"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	id, typ, attrs := decodeDataIDTypeAttrs(t, *body)
	if id != "folder_x" {
		t.Errorf("data.id=%q want folder_x (backend 400s without it — MIO-2564)", id)
	}
	if typ != "folders" {
		t.Errorf("data.type=%q want folders", typ)
	}
	if attrs["name"] != "Renamed" {
		t.Errorf("attributes.name=%v want Renamed", attrs["name"])
	}
}

func TestBatchA_PlaylistsUpdate_IncludesDataID(t *testing.T) {
	const resp = `{"data":{"id":"pl_x","type":"playlists","attributes":{"title":"New"}}}`
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, resp)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "update", "pl_x",
			"--title", "New", "--hub-id", "hub_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPatch {
		t.Errorf("method=%q want PATCH", *method)
	}
	if want := "/api/v1/teams/t_team1/playlists/pl_x"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	id, typ, attrs := decodeDataIDTypeAttrs(t, *body)
	if id != "pl_x" {
		t.Errorf("data.id=%q want pl_x (backend 400s without it — MIO-2608)", id)
	}
	if typ != "playlists" {
		t.Errorf("data.type=%q want playlists", typ)
	}
	if attrs["title"] != "New" {
		t.Errorf("attributes.title=%v want New", attrs["title"])
	}
	if attrs["hub_id"] != "hub_1" {
		t.Errorf("attributes.hub_id=%v want hub_1", attrs["hub_id"])
	}
}

func TestBatchA_EmailConfigSet_WrapsAttributesInEnvelope(t *testing.T) {
	const resp = `{"data":{"id":"ec_1","type":"email_configs","attributes":{}}}`
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, resp)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_x", "email", "config", "set",
			"--mail-host", "smtp.example.com", "--mail-port", "587",
			"--mail-username", "u", "--mail-password", "p",
			"--from-email", "qa@example.com", "--from-name", "QA",
			"--mail-encryption", "tls")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPut {
		t.Errorf("method=%q want PUT", *method)
	}
	if want := "/v1/hubs/hub_x/email-config"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	_, typ, attrs := decodeDataIDTypeAttrs(t, *body)
	if typ == "" {
		t.Errorf("data.type is empty — body was not enveloped (MIO-2640 flat-body regression)")
	}
	if attrs["mail_host"] != "smtp.example.com" {
		t.Errorf("data.attributes.mail_host=%v want smtp.example.com (MIO-2640: flat body → backend sees {})", attrs["mail_host"])
	}
	if attrs["mail_from_email"] != "qa@example.com" {
		t.Errorf("data.attributes.mail_from_email=%v want qa@example.com", attrs["mail_from_email"])
	}
	if attrs["mail_port"] != float64(587) {
		t.Errorf("data.attributes.mail_port=%v want 587", attrs["mail_port"])
	}
}

func TestBatchA_OptionsCreate_SendsSlugNotValue(t *testing.T) {
	const resp = `{"data":{"id":"opt_1","type":"contact_attribute_options","attributes":{"slug":"vegetable","label":"Vegetable"}}}`
	srv, method, path, _, body := captureAdminReq(t, http.StatusCreated, resp)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "options", "create", "def_x",
			"--label", "Vegetable", "--slug", "vegetable")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost {
		t.Errorf("method=%q want POST", *method)
	}
	if want := "/api/v1/teams/t_team1/contact-attributes/def_x/options"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	_, typ, attrs := decodeDataIDTypeAttrs(t, *body)
	if typ != "contact_attribute_options" {
		t.Errorf("data.type=%q want contact_attribute_options", typ)
	}
	if attrs["slug"] != "vegetable" {
		t.Errorf("attributes.slug=%v want vegetable (MIO-2581: backend requires slug, not value)", attrs["slug"])
	}
	if _, ok := attrs["value"]; ok {
		t.Errorf("attributes must NOT include 'value' (backend extra=forbid rejects it): %v", attrs)
	}
	if attrs["label"] != "Vegetable" {
		t.Errorf("attributes.label=%v want Vegetable", attrs["label"])
	}
}

func TestBatchA_OptionsUpdate_SendsSlugNotValue(t *testing.T) {
	const resp = `{"data":{"id":"opt_1","type":"contact_attribute_options","attributes":{"slug":"veggies","label":"Veggies"}}}`
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, resp)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "options", "update", "def_x", "opt_1",
			"--label", "Veggies", "--slug", "veggies")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPatch {
		t.Errorf("method=%q want PATCH", *method)
	}
	if want := "/api/v1/teams/t_team1/contact-attributes/def_x/options/opt_1"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	_, typ, attrs := decodeDataIDTypeAttrs(t, *body)
	if typ != "contact_attribute_options" {
		t.Errorf("data.type=%q want contact_attribute_options", typ)
	}
	if attrs["slug"] != "veggies" {
		t.Errorf("attributes.slug=%v want veggies (MIO-2581: update must send slug, not value)", attrs["slug"])
	}
	if _, ok := attrs["value"]; ok {
		t.Errorf("attributes must NOT include 'value' (backend extra=forbid rejects it): %v", attrs)
	}
	if attrs["label"] != "Veggies" {
		t.Errorf("attributes.label=%v want Veggies", attrs["label"])
	}
}

func TestBatchA_UsageGuards_NoRequestOnEmptyWrite(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"folders update no flags", []string{"media", "folders", "update", "folder_x"}},
		{"playlists update no flags", []string{"media", "playlists", "update", "pl_x"}},
		{"options create no flags", []string{"contact-attributes", "options", "create", "def_x"}},
		{"options update no flags", []string{"contact-attributes", "options", "update", "def_x", "opt_1"}},
		{"email config set no flags", []string{"--hub", "hub_x", "email", "config", "set"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := firedGuardServer(t)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", tc.args...)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
			}
			if *fired {
				t.Error("a no-op write must exit before any HTTP request")
			}
		})
	}
}
