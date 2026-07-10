package cmd

// community_spaces_test.go — contract tests for `mio community spaces`
// create/update field alignment to the backend Space schema and the new
// `spaces reorder` command (MIO-2260). The old --is-private flag was bogus
// (Space has no is_private column; access is governed by access_level).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const minimalSpaceListBody = `{"data":[{"id":"sp_1","type":"spaces","attributes":{"name":"General","position":0}}]}`

// TestCommunitySpacesCreate_Fields verifies the create body carries the real
// Space schema keys and omits the removed bogus is_private/is-private.
func TestCommunitySpacesCreate_Fields(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "spaces", "create",
			"--name", "General", "--slug", "general",
			"--access-level", "public", "--posting-permission", "any_member",
			"--position", "0", "--is-pinned",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/spaces") {
		t.Errorf("path %q does not end with /spaces", *gotPath)
	}

	attrs := decodeHubAttrs(t, *gotBody)
	if attrs["name"] != "General" || attrs["slug"] != "general" {
		t.Errorf("name/slug = %v/%v, want General/general", attrs["name"], attrs["slug"])
	}
	if attrs["access_level"] != "public" {
		t.Errorf("access_level = %v, want public", attrs["access_level"])
	}
	if attrs["posting_permission"] != "any_member" {
		t.Errorf("posting_permission = %v, want any_member", attrs["posting_permission"])
	}
	if attrs["position"] != float64(0) {
		t.Errorf("position = %#v, want 0", attrs["position"])
	}
	if attrs["is_pinned"] != true {
		t.Errorf("is_pinned = %v, want true", attrs["is_pinned"])
	}
	for _, k := range []string{"is_private", "is-private"} {
		if _, ok := attrs[k]; ok {
			t.Errorf("data.attributes.%s must NOT be present (removed); attrs=%v", k, attrs)
		}
	}
}

// TestCommunitySpacesCreate_RejectsInvalidEnums verifies out-of-enum
// --access-level / --posting-permission are rejected client-side with no request.
func TestCommunitySpacesCreate_RejectsInvalidEnums(t *testing.T) {
	for _, tc := range []struct{ name, flag, val string }{
		{"access-level", "--access-level", "everyone"},
		{"posting-permission", "--posting-permission", "nobody"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fired := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fired = true
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(minimalHubBody))
			}))
			t.Cleanup(srv.Close)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1", "--hub", "hub_123",
					// --slug present so that if enum validation regressed the create
					// would reach the POST (fired=true) rather than false-passing on
					// the missing-slug usage error.
					"community", "spaces", "create", "--name", "X", "--slug", "x", tc.flag, tc.val,
				)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if fired {
				t.Errorf("invalid %s must exit before any HTTP request", tc.flag)
			}
		})
	}
}

// TestCommunitySpacesCreate_RequiresSlug verifies a create missing the required
// --slug exits ExitUsage before any HTTP request (slug is a required Space field).
func TestCommunitySpacesCreate_RequiresSlug(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "spaces", "create", "--name", "General",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("a create missing --slug must exit before any HTTP request")
	}
}

// TestCommunitySpacesReorder_SendsDataList verifies reorder POSTs to
// .../spaces/reorder with a bare {data:[{type,id}]} list, order = position.
func TestCommunitySpacesReorder_SendsDataList(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalSpaceListBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "spaces", "reorder", "--order", "sp_1,sp_2,sp_3",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/spaces/reorder") {
		t.Errorf("path %q does not end with /spaces/reorder", gotPath)
	}

	var doc struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("body is not valid JSON: %v; body=%q", err, gotBody)
	}
	if len(doc.Data) != 3 {
		t.Fatalf("data should be a 3-item list; got %#v", doc.Data)
	}
	for i, id := range []string{"sp_1", "sp_2", "sp_3"} {
		if doc.Data[i]["id"] != id || doc.Data[i]["type"] != "spaces" {
			t.Errorf("data[%d] = %#v, want {type:spaces, id:%s}", i, doc.Data[i], id)
		}
	}
}
