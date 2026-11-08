package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestWiring_HubFlagResolvesName(t *testing.T) {
	hubsBody := `{"data":[
	  {"id":"hub_real","type":"hubs","attributes":{"name":"My Community","slug":"my-community"}}
	]}`
	var contentPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/hubs"):
			_, _ = w.Write([]byte(hubsBody))
		case strings.Contains(r.URL.Path, "/content"):
			contentPath = r.URL.Path
			_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "My Community", "content", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if !strings.Contains(contentPath, "/hubs/hub_real/content") {
		t.Errorf("content path did not use the resolved hub id; got %q", contentPath)
	}
}

func TestWiring_HubFlagRawIDSkipsResolution(t *testing.T) {
	var hubsListed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if strings.HasSuffix(r.URL.Path, "/hubs") {
			hubsListed.Store(true)
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc123", "content", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if hubsListed.Load() {
		t.Error("raw hub id should NOT trigger a hubs list call")
	}
}

func TestWiring_HubFlagUnknownName(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Method: "GET", PathPfx: "/api/v1/teams/", Status: 200, Body: `{"data":[]}`},
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "Nonexistent", "content", "list")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("unknown hub name exit code = %d, want %d (ExitUsage)", res.Code, errs.ExitUsage)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("error path must produce no stdout; got %q", res.Stdout)
	}
}

func TestWiring_HubFlagUnknownName_FriendlyError(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Method: "GET", PathPfx: "/api/v1/teams/", Status: 200, Body: `{"data":[]}`},
	})
	bin := buildBinary(t)
	_, stderr, code := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	}, "--team", "t_team1", "--hub", "Nonexistent", "content", "list")
	if code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", code, errs.ExitUsage)
	}
	if !strings.Contains(stderr, "no hub named") {
		t.Errorf("expected friendly not-found error in stderr; got %q", stderr)
	}
}

func TestWiring_SingleTeamAutoDefault(t *testing.T) {
	var tagsPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.URL.Path == "/api/v1/teams":
			_, _ = w.Write([]byte(`{"data":[{"id":"team_only","type":"teams","attributes":{"name":"Solo","slug":"solo"}}]}`))
		case strings.Contains(r.URL.Path, "/tags"):
			tagsPath = r.URL.Path
			_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	env := append(baseEnv(srv.URL), "XDG_CONFIG_HOME="+t.TempDir())
	res := runContract(t, env, "tags", "list")
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if !strings.Contains(tagsPath, "/api/v1/teams/team_only/tags") {
		t.Errorf("tags path did not use the auto-defaulted team; got %q", tagsPath)
	}
}

func TestWiring_MultiTeamNoDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.URL.Path == "/api/v1/teams" {
			_, _ = w.Write([]byte(`{"data":[
			  {"id":"team_a","type":"teams","attributes":{"name":"A","slug":"a"}},
			  {"id":"team_b","type":"teams","attributes":{"name":"B","slug":"b"}}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"status":"404"}]}`))
	}))
	t.Cleanup(srv.Close)

	env := append(baseEnv(srv.URL), "XDG_CONFIG_HOME="+t.TempDir())
	res := runContract(t, env, "tags", "list")
	if res.Code != errs.ExitUsage {
		t.Errorf("multi-team no-default exit code = %d, want %d (ExitUsage)", res.Code, errs.ExitUsage)
	}
}

func TestWiring_SingleHubAutoDefault(t *testing.T) {
	var contentPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/hubs"):
			_, _ = w.Write([]byte(`{"data":[{"id":"hub_only","type":"hubs","attributes":{"name":"The Hub","slug":"the-hub"}}]}`))
		case strings.Contains(r.URL.Path, "/content"):
			contentPath = r.URL.Path
			_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "content", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if !strings.Contains(contentPath, "/hubs/hub_only/content") {
		t.Errorf("content path did not use the auto-defaulted hub; got %q", contentPath)
	}
}

func TestWiring_TagsAssignByEmailAndTagName(t *testing.T) {
	var assignPath string
	var assignBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contacts"):
			if got := r.URL.Query().Get("filter[email]"); got != "alice@example.com" {
				t.Errorf("contact resolve filter[email] = %q", got)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"ctt_alice","type":"team_contacts","attributes":{"email":"alice@example.com"}}]}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			_, _ = w.Write([]byte(`{"data":[{"id":"tag_vip","type":"tags","attributes":{"name":"VIP","slug":"vip"}}]}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/contacts/ctt_alice/tags"):
			assignPath = r.URL.Path
			b := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(b)
			assignBody = string(b)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":[{"id":"tag_vip","type":"tags","attributes":{"name":"VIP","slug":"vip"}}],"meta":{"count":1}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"unexpected ` + r.Method + " " + r.URL.Path + `"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "assign", "--email", "alice@example.com", "--tag", "VIP")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(assignPath, "/contacts/ctt_alice/tags") {
		t.Errorf("assign path = %q, want resolved contact id", assignPath)
	}
	if !strings.Contains(assignBody, "tag_vip") {
		t.Errorf("assign body did not carry the resolved tag id; body=%q", assignBody)
	}
}

func TestWiring_TagsAssignRawIDsStillWork(t *testing.T) {
	var listed atomic.Bool
	var assignPath, assignBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodGet {
			listed.Store(true)
		}
		if r.Method == http.MethodPost {
			assignPath = r.URL.Path
			b := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(b)
			assignBody = string(b)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":[{"id":"tag_abc","type":"tags","attributes":{"name":"VIP","slug":"vip"}}],"meta":{"count":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "assign", "ctt_xyz", "--tag-id", "tag_abc")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if listed.Load() {
		t.Error("raw-id assign should make NO resolution list calls")
	}
	if !strings.HasSuffix(assignPath, "/contacts/ctt_xyz/tags") {
		t.Errorf("assign path = %q", assignPath)
	}
	if !strings.Contains(assignBody, "tag_abc") {
		t.Errorf("assign body = %q, want tag_abc", assignBody)
	}
}

func TestWiring_TagsAssignBulkSendsArrayAndDecodesCollection(t *testing.T) {
	var bulkPath, bulkBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tags/bulk") {
			bulkPath = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			bulkBody = string(b)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":[` +
				`{"id":"tag_abc","type":"tags","attributes":{"name":"VIP","slug":"vip"}},` +
				`{"id":"tag_def","type":"tags","attributes":{"name":"Lead","slug":"lead"}}` +
				`],"meta":{"count":2}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"unexpected ` + r.Method + " " + r.URL.Path + `"}]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "assign-bulk", "ctt_xyz", "--tag-ids", "tag_abc,tag_def")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(bulkPath, "/contacts/ctt_xyz/tags/bulk") {
		t.Errorf("bulk path = %q, want .../contacts/ctt_xyz/tags/bulk", bulkPath)
	}
	var env struct {
		Data struct {
			Type       string `json:"type"`
			Attributes struct {
				TagIDs any `json:"tag_ids"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(bulkBody), &env); err != nil {
		t.Fatalf("bulk body is not a valid JSON:API document: %v; body=%q", err, bulkBody)
	}
	if env.Data.Type != "tag_assignments" {
		t.Errorf("data.type = %q, want \"tag_assignments\"; body=%q", env.Data.Type, bulkBody)
	}
	wantIDs := []any{"tag_abc", "tag_def"}
	if !reflect.DeepEqual(env.Data.Attributes.TagIDs, wantIDs) {
		t.Errorf("attributes.tag_ids = %#v, want %#v (an array, not a comma-joined string); body=%q",
			env.Data.Attributes.TagIDs, wantIDs, bulkBody)
	}
}

func TestWiring_TagsAssignBulkWhitespaceTagIDsIsUsageError(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"status":"404"}]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "assign-bulk", "ctt_xyz", "--tag-ids", "  ,  ")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("whitespace-only --tag-ids exit code = %d, want %d (ExitUsage)", res.Code, errs.ExitUsage)
	}
	if called.Load() {
		t.Error("usage error should make NO HTTP request")
	}
}

func TestWiring_TagsAssignBulkNoTagIDsIsUsageError(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"status":"404"}]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "assign-bulk", "ctt_xyz")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("missing --tag-ids exit code = %d, want %d (ExitUsage)", res.Code, errs.ExitUsage)
	}
	if called.Load() {
		t.Error("usage error should make NO HTTP request")
	}
}

func TestWiring_TagsRemoveByName(t *testing.T) {
	var deletePath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contacts"):
			_, _ = w.Write([]byte(`{"data":[{"id":"ctt_alice","type":"team_contacts","attributes":{"email":"alice@example.com"}}]}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			_, _ = w.Write([]byte(`{"data":[{"id":"tag_vip","type":"tags","attributes":{"name":"VIP","slug":"vip"}}]}`))
		case r.Method == http.MethodDelete:
			deletePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "remove", "--email", "alice@example.com", "--tag", "VIP", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(deletePath, "/contacts/ctt_alice/tags/tag_vip") {
		t.Errorf("delete path = %q, want resolved contact+tag", deletePath)
	}
}

func TestWiring_TagsRemoveRawPositionalsStillWork(t *testing.T) {
	var listed atomic.Bool
	var deletePath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodGet {
			listed.Store(true)
		}
		if r.Method == http.MethodDelete {
			deletePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "remove", "ctt_xyz", "tag_abc", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}
	if listed.Load() {
		t.Error("raw positional remove should make NO resolution list calls")
	}
	if !strings.HasSuffix(deletePath, "/contacts/ctt_xyz/tags/tag_abc") {
		t.Errorf("delete path = %q", deletePath)
	}
}

func TestWiring_TagsAssignAmbiguousFlags(t *testing.T) {
	srv := newMockServer(t, nil)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "tags", "assign", "ctt_xyz", "--tag", "VIP", "--tag-id", "tag_abc")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("both --tag and --tag-id exit code = %d, want %d (ExitUsage)", res.Code, errs.ExitUsage)
	}
}
