package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const teamResourceBody = `{"data":{"id":"t_new","type":"teams","attributes":{"name":"Acme Corp","slug":"acme","owner_id":"u_1"}}}`

type teamsWireRequest struct {
	Method string
	Path   string
	Body   []byte
}

func newTeamsRecordingServer(t *testing.T) (*httptest.Server, func() []teamsWireRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []teamsWireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, teamsWireRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write([]byte(teamResourceBody))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []teamsWireRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]teamsWireRequest(nil), got...)
	}
}

func runTeamsCLI(t *testing.T, env []string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	restore := overlayEnv(t, env)
	defer restore()
	resetGlobalFlags()
	root := RootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	defer root.SetArgs(nil)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func assertOneTeamsWrite(t *testing.T, reqs []teamsWireRequest, method, path, wantBody string) {
	t.Helper()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d request(s), want exactly 1: %+v", len(reqs), reqs)
	}
	if reqs[0].Method != method || reqs[0].Path != path {
		t.Errorf("request = %s %s, want %s %s", reqs[0].Method, reqs[0].Path, method, path)
	}
	assertExactBody(t, reqs[0].Body, wantBody)
}

const teamsCreateAcmeBody = `{"data":{"type":"teams","attributes":{"name":"Acme Corp","slug":"acme"}}}`

func TestTeamsCreate_SendsSlug(t *testing.T) {
	srv, reqs := newTeamsRecordingServer(t)

	_, stderr, err := runTeamsCLI(t, baseEnv(srv.URL),
		"teams", "create", "--name", "Acme Corp", "--slug", "acme")
	if code := codeForExecuteErr(err); code != errs.ExitOK {
		t.Fatalf("exit code = %d, want 0; err=%v stderr=%q", code, err, stderr)
	}
	assertOneTeamsWrite(t, reqs(), http.MethodPost, "/api/v1/teams", teamsCreateAcmeBody)
}

func TestTeamsCreate_SubdomainIsDeprecatedAliasForSlug(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"subdomain alone", []string{"--subdomain", "acme"}},
		{"subdomain equal to slug", []string{"--slug", "acme", "--subdomain", "acme"}},
		{"subdomain before slug, equal", []string{"--subdomain=acme", "--slug=acme"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, reqs := newTeamsRecordingServer(t)

			args := append([]string{"--output", "json", "teams", "create", "--name", "Acme Corp"}, tc.args...)
			stdout, stderr, err := runTeamsCLI(t, baseEnv(srv.URL), args...)
			if code := codeForExecuteErr(err); code != errs.ExitOK {
				t.Fatalf("exit code = %d, want 0; err=%v stderr=%q", code, err, stderr)
			}
			assertOneTeamsWrite(t, reqs(), http.MethodPost, "/api/v1/teams", teamsCreateAcmeBody)

			if !strings.Contains(stderr, "--subdomain is deprecated") || !strings.Contains(stderr, "--slug") {
				t.Errorf("stderr does not carry the --subdomain deprecation notice naming --slug: %q", stderr)
			}
			var obj map[string]any
			if jerr := json.Unmarshal([]byte(stdout), &obj); jerr != nil {
				t.Errorf("stdout is not a pure JSON object (the deprecation notice must go to stderr): %v\nstdout=%q", jerr, stdout)
			}
		})
	}
}

func TestTeamsCreate_SlugAndSubdomainDiffer_UsageErrorNoRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(string) []string
		args []string
	}{
		{"slug first", baseEnv, []string{"--slug", "acme", "--subdomain", "other"}},
		{"subdomain first", baseEnv, []string{"--subdomain", "other", "--slug", "acme"}},
		{"no credentials", func(u string) []string {
			return []string{"MIO_API_KEY=", "MIO_API_BASE_URL=" + u}
		}, []string{"--slug", "acme", "--subdomain", "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, reqs := newTeamsRecordingServer(t)

			args := append([]string{"teams", "create", "--name", "Acme Corp"}, tc.args...)
			_, _, err := runTeamsCLI(t, tc.env(srv.URL), args...)
			if code := codeForExecuteErr(err); code != errs.ExitUsage {
				t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", code, errs.ExitUsage, err)
			}
			for _, want := range []string{"--slug", "--subdomain", `"acme"`, `"other"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %s", err.Error(), want)
				}
			}
			if got := reqs(); len(got) != 0 {
				t.Errorf("server saw %d request(s), want 0: %+v", len(got), got)
			}
		})
	}
}

func TestTeamsCreate_RequiresNameAndSlug(t *testing.T) {
	noCreds := func(u string) []string { return []string{"MIO_API_KEY=", "MIO_API_BASE_URL=" + u} }
	for _, tc := range []struct {
		name     string
		env      func(string) []string
		args     []string
		wantFlag string
	}{
		{"missing slug", baseEnv, []string{"--name", "Acme Corp"}, `"slug"`},
		{"missing name", baseEnv, []string{"--slug", "acme"}, `"name"`},
		{"missing name, subdomain alias", baseEnv, []string{"--subdomain", "acme"}, `"name"`},
		{"missing slug, no credentials", noCreds, []string{"--name", "Acme Corp"}, `"slug"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, reqs := newTeamsRecordingServer(t)

			args := append([]string{"teams", "create"}, tc.args...)
			_, _, err := runTeamsCLI(t, tc.env(srv.URL), args...)
			if code := codeForExecuteErr(err); code != errs.ExitUsage {
				t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", code, errs.ExitUsage, err)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantFlag) {
				t.Errorf("error %v does not name the missing flag %s", err, tc.wantFlag)
			}
			if got := reqs(); len(got) != 0 {
				t.Errorf("server saw %d request(s), want 0: %+v", len(got), got)
			}
		})
	}
}

func TestTeamsCreate_HelpShowsSlugHidesSubdomain(t *testing.T) {
	stdout, stderr, err := runTeamsCLI(t, nil, "teams", "create", "--help")
	if err != nil {
		t.Fatalf("help failed: %v", err)
	}
	help := stdout + stderr
	if !strings.Contains(help, "--slug") {
		t.Errorf("teams create --help does not document --slug:\n%s", help)
	}
	if strings.Contains(help, "--subdomain") {
		t.Errorf("teams create --help advertises the deprecated --subdomain:\n%s", help)
	}
}

func TestTeamsUpdate_SendsOnlyName(t *testing.T) {
	srv, reqs := newTeamsRecordingServer(t)

	_, stderr, err := runTeamsCLI(t, baseEnv(srv.URL), "teams", "update", "t_1", "--name", "New Name")
	if code := codeForExecuteErr(err); code != errs.ExitOK {
		t.Fatalf("exit code = %d, want 0; err=%v stderr=%q", code, err, stderr)
	}
	assertOneTeamsWrite(t, reqs(), http.MethodPatch, "/api/v1/teams/t_1",
		`{"data":{"type":"teams","attributes":{"name":"New Name"}}}`)
}

func TestTeamsUpdate_HasNoSlugFlag(t *testing.T) {
	for _, flag := range []string{"--subdomain", "--slug"} {
		t.Run(flag, func(t *testing.T) {
			srv, reqs := newTeamsRecordingServer(t)

			_, _, err := runTeamsCLI(t, baseEnv(srv.URL), "teams", "update", "t_1", "--name", "N", flag, "x")
			if code := codeForExecuteErr(err); code != errs.ExitUsage {
				t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", code, errs.ExitUsage, err)
			}
			if err == nil || !strings.Contains(err.Error(), "unknown flag: "+flag) {
				t.Errorf("error %v is not cobra's unknown-flag error for %s", err, flag)
			}
			if got := reqs(); len(got) != 0 {
				t.Errorf("server saw %d request(s), want 0: %+v", len(got), got)
			}
		})
	}
}
