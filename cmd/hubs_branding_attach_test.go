package cmd

// hubs_branding_attach_test.go — contract tests for `mio hubs branding attach`
// (MIO-3465). The managed branding-image attach: resolve the file's media_id
// (GET /files/{id}), preflight eligibility client-side, POST a
// hub-branding-attachment, then re-read the hub to surface the resolved public
// branding URL the read-time resolver overlays (MIO-2115/MIO-3354).
//
// Eligibility preflight mirrors the backend's is_media_eligible_for_public_branding
// (app/infrastructure/storage/public_asset_url.py): the backend ACCEPTS an
// ineligible attach (the row is created) but never publishes it to the public
// bucket, so without the preflight the failure mode is a silent no-op — the
// operator sees a 201 and no logo. The CLI errors up front instead.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// brandingAttachStub wires the three-route flow: GET file → POST attach → GET hub.
// fileAttrs is the raw attributes JSON for the file resource.
func brandingAttachStub(t *testing.T, fileAttrs, hubBranding string) (srv *httptest.Server, postBody *[]byte, postHit *bool, hubGetHit *bool) {
	t.Helper()
	var body []byte
	var post, hubGet bool
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/teams/t_team1/files/file_x":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"file_x","type":"files","attributes":` + fileAttrs + `}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/teams/t_team1/hub-branding-attachments":
			post = true
			body, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"type":"attachments","id":"att_x","attributes":{"media_id":"media_pk_1","target_type":"hub_branding","target_id":"hub_x","role":"auth_logo","position":0}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/teams/t_team1/hubs/hub_x":
			hubGet = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_x","type":"hubs","attributes":{"name":"Hub","branding":` + hubBranding + `}}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &post, &hubGet
}

const eligiblePNG = `{"title":"Logo","media_id":"media_pk_1","status_upload":"READY","mime_type":"image/png"}`

// TestHubsBrandingAttach_EndToEnd pins the full flow: resolved media_id on the
// wire (not the file id), pinned target_type, the chosen role, and the resolved
// public URL surfaced from the post-attach hub read.
func TestHubsBrandingAttach_EndToEnd(t *testing.T) {
	srv, postBody, postHit, hubGetHit := brandingAttachStub(t, eligiblePNG,
		`{"auth_logo_url":"https://cdn.example.com/hub-branding/t_team1/auth_logo/media_pk_1"}`)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "branding", "attach", "hub_x",
			"--file-id", "file_x", "--role", "auth_logo")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !*postHit {
		t.Fatal("hub-branding-attachments POST never fired")
	}
	if !*hubGetHit {
		t.Fatal("post-attach hub GET never fired — the resolved public URL cannot be surfaced without it")
	}
	typ, attrs := decodeDataTypeAttrs(t, *postBody)
	if typ != "attachments" {
		t.Errorf("type=%q want attachments", typ)
	}
	if attrs["media_id"] != "media_pk_1" {
		t.Errorf("media_id=%v want media_pk_1 (resolved from the file, not the file id)", attrs["media_id"])
	}
	if attrs["target_type"] != "hub_branding" {
		t.Errorf("target_type=%v want hub_branding", attrs["target_type"])
	}
	if attrs["target_id"] != "hub_x" {
		t.Errorf("target_id=%v want hub_x", attrs["target_id"])
	}
	if attrs["role"] != "auth_logo" {
		t.Errorf("role=%v want auth_logo", attrs["role"])
	}
	if want := "https://cdn.example.com/hub-branding/t_team1/auth_logo/media_pk_1"; !strings.Contains(res.Stdout, want) {
		t.Errorf("stdout does not surface the resolved public URL %q; stdout=%q", want, res.Stdout)
	}
}

func TestHubsBrandingAttach_RequiresFileID(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "branding", "attach", "hub_x", "--role", "logo")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --file-id must exit before any HTTP request")
	}
}

func TestHubsBrandingAttach_RequiresRole(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "branding", "attach", "hub_x", "--file-id", "file_x")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --role must exit before any HTTP request")
	}
}

// TestHubsBrandingAttach_InvalidRole pins the role allowlist to the backend's
// BRANDING_ROLES — a non-branding attachment role (e.g. thumbnail) is refused
// with an error that names the four valid roles.
func TestHubsBrandingAttach_InvalidRole(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "branding", "attach", "hub_x",
			"--file-id", "file_x", "--role", "thumbnail")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("invalid --role must exit before any HTTP request")
	}
}

// TestValidateBrandingRole pins the allowlist and that the error names every
// valid role (main.go renders RunE error text outside the contract harness,
// so the message is asserted here directly).
func TestValidateBrandingRole(t *testing.T) {
	for _, role := range []string{"logo", "favicon", "social_image", "auth_logo"} {
		if err := validateBrandingRole(role); err != nil {
			t.Errorf("validateBrandingRole(%q) = %v, want nil", role, err)
		}
	}
	err := validateBrandingRole("thumbnail")
	if err == nil {
		t.Fatal("thumbnail is not a branding role — want error")
	}
	var cliErr *errs.CLIError
	if !errors.As(err, &cliErr) || cliErr.Code != errs.ExitUsage {
		t.Errorf("error must carry ExitUsage; got %v", err)
	}
	for _, role := range []string{"logo", "favicon", "social_image", "auth_logo"} {
		if !strings.Contains(err.Error(), role) {
			t.Errorf("error must name valid role %q; got %q", role, err.Error())
		}
	}
}

func TestHubsBrandingAttach_NegativePosition(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "branding", "attach", "hub_x",
			"--file-id", "file_x", "--role", "logo", "--position", "-1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("negative --position must exit before any HTTP request")
	}
}

// ineligibleCase runs attach against a file with the given attributes and
// asserts a usage error fires BEFORE the attach POST. (Error message CONTENT
// is pinned in TestBrandingAttachPreflight_Messages — main.go renders RunE
// error text outside this harness's capture, so it cannot be asserted here.)
func ineligibleCase(t *testing.T, fileAttrs string) {
	t.Helper()
	srv, _, postHit, _ := brandingAttachStub(t, fileAttrs, `{}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "branding", "attach", "hub_x",
			"--file-id", "file_x", "--role", "auth_logo")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *postHit {
		t.Error("ineligible media must not fire the attach POST")
	}
}

func TestHubsBrandingAttach_FileWithoutMedia(t *testing.T) {
	ineligibleCase(t, `{"title":"Logo","status_upload":"READY","mime_type":"image/png"}`)
}

func TestHubsBrandingAttach_NotReady(t *testing.T) {
	ineligibleCase(t, `{"title":"Logo","media_id":"media_pk_1","status_upload":"PENDING","mime_type":"image/png"}`)
}

// SVG is the load-bearing exclusion: the backend attach succeeds but the asset
// is never published (XSS surface), so without this preflight the operator
// gets a silent no-op.
func TestHubsBrandingAttach_SVGRejected(t *testing.T) {
	ineligibleCase(t, `{"title":"Logo","media_id":"media_pk_1","status_upload":"READY","mime_type":"image/svg+xml"}`)
}

func TestHubsBrandingAttach_NonImageRejected(t *testing.T) {
	ineligibleCase(t, `{"title":"Clip","media_id":"media_pk_1","status_upload":"READY","mime_type":"video/mp4"}`)
}

// TestBrandingAttachPreflight_Messages pins the preflight's verdicts AND the
// self-naming error text — the "helpful error for non-eligible media" half of
// the MIO-3465 acceptance. Each ineligible case must name what is wrong;
// attributes the file read does not carry must be skipped (backend authority).
func TestBrandingAttachPreflight_Messages(t *testing.T) {
	cases := []struct {
		name     string
		attrs    map[string]any
		wantID   string
		wantMsgs []string // all must appear in the error; empty = no error
	}{
		{"eligible png", map[string]any{"media_id": "m1", "status_upload": "READY", "mime_type": "image/png"}, "m1", nil},
		{"missing optional attrs skipped", map[string]any{"media_id": "m1"}, "m1", nil},
		{"no media", map[string]any{"status_upload": "READY", "mime_type": "image/png"}, "", []string{"no media"}},
		{"not ready", map[string]any{"media_id": "m1", "status_upload": "PENDING"}, "", []string{"PENDING", "READY"}},
		{"svg", map[string]any{"media_id": "m1", "status_upload": "READY", "mime_type": "image/svg+xml"}, "", []string{"SVG", "image/svg+xml"}},
		{"non-image", map[string]any{"media_id": "m1", "status_upload": "READY", "mime_type": "video/mp4"}, "", []string{"not an image", "video/mp4"}},
		{"non-raster image", map[string]any{"media_id": "m1", "status_upload": "READY", "mime_type": "image/tiff"}, "", []string{"allowlist", "image/tiff"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := brandingAttachPreflight("file_x", tc.attrs)
			if len(tc.wantMsgs) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if id != tc.wantID {
					t.Errorf("media_id=%q want %q", id, tc.wantID)
				}
				return
			}
			if err == nil {
				t.Fatal("want error, got nil")
			}
			var cliErr *errs.CLIError
			if !errors.As(err, &cliErr) || cliErr.Code != errs.ExitUsage {
				t.Errorf("error must carry ExitUsage; got %v", err)
			}
			for _, m := range tc.wantMsgs {
				if !strings.Contains(err.Error(), m) {
					t.Errorf("error must mention %q; got %q", m, err.Error())
				}
			}
		})
	}
}

// TestHubsBrandingAttach_URLNotResolved: the attach succeeded but the hub read
// shows no overlaid URL for the role (e.g. public CDN disabled on the env).
// Still exit 0 — the attachment exists — but warn on stderr so the operator
// knows the URL is not live yet.
func TestHubsBrandingAttach_URLNotResolved(t *testing.T) {
	srv, _, postHit, _ := brandingAttachStub(t, eligiblePNG, `{}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "branding", "attach", "hub_x",
			"--file-id", "file_x", "--role", "auth_logo")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !*postHit {
		t.Fatal("attach POST never fired")
	}
	if !strings.Contains(res.Stderr, "auth_logo_url") {
		t.Errorf("stderr must warn that auth_logo_url is not resolved; stderr=%q", res.Stderr)
	}
}

// TestAttachmentsUpdate_AcceptsAuthLogoRole: the generic attachments update
// role allowlist must include auth_logo (added to the backend enum by
// MIO-3354; the CLI list predates it).
func TestAttachmentsUpdate_AcceptsAuthLogoRole(t *testing.T) {
	srv, method, _, _, _ := captureAdminReq(t, http.StatusOK, attachmentResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "attachments", "update", "att_x", "--role", "auth_logo")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPatch {
		t.Errorf("method=%q want PATCH", *method)
	}
}
