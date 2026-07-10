package cmd

// pages_schema_test.go — contract tests for `mio pages create/update` flag
// alignment to the backend PageCreate/PageUpdateAttributes schema (extra=forbid)
// and for the `sections reorder` request-body shape (MIO-2257).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const minimalSectionListBody = `{"data":[{"id":"sec_1","type":"sections","attributes":{"type":"text","position":0}}]}`

// TestPagesCreate_SchemaFields verifies the create body carries the real schema
// keys (title, slug, type, privacy, is_homepage, position) and NONE of the
// removed non-schema flags (published, description, layout) or the wrong
// is_home key.
func TestPagesCreate_SchemaFields(t *testing.T) {
	srv, gotMethod, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "create",
			"--title", "Home", "--slug", "dashboard",
			"--type", "generic", "--privacy", "public",
			"--is-home", "--position", "0",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", *gotMethod)
	}

	attrs := decodeHubAttrs(t, *gotBody)
	if attrs["title"] != "Home" || attrs["slug"] != "dashboard" {
		t.Errorf("title/slug = %v/%v, want Home/dashboard", attrs["title"], attrs["slug"])
	}
	if attrs["type"] != "generic" {
		t.Errorf("type = %v, want generic", attrs["type"])
	}
	if attrs["privacy"] != "public" {
		t.Errorf("privacy = %v, want public", attrs["privacy"])
	}
	if attrs["is_homepage"] != true {
		t.Errorf("is_homepage = %v, want true", attrs["is_homepage"])
	}
	if attrs["position"] != float64(0) {
		t.Errorf("position = %#v, want 0", attrs["position"])
	}
	for _, k := range []string{"published", "description", "layout", "is_home"} {
		if _, ok := attrs[k]; ok {
			t.Errorf("data.attributes.%s must NOT be present (not in PageCreateAttributes); attrs=%v", k, attrs)
		}
	}
}

// TestPagesCreate_RejectsInvalidPrivacy verifies an out-of-enum --privacy is
// rejected client-side with ExitUsage and fires no request.
func TestPagesCreate_RejectsInvalidPrivacy(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "create", "--title", "X", "--privacy", "secret",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an invalid --privacy must be rejected before any HTTP request")
	}
}

// TestPagesCreate_RemovedFlagsRejected verifies the dropped non-schema flags
// (--published/--description/--layout) are rejected as unknown flags (ExitUsage)
// and fire no request, rather than 422ing at the backend.
func TestPagesCreate_RemovedFlagsRejected(t *testing.T) {
	for _, flag := range []string{"--published", "--description", "--layout"} {
		flag := flag
		t.Run(flag, func(t *testing.T) {
			fired := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fired = true
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(minimalHubBody))
			}))
			t.Cleanup(srv.Close)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1", "--hub", "hub_123",
					"pages", "create", "--title", "X", flag,
				)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage) for %s; stderr=%q", res.Code, errs.ExitUsage, flag, res.Stderr)
			}
			if fired {
				t.Errorf("%s must be rejected before any HTTP request", flag)
			}
		})
	}
}

// TestPagesCreate_RejectsNegativePosition verifies --position < 0 is rejected
// client-side (schema requires position >= 0) with ExitUsage and no request.
func TestPagesCreate_RejectsNegativePosition(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "create", "--title", "X", "--position", "-1",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("a negative --position must be rejected before any HTTP request")
	}
}

// TestPagesUpdate_IsHomeMapsToIsHomepage verifies --is-home maps to
// data.attributes.is_homepage (not is_home) on update.
func TestPagesUpdate_IsHomeMapsToIsHomepage(t *testing.T) {
	srv, gotMethod, _, gotBody := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "update", "page_x", "--is-home",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", *gotMethod)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	if attrs["is_homepage"] != true {
		t.Errorf("is_homepage = %v, want true", attrs["is_homepage"])
	}
	if _, ok := attrs["is_home"]; ok {
		t.Errorf("data.attributes.is_home must NOT be present (schema uses is_homepage); attrs=%v", attrs)
	}
}

// TestPagesSectionsReorder_SendsDataList verifies `sections reorder --order a,b`
// sends a bare `{"data":[{"id","position"}]}` list (SectionReorderEnvelope),
// NOT the standard {data:{type,attributes:{order}}} envelope.
func TestPagesSectionsReorder_SendsDataList(t *testing.T) {
	var gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalSectionListBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"pages", "sections", "reorder", "page_x", "--order", "sec_1,sec_2,sec_3",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", gotMethod)
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
	want := []struct {
		id  string
		pos float64
	}{{"sec_1", 0}, {"sec_2", 1}, {"sec_3", 2}}
	for i, w := range want {
		if doc.Data[i]["id"] != w.id || doc.Data[i]["position"] != w.pos {
			t.Errorf("data[%d] = %#v, want {id:%s, position:%v}", i, doc.Data[i], w.id, w.pos)
		}
	}
}
