package cmd

// Wire contract for the team-scoped file thumbnail upsert and removal (MIO-5832).
import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestFileThumbnailSet_Wire(t *testing.T) {
	for _, tc := range []struct {
		name, flag, value string
		wantGet           bool
	}{
		{"file", "--image-file-id", "image_file", true}, {"media", "--image-media-id", "media_pk", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/vnd.api+json")
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/teams/t_team1/files/image_file":
					_, _ = w.Write([]byte(`{"data":{"type":"files","id":"image_file","attributes":{"media_id":"media_pk"}}}`))
				case "POST /api/v1/teams/t_team1/file-thumbnail-attachments":
					body, _ = io.ReadAll(r.Body)
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"data":{"type":"attachments","id":"att","attributes":{}}}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "media", "files", "thumbnail", "set", "episode", tc.flag, tc.value)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit=%d stderr=%s", res.Code, res.Stderr)
			}
			want := []string{"POST /api/v1/teams/t_team1/file-thumbnail-attachments"}
			if tc.wantGet {
				want = append([]string{"GET /api/v1/teams/t_team1/files/image_file"}, want...)
			}
			if !reflect.DeepEqual(requests, want) {
				t.Errorf("requests=%v want %v", requests, want)
			}
			typ, attrs := decodeDataTypeAttrs(t, body)
			if typ != "attachments" {
				t.Errorf("type=%q want attachments", typ)
			}
			for k, v := range map[string]any{"media_id": "media_pk", "target_type": "file", "target_id": "episode", "role": "thumbnail"} {
				if attrs[k] != v {
					t.Errorf("%s=%v want %v", k, attrs[k], v)
				}
			}
			if len(attrs) != 4 {
				t.Errorf("unexpected attributes: %v", attrs)
			}
		})
	}
}

func TestFileThumbnailSet_InvalidFlags(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"neither", nil}, {"both", []string{"--image-file-id", "f", "--image-media-id", "m"}},
		{"empty_file", []string{"--image-file-id", ""}}, {"empty_media", []string{"--image-media-id", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := firedGuardServer(t)
			args := append([]string{"media", "files", "thumbnail", "set", "episode"}, tc.flags...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != errs.ExitUsage || *fired {
				t.Errorf("exit=%d request=%v stderr=%s", res.Code, *fired, res.Stderr)
			}
		})
	}
}

func TestFileThumbnailRemove_Wire(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		yes, missingFile, thumbnail bool
		code                        int
	}{
		{"remove", true, false, true, errs.ExitOK}, {"absent", true, false, false, errs.ExitOK},
		{"unknown_file", true, true, false, errs.ExitNotFound}, {"confirmation", false, false, true, errs.ExitNeedsConfir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/vnd.api+json")
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/teams/t_team1/files/episode":
					if tc.missingFile {
						w.WriteHeader(404)
						_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"missing"}]}`))
						return
					}
					_, _ = w.Write([]byte(`{"data":{"type":"files","id":"episode","attributes":{}}}`))
				case "GET /api/v1/teams/t_team1/attachments":
					if r.URL.Query().Get("target_type") != "file" || r.URL.Query().Get("target_id") != "episode" {
						t.Errorf("filters=%v", r.URL.Query())
					}
					data := `{"type":"attachments","id":"other","attributes":{"role":"main"}}`
					if tc.thumbnail {
						data += `,{"type":"attachments","id":"thumb","attributes":{"role":"thumbnail"}}`
					}
					_, _ = w.Write([]byte(`{"data":[` + data + `]}`))
				case "DELETE /api/v1/teams/t_team1/attachments/thumb":
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			args := []string{"media", "files", "thumbnail", "remove", "episode"}
			if tc.yes {
				args = append(args, "--yes")
			}
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != tc.code {
				t.Errorf("exit=%d want %d stderr=%s", res.Code, tc.code, res.Stderr)
			}
			var want []string
			if tc.yes {
				want = append(want, "GET /api/v1/teams/t_team1/files/episode")
				if !tc.missingFile {
					want = append(want, "GET /api/v1/teams/t_team1/attachments")
					if tc.thumbnail {
						want = append(want, "DELETE /api/v1/teams/t_team1/attachments/thumb")
					}
				}
			}
			if !reflect.DeepEqual(requests, want) {
				t.Errorf("requests=%v want %v", requests, want)
			}
			if tc.name == "absent" && !strings.Contains(res.Stderr, "file episode has no thumbnail") {
				t.Errorf("stderr=%q", res.Stderr)
			}
		})
	}
}

func TestFileThumbnailRemove_PagesAttachments(t *testing.T) {
	var pages, deleted int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/teams/t_team1/files/episode":
			_, _ = w.Write([]byte(`{"data":{"type":"files","id":"episode","attributes":{}}}`))
		case "GET /api/v1/teams/t_team1/attachments":
			pages++
			if r.URL.Query().Get("target_type") != "file" || r.URL.Query().Get("target_id") != "episode" {
				t.Errorf("filters=%v", r.URL.Query())
			}
			if pages == 1 {
				_, _ = w.Write([]byte(`{"data":[{"type":"attachments","id":"other","attributes":{"role":"main"}}],"links":{"next":"https://example.test/attachments?page%5Bafter%5D=next"}}`))
			} else if pages == 2 && r.URL.Query().Get("page[after]") == "next" {
				_, _ = w.Write([]byte(`{"data":[{"type":"attachments","id":"thumb","attributes":{"role":"thumbnail"}}]}`))
			} else {
				t.Errorf("unexpected page %d query %v", pages, r.URL.Query())
				w.WriteHeader(500)
			}
		case "DELETE /api/v1/teams/t_team1/attachments/thumb":
			deleted++
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "media", "files", "thumbnail", "remove", "episode", "--yes")...)
	if res.Code != errs.ExitOK || pages != 2 || deleted != 1 {
		t.Errorf("exit=%d pages=%d deleted=%d stderr=%s", res.Code, pages, deleted, res.Stderr)
	}
}

func TestFileThumbnailRemove_RejectsUnusableCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch r.URL.Path {
		case "/api/v1/teams/t_team1/files/episode":
			_, _ = w.Write([]byte(`{"data":{"type":"files","id":"episode","attributes":{}}}`))
		case "/api/v1/teams/t_team1/attachments":
			_, _ = w.Write([]byte(`{"data":[],"meta":{"has_more":true}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	res, err := runResume(t, baseEnv(srv.URL), withTeam("t_team1", "media", "files", "thumbnail", "remove", "episode", "--yes")...)
	if res.Code != errs.ExitGeneric || err == nil || !strings.Contains(err.Error(), "missing or repeated cursor") {
		t.Errorf("exit=%d err=%v", res.Code, err)
	}
}
