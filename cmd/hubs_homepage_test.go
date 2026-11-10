package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const homepagePageID = "0197a1b2-c3d4-7e5f-8a6b-1c2d3e4f5a6b"

var homepagePatchPath = "/api/v1" + strings.TrimPrefix(hubsPath("t_team1", "hub_abc123"), "/api")

const hubWithHomepageBody = `{
	"data": {
		"id": "hub_abc123",
		"type": "hubs",
		"attributes": {
			"title": "H", "slug": "h", "is_private": false,
			"homepage": {"kind": "content_index"}
		}
	}
}`

type homepageServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	methods []string
	paths   []string
	body    []byte
}

func (h *homepageServer) requests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.methods)
}

func (h *homepageServer) assertSinglePatchTo(t *testing.T, wantPath string) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var patches []string
	for i, m := range h.methods {
		if m == http.MethodPatch {
			patches = append(patches, h.paths[i])
		}
	}
	if len(patches) != 1 || patches[0] != wantPath {
		t.Errorf("PATCH requests = %v, want exactly one to %q", patches, wantPath)
	}
}

func (h *homepageServer) patchBody() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.body
}

func newHomepageServer(t *testing.T, status int, respBody string) *homepageServer {
	t.Helper()
	h := &homepageServer{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.methods = append(h.methods, r.Method)
		h.paths = append(h.paths, r.URL.Path)
		if r.Method == http.MethodPatch {
			h.body = raw
		}
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPatch {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(respBody))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubWithBlobsBody))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func homepageAttr(t *testing.T, body []byte) any {
	t.Helper()
	attrs := decodeHubAttrs(t, body)
	v, ok := attrs["homepage"]
	if !ok {
		t.Fatalf("PATCH attributes carry no homepage; attrs=%v", attrs)
	}
	return v
}

func TestHubsUpdate_HomepageKinds(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want map[string]any
	}{
		{"custom with page id", []string{"--homepage", "custom", "--homepage-page-id", homepagePageID},
			map[string]any{"kind": "custom", "page_id": homepagePageID}},
		{"discussions_index", []string{"--homepage", "discussions_index"},
			map[string]any{"kind": "discussions_index"}},
		{"content_index", []string{"--homepage", "content_index"},
			map[string]any{"kind": "content_index"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHomepageServer(t, http.StatusOK, hubWithHomepageBody)
			args := append([]string{"hubs", "update", "hub_abc123"}, tc.args...)

			res := runContract(t, baseEnv(h.srv.URL), withTeam("t_team1", args...)...)

			if res.Code != errs.ExitOK {
				t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
			}
			if h.requests() != 1 {
				t.Fatalf("requests = %d, want exactly 1 PATCH (homepage is a plain attribute, no read-modify-write)", h.requests())
			}
			h.assertSinglePatchTo(t, homepagePatchPath)
			got := homepageAttr(t, h.patchBody())
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("homepage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHubsUpdate_HomepageAloneCountsAsSomethingToUpdate(t *testing.T) {
	h := newHomepageServer(t, http.StatusOK, hubWithHomepageBody)

	res := runContract(t, baseEnv(h.srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_abc123", "--homepage", "content_index")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	h.assertSinglePatchTo(t, homepagePatchPath)
	attrs := decodeHubAttrs(t, h.patchBody())
	if len(attrs) != 1 {
		t.Errorf("PATCH attributes = %v, want only homepage", attrs)
	}
}

func TestHubsUpdate_HomepageUsageErrors(t *testing.T) {
	cases := []struct {
		name    string
		bad     []string
		good    []string
		wantMsg string
	}{
		{
			name:    "custom without page id",
			bad:     []string{"--homepage", "custom"},
			good:    []string{"--homepage", "custom", "--homepage-page-id", homepagePageID},
			wantMsg: "--homepage-page-id",
		},
		{
			name:    "page id without homepage",
			bad:     []string{"--homepage-page-id", homepagePageID},
			good:    []string{"--homepage", "custom", "--homepage-page-id", homepagePageID},
			wantMsg: "needs --homepage custom",
		},
		{
			name:    "page id with content_index",
			bad:     []string{"--homepage", "content_index", "--homepage-page-id", homepagePageID},
			good:    []string{"--homepage", "content_index"},
			wantMsg: "--homepage-page-id",
		},
		{
			name:    "page id with discussions_index",
			bad:     []string{"--homepage", "discussions_index", "--homepage-page-id", homepagePageID},
			good:    []string{"--homepage", "discussions_index"},
			wantMsg: "--homepage-page-id",
		},
	}
	bin := buildBinary(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHomepageServer(t, http.StatusOK, hubWithHomepageBody)
			env := baseEnv(h.srv.URL)
			base := []string{"--team", "t_team1", "hubs", "update", "hub_abc123"}

			_, badStderr, badCode := runBinary(t, bin, env, append(append([]string{}, base...), tc.bad...)...)

			if badCode != errs.ExitUsage {
				t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", badCode, errs.ExitUsage, badStderr)
			}
			if !strings.Contains(badStderr, tc.wantMsg) {
				t.Errorf("stderr %q does not mention %q", badStderr, tc.wantMsg)
			}
			if strings.Contains(badStderr, "unknown flag") {
				t.Errorf("stderr %q reports an unknown flag, not the contradictory input", badStderr)
			}
			if h.requests() != 0 {
				t.Fatalf("a usage error must make no request; got %d", h.requests())
			}

			_, goodStderr, goodCode := runBinary(t, bin, env, append(append([]string{}, base...), tc.good...)...)

			if goodCode != errs.ExitOK {
				t.Fatalf("positive control: exit code = %d, want %d; stderr=%q", goodCode, errs.ExitOK, goodStderr)
			}
			if h.requests() != 1 {
				t.Errorf("positive control: requests = %d, want 1", h.requests())
			}
			h.assertSinglePatchTo(t, homepagePatchPath)
		})
	}
}

func TestHubsUpdate_HomepageUnknownKindPassesThroughAnd422Surfaces(t *testing.T) {
	const apiError = `{"errors":[{"status":"422","code":"invalid_attribute","title":"Unprocessable Entity","detail":"Unknown homepage kind.","source":{"pointer":"/data/attributes/homepage"}}]}`
	h := newHomepageServer(t, http.StatusUnprocessableEntity, apiError)

	_, stderr, code := runBinary(t, buildBinary(t), baseEnv(h.srv.URL),
		"--team", "t_team1", "hubs", "update", "hub_abc123", "--homepage", "carousel")

	if code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage, from 422); stderr=%q", code, errs.ExitUsage, stderr)
	}
	if h.requests() != 1 {
		t.Fatalf("requests = %d, want 1: the CLI must not pre-empt the API's kind validation", h.requests())
	}
	h.assertSinglePatchTo(t, homepagePatchPath)
	got := homepageAttr(t, h.patchBody())
	if want := map[string]any{"kind": "carousel"}; !reflect.DeepEqual(got, want) {
		t.Errorf("homepage = %v, want %v", got, want)
	}
	if !strings.Contains(stderr, "/data/attributes/homepage") {
		t.Errorf("stderr %q does not surface the API's source pointer", stderr)
	}
}

func TestHubsUpdate_HomepageComposesWithOtherFlags(t *testing.T) {
	h := newHomepageServer(t, http.StatusOK, hubWithHomepageBody)

	res := runContract(t, baseEnv(h.srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_abc123",
			"--name", "Renamed", "--homepage", "content_index")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if h.requests() != 1 {
		t.Fatalf("requests = %d, want one PATCH carrying both", h.requests())
	}
	h.assertSinglePatchTo(t, homepagePatchPath)
	attrs := decodeHubAttrs(t, h.patchBody())
	if attrs["title"] != "Renamed" {
		t.Errorf("title = %v, want Renamed", attrs["title"])
	}
	if !reflect.DeepEqual(attrs["homepage"], map[string]any{"kind": "content_index"}) {
		t.Errorf("homepage = %v, want content_index", attrs["homepage"])
	}
}

func TestHubsUpdate_HomepageSurvivesReadModifyWrite(t *testing.T) {
	h := newHomepageServer(t, http.StatusOK, hubWithHomepageBody)

	res := runContract(t, baseEnv(h.srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_abc123",
			"--homepage", "discussions_index", "--branding-json", `{"primary":"#6747E3"}`)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	h.assertSinglePatchTo(t, homepagePatchPath)
	attrs := decodeHubAttrs(t, h.patchBody())
	if !reflect.DeepEqual(attrs["homepage"], map[string]any{"kind": "discussions_index"}) {
		t.Errorf("homepage = %v, want discussions_index after read-modify-write", attrs["homepage"])
	}
	b, ok := attrs["branding"].(map[string]any)
	if !ok || b["primary"] != "#6747E3" || b["logo_url"] != "https://old/logo.png" {
		t.Errorf("branding = %v, want the merged blob", attrs["branding"])
	}
}

func TestHubsUpdate_HomepageShownInOutput(t *testing.T) {
	args := withTeam("t_team1", "hubs", "update", "hub_abc123", "--homepage", "content_index")

	jsServer := newHomepageServer(t, http.StatusOK, hubWithHomepageBody)
	js := runContract(t, baseEnv(jsServer.srv.URL), append(args, "--output", "json")...)
	if js.Code != errs.ExitOK {
		t.Fatalf("json: exit code = %d; stderr=%q", js.Code, js.Stderr)
	}
	jsServer.assertSinglePatchTo(t, homepagePatchPath)
	var out map[string]any
	if err := json.Unmarshal([]byte(js.Stdout), &out); err != nil {
		t.Fatalf("json output is not an object: %v; stdout=%q", err, js.Stdout)
	}
	if !reflect.DeepEqual(out["homepage"], map[string]any{"kind": "content_index"}) {
		t.Errorf("json homepage = %v, want {kind: content_index}", out["homepage"])
	}

	plainServer := newHomepageServer(t, http.StatusOK, hubWithHomepageBody)
	plain := runContract(t, baseEnv(plainServer.srv.URL), append(args, "--output", "plain")...)
	if plain.Code != errs.ExitOK {
		t.Fatalf("plain: exit code = %d; stderr=%q", plain.Code, plain.Stderr)
	}
	plainServer.assertSinglePatchTo(t, homepagePatchPath)
	if !strings.Contains(plain.Stdout, `homepage={"kind":"content_index"}`) {
		t.Errorf("plain output does not show homepage; stdout=%q", plain.Stdout)
	}
}
