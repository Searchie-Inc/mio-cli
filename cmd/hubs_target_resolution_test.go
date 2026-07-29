package cmd

// hubs_target_resolution_test.go — MIO-2732: the `mio hubs …` verbs must resolve
// their hub from the ambient context (--hub / config current_hub) when the
// positional id is omitted, like every other hub-scoped verb.
//
// Before this, the hub id was positional-ONLY, so `mio hubs retrieve --hub <id>`
// — the conventional invocation everywhere else in the CLI — died with Cobra's
// generic "accepts 1 arg(s), received 0". Read next to a flattened `errors`
// envelope that looks like an empty record, that message cost a reporter about an
// hour chasing a phantom data-loss bug.
//
// Coverage: the positional still wins and still works; --hub and current_hub are
// honoured; a hub resolvable from NO source produces an error naming all three
// sources rather than an arg count; and the sibling verbs that shared the defect
// (policies, redirect-origins, email-settings, navigation) behave the same.
// `hubs delete` deliberately keeps the positional requirement — pinned here too,
// including its explain-why message.
//
// Reuses the in-process harness from contract_test.go.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// hubPathRecorder serves a minimal hub resource for any request and records the
// path of the LAST one, which is the request whose hub segment the tests assert.
func hubPathRecorder(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var last string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":{"id":"hub_x","type":"hubs","attributes":{"slug":"my-hub","navigation":{}}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

// seedConfigHub writes a config.toml carrying current_hub into a throwaway
// XDG_CONFIG_HOME and returns the env pair, so a test can exercise the
// config-context path without touching the developer's real config.
func seedConfigHub(t *testing.T, hubID string) string {
	t.Helper()
	dir := t.TempDir()
	mioDir := filepath.Join(dir, "mio")
	if err := os.MkdirAll(mioDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mioDir, "config.toml"),
		[]byte("current_hub = \""+hubID+"\"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return "XDG_CONFIG_HOME=" + dir
}

// ─── hubs retrieve — the reported verb ──────────────────────────────────────────

// TestHubsRetrieve_HonorsHubFlag is the ticket's exact repro: the conventional
// --hub flag with no positional must resolve, not error on arg count.
func TestHubsRetrieve_HonorsHubFlag(t *testing.T) {
	srv, path := hubPathRecorder(t)

	res := runContract(t, append(baseEnv(srv.URL), seedConfigHub(t, "")),
		withTeam("t_team1", "--hub", "hub_from_flag", "hubs", "retrieve")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(*path, "/hubs/hub_from_flag") {
		t.Errorf("request path = %q, want it to target the --hub id", *path)
	}
}

// TestHubsRetrieve_HonorsCurrentHubFromConfig: the other half of the repro — a
// hub IS set in context via `mio config set current_hub`, and a bare
// `hubs retrieve` must use it.
func TestHubsRetrieve_HonorsCurrentHubFromConfig(t *testing.T) {
	srv, path := hubPathRecorder(t)

	env := append(baseEnv(srv.URL), seedConfigHub(t, "hub_from_config"))
	res := runContract(t, env, withTeam("t_team1", "hubs", "retrieve")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(*path, "/hubs/hub_from_config") {
		t.Errorf("request path = %q, want it to target current_hub from config", *path)
	}
}

// TestHubsRetrieve_PositionalStillWorks: the pre-existing invocation is
// untouched — this is the regression guard on the whole change.
func TestHubsRetrieve_PositionalStillWorks(t *testing.T) {
	srv, path := hubPathRecorder(t)

	res := runContract(t, append(baseEnv(srv.URL), seedConfigHub(t, "")),
		withTeam("t_team1", "hubs", "retrieve", "hub_positional")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(*path, "/hubs/hub_positional") {
		t.Errorf("request path = %q, want the positional id", *path)
	}
}

// TestHubsRetrieve_PositionalBeatsContext: precedence is positional > --hub >
// current_hub. Operators must keep being able to target any hub regardless of
// the active context — that is why these verbs take a positional at all.
func TestHubsRetrieve_PositionalBeatsContext(t *testing.T) {
	srv, path := hubPathRecorder(t)

	env := append(baseEnv(srv.URL), seedConfigHub(t, "hub_from_config"))
	res := runContract(t, env,
		withTeam("t_team1", "--hub", "hub_from_flag", "hubs", "retrieve", "hub_positional")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(*path, "/hubs/hub_positional") {
		t.Errorf("request path = %q, want the positional id to win", *path)
	}
}

// TestHubsRetrieve_NoHubAnywhereIsUsageError: with no positional, no --hub, no
// current_hub and no single-hub auto-default, the failure is a usage error — the
// same class as before, but reached deliberately rather than via Cobra's arg
// count.
func TestHubsRetrieve_NoHubAnywhereIsUsageError(t *testing.T) {
	// Two hubs, so the single-hub auto-default cannot rescue the resolution.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[
		  {"id":"hub_a","type":"hubs","attributes":{"slug":"a"}},
		  {"id":"hub_b","type":"hubs","attributes":{"slug":"b"}}
		]}`))
	}))
	t.Cleanup(srv.Close)

	env := append(baseEnv(srv.URL), seedConfigHub(t, ""))
	res := runContract(t, env, withTeam("t_team1", "hubs", "retrieve")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage (2); stderr=%q", res.Code, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("error path must produce no stdout; got %q", res.Stdout)
	}
}

// TestHubsRetrieve_NoHubAnywhereNamesTheRealCause drives the real binary so the
// JSON:API error envelope main.go writes on the os.Exit path can be read. The
// message must name every source of a hub id — and must NOT be Cobra's arg-count
// message, which is what made the original report look like data loss.
func TestHubsRetrieve_NoHubAnywhereNamesTheRealCause(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[
		  {"id":"hub_a","type":"hubs","attributes":{"slug":"a"}},
		  {"id":"hub_b","type":"hubs","attributes":{"slug":"b"}}
		]}`))
	}))
	t.Cleanup(srv.Close)

	bin := buildBinary(t)
	_, stderr, code := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	}, "--team", "t_team1", "hubs", "retrieve")

	if code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage (2); stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "accepts 1 arg(s)") {
		t.Errorf("still emitting Cobra's arg-count error; stderr=%q", stderr)
	}
	for _, want := range []string{"mio hubs retrieve <hub_id>", "--hub", "current_hub"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("error does not name %q as a source of the hub id; stderr=%q", want, stderr)
		}
	}
}

// ─── sibling verbs that shared the defect ───────────────────────────────────────

// TestHubsSiblingVerbs_HonorHubFlag sweeps every other `mio hubs` verb whose hub
// id was positional-only. Each must now accept --hub with no positional and put
// that id in the request path.
func TestHubsSiblingVerbs_HonorHubFlag(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantPath string
	}{
		{
			"update",
			[]string{"hubs", "update", "--name", "Renamed"},
			"/hubs/hub_from_flag",
		},
		{
			"policies update",
			[]string{"hubs", "policies", "update", "--policy-type", "tos", "--content", "# Terms"},
			"/hubs/hub_from_flag/policies",
		},
		{
			"policies gate",
			[]string{"hubs", "policies", "gate", "--enabled"},
			"/hubs/hub_from_flag/policies/gate",
		},
		{
			"redirect-origins get",
			[]string{"hubs", "redirect-origins", "get"},
			"/hubs/hub_from_flag/redirect-origins",
		},
		{
			"redirect-origins set",
			[]string{"hubs", "redirect-origins", "set", "--clear"},
			"/hubs/hub_from_flag/redirect-origins",
		},
		{
			"email-settings get",
			[]string{"hubs", "email-settings", "get"},
			"/hubs/hub_from_flag/email-settings",
		},
		{
			"email-settings update",
			[]string{"hubs", "email-settings", "update", "--from-name", "Support"},
			"/hubs/hub_from_flag/email-settings",
		},
		{
			"navigation list",
			[]string{"hubs", "navigation", "list"},
			"/hubs/hub_from_flag",
		},
		{
			"navigation add",
			[]string{"hubs", "navigation", "add", "header", "--type", "url", "--href", "/my-hub/x", "--label", "X"},
			"/hubs/hub_from_flag",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, path := hubPathRecorder(t)
			env := append(baseEnv(srv.URL), seedConfigHub(t, ""))
			args := append([]string{"--hub", "hub_from_flag"}, tc.args...)

			res := runContract(t, env, withTeam("t_team1", args...)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
			}
			if !strings.Contains(*path, tc.wantPath) {
				t.Errorf("request path = %q, want it to contain %q", *path, tc.wantPath)
			}
		})
	}
}

// ─── navigation bucket/hub positional disambiguation ────────────────────────────

// TestHubsNavigation_BucketOnlyPositionalUsesAmbientHub: `navigation add header`
// (bucket only) must mean "the header bucket of the ambient hub". A bucket name
// could never have addressed a hub — positional hub ids are passed through
// verbatim, never resolved from a name or slug — so no invocation changes meaning.
func TestHubsNavigation_BucketOnlyPositionalUsesAmbientHub(t *testing.T) {
	srv, path := hubPathRecorder(t)

	env := append(baseEnv(srv.URL), seedConfigHub(t, "hub_from_config"))
	res := runContract(t, env, withTeam("t_team1",
		"hubs", "navigation", "add", "header", "--type", "url", "--href", "/my-hub/x", "--label", "X")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(*path, "/hubs/hub_from_config") {
		t.Errorf("request path = %q, want the ambient hub", *path)
	}
}

// TestHubsNavigation_HubAndBucketPositionalsUnchanged: the two-positional form
// keeps meaning <hub_id> <bucket>.
func TestHubsNavigation_HubAndBucketPositionalsUnchanged(t *testing.T) {
	srv, path := hubPathRecorder(t)

	env := append(baseEnv(srv.URL), seedConfigHub(t, "hub_from_config"))
	res := runContract(t, env, withTeam("t_team1",
		"hubs", "navigation", "add", "hub_positional", "header",
		"--type", "url", "--href", "/my-hub/x", "--label", "X")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(*path, "/hubs/hub_positional") {
		t.Errorf("request path = %q, want the positional hub id", *path)
	}
}

// TestHubsNavigation_MissingBucketIsStillAUsageError: a lone non-bucket
// positional is read as the hub id, leaving no bucket — which add/remove/reorder
// must still reject, before any HTTP request.
func TestHubsNavigation_MissingBucketIsStillAUsageError(t *testing.T) {
	var fired atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired.Store(true)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(srv.Close)

	env := append(baseEnv(srv.URL), seedConfigHub(t, ""))
	res := runContract(t, env, withTeam("t_team1",
		"hubs", "navigation", "add", "hub_abc123", "--type", "url", "--href", "/x", "--label", "X")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage (2); stderr=%q", res.Code, res.Stderr)
	}
	if fired.Load() {
		t.Error("a bucket usage error must fire no HTTP request")
	}
}

// ─── hubs delete keeps the explicit positional ─────────────────────────────────

// TestHubsDelete_StillRequiresPositional: deleting a whole hub is irreversible,
// so it deliberately does NOT inherit the ambient-hub fallback. A bare invocation
// must fail as a usage error and fire no request even though --hub is set.
func TestHubsDelete_StillRequiresPositional(t *testing.T) {
	var fired atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired.Store(true)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(srv.Close)

	env := append(baseEnv(srv.URL), seedConfigHub(t, "hub_from_config"))
	res := runContract(t, env, withTeam("t_team1", "--hub", "hub_from_flag", "--yes", "hubs", "delete")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage (2); stderr=%q", res.Code, res.Stderr)
	}
	if fired.Load() {
		t.Error("hubs delete must fire no HTTP request when the hub id is missing")
	}
}

// TestHubsDelete_ExplainsWhyItRequiresTheID: refusing is only useful if it says
// why this verb is the exception. Subprocess, because the message lives in the
// error envelope main.go writes past os.Exit.
func TestHubsDelete_ExplainsWhyItRequiresTheID(t *testing.T) {
	bin := buildBinary(t)
	_, stderr, code := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=http://127.0.0.1:1",
	}, "--team", "t_team1", "--hub", "hub_from_flag", "--yes", "hubs", "delete")

	if code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage (2); stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "accepts 1 arg(s)") {
		t.Errorf("still emitting Cobra's arg-count error; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "irreversible") {
		t.Errorf("delete refusal does not explain why the id is required; stderr=%q", stderr)
	}
}

// TestHubsDelete_PositionalStillWorks: the supported invocation is untouched.
func TestHubsDelete_PositionalStillWorks(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	env := append(baseEnv(srv.URL), seedConfigHub(t, ""))
	res := runContract(t, env, withTeam("t_team1", "--yes", "hubs", "delete", "hub_positional")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodDelete || !strings.HasSuffix(gotPath, "/hubs/hub_positional") {
		t.Errorf("got %s %s, want DELETE …/hubs/hub_positional", gotMethod, gotPath)
	}
}
