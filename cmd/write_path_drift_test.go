package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

const minimalContactBody = `{"data":{"id":"ctt_1","type":"team_contacts","attributes":{"email":"x@test.member.dev"}}}`

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

const minimalSegmentBody = `{"data":{"id":"seg_1","type":"segment","attributes":{"name":"Test"}}}`

const segmentConditionsJSON = `{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":"@test.member.dev"}]}]}`

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

func TestWritePath_SegmentsCreate_SegmentTypeFlagRemoved(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalSegmentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "create",
			"--name", "X",
			"--conditions", segmentConditionsJSON,
			"--segment-type", "static",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for removed --segment-type); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when an unknown flag is passed")
	}
}

const minimalProductBody = `{"data":{"id":"prod_1","type":"products","attributes":{"name":"Course"}}}`

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

	t.Run("missing type, no credentials: still a usage error", func(t *testing.T) {
		res := runContract(t, []string{"MIO_API_KEY=", "MIO_API_BASE_URL=http://127.0.0.1:1"},
			"products", "create", "--name", "Intro to Go", "--team", "t_team1")
		if res.Code != errs.ExitUsage {
			t.Errorf("exit code = %d, want %d (ExitUsage): --type must be enforced by cobra, before auth; stderr=%q",
				res.Code, errs.ExitUsage, res.Stderr)
		}
	})
}

func TestWritePath_ProductsCreate_NoStaleStatusFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalProductBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "create",
			"--name", "X",
			"--type", "course",
			"--status", "active",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("MIO-941: exit code = %d, want %d (ExitUsage for removed --status flag); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("MIO-941: POST must NOT be fired when an unknown flag is passed")
	}
}

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

const minimalContentBody = `{"data":{"id":"cnt_1","type":"content_nodes","attributes":{"title":"Module 1"}}}`

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

func TestWritePath_ContentCreate_NoStaleStatusFlag(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "X",
			"--node-type", "lesson",
			"--status", "draft",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("MIO-942: exit code = %d, want %d (ExitUsage for removed --status flag); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("MIO-942: POST must NOT be fired when an unknown flag is passed")
	}
}
