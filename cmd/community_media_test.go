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

const spaceBody = `{
	"data": {
		"id": "space_1",
		"type": "spaces",
		"attributes": {"name": "General", "slug": "general"}
	}
}`

const moderationActionBody = `{
	"data": {
		"id": "ma_1",
		"type": "moderation_actions",
		"attributes": {"action_type": "ban_member"}
	}
}`

const mediaFileBody = `{
	"data": {
		"id": "file_1",
		"type": "files",
		"attributes": {"title": "My Video", "visibility": "private"}
	}
}`

const mediaFolderBody = `{
	"data": {
		"id": "folder_1",
		"type": "folders",
		"attributes": {"name": "Videos"}
	}
}`

const mediaPlaylistBody = `{
	"data": {
		"id": "pl_1",
		"type": "playlists",
		"attributes": {"title": "Course Videos", "visibility": "public"}
	}
}`

func TestCommunitySpacesList_PathAndMethod(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"community", "spaces", "list",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if want := "/admin/teams/t_team1/hubs/hub_abc123/spaces"; !strings.Contains(gotPath, want) {
		t.Errorf("path %q does not contain %q", gotPath, want)
	}
}

func TestCommunitySpacesCreate_BodyShape(t *testing.T) {
	var gotBody []byte
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(spaceBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"community", "spaces", "create",
			"--name", "General",
			"--slug", "general",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if want := "/admin/teams/t_team1/hubs/hub_abc123/spaces"; !strings.Contains(gotPath, want) {
		t.Errorf("path %q does not contain %q", gotPath, want)
	}

	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("body not valid JSON: %v; raw=%s", err, gotBody)
	}
	if doc.Data.Type != "spaces" {
		t.Errorf("data.type = %q, want \"spaces\"", doc.Data.Type)
	}
	if doc.Data.Attributes["name"] != "General" {
		t.Errorf("attributes.name = %v, want General", doc.Data.Attributes["name"])
	}
	if doc.Data.Attributes["slug"] != "general" {
		t.Errorf("attributes.slug = %v, want general", doc.Data.Attributes["slug"])
	}
}

func TestCommunitySpacesCreate_RequiresName(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{Status: 201, Body: spaceBody}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"community", "spaces", "create",
			"--slug", "general",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want ExitUsage (%d); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestCommunitySpacesDelete_RequiresYes(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{Status: 204, Body: ""}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"community", "spaces", "delete", "space_1",
		)...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want ExitNeedsConfir (%d)", res.Code, errs.ExitNeedsConfir)
	}
}

func TestCommunityDiscussionsList_Path(t *testing.T) {
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"community", "discussions", "list",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if want := "/admin/teams/t_team1/hubs/hub_abc123/discussions"; !strings.Contains(gotPath, want) {
		t.Errorf("path %q does not contain %q", gotPath, want)
	}
}

func TestCommunityMembersBan_MethodAndPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(moderationActionBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"community", "members", "ban", "contact_xyz",
			"--notes", "Spam violation", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	want := "/api/v1/admin/teams/t_team1/hubs/hub_abc123/members/contact_xyz/ban"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if _, hasData := body["data"]; hasData {
		t.Errorf("body has unexpected 'data' envelope: flat body expected")
	}
	if body["notes"] != "Spam violation" {
		t.Errorf("body.notes = %v, want 'Spam violation'", body["notes"])
	}
}

func TestCommunityMembersUnban_Path(t *testing.T) {
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(moderationActionBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"community", "members", "unban", "contact_xyz", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	want := "/api/v1/admin/teams/t_team1/hubs/hub_abc123/members/contact_xyz/unban"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestHubMembershipsBan_PathAndFlatBody(t *testing.T) {
	var gotPath string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(moderationActionBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"hub-memberships", "ban", "contact_xyz", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	want := "/api/v1/admin/teams/t_team1/hubs/hub_abc123/members/contact_xyz/ban"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if _, hasData := body["data"]; hasData {
		t.Errorf("body has unexpected 'data' envelope")
	}
}

func TestHubMembershipsWarn_Path(t *testing.T) {
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(moderationActionBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc123",
			"hub-memberships", "warn", "contact_xyz", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	want := "/api/v1/admin/teams/t_team1/hubs/hub_abc123/members/contact_xyz/warn"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestMediaFilesList_PathAndMethod(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "list")...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if want := "/api/v1/teams/t_team1/files"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestMediaFilesUpdate_BodyShape(t *testing.T) {
	var gotBody []byte
	var gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mediaFileBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "update", "file_1",
			"--title", "My Video",
			"--visibility", "public",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}

	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			ID         string         `json:"id"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("body not valid JSON: %v; raw=%s", err, gotBody)
	}
	if doc.Data.Type != "files" {
		t.Errorf("data.type = %q, want \"files\"", doc.Data.Type)
	}
	if doc.Data.ID != "file_1" {
		t.Errorf("data.id = %q, want file_1 (backend requires it in the body)", doc.Data.ID)
	}
	if doc.Data.Attributes["title"] != "My Video" {
		t.Errorf("attributes.title = %v, want 'My Video'", doc.Data.Attributes["title"])
	}
	if doc.Data.Attributes["visibility"] != "public" {
		t.Errorf("attributes.visibility = %v, want 'public'", doc.Data.Attributes["visibility"])
	}
	if _, ok := doc.Data.Attributes["description"]; ok {
		t.Errorf("attributes unexpectedly contains 'description'")
	}
}

func TestMediaFilesDelete_RequiresYes(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{Status: 204, Body: ""}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "delete", "file_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want ExitNeedsConfir (%d)", res.Code, errs.ExitNeedsConfir)
	}
}

func TestMediaFoldersCreate_RequiresName(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{Status: 201, Body: mediaFolderBody}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "folders", "create",
			"--parent-id", "folder_root",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want ExitUsage (%d); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestMediaPlaylistsCreate_RequiresTitle(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{Status: 201, Body: mediaPlaylistBody}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "create",
			"--visibility", "public",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want ExitUsage (%d); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestMediaFoldersList_Path(t *testing.T) {
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "folders", "list")...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if want := "/api/v1/teams/t_team1/folders"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestMediaFoldersCreate_BodyShape(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(mediaFolderBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "folders", "create",
			"--name", "Videos",
			"--parent-id", "folder_root",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}

	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("body not valid JSON: %v; raw=%s", err, gotBody)
	}
	if doc.Data.Type != "folders" {
		t.Errorf("data.type = %q, want \"folders\"", doc.Data.Type)
	}
	if doc.Data.Attributes["name"] != "Videos" {
		t.Errorf("attributes.name = %v, want 'Videos'", doc.Data.Attributes["name"])
	}
	if doc.Data.Attributes["parent_id"] != "folder_root" {
		t.Errorf("attributes.parent_id = %v, want 'folder_root'", doc.Data.Attributes["parent_id"])
	}
}

func TestMediaPlaylistsList_Path(t *testing.T) {
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "list")...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if want := "/api/v1/teams/t_team1/playlists"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestMediaPlaylistsCreate_BodyShape(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(mediaPlaylistBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "create",
			"--title", "Course Videos",
			"--visibility", "public",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}

	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("body not valid JSON: %v; raw=%s", err, gotBody)
	}
	if doc.Data.Type != "playlists" {
		t.Errorf("data.type = %q, want \"playlists\"", doc.Data.Type)
	}
	if doc.Data.Attributes["title"] != "Course Videos" {
		t.Errorf("attributes.title = %v, want 'Course Videos'", doc.Data.Attributes["title"])
	}
	if doc.Data.Attributes["visibility"] != "public" {
		t.Errorf("attributes.visibility = %v, want 'public'", doc.Data.Attributes["visibility"])
	}
}

func TestMediaPlaylistsUpdate_PodcastFlag(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mediaPlaylistBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "update", "pl_1",
			"--podcast-feed-enabled=true",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}

	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("body not valid JSON: %v; raw=%s", err, gotBody)
	}
	if doc.Data.Type != "playlists" {
		t.Errorf("data.type = %q, want \"playlists\"", doc.Data.Type)
	}
	if v, ok := doc.Data.Attributes["podcast_feed_enabled"]; !ok || v != true {
		t.Errorf("attributes.podcast_feed_enabled = %v (ok=%v), want true", v, ok)
	}
}

func TestCommunityGroupGuard_UnknownSubcommand(t *testing.T) {
	res := runContract(t, baseEnv("http://localhost"),
		"--team", "t1", "community", "frobnicate",
	)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want ExitUsage (%d)", res.Code, errs.ExitUsage)
	}
}

func TestMediaGroupGuard_UnknownSubcommand(t *testing.T) {
	res := runContract(t, baseEnv("http://localhost"),
		"--team", "t1", "media", "frobnicate",
	)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want ExitUsage (%d)", res.Code, errs.ExitUsage)
	}
}

func TestHubMembershipsGroupGuard_UnknownSubcommand(t *testing.T) {
	res := runContract(t, baseEnv("http://localhost"),
		"--team", "t1", "hub-memberships", "frobnicate",
	)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want ExitUsage (%d)", res.Code, errs.ExitUsage)
	}
}
