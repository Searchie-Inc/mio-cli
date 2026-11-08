package cmd

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

func TestHubsRetrieve_NoHubAnywhereIsUsageError(t *testing.T) {
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

func TestHubsVerbs_BlankPositionalNeverFallsBackToAmbientHub(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"retrieve empty", []string{"hubs", "retrieve", ""}},
		{"retrieve whitespace", []string{"hubs", "retrieve", "   "}},
		{"update empty", []string{"hubs", "update", "", "--name", "Renamed"}},
		{"policies gate empty", []string{"hubs", "policies", "gate", "", "--enabled"}},
		{"email-settings update empty", []string{"hubs", "email-settings", "update", "", "--from-name", "X"}},
		{"redirect-origins set empty", []string{"hubs", "redirect-origins", "set", "", "--clear"}},
		{"navigation add empty hub", []string{"hubs", "navigation", "add", "", "header",
			"--type", "url", "--href", "/my-hub/x", "--label", "X"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fired atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fired.Store(true)
				w.Header().Set("Content-Type", "application/vnd.api+json")
				_, _ = w.Write([]byte(`{"data":{"id":"hub_x","type":"hubs","attributes":{"slug":"s","navigation":{}}}}`))
			}))
			t.Cleanup(srv.Close)

			env := append(baseEnv(srv.URL), seedConfigHub(t, "hub_from_config"))
			args := append([]string{"--hub", "hub_from_flag"}, tc.args...)

			res := runContract(t, env, withTeam("t_team1", args...)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit=%d want ExitUsage (2) — a supplied-but-blank hub id must not resolve; stderr=%q",
					res.Code, res.Stderr)
			}
			if fired.Load() {
				t.Error("a blank positional hub id must fire NO HTTP request — it silently retargeted the ambient hub")
			}
		})
	}
}

func TestHubsDelete_BlankPositionalIsRejected(t *testing.T) {
	for _, arg := range []string{"", "   "} {
		t.Run("arg="+arg, func(t *testing.T) {
			var fired atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fired.Store(true)
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(srv.Close)

			env := append(baseEnv(srv.URL), seedConfigHub(t, ""))
			res := runContract(t, env, withTeam("t_team1", "--yes", "hubs", "delete", arg)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit=%d want ExitUsage (2); stderr=%q", res.Code, res.Stderr)
			}
			if fired.Load() {
				t.Error("a blank hub id on `hubs delete` must fire NO HTTP request — it targeted the hubs COLLECTION")
			}
		})
	}
}

func TestHubsVerbs_BlankPositionalFiresNoRequestWithoutTeam(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"retrieve", []string{"hubs", "retrieve", ""}},
		{"update", []string{"hubs", "update", "", "--name", "X"}},
		{"delete", []string{"hubs", "delete", "", "--yes"}},
		{"navigation add", []string{"hubs", "navigation", "add", "", "header",
			"--type", "url", "--href", "/h/x", "--label", "X"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fired atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fired.Store(true)
				w.Header().Set("Content-Type", "application/vnd.api+json")
				_, _ = w.Write([]byte(`{"data":[{"id":"team_only","type":"teams","attributes":{"name":"S","slug":"s"}}]}`))
			}))
			t.Cleanup(srv.Close)

			env := append(baseEnv(srv.URL), seedConfigHub(t, ""))
			res := runContract(t, env, tc.args...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit=%d want ExitUsage (2); stderr=%q", res.Code, res.Stderr)
			}
			if fired.Load() {
				t.Error("usage error fired an HTTP request (team resolution) before rejecting the blank hub id")
			}
		})
	}
}

func TestHubsPoliciesUpdate_BadFlagsFireNoRequest(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"bad policy-type", []string{"hubs", "policies", "update", "hub_x",
			"--policy-type", "bogus", "--content", "x"}},
		{"content and reset together", []string{"hubs", "policies", "update", "hub_x",
			"--policy-type", "tos", "--content", "x", "--reset-content"}},
		{"neither content nor reset", []string{"hubs", "policies", "update", "hub_x",
			"--policy-type", "tos"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fired atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fired.Store(true)
				w.Header().Set("Content-Type", "application/vnd.api+json")
				_, _ = w.Write([]byte(`{"data":[{"id":"team_only","type":"teams","attributes":{"name":"S","slug":"s"}}]}`))
			}))
			t.Cleanup(srv.Close)

			env := append(baseEnv(srv.URL), seedConfigHub(t, ""))
			res := runContract(t, env, tc.args...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit=%d want ExitUsage (2); stderr=%q", res.Code, res.Stderr)
			}
			if fired.Load() {
				t.Error("a flag usage error must fire NO HTTP request; context was resolved first")
			}
		})
	}
}

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

var navBucketIDPrefixes = []string{"hub_", "h_"}

func looksLikeHubID(s string) bool {
	for _, p := range navBucketIDPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

func TestNavBuckets_AreNeverIDShaped(t *testing.T) {
	for _, b := range navBuckets {
		if looksLikeHubID(b) {
			t.Errorf("navigation bucket %q is hub-id-shaped — splitNavArgs can no longer tell a "+
				"lone positional bucket from a hub id; rename the bucket or require the hub id "+
				"explicitly on the navigation verbs", b)
		}
		if strings.TrimSpace(b) == "" {
			t.Errorf("navigation bucket %q is blank — it would collide with the omitted-positional case", b)
		}
	}
}

func TestSplitNavArgs_Table(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantHub    string
		wantGiven  bool
		wantBucket string
	}{
		{"nothing", nil, "", false, ""},
		{"bucket only", []string{"header"}, "", false, "header"},
		{"mobile bucket only", []string{"mobile"}, "", false, "mobile"},
		{"hub only", []string{"hub_abc123"}, "hub_abc123", true, ""},
		{"hub and bucket", []string{"hub_abc123", "footer"}, "hub_abc123", true, "footer"},
		{"blank hub", []string{""}, "", true, ""},
		{"blank hub with bucket", []string{"", "header"}, "", true, "header"},
		{"unknown bucket", []string{"hub_abc123", "sidebar"}, "hub_abc123", true, "sidebar"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub, given, bucket := splitNavArgs(tc.args)
			if hub != tc.wantHub || given != tc.wantGiven || bucket != tc.wantBucket {
				t.Errorf("splitNavArgs(%q) = (%q, %v, %q), want (%q, %v, %q)",
					tc.args, hub, given, bucket, tc.wantHub, tc.wantGiven, tc.wantBucket)
			}
		})
	}
}

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
