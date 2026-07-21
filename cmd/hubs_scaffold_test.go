package cmd

// hubs_scaffold_test.go — orchestrator-skeleton contract tests for
// `mio hubs scaffold` (MIO-2543 Task 11).
//
// These pin the command shell, NOT the per-step bodies (those are filled in by
// Phase 4). They assert three things the skeleton must guarantee:
//   - --dry-run emits the ordered plan naming every pipeline step and fires no
//     mutating HTTP;
//   - resume mode (--hub <id>) GETs the hub so scaffoldContext.hubSlug is
//     populated (ResolveHub short-circuits id-shaped values with no lookup);
//   - an unknown --template is a usage error BEFORE any HTTP.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// scaffoldStepNames is the ordered pipeline the dry-run plan must name, in order.
var scaffoldStepNames = []string{
	"hub", "blobs", "spaces", "onboarding", "policies",
	"playlists", "homepage", "publish", "backend-gated",
}

// mutationGuardServer starts a test server that flips *mutated to true on ANY
// non-GET (mutating) request, so a dry-run can assert it created/changed
// nothing. GETs (context resolution) are allowed and answered with a minimal
// hub body.
func mutationGuardServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	mutated := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutated = true
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_x","type":"hubs","attributes":{"slug":"x","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &mutated
}

// TestScaffold_DryRunEmitsPlanNoHTTP: `hubs scaffold --dry-run` prints the
// ordered plan naming every step and fires no mutating HTTP.
func TestScaffold_DryRunEmitsPlanNoHTTP(t *testing.T) {
	srv, mutated := mutationGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x", "--dry-run")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("dry-run exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *mutated {
		t.Errorf("dry-run must fire NO mutating (non-GET) request")
	}

	// The plan must name every step, in the pipeline order.
	prev := -1
	for _, name := range scaffoldStepNames {
		idx := strings.Index(res.Stdout, name)
		if idx < 0 {
			t.Errorf("dry-run plan is missing step %q; stdout:\n%s", name, res.Stdout)
			continue
		}
		if idx <= prev {
			t.Errorf("dry-run plan step %q is out of order (idx %d <= prev %d); stdout:\n%s",
				name, idx, prev, res.Stdout)
		}
		prev = idx
	}
}

// TestScaffold_ResumeGetsHubForSlug: resume mode (--hub) GETs the hub and
// populates scaffoldContext.hubSlug from the response.
func TestScaffold_ResumeGetsHubForSlug(t *testing.T) {
	gotGet := false
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gotGet = true
			gotPath = r.URL.Path
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"hub_1","type":"hubs","attributes":{"slug":"acme","is_private":true}}}`))
	}))
	t.Cleanup(srv.Close)

	// Observe the resolved context via the test seam.
	var gotSlug string
	scaffoldAfterResolve = func(sc *scaffoldContext) { gotSlug = sc.hubSlug }
	defer func() { scaffoldAfterResolve = nil }()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold", "--hub", "hub_1", "--template", "community")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("resume exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !gotGet {
		t.Fatalf("resume mode must GET the hub to populate hubSlug")
	}
	if !strings.HasSuffix(gotPath, "/teams/t_team1/hubs/hub_1") {
		t.Errorf("resume GET path = %q, want .../teams/t_team1/hubs/hub_1", gotPath)
	}
	if gotSlug != "acme" {
		t.Errorf("scaffoldContext.hubSlug = %q, want %q (populated from the hub GET)", gotSlug, "acme")
	}
}

// TestScaffold_CreateModeIgnoresConfiguredDefaultHub: a configured default hub
// (`mio config set hub`) must NOT turn a create-mode invocation into a resume.
// Resume keys on the EXPLICIT --hub flag only — gating on the merged
// resolved.HubID would silently resume onto a user's default hub, so no hub GET
// may fire for a create-mode (--name/--slug, no --hub) invocation here.
func TestScaffold_CreateModeIgnoresConfiguredDefaultHub(t *testing.T) {
	srv, fired := firedGuardServer(t)

	// Sandbox a config that sets a default hub (current_hub) — this feeds
	// resolved.HubID but NOT flags.hub.
	cfgHome := t.TempDir()
	mioDir := filepath.Join(cfgHome, "mio")
	if err := os.MkdirAll(mioDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mioDir, "config.toml"),
		[]byte("current_hub = \"hub_configured\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env := append(baseEnv(srv.URL), "XDG_CONFIG_HOME="+cfgHome)
	res := runContract(t, env,
		withTeam("t_team1", "hubs", "scaffold",
			"--template", "community", "--name", "X", "--slug", "x")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("create-mode exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *fired {
		t.Errorf("a configured default hub must NOT trigger resume in create mode (no hub GET should fire)")
	}
}

// TestScaffold_UnknownTemplate: an unknown --template is ExitUsage before any HTTP.
func TestScaffold_UnknownTemplate(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "scaffold", "--template", "nope")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("unknown-template exit code = %d, want %d (ExitUsage); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Errorf("unknown template must fail BEFORE any HTTP request")
	}
}
