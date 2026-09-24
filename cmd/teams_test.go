package cmd

// teams_test.go — MIO-3830: `teams create` / `teams update` send what the API's
// write schemas accept.
//
// mio-backend app/teams/schemas.py (origin/main):
//
//	class TeamCreate(_JsonApiInbound):   extra="forbid"; name: str; slug: str
//	class TeamUpdate(_JsonApiInbound):   extra="forbid"; name: str | None
//
// Up to v0.23.0 `teams create` had no --slug, so it never sent the required
// `slug` and answered 422 ("Field required (/slug)") whatever was passed. The
// --subdomain flag both commands had sent `subdomain`, a field neither schema
// has ("Extra inputs are not permitted (/subdomain)"), so `teams update
// --subdomain` answered 422 too. The oracle here is the WIRE: each test
// captures the request the real command tree sends and compares the whole
// body, so a stale or extra attribute fails by name.

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

// newTeamsRecordingServer records every request and answers each with a teams
// resource (201 for POST, 200 otherwise).
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

// runTeamsCLI drives the real command tree in-process and returns both streams
// and the error Execute returned (so the message can be asserted, not only the
// exit code).
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

// assertOneTeamsWrite asserts exactly one request was sent, with the given
// method and path, and that its body equals wantBody (deep JSON equality).
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

// TestTeamsCreate_SendsSlug pins the wire body for `teams create`: the TeamCreate
// fields name + slug and nothing else (the schema is extra="forbid").
func TestTeamsCreate_SendsSlug(t *testing.T) {
	srv, reqs := newTeamsRecordingServer(t)

	_, stderr, err := runTeamsCLI(t, baseEnv(srv.URL),
		"teams", "create", "--name", "Acme Corp", "--slug", "acme")
	if code := codeForExecuteErr(err); code != errs.ExitOK {
		t.Fatalf("exit code = %d, want 0; err=%v stderr=%q", code, err, stderr)
	}
	assertOneTeamsWrite(t, reqs(), http.MethodPost, "/api/v1/teams", teamsCreateAcmeBody)
}

// TestTeamsCreate_SubdomainIsDeprecatedAliasForSlug: the pre-fix spelling keeps
// working, sends `slug` (never `subdomain`), and says it is deprecated on
// STDERR only, so a JSON stdout stays parseable.
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

// TestTeamsCreate_SlugAndSubdomainDiffer_UsageErrorNoRequest: the alias and the
// canonical flag naming DIFFERENT slugs is ambiguous, so it is a usage error
// before any credential lookup or request — never a silent last-one-wins.
func TestTeamsCreate_SlugAndSubdomainDiffer_UsageErrorNoRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(string) []string
		args []string
	}{
		{"slug first", baseEnv, []string{"--slug", "acme", "--subdomain", "other"}},
		{"subdomain first", baseEnv, []string{"--subdomain", "other", "--slug", "acme"}},
		// No credentials at all: still exit 2, not 3, so the check runs before auth.
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

// TestTeamsCreate_RequiresNameAndSlug: TeamCreate requires both, so omitting
// either is refused by cobra before any request (exit 2, even with no key).
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

// TestTeamsCreate_HelpShowsSlugHidesSubdomain: --slug is the documented flag;
// the deprecated alias works but is not advertised.
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

// TestTeamsUpdate_SendsOnlyName pins the wire body for `teams update`:
// TeamUpdate has exactly one field, name.
func TestTeamsUpdate_SendsOnlyName(t *testing.T) {
	srv, reqs := newTeamsRecordingServer(t)

	_, stderr, err := runTeamsCLI(t, baseEnv(srv.URL), "teams", "update", "t_1", "--name", "New Name")
	if code := codeForExecuteErr(err); code != errs.ExitOK {
		t.Fatalf("exit code = %d, want 0; err=%v stderr=%q", code, err, stderr)
	}
	assertOneTeamsWrite(t, reqs(), http.MethodPatch, "/api/v1/teams/t_1",
		`{"data":{"type":"teams","attributes":{"name":"New Name"}}}`)
}

// TestTeamsUpdate_HasNoSlugFlag: TeamUpdate takes no slug (a team's slug is set
// at creation), so neither spelling exists on update. Before MIO-3830,
// `--subdomain` was accepted here and always answered 422; now it is an unknown
// flag, still exit 2, and no request is sent.
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
