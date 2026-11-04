package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestRemoteConnectionsWire(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		args                          []string
		method, path, query, response string
		body                          map[string]any
		note                          string
	}{
		{"list", []string{"list"}, "GET", "", "", `{"data":[]}`, nil, ""},
		{"connect", []string{"connect", "--url", "https://example.com/feed"}, "POST", "", "", `{"data":{"type":"remote_connections","id":"c","attributes":{}}}`, map[string]any{"provider": "podcast", "url": "https://example.com/feed"}, ""},
		{"connect options", []string{"connect", "--url", "https://example.com/feed", "--no-auto-import", "--target-playlist-id", "p1", "--target-playlist-id", "p2"}, "POST", "", "", `{"data":{"type":"remote_connections","id":"c","attributes":{}}}`, map[string]any{"provider": "podcast", "url": "https://example.com/feed", "auto_import": false, "target_playlist_ids": []any{"p1", "p2"}}, ""},
		{"disconnect", []string{"disconnect", "c", "--yes"}, "DELETE", "/c", "", "", nil, ""},
		{"browse", []string{"browse", "c", "--limit", "2", "--after", "old"}, "GET", "/c/browse", "page%5Bafter%5D=old&page%5Bsize%5D=2", `{"data":[{"type":"remote_recordings","id":"e","attributes":{"title":"Episode"}}],"links":{"next":"https://example.com/?page%5Bafter%5D=next"}}`, nil, "--after next"},
		{"browse default", []string{"browse", "c"}, "GET", "/c/browse", "", `{"data":[]}`, nil, ""},
		{"import", []string{"import", "c", "--external-id", "e1", "--external-ids", "e2,e3"}, "POST", "/c/import", "", `{"data":[{"type":"remote_imports","id":"e1","attributes":{"status":"rejected","error":"not_found"}},{"type":"remote_imports","id":"e2","attributes":{"status":"imported"}}]}`, map[string]any{"data": map[string]any{"type": "remote_imports", "attributes": map[string]any{"external_ids": []any{"e1", "e2", "e3"}}}}, "note: 1 of 2 episodes were rejected (see .attributes.error)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n++
				if r.Method != tc.method || r.URL.Path != "/api/v1/teams/t_team1/remote-connections"+tc.path || r.URL.RawQuery != tc.query {
					t.Errorf("request=%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
				}
				if tc.body != nil {
					var got map[string]any
					b, _ := io.ReadAll(r.Body)
					if err := json.Unmarshal(b, &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, tc.body) {
						t.Errorf("body=%v want %v", got, tc.body)
					}
				}
				if tc.method == "DELETE" {
					w.WriteHeader(204)
					return
				}
				w.Header().Set("Content-Type", "application/vnd.api+json")
				fmt.Fprint(w, tc.response)
			}))
			defer srv.Close()
			args := append([]string{"media", "remote-connections"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != 0 || n != 1 {
				t.Errorf("exit=%d requests=%d stderr=%s", res.Code, n, res.Stderr)
			}
			if tc.note != "" && !strings.Contains(res.Stderr, tc.note) {
				t.Errorf("note missing: %q", res.Stderr)
			}
			if tc.method == "GET" || tc.name == "import" {
				var arr []any
				if err := json.Unmarshal([]byte(res.Stdout), &arr); err != nil {
					t.Errorf("stdout not bare array: %s", res.Stdout)
				}
			}
		})
	}
}

func TestRemoteConnectionsGuards(t *testing.T) {
	playlists := make([]string, 0, 22)
	for i := 0; i < 11; i++ {
		playlists = append(playlists, "--target-playlist-id", fmt.Sprint(i))
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprint(i)
	}
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"missing url", []string{"connect"}, 2}, {"blank url", []string{"connect", "--url", "  "}, 2},
		{"confirmation", []string{"disconnect", "c"}, 5}, {"blank disconnect", []string{"disconnect", " ", "--yes"}, 2}, {"blank browse", []string{"browse", " "}, 2}, {"blank import", []string{"import", " ", "--external-id", "e"}, 2},
		{"no ids", []string{"import", "c"}, 2}, {"duplicate", []string{"import", "c", "--external-id", "a", "--external-ids", "a,b"}, 2}, {"blank", []string{"import", "c", "--external-ids", "a,,b"}, 2}, {"too many", []string{"import", "c", "--external-ids", strings.Join(many, ",")}, 2},
		{"playlist limit", append([]string{"connect", "--url", "x"}, playlists...), 2},
		{"playlist duplicate", []string{"connect", "--url", "x", "--target-playlist-id", "p", "--target-playlist-id", "p"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := firedGuardServer(t)
			args := append([]string{"media", "remote-connections"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != tc.code || *fired {
				t.Errorf("exit=%d requests=%v want %d", res.Code, *fired, tc.code)
			}
		})
	}
}

func TestRemoteConnectForbiddenHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"errors":[{"status":"403","detail":"requires user (JWT)"}]}`)
	}))
	defer srv.Close()
	res, err := runResume(t, baseEnv(srv.URL), withTeam("t_team1", "media", "remote-connections", "connect", "--url", "https://example.com/feed")...)
	if res.Code != errs.ExitAuth || err == nil || !strings.Contains(err.Error(), "POST /api/v1/auth/login") || !strings.Contains(err.Error(), "--api-key") {
		t.Errorf("exit=%d err=%v", res.Code, err)
	}
}
