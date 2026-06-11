package cmd

// write_path_drift_test.go — regression tests for the four write-path
// serializer bugs reported in MIO-937, MIO-938, MIO-941, MIO-942.
//
// All four bugs were the same class: wrong JSON:API data.type values and/or
// missing/incorrect required attribute flags. These tests pin the CORRECT
// wire behaviour so the drift class is caught immediately if re-introduced.
//
// Wire-body tests assert the EXACT serialized request body (full JSON
// equality of the whole envelope: data.type + the complete attributes map),
// so a stale or extra attribute fails loudly instead of slipping past
// key-by-key checks.
//
// MIO-937 (contacts): data.type must be "team_contacts" (not "contacts")
// MIO-938 (segments): segment_type must NOT be sent; --name + --conditions required on create
// MIO-941 (products): attributes.type (product kind) must be sent; --name + --type required
// MIO-942 (content):  data.type must be "content_nodes"; --node-type maps to node_type;
//                     --title + --node-type required; --published removed (schema has
//                     published_at, exposed as --published-at)

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ── helpers shared by all drift tests ─────────────────────────────────────────

// captureWriteRequest starts a test server that captures the first request's
// body and returns a pointer to it. The server replies with a canned resource
// document at the given HTTP status code.
func captureWriteRequest(t *testing.T, status int, responseBody string) (*httptest.Server, *[]byte) {
	t.Helper()
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotBody
}

// assertExactBody asserts that the captured request body is EXACTLY equal to
// wantJSON (deep JSON equality of the entire envelope — data.type plus the
// complete attributes map). Any extra, missing, or stale attribute fails.
func assertExactBody(t *testing.T, gotBody []byte, wantJSON string) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatalf("wantJSON is not valid JSON (test bug): %v; want=%q", err, wantJSON)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wire body mismatch:\n got: %s\nwant: %s", gotBody, wantJSON)
	}
}

// newNoRequestServer starts a server that records whether ANY request arrived.
// Used by requiredness tests: client-side validation must fire BEFORE the API.
func newNoRequestServer(t *testing.T, responseBody string) (*httptest.Server, *bool) {
	t.Helper()
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &fired
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-937 — contacts create / update type value
// ═══════════════════════════════════════════════════════════════════════════════

const minimalContactBody = `{"data":{"id":"ctt_1","type":"team_contacts","attributes":{"email":"x@test.member.dev"}}}`

// TestWritePath_ContactsCreate_ExactBody pins the EXACT wire body for
// `mio contacts create`: data.type = "team_contacts" and only the attributes
// the user set.
//
// CONTRACT (MIO-937): contacts create → {"data":{"type":"team_contacts","attributes":{…}}}
func TestWritePath_ContactsCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContactBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "create",
			"--email", "x@test.member.dev",
			"--first-name", "Claudiu",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "team_contacts",
			"attributes": {
				"email": "x@test.member.dev",
				"first_name": "Claudiu"
			}
		}
	}`)
}

// TestWritePath_ContactsUpdate_ExactBody pins the EXACT wire body for
// `mio contacts update` (PATCH path).
//
// CONTRACT (MIO-937): contacts update → {"data":{"type":"team_contacts","attributes":{…}}}
func TestWritePath_ContactsUpdate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalContactBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "update", "ctt_1", "--first-name", "Alice")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "team_contacts",
			"attributes": {
				"first_name": "Alice"
			}
		}
	}`)
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-938 — segments create / update
// ═══════════════════════════════════════════════════════════════════════════════

const minimalSegmentBody = `{"data":{"id":"seg_1","type":"segment","attributes":{"name":"Test"}}}`

const segmentConditionsJSON = `{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":"@test.member.dev"}]}]}`

// TestWritePath_SegmentsCreate_ExactBody pins the EXACT wire body for
// `mio segments create`: type "segment", the parsed conditions tree inside
// attributes, and — critically — NO segment_type key (the backend schema is
// extra="forbid"; segment_type caused 422 "extra inputs not permitted").
//
// CONTRACT (MIO-938): segments create →
//
//	{"data":{"type":"segment","attributes":{"name":…,"conditions":{…}}}}
func TestWritePath_SegmentsCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalSegmentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "create",
			"--name", "Test Segment",
			"--conditions", segmentConditionsJSON,
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "segment",
			"attributes": {
				"name": "Test Segment",
				"conditions": {
					"version": 1,
					"groups": [
						{
							"logic": "AND",
							"conditions": [
								{"type": "email", "operator": "contains", "value": "@test.member.dev"}
							]
						}
					]
				}
			}
		}
	}`)
}

// TestWritePath_SegmentsCreate_RequiredFlags pins that `mio segments create`
// validates BOTH --name and --conditions client-side: any missing combination
// exits 2 (ExitUsage) and fires NO request.
//
// CONTRACT (MIO-938): segments create requires --name AND --conditions.
func TestWritePath_SegmentsCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing conditions", []string{"--name", "Test Segment"}},
		{"missing name", []string{"--conditions", segmentConditionsJSON}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalSegmentBody)

			args := append([]string{"segments", "create"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("POST must NOT be fired when required flags are missing")
			}
		})
	}
}

// TestWritePath_SegmentsUpdate_ExactBody pins the EXACT wire body for
// `mio segments update` — and proves no segment_type key is emitted.
//
// CONTRACT (MIO-938): segments update → {"data":{"type":"segment","attributes":{…}}}
func TestWritePath_SegmentsUpdate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalSegmentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "update", "seg_abc123",
			"--name", "Renamed Segment",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "segment",
			"attributes": {
				"name": "Renamed Segment"
			}
		}
	}`)
}

// TestWritePath_SegmentsUpdate_ConditionsExactBody pins that --conditions on
// update delivers the parsed tree inside attributes.conditions.
//
// CONTRACT (MIO-938): segments update --conditions → attributes.conditions = parsed tree
func TestWritePath_SegmentsUpdate_ConditionsExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalSegmentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "update", "seg_abc123",
			"--conditions", segmentConditionsJSON,
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "segment",
			"attributes": {
				"conditions": {
					"version": 1,
					"groups": [
						{
							"logic": "AND",
							"conditions": [
								{"type": "email", "operator": "contains", "value": "@test.member.dev"}
							]
						}
					]
				}
			}
		}
	}`)
}

// TestWritePath_SegmentsCreate_SegmentTypeFlagRemoved pins that the stale
// --segment-type flag no longer exists: passing it is an unknown flag → exit 2.
//
// CONTRACT (MIO-938): segments create --segment-type X → exit 2 (unknown flag)
func TestWritePath_SegmentsCreate_SegmentTypeFlagRemoved(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalSegmentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "create",
			"--name", "X",
			"--conditions", segmentConditionsJSON,
			"--segment-type", "static", // removed flag — must be unknown
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --segment-type); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when an unknown flag is passed")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-941 — products create / update
// ═══════════════════════════════════════════════════════════════════════════════

const minimalProductBody = `{"data":{"id":"prod_1","type":"products","attributes":{"name":"Course"}}}`

// TestWritePath_ProductsCreate_ExactBody pins the EXACT wire body for
// `mio products create`: attributes.type (the product kind) is present and no
// stale status/published keys exist.
//
// CONTRACT (MIO-941): products create →
//
//	{"data":{"type":"products","attributes":{"name":…,"type":…}}}
func TestWritePath_ProductsCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalProductBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "create",
			"--name", "Intro to Go",
			"--type", "course",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "products",
			"attributes": {
				"name": "Intro to Go",
				"type": "course"
			}
		}
	}`)
}

// TestWritePath_ProductsCreate_RequiredFlags pins that `mio products create`
// validates BOTH --name and --type client-side: any missing combination exits
// 2 (ExitUsage) and fires NO request.
//
// CONTRACT (MIO-941): products create requires --name AND --type.
func TestWritePath_ProductsCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing type", []string{"--name", "Intro to Go"}},
		{"missing name", []string{"--type", "course"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalProductBody)

			args := append([]string{"products", "create"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("POST must NOT be fired when required flags are missing")
			}
		})
	}
}

// TestWritePath_ProductsCreate_NoStaleStatusFlag pins that the stale --status
// flag no longer exists on products create (unknown flag → exit 2; no API call).
//
// CONTRACT (MIO-941): products create --status X → exit 2 (unknown flag)
func TestWritePath_ProductsCreate_NoStaleStatusFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalProductBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "create",
			"--name", "X",
			"--type", "course",
			"--status", "active", // stale flag — must not exist
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("MIO-941: exit code = %d, want %d (ExitUsage for removed --status flag); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("MIO-941: POST must NOT be fired when an unknown flag is passed")
	}
}

// TestWritePath_ProductsUpdate_ExactBody pins the EXACT wire body for
// `mio products update` (partial update: only the set flag is serialized).
//
// CONTRACT (MIO-941): products update --type membership →
//
//	{"data":{"type":"products","attributes":{"type":"membership"}}}
func TestWritePath_ProductsUpdate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalProductBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "update", "prod_abc123",
			"--type", "membership",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "products",
			"attributes": {
				"type": "membership"
			}
		}
	}`)
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-942 — content create / update
// ═══════════════════════════════════════════════════════════════════════════════

const minimalContentBody = `{"data":{"id":"cnt_1","type":"content_nodes","attributes":{"title":"Module 1"}}}`

// TestWritePath_ContentCreate_ExactBody pins the EXACT wire body for
// `mio content create`: data.type = "content_nodes" and --node-type → node_type.
//
// CONTRACT (MIO-942): content create →
//
//	{"data":{"type":"content_nodes","attributes":{"title":…,"node_type":…}}}
func TestWritePath_ContentCreate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Module 1",
			"--node-type", "container",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"title": "Module 1",
				"node_type": "container"
			}
		}
	}`)
}

// TestWritePath_ContentCreate_LessonWithContentTypeExactBody pins the full
// body for a lesson node carrying the optional content_type sub-type.
//
// CONTRACT (MIO-942): --content-type video → attributes.content_type = "video"
func TestWritePath_ContentCreate_LessonWithContentTypeExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Welcome Video",
			"--node-type", "lesson",
			"--content-type", "video",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"title": "Welcome Video",
				"node_type": "lesson",
				"content_type": "video"
			}
		}
	}`)
}

// TestWritePath_ContentCreate_PublishedAtExactBody pins that --published-at
// maps to attributes.published_at (the schema's nullable publish timestamp;
// the backend gates member visibility on published_at <= now).
//
// CONTRACT (MIO-942 / Codex R1): --published-at X → attributes.published_at = X
func TestWritePath_ContentCreate_PublishedAtExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Module 1",
			"--node-type", "container",
			"--published-at", "2026-06-11T00:00:00Z",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"title": "Module 1",
				"node_type": "container",
				"published_at": "2026-06-11T00:00:00Z"
			}
		}
	}`)
}

// TestWritePath_ContentCreate_RequiredFlags pins that `mio content create`
// validates BOTH --title and --node-type client-side: any missing combination
// exits 2 (ExitUsage) and fires NO request.
//
// CONTRACT (MIO-942): content create requires --title AND --node-type.
func TestWritePath_ContentCreate_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing node-type", []string{"--title", "Module 1"}},
		{"missing title", []string{"--node-type", "container"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalContentBody)

			args := append([]string{"--hub", "hub_abc", "content", "create"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("POST must NOT be fired when required flags are missing")
			}
		})
	}
}

// TestWritePath_ContentUpdate_ExactBody pins the EXACT wire body for
// `mio content update` (partial update — the missing regression from R1).
//
// CONTRACT (MIO-942): content update --title X →
//
//	{"data":{"type":"content_nodes","attributes":{"title":"X"}}}
func TestWritePath_ContentUpdate_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "update", "cnt_abc123",
			"--title", "New Title",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"title": "New Title"
			}
		}
	}`)
}

// TestWritePath_ContentUpdate_PublishedAtExactBody pins --published-at on the
// update path (published_at is mutable post-create per the backend schema).
//
// CONTRACT (Codex R1): content update --published-at X → attributes.published_at = X
func TestWritePath_ContentUpdate_PublishedAtExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "update", "cnt_abc123",
			"--published-at", "2026-06-11T00:00:00Z",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"published_at": "2026-06-11T00:00:00Z"
			}
		}
	}`)
}

// TestWritePath_Content_PublishedFlagRemoved pins that the stale --published
// bool flag no longer exists on content create OR update. The backend schema
// (extra="forbid") has published_at, not published — the bool flag was a
// guaranteed-422 dead flag (Codex R1 finding 1).
//
// CONTRACT (Codex R1): content create/update --published → exit 2 (unknown flag)
func TestWritePath_Content_PublishedFlagRemoved(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"create", []string{"content", "create", "--title", "X", "--node-type", "lesson", "--published"}},
		{"update", []string{"content", "update", "cnt_abc123", "--title", "X", "--published"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalContentBody)

			args := append([]string{"--hub", "hub_abc"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage for removed --published flag); stderr=%q",
					res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("request must NOT be fired when an unknown flag is passed")
			}
		})
	}
}

// TestWritePath_ContentCreate_NoStaleStatusFlag pins that the stale --status
// flag no longer exists on content create (unknown flag → exit 2; no API call).
//
// CONTRACT (MIO-942): content create --status X → exit 2 (unknown flag)
func TestWritePath_ContentCreate_NoStaleStatusFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "X",
			"--node-type", "lesson",
			"--status", "draft", // stale flag — must not exist
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("MIO-942: exit code = %d, want %d (ExitUsage for removed --status flag); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("MIO-942: POST must NOT be fired when an unknown flag is passed")
	}
}
