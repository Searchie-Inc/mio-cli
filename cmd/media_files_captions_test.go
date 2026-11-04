package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestFileCaptionsAndChapters_Wire(t *testing.T) {
	vtt := "WEBVTT\n\n00:00.000 --> 00:01.000\nHello\n"
	file := filepath.Join(t.TempDir(), "captions.vtt")
	if err := os.WriteFile(file, []byte(vtt), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name              string
		args              []string
		method, path, typ string
		attrs             map[string]any
		status            int
		response          string
		code              int
	}{
		{"set", []string{"captions", "set", "f", "--language", "en", "--vtt", "@" + file}, "PUT", "captions", "file_captions", map[string]any{"language": "en", "vtt": vtt}, 200, `{"data":{"type":"file_captions","id":"f","attributes":{}}}`, errs.ExitOK},
		{"get_null", []string{"captions", "get", "f"}, "GET", "captions", "", nil, 200, `{"data":null}`, errs.ExitOK},
		{"get_present", []string{"captions", "get", "f"}, "GET", "captions", "", nil, 200, `{"data":{"type":"file_captions","id":"f","attributes":{"language":"en","source":"imported","url":"https://x.test/c.vtt"}}}`, errs.ExitOK},
		{"remove", []string{"captions", "remove", "f", "--yes"}, "DELETE", "captions", "", nil, 204, "", errs.ExitOK},
		{"import", []string{"chapters", "import", "f", "--vtt", "@" + file, "--yes"}, "POST", "chapters/import", "file_chapters", map[string]any{"vtt": vtt}, 200, `{"data":[]}`, errs.ExitOK},
		{"auto", []string{"chapters", "auto", "f", "--yes"}, "POST", "chapters/auto", "", nil, 200, `{"data":[]}`, errs.ExitOK},
		{"off", []string{"chapters", "auto", "f", "--off", "--yes"}, "DELETE", "chapters/auto", "", nil, 200, `{"data":[]}`, errs.ExitOK},
		{"rejected", []string{"captions", "set", "f", "--language", "bad", "--vtt", "bad"}, "PUT", "captions", "file_captions", map[string]any{"language": "bad", "vtt": "bad"}, 422, `{"errors":[{"status":"422","detail":"bad caption format"}]}`, errs.ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				body, _ := io.ReadAll(r.Body)
				if tc.typ != "" {
					typ, attrs := decodeDataTypeAttrs(t, body)
					if typ != tc.typ || !reflect.DeepEqual(attrs, tc.attrs) {
						t.Errorf("type=%s attrs=%v", typ, attrs)
					}
					if r.Header.Get("Content-Type") != "application/vnd.api+json" {
						t.Errorf("content type=%s", r.Header.Get("Content-Type"))
					}
				} else if len(body) != 0 {
					t.Errorf("unexpected body %s", body)
				}
				w.Header().Set("Content-Type", "application/vnd.api+json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()
			args := append([]string{"media", "files"}, tc.args...)
			res, runErr := runResume(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != tc.code {
				t.Errorf("exit=%d stderr=%s", res.Code, res.Stderr)
			}
			want := []string{tc.method + " /api/v1/teams/t_team1/files/f/" + tc.path}
			if !reflect.DeepEqual(requests, want) {
				t.Errorf("requests=%v want=%v", requests, want)
			}
			if tc.name == "get_null" && (res.Stdout != "" || !strings.Contains(res.Stderr, "file f has no caption track")) {
				t.Errorf("stdout=%q stderr=%q", res.Stdout, res.Stderr)
			}
			if tc.name == "rejected" && (runErr == nil || !strings.Contains(runErr.Error(), "bad caption format")) {
				t.Errorf("the API's 422 detail must surface verbatim; err=%v", runErr)
			}
			if tc.name == "get_present" && !strings.Contains(res.Stdout, "imported") {
				t.Errorf("a present caption must be rendered; stdout=%q stderr=%q", res.Stdout, res.Stderr)
			}
		})
	}
}

func TestFileCaptionsAndChapters_Guards(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"remove confirmation", []string{"captions", "remove", "f"}, errs.ExitNeedsConfir},
		{"import confirmation", []string{"chapters", "import", "f", "--vtt", "text"}, errs.ExitNeedsConfir},
		{"auto confirmation", []string{"chapters", "auto", "f"}, errs.ExitNeedsConfir},
		{"auto off confirmation", []string{"chapters", "auto", "f", "--off"}, errs.ExitNeedsConfir},
		{"missing language", []string{"captions", "set", "f", "--vtt", "text"}, errs.ExitUsage},
		{"blank language", []string{"captions", "set", "f", "--language", " ", "--vtt", "text"}, errs.ExitUsage},
		{"missing vtt", []string{"captions", "set", "f", "--language", "en"}, errs.ExitUsage},
		{"blank captions vtt", []string{"captions", "set", "f", "--language", "en", "--vtt", " "}, errs.ExitUsage},
		{"missing import vtt", []string{"chapters", "import", "f", "--yes"}, errs.ExitUsage},
		{"blank vtt", []string{"chapters", "import", "f", "--vtt", "  ", "--yes"}, errs.ExitUsage},
		{"missing file", []string{"chapters", "import", "f", "--vtt", "@/nonexistent/mio-vtt", "--yes"}, errs.ExitUsage},
		{"blank id get", []string{"captions", "get", " "}, errs.ExitUsage},
		{"blank id set", []string{"captions", "set", " ", "--language", "en", "--vtt", "text"}, errs.ExitUsage},
		{"blank id remove", []string{"captions", "remove", " ", "--yes"}, errs.ExitUsage},
		{"blank id import", []string{"chapters", "import", " ", "--vtt", "text", "--yes"}, errs.ExitUsage},
		{"blank id auto", []string{"chapters", "auto", " ", "--off"}, errs.ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := firedGuardServer(t)
			args := append([]string{"media", "files"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != tc.code || *fired {
				t.Errorf("exit=%d request=%v stderr=%s", res.Code, *fired, res.Stderr)
			}
		})
	}
}
