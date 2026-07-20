package cmd

// media_durable_url_test.go — contract tests for `mio media files durable-url`
// (MIO-2514). The command reads a file's durable_variants and emits each joined
// with the REQUIRED ?hub_id= param, ready to inline into a page-tree image node;
// --publish also POSTs a public hub_media row so the URL resolves for anon.
//
// ResolveHub short-circuits for id-shaped --hub values (hub_abc123), so no hub
// resolve request fires — the only requests are the file GET and (with --publish)
// the hub-media POST.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const durableFileBody = `{"data":{"type":"files","id":"f_1","attributes":{"title":"Cover","durable_variants":{"medium-720":"https://api.member.dev/file/f_1/image/medium-720","thumbnail-160":"https://api.member.dev/file/f_1/image/thumbnail-160"}}}}`

func TestMediaFilesDurableURL_AppendsHubID(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK, durableFileBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc123", "media", "files", "durable-url", "f_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET (retrieve)", *method)
	}
	if want := "/api/v1/teams/t_team1/files/f_1"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	for _, want := range []string{
		"https://api.member.dev/file/f_1/image/medium-720?hub_id=hub_abc123",
		"https://api.member.dev/file/f_1/image/thumbnail-160?hub_id=hub_abc123",
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("stdout missing %q; got %q", want, res.Stdout)
		}
	}
}

func TestMediaFilesDurableURL_Preset(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, durableFileBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc123", "media", "files", "durable-url", "f_1", "--preset", "medium-720")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "image/medium-720?hub_id=hub_abc123") {
		t.Errorf("stdout missing medium-720 url; got %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "thumbnail-160") {
		t.Errorf("--preset medium-720 must not emit thumbnail-160; got %q", res.Stdout)
	}
}

func TestMediaFilesDurableURL_UnknownPreset(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, durableFileBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc123", "media", "files", "durable-url", "f_1", "--preset", "nope")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage (unknown preset); stderr=%q", res.Code, res.Stderr)
	}
}

func TestMediaFilesDurableURL_NonImageErrors(t *testing.T) {
	body := `{"data":{"type":"files","id":"f_doc","attributes":{"title":"Doc","durable_variants":{}}}}`
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, body)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc123", "media", "files", "durable-url", "f_doc")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage (non-image file); stderr=%q", res.Code, res.Stderr)
	}
}

// ── helper-level tests (assert error message content, which the in-process
//    contract harness does not surface) ────────────────────────────────────────

func TestDurableVariants_NonImage(t *testing.T) {
	if _, err := durableVariants("f_doc", map[string]any{"durable_variants": map[string]any{}}); err == nil || !strings.Contains(err.Error(), "image-only") {
		t.Fatalf("empty durable_variants: want image-only error; got %v", err)
	}
	if _, err := durableVariants("f_doc", map[string]any{}); err == nil {
		t.Error("absent durable_variants must error")
	}
}

func TestBuildDurableURLs_AppendsHubIDFilterAndUnknown(t *testing.T) {
	vars := map[string]string{
		"medium-720":    "https://api.member.dev/file/f_1/image/medium-720",
		"thumbnail-160": "https://api.member.dev/file/f_1/image/thumbnail-160",
	}
	all, err := buildDurableURLs(vars, "h_1", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if all["medium-720"] != "https://api.member.dev/file/f_1/image/medium-720?hub_id=h_1" {
		t.Errorf("medium-720 = %q", all["medium-720"])
	}
	one, err := buildDurableURLs(vars, "h_1", "thumbnail-160")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(one) != 1 || one["thumbnail-160"] == "" {
		t.Errorf("preset filter = %v", one)
	}
	_, err = buildDurableURLs(vars, "h_1", "nope")
	if err == nil || !strings.Contains(err.Error(), "medium-720") || !strings.Contains(err.Error(), "thumbnail-160") {
		t.Fatalf("unknown preset should list available presets; got %v", err)
	}
}

func TestBuildDurableURLs_ExistingQueryUsesAmpersand(t *testing.T) {
	out, err := buildDurableURLs(map[string]string{"x": "https://h/img?v=2"}, "h_1", "x")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out["x"] != "https://h/img?v=2&hub_id=h_1" {
		t.Errorf("existing-query url = %q, want &hub_id appended", out["x"])
	}
}

func TestMediaFilesDurableURL_PublishPostsPublicHubMedia(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, durableFileBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc123", "media", "files", "durable-url", "f_1", "--publish")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	// The last request is the publish POST (retrieve GET fired first).
	if *method != http.MethodPost {
		t.Errorf("last method=%q want POST (publish)", *method)
	}
	if want := "/api/v1/teams/t_team1/hubs/hub_abc123/media"; *path != want {
		t.Errorf("publish path=%q want %q", *path, want)
	}
	var env struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*body, &env); err != nil {
		t.Fatalf("bad publish body: %v; raw=%s", err, *body)
	}
	if env.Data.Attributes["file_id"] != "f_1" {
		t.Errorf("publish file_id=%v want f_1", env.Data.Attributes["file_id"])
	}
	if env.Data.Attributes["visibility"] != "public" {
		t.Errorf("publish visibility=%v want public", env.Data.Attributes["visibility"])
	}
	if _, ok := env.Data.Attributes["published_at"].(string); !ok {
		t.Errorf("publish must set published_at (RFC3339); attrs=%v", env.Data.Attributes)
	}
}
