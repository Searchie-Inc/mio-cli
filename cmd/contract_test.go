// Package cmd — contract_test.go
//
// Golden contract suite for the mio CLI.
//
// PURPOSE: pin the externally-observable behaviour of the CLI BEFORE any
// UX-rejig changes land, so regressions fail loudly instead of silently.
// Every assertion in this file encodes a CONTRACT that agents, CI scripts,
// and downstream tooling depend on. Do NOT change any assertion without a
// deliberate, reviewed decision.
//
// STRUCTURE
//
//   - TestContract_ExitCodes_*         — process exit codes per scenario
//   - TestContract_JSON_*              — default (non-TTY) JSON output shapes
//   - TestContract_Raw_*               — --raw envelope preservation
//   - TestContract_JQ_*                — --jq filter behaviour
//   - TestContract_OutputFormats_*     — --output table|plain shapes
//   - TestContract_Destructive_*       — --yes / non-TTY guard behaviour
//   - TestContract_ErrorEnvelope_*     — stderr JSON:API error shape (subprocess)
//   - TestContract_TTY_*               — TTY vs non-TTY branching
//
// CONTRACT-BUG(P0) annotations mark places where the CURRENT behaviour is a
// known bug that the P0 UX rejig will intentionally fix. Each annotation names
// the new expected behaviour so it is easy to flip the assertion.
//
// HOW COMMANDS ARE DRIVEN
//
// Commands are driven in-process via the real cobra command tree (RootCmd())
// with captured *bytes.Buffer for stdout/stderr and a httptest.Server injected
// via --api-base. Team scope is injected via the --team persistent flag.
// MIO_API_KEY is set via env var. The MIO_API_BASE_URL env var (not MIO_API_BASE)
// is what config.go reads for the API base override.
//
// A small number of tests (TestContract_ErrorEnvelope_*) drive the real binary
// via os/exec because the JSON:API error envelope is written by main.go to
// os.Stderr after os.Exit — only a subprocess can capture it.

package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── in-process driver ────────────────────────────────────────────────────────

// contractResult holds the captured outputs of a single in-process invocation.
type contractResult struct {
	Stdout string
	Stderr string
	Code   int
}

// runContract executes the cobra command tree in-process with the given args,
// overlays env vars for the duration of the call, and returns captured
// stdout/stderr and the exit code (via the same logic as main.go).
//
// It resets the global persistent flags to their zero values before each
// execution so that flag state from a prior test cannot bleed into this one.
// The rootCmd is a package-level singleton; without this reset, a test that
// sets --output yaml would leave flags.output="yaml" for every subsequent test.
func runContract(t *testing.T, env []string, args ...string) contractResult {
	t.Helper()

	restore := overlayEnv(t, env)
	defer restore()

	// Reset the global flags struct so prior test invocations don't bleed over.
	resetGlobalFlags()

	root := RootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	defer root.SetArgs(nil)

	err := root.Execute()
	return contractResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Code:   codeForExecuteErr(err),
	}
}

// resetGlobalFlags resets the package-level flags variable to its zero value so
// tests that set --output/--jq/--raw/--yes/--team/--hub/--api-key/--api-base do
// not contaminate subsequent tests. The rootCmd is a singleton; without this
// reset flag state persists across tests in the same process.
//
// It also resets every PER-COMMAND leaf flag back to its registered default and
// clears its Changed bit by walking the command tree. cobra registers leaf flags
// (e.g. `tags assign --tag`) once on the singleton command, so a value set by
// one test would otherwise bleed into the next — exactly the kind of cross-test
// contamination this helper exists to prevent. The real `mio` binary forks a
// fresh process per invocation, so this only mirrors production isolation.
func resetGlobalFlags() {
	flags = globalFlags{}
	resetCommandFlags(rootCmd)
}

// resetCommandFlags restores every flag on cmd (and its subcommands) to its
// default value and clears the Changed bit, so leaf-flag state from a prior
// in-process invocation cannot leak into the next one.
func resetCommandFlags(cmd *cobra.Command) {
	reset := func(fs *pflag.FlagSet) {
		fs.VisitAll(func(f *pflag.Flag) {
			_ = fs.Set(f.Name, f.DefValue)
			f.Changed = false
		})
	}
	reset(cmd.Flags())
	reset(cmd.PersistentFlags())
	for _, sub := range cmd.Commands() {
		resetCommandFlags(sub)
	}
}

// overlayEnv sets key=value pairs on the process environment for the duration
// of the test and returns a cleanup function that restores the original values.
// A pair whose value is empty causes the key to be unset.
func overlayEnv(t *testing.T, pairs []string) func() {
	t.Helper()
	prev := make(map[string]string)
	missing := make([]string, 0)

	for _, kv := range pairs {
		k, v, _ := strings.Cut(kv, "=")
		if old, ok := os.LookupEnv(k); ok {
			prev[k] = old
		} else {
			missing = append(missing, k)
		}
		if v == "" {
			os.Unsetenv(k) //nolint:errcheck
		} else {
			os.Setenv(k, v) //nolint:errcheck
		}
	}

	return func() {
		for k, v := range prev {
			os.Setenv(k, v) //nolint:errcheck
		}
		for _, k := range missing {
			os.Unsetenv(k) //nolint:errcheck
		}
	}
}

// ─── mock HTTP server ──────────────────────────────────────────────────────────

// mockHandler describes a single canned HTTP response.
// Method="" matches any method; PathPfx="" matches any path; first match wins.
type mockHandler struct {
	Method  string
	PathPfx string
	Status  int
	Body    string
}

// newMockServer starts an httptest.Server dispatching to the first matching
// handler. Unmatched requests return a 404 JSON:API body.
func newMockServer(t *testing.T, handlers []mockHandler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range handlers {
			if h.Method != "" && h.Method != r.Method {
				continue
			}
			if h.PathPfx != "" && !strings.HasPrefix(r.URL.Path, h.PathPfx) {
				continue
			}
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(h.Status)
			_, _ = w.Write([]byte(h.Body))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// baseEnv returns the env vars needed for API-hitting commands.
// MIO_API_BASE_URL is the env var the config package reads (not MIO_API_BASE).
// The team scope is passed as a --team flag via withTeam.
func baseEnv(apiBase string) []string {
	return []string{
		"MIO_API_KEY=test-key-contract",
		"MIO_API_BASE_URL=" + apiBase,
	}
}

// withTeam prepends the --team flag to the given command args, allowing
// in-process tests to inject a team scope without writing a config file.
// Usage: runContract(t, baseEnv(srv.URL), withTeam("t_team1", "contacts", "list")...)
func withTeam(teamID string, args ...string) []string {
	return append([]string{"--team", teamID}, args...)
}

// ─── testdata helpers ──────────────────────────────────────────────────────────

func mustReadTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return string(b)
}

// ─── string / slice helpers ────────────────────────────────────────────────────

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func indexContaining(lines []string, substr string) int {
	for i, l := range lines {
		if strings.Contains(l, substr) {
			return i
		}
	}
	return -1
}

// ─── subprocess helpers ────────────────────────────────────────────────────────

// buildBinary compiles the mio binary into a temp dir and returns its path.
// Used only by TestContract_ErrorEnvelope_* which capture the JSON:API envelope
// written by main.go's os.Exit path.
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := dir + "/mio"

	moduleRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	moduleRoot = strings.TrimSuffix(moduleRoot, "/cmd")

	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = moduleRoot
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		t.Fatalf("build failed: %v\n%s", buildErr, out)
	}
	return bin
}

// runBinary executes the compiled mio binary with the given env vars and args,
// returning stdout, stderr, and process exit code.
func runBinary(t *testing.T, bin string, envPairs []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(), // isolate config files
	}
	for _, kv := range envPairs {
		k, v, _ := strings.Cut(kv, "=")
		if v != "" {
			env = append(env, k+"="+v)
		}
	}

	cmd := exec.Command(bin, args...)
	cmd.Env = env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	_ = cmd.Run()
	return outBuf.String(), errBuf.String(), cmd.ProcessState.ExitCode()
}

// ═══════════════════════════════════════════════════════════════════════════════
// Exit code contracts
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_ExitCodes_Success: a well-formed command with a 200 API
// response exits 0.
//
// CONTRACT: success → exit 0
func TestContract_ExitCodes_Success(t *testing.T) {
	body := mustReadTestdata(t, "list_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "list")...)
	if res.Code != errs.ExitOK {
		t.Errorf("CONTRACT: success exit code = %d, want %d (ExitOK); stderr=%q",
			res.Code, errs.ExitOK, res.Stderr)
	}
}

// TestContract_ExitCodes_UnknownFlag: an unrecognised flag on a leaf command
// exits 2.
//
// CONTRACT: unknown flag → exit 2 (ExitUsage)
func TestContract_ExitCodes_UnknownFlag(t *testing.T) {
	res := runContract(t, nil, "version", "--definitely-not-a-real-flag")
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: unknown-flag exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
}

// TestContract_ExitCodes_MissingRequiredFlagSemantics: calling a command with
// insufficient flags triggers the command's own usage error (exit 2).
//
// CONTRACT: insufficient user-facing required fields → exit 2 (ExitUsage)
func TestContract_ExitCodes_MissingRequiredFlagSemantics(t *testing.T) {
	// contacts create requires at least --email; no flags → "nothing to create"
	// error returned as ExitUsage from RunE.
	srv := newMockServer(t, nil)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "create")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: missing-required-field exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
}

// TestContract_ExitCodes_WrongArgCount: wrong positional argument count exits 2.
//
// CONTRACT: wrong arg count → exit 2 (ExitUsage)
func TestContract_ExitCodes_WrongArgCount(t *testing.T) {
	// contacts retrieve expects exactly 1 arg.
	res := runContract(t, nil, "contacts", "retrieve")
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: wrong-arg-count exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
}

// TestContract_ExitCodes_UnknownRootCommand: an unknown command at root exits 2.
//
// CONTRACT: unknown root command → exit 2 (ExitUsage)
func TestContract_ExitCodes_UnknownRootCommand(t *testing.T) {
	res := runContract(t, nil, "frobnicate-this-does-not-exist")
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: unknown-root-command exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
}

// TestContract_ExitCodes_UnknownSubcommandOnResourceGroup: unknown subcommand
// on a resource group (e.g. `mio contacts frobnicate`).
//
// CONTRACT (P0-FIXED): an unknown subcommand on a resource group exits 2
// (ExitUsage). Previously this was exit 0 — cobra treated a group-only command
// with no RunE as a successful no-op when it received unrecognised args, which
// silently swallowed typos. The P0 rejig attaches a RunE guard to every group
// command (see attachGroupGuards in root.go) so unknown/missing subcommands now
// return ExitUsage and print usage to stderr.
func TestContract_ExitCodes_UnknownSubcommandOnResourceGroup(t *testing.T) {
	res := runContract(t, nil, "contacts", "frobnicate-unknown-subcommand")
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: unknown-subcommand-on-group exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
	// The error path must not contaminate stdout — usage goes to stderr only.
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("CONTRACT: unknown-subcommand-on-group must produce no stdout; got: %q", res.Stdout)
	}
}

// TestContract_ExitCodes_NoCredentials: commands requiring auth exit 3 when
// no API key is configured.
//
// CONTRACT: no API key → exit 3 (ExitAuth)
func TestContract_ExitCodes_NoCredentials(t *testing.T) {
	restore := overlayEnv(t, []string{
		"MIO_API_KEY=", // unset
	})
	defer restore()
	os.Unsetenv("MIO_API_KEY") //nolint:errcheck

	root := RootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--team", "t_team1", "contacts", "list"})
	defer root.SetArgs(nil)

	err := root.Execute()
	code := codeForExecuteErr(err)

	if code != errs.ExitAuth {
		t.Errorf("CONTRACT: no-credentials exit code = %d, want %d (ExitAuth)",
			code, errs.ExitAuth)
	}
}

// TestContract_ExitCodes_NotFound: a 404 API response exits 4.
//
// CONTRACT: 404 API response → exit 4 (ExitNotFound)
func TestContract_ExitCodes_NotFound(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Status: 404, Body: `{"errors":[{"status":"404","detail":"contact not found"}]}`},
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "retrieve", "ctt_doesnotexist")...)
	if res.Code != errs.ExitNotFound {
		t.Errorf("CONTRACT: 404 exit code = %d, want %d (ExitNotFound)",
			res.Code, errs.ExitNotFound)
	}
}

// TestContract_ExitCodes_NonTTYDestructiveWithoutYes: destructive command in
// non-TTY without --yes exits 5.
//
// CONTRACT: non-TTY destructive without --yes → exit 5 (ExitNeedsConfir)
func TestContract_ExitCodes_NonTTYDestructiveWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil) // DELETE must NOT be called

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "delete", "ctt_any")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("CONTRACT: non-TTY-no-yes exit code = %d, want %d (ExitNeedsConfir=5)",
			res.Code, errs.ExitNeedsConfir)
	}
}

// TestContract_ExitCodes_NonTTYDestructiveWithYes: --yes bypasses the guard
// and the command proceeds (exit 0).
//
// CONTRACT: non-TTY destructive with --yes → exit 0 (ExitOK)
func TestContract_ExitCodes_NonTTYDestructiveWithYes(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Method: "DELETE", PathPfx: "/api/v1/teams/", Status: 204, Body: ""},
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "delete", "ctt_any", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Errorf("CONTRACT: non-TTY-with-yes exit code = %d, want %d (ExitOK)",
			res.Code, errs.ExitOK)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Default JSON output shape contracts
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_JSON_ListIsArray: the default JSON output of a list command is
// a bare JSON array of flat objects — NOT a JSON:API envelope.
//
// CONTRACT: list JSON → bare array [{"id":...,"type":...,...}, ...]
func TestContract_JSON_ListIsArray(t *testing.T) {
	body := mustReadTestdata(t, "list_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "json", "contacts", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}

	var parsed any
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, res.Stdout)
	}

	arr, ok := parsed.([]any)
	if !ok {
		t.Fatalf("CONTRACT: list JSON must be a bare array; got %T:\n%s", parsed, res.Stdout)
	}
	if len(arr) != 2 {
		t.Errorf("CONTRACT: list array length = %d, want 2", len(arr))
	}

	for i, elem := range arr {
		obj, ok := elem.(map[string]any)
		if !ok {
			t.Errorf("CONTRACT: list[%d] is %T, want flat map", i, elem)
			continue
		}
		if _, hasID := obj["id"]; !hasID {
			t.Errorf("CONTRACT: list[%d] missing 'id'", i)
		}
		if _, hasType := obj["type"]; !hasType {
			t.Errorf("CONTRACT: list[%d] missing 'type'", i)
		}
		// Attributes must be flattened to top-level — no nested "attributes" key.
		if _, hasAttrs := obj["attributes"]; hasAttrs {
			t.Errorf("CONTRACT: list[%d] must NOT have nested 'attributes' (must be flattened)", i)
		}
		// A known attribute promoted to top-level.
		if _, hasEmail := obj["email"]; !hasEmail {
			t.Errorf("CONTRACT: list[%d] missing top-level 'email' (not flattened)", i)
		}
	}
}

// TestContract_JSON_RetrieveIsFlatObject: the default JSON output of a
// retrieve command is a single flat object (id + type + attributes merged) —
// NOT a JSON:API {"data":...} envelope.
//
// CONTRACT: retrieve JSON → flat object {"id":...,"type":...,...}
func TestContract_JSON_RetrieveIsFlatObject(t *testing.T) {
	body := mustReadTestdata(t, "retrieve_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "json", "contacts", "retrieve", "ctt_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}

	var parsed any
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("stdout not valid JSON: %v\nstdout=%q", err, res.Stdout)
	}

	obj, ok := parsed.(map[string]any)
	if !ok {
		t.Fatalf("CONTRACT: retrieve JSON must be a flat object; got %T:\n%s", parsed, res.Stdout)
	}
	if obj["id"] != "ctt_1" {
		t.Errorf("CONTRACT: retrieve.id = %v, want ctt_1", obj["id"])
	}
	if obj["type"] != "team-contacts" {
		t.Errorf("CONTRACT: retrieve.type = %v, want team-contacts", obj["type"])
	}
	if _, has := obj["data"]; has {
		t.Error("CONTRACT: retrieve JSON must NOT have a top-level 'data' key")
	}
	if _, has := obj["attributes"]; has {
		t.Error("CONTRACT: retrieve JSON must NOT have a nested 'attributes' key")
	}
	if obj["email"] != "alice@example.com" {
		t.Errorf("CONTRACT: retrieve missing flattened 'email'; got %v", obj["email"])
	}
}

// TestContract_JSON_NonTTYDefaultIsJSON: off a TTY (in-process buffer), the
// default output format is JSON, not table.
//
// CONTRACT: non-TTY list default → JSON array (not table text)
//
// CONTRACT-BUG(P0): the P0 rejig must NOT change the non-TTY default format.
// If the rejig introduces a new non-TTY default (e.g. "agent" format), update
// this test explicitly.
func TestContract_JSON_NonTTYDefaultIsJSON(t *testing.T) {
	body := mustReadTestdata(t, "list_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	// No --output flag: format inferred from isTTY, which returns false for buffer.
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed: %s", res.Stderr)
	}

	var parsed any
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("CONTRACT: non-TTY output is not JSON: %v\nstdout=%q", err, res.Stdout)
	}
	if _, isArr := parsed.([]any); !isArr {
		t.Errorf("CONTRACT: non-TTY default output is not a JSON array; got %T", parsed)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// --raw envelope contracts
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_Raw_RetrievePreservesEnvelope: --raw on retrieve emits the full
// JSON:API envelope including links, included, and meta.
//
// CONTRACT: --raw retrieve → full JSON:API envelope (data + links + included + meta)
func TestContract_Raw_RetrievePreservesEnvelope(t *testing.T) {
	body := mustReadTestdata(t, "retrieve_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--raw", "--output", "json", "contacts", "retrieve", "ctt_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}

	for _, must := range []string{`"data"`, `"links"`, `"included"`, `"meta"`, `"ctt_1"`, `"tag_1"`, `"req_abc"`} {
		if !strings.Contains(res.Stdout, must) {
			t.Errorf("CONTRACT: --raw retrieve missing %s; stdout:\n%s", must, res.Stdout)
		}
	}
}

// TestContract_Raw_ListPreservesEnvelope: --raw on list emits the full JSON:API
// collection envelope.
//
// CONTRACT: --raw list → full JSON:API collection envelope (data + meta + links)
func TestContract_Raw_ListPreservesEnvelope(t *testing.T) {
	body := mustReadTestdata(t, "list_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--raw", "--output", "json", "contacts", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}

	for _, must := range []string{`"data"`, `"meta"`, `"links"`, `"ctt_1"`, `"ctt_2"`} {
		if !strings.Contains(res.Stdout, must) {
			t.Errorf("CONTRACT: --raw list missing %s; stdout:\n%s", must, res.Stdout)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// --jq filter contracts
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_JQ_ListIDs: --jq '.[].id' on a list extracts the id of every
// resource into a JSON array.
//
// CONTRACT: list + --jq '.[].id' → JSON array of id strings
func TestContract_JQ_ListIDs(t *testing.T) {
	body := mustReadTestdata(t, "list_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "json", "--jq", ".[].id", "contacts", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}

	var parsed any
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("--jq output not valid JSON: %v\nstdout=%q", err, res.Stdout)
	}

	ids, ok := parsed.([]any)
	if !ok {
		t.Fatalf("CONTRACT: --jq '.[].id' on list must produce an array; got %T", parsed)
	}
	if len(ids) != 2 {
		t.Errorf("CONTRACT: --jq ids count = %d, want 2", len(ids))
	}
	for i, want := range []string{"ctt_1", "ctt_2"} {
		if i >= len(ids) {
			break
		}
		if ids[i] != want {
			t.Errorf("CONTRACT: --jq ids[%d] = %v, want %q", i, ids[i], want)
		}
	}
}

// TestContract_JQ_SingleFieldRetrieve: --jq '.email' on a retrieve returns
// the field value as a JSON string.
//
// CONTRACT: retrieve + --jq '.field' → JSON value of that field
func TestContract_JQ_SingleFieldRetrieve(t *testing.T) {
	body := mustReadTestdata(t, "retrieve_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "json", "--jq", ".email", "contacts", "retrieve", "ctt_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}

	got := strings.TrimSpace(res.Stdout)
	if got != `"alice@example.com"` {
		t.Errorf("CONTRACT: --jq '.email' = %q, want %q", got, `"alice@example.com"`)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// --output format contracts
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_OutputFormats_TableList: --output table on a list command
// produces an all-caps header row (ID first, TYPE second, rest alpha) followed
// by data rows.
//
// CONTRACT: --output table list → uppercase header (ID TYPE ...) + data rows
func TestContract_OutputFormats_TableList(t *testing.T) {
	body := mustReadTestdata(t, "list_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "table", "contacts", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}

	lines := nonEmptyLines(res.Stdout)
	if len(lines) == 0 {
		t.Fatal("CONTRACT: table output has no lines")
	}

	header := lines[0]
	if !strings.HasPrefix(header, "ID") {
		t.Errorf("CONTRACT: table header must start with 'ID'; got %q", header)
	}
	if !strings.Contains(header, "TYPE") {
		t.Errorf("CONTRACT: table header must contain 'TYPE'; got %q", header)
	}
	if header != strings.ToUpper(header) {
		t.Errorf("CONTRACT: table header must be all-uppercase; got %q", header)
	}
	if len(lines) < 3 { // header + 2 data rows
		t.Errorf("CONTRACT: table expected ≥3 lines (header + 2 rows); got %d", len(lines))
	}
}

// TestContract_OutputFormats_PlainRetrieve: --output plain on a retrieve
// command produces key=value lines sorted alphabetically.
//
// CONTRACT: --output plain retrieve → sorted key=value lines, one per field
func TestContract_OutputFormats_PlainRetrieve(t *testing.T) {
	body := mustReadTestdata(t, "retrieve_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "plain", "contacts", "retrieve", "ctt_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
	}

	lines := nonEmptyLines(res.Stdout)
	if len(lines) == 0 {
		t.Fatal("CONTRACT: plain output has no lines")
	}

	for _, line := range lines {
		if !strings.Contains(line, "=") {
			t.Errorf("CONTRACT: plain line not key=value: %q", line)
		}
	}

	if !strings.Contains(res.Stdout, "id=ctt_1") {
		t.Errorf("CONTRACT: plain output missing 'id=ctt_1'; got:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "email=alice@example.com") {
		t.Errorf("CONTRACT: plain output missing 'email=alice@example.com'; got:\n%s", res.Stdout)
	}

	// Lines must be alphabetically sorted: email < id < type.
	emailIdx := indexContaining(lines, "email=")
	idIdx := indexContaining(lines, "id=")
	typeIdx := indexContaining(lines, "type=")
	if emailIdx < 0 || idIdx < 0 || typeIdx < 0 {
		t.Errorf("CONTRACT: plain output missing email/id/type lines; lines=%v", lines)
	} else if emailIdx >= idIdx || idIdx >= typeIdx {
		t.Errorf("CONTRACT: plain output not alphabetically sorted; email@%d id@%d type@%d; lines=%v",
			emailIdx, idIdx, typeIdx, lines)
	}
}

// TestContract_OutputFormats_InvalidFormat: an unrecognised --output value
// exits 2 (ExitUsage).
//
// CONTRACT: unknown --output value → exit 2 (ExitUsage)
//
// Note: --output is validated in newContext (resolveFormat), which is only
// called by resource commands — not by `version`. We therefore use `contacts
// list` with a mock server; the format validation fires before any API call.
func TestContract_OutputFormats_InvalidFormat(t *testing.T) {
	srv := newMockServer(t, nil) // no request expected; format check fires first
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "yaml", "contacts", "list")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: invalid-format exit code = %d, want %d (ExitUsage); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Destructive-operation contracts
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_Destructive_NonTTYNoYes_StdoutEmpty: a destructive command
// refused for missing --yes must produce NO stdout.
//
// CONTRACT: destructive without --yes → stdout empty (operation was refused)
func TestContract_Destructive_NonTTYNoYes_StdoutEmpty(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "delete", "ctt_any")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("CONTRACT: exit code = %d, want %d (ExitNeedsConfir=5)",
			res.Code, errs.ExitNeedsConfir)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("CONTRACT: refused destructive must produce no stdout; got: %q", res.Stdout)
	}
}

// TestContract_Destructive_WithYes_DeleteCalled: --yes causes DELETE to be
// sent to the API.
//
// CONTRACT: destructive with --yes → DELETE called, exit 0
func TestContract_Destructive_WithYes_DeleteCalled(t *testing.T) {
	deleteCalled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalled = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "delete", "ctt_any", "--yes")...)

	if !deleteCalled {
		t.Error("CONTRACT: --yes should cause DELETE to be called")
	}
	if res.Code != errs.ExitOK {
		t.Errorf("CONTRACT: --yes exit code = %d, want %d (ExitOK)", res.Code, errs.ExitOK)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// teams switch stdout-purity contract
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_TeamsSwitch_StdoutMachineClean: `teams switch <id> --output json`
// off a TTY must emit ONLY the rendered payload on stdout — no human prose. The
// "Switched to team …" confirmation is a TTY-only nicety and must live on
// stderr, never stdout, or it would corrupt the JSON an agent parses (and break
// any downstream --jq pipe).
//
// CONTRACT: teams switch --output json (non-TTY) → stdout is valid JSON only
//
// Two server shapes are exercised: an endpoint that returns a resource body
// (stdout must be exactly that JSON object) and one that returns 204 No Content
// (stdout must be empty, per the existing no-body convention). In both cases the
// config write + hub clear happens regardless; we sandbox it via XDG_CONFIG_HOME
// so the test never touches the developer's real config file.
func TestContract_TeamsSwitch_StdoutMachineClean(t *testing.T) {
	t.Run("with-body", func(t *testing.T) {
		body := `{"data":{"id":"t_new","type":"teams","attributes":{"name":"New Team"}}}`
		srv := newMockServer(t, []mockHandler{
			{Method: "POST", PathPfx: "/api/v1/teams/", Status: 200, Body: body},
		})

		env := append(baseEnv(srv.URL), "XDG_CONFIG_HOME="+t.TempDir())
		res := runContract(t, env,
			withTeam("t_old", "--output", "json", "teams", "switch", "t_new")...)
		if res.Code != errs.ExitOK {
			t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
		}

		// stdout must be a single valid JSON value with NO leading/trailing prose.
		var parsed any
		if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
			t.Fatalf("CONTRACT: teams switch --output json stdout is not pure JSON: %v\nstdout=%q",
				err, res.Stdout)
		}
		obj, ok := parsed.(map[string]any)
		if !ok {
			t.Fatalf("CONTRACT: teams switch stdout must be a JSON object; got %T:\n%s", parsed, res.Stdout)
		}
		if obj["id"] != "t_new" {
			t.Errorf("CONTRACT: teams switch stdout id = %v, want t_new", obj["id"])
		}
		// The human confirmation must NOT appear on stdout.
		if strings.Contains(res.Stdout, "Switched to team") {
			t.Errorf("CONTRACT: human confirmation leaked into stdout: %q", res.Stdout)
		}
	})

	t.Run("no-body", func(t *testing.T) {
		srv := newMockServer(t, []mockHandler{
			{Method: "POST", PathPfx: "/api/v1/teams/", Status: 204, Body: ""},
		})

		env := append(baseEnv(srv.URL), "XDG_CONFIG_HOME="+t.TempDir())
		res := runContract(t, env,
			withTeam("t_old", "--output", "json", "teams", "switch", "t_new")...)
		if res.Code != errs.ExitOK {
			t.Fatalf("command failed (code %d): %s", res.Code, res.Stderr)
		}

		// No body from the server → stdout empty (no prose). The confirmation,
		// if any, is on stderr and only on a TTY (the test buffer is non-TTY, so
		// there should be no confirmation at all here).
		if strings.TrimSpace(res.Stdout) != "" {
			t.Errorf("CONTRACT: teams switch (no body) must produce empty stdout; got: %q", res.Stdout)
		}
		if strings.Contains(res.Stderr, "Switched to team") {
			t.Errorf("CONTRACT: non-TTY stderr should not carry the confirmation; got: %q", res.Stderr)
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Error-envelope shape contracts (subprocess)
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_ErrorEnvelope_Shape drives the real binary to pin the exact
// JSON:API error envelope shape written to stderr on any error.
//
// CONTRACT: any error → JSON:API envelope on stderr:
//
//	{"errors":[{"status":"<http-ish>","detail":"<msg>","meta":{"exit_code":<n>}}]}
//
// CONTRACT-BUG(P0): the P0 rejig will add a TTY-aware branch: on a real
// terminal the error may be formatted as human-friendly text instead. The
// NON-TTY path (piped stderr) must remain a JSON:API envelope. This test runs
// piped — it pins the non-TTY path and requires NO change when the TTY branch
// is added.
func TestContract_ErrorEnvelope_Shape(t *testing.T) {
	bin := buildBinary(t)

	// No MIO_API_KEY → ExitAuth
	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_TEAM_IS_NOT_A_REAL_VAR=t_team1", // team injected via --team flag
	}, "--team", "t_team1", "contacts", "list")

	raw := strings.TrimSpace(stderr)
	if raw == "" {
		t.Fatal("CONTRACT: stderr was empty; expected JSON:API error envelope")
	}

	var envelope struct {
		Errors []struct {
			Status string         `json:"status"`
			Detail string         `json:"detail"`
			Meta   map[string]any `json:"meta"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("CONTRACT: stderr not valid JSON:API envelope: %v\nstderr=%q", err, raw)
	}
	if len(envelope.Errors) == 0 {
		t.Errorf("CONTRACT: error envelope has empty 'errors' array; stderr=%q", raw)
	}

	e := envelope.Errors[0]
	if e.Status == "" {
		t.Errorf("CONTRACT: errors[0].status empty; stderr=%q", raw)
	}
	if e.Detail == "" {
		t.Errorf("CONTRACT: errors[0].detail empty; stderr=%q", raw)
	}
	if e.Meta == nil {
		t.Errorf("CONTRACT: errors[0].meta nil; stderr=%q", raw)
	} else if _, ok := e.Meta["exit_code"]; !ok {
		t.Errorf("CONTRACT: errors[0].meta missing 'exit_code'; stderr=%q", raw)
	}

	if exitCode != errs.ExitAuth {
		t.Errorf("CONTRACT: no-credentials exit code = %d, want %d (ExitAuth)", exitCode, errs.ExitAuth)
	}
}

// TestContract_ErrorEnvelope_404 pins that a 404 API response produces a
// JSON:API error envelope on stderr with status "404" and process exit code 4.
//
// CONTRACT: 404 API response → JSON envelope on stderr with status "404", exit 4
func TestContract_ErrorEnvelope_404(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Status: 404, Body: `{"errors":[{"status":"404","detail":"contact not found"}]}`},
	})

	bin := buildBinary(t)
	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	}, "--team", "t_team1", "contacts", "retrieve", "ctt_missing")

	if exitCode != errs.ExitNotFound {
		t.Errorf("CONTRACT: 404 exit code = %d, want %d (ExitNotFound)", exitCode, errs.ExitNotFound)
	}

	raw := strings.TrimSpace(stderr)
	var envelope struct {
		Errors []struct {
			Status string `json:"status"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("CONTRACT: 404 stderr not valid JSON: %v; stderr=%q", err, raw)
	}
	if len(envelope.Errors) == 0 {
		t.Errorf("CONTRACT: 404 error envelope empty; stderr=%q", raw)
	} else if envelope.Errors[0].Status != "404" {
		t.Errorf("CONTRACT: 404 errors[0].status = %q, want 404; stderr=%q",
			envelope.Errors[0].Status, raw)
	}
}

// TestContract_ErrorEnvelope_NonTTYDestructiveNoYes pins that refusing a
// destructive command produces a JSON:API envelope on stderr with status "412"
// (ExitNeedsConfir maps to 412) and exit code 5.
//
// CONTRACT: non-TTY destructive without --yes → stderr JSON status "412", exit 5
func TestContract_ErrorEnvelope_NonTTYDestructiveNoYes(t *testing.T) {
	srv := newMockServer(t, nil) // DELETE must not be reached

	bin := buildBinary(t)
	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	}, "--team", "t_team1", "contacts", "delete", "ctt_any")
	// No --yes → ExitNeedsConfir=5

	if exitCode != errs.ExitNeedsConfir {
		t.Errorf("CONTRACT: non-TTY-no-yes exit code = %d, want %d (ExitNeedsConfir=5)",
			exitCode, errs.ExitNeedsConfir)
	}

	raw := strings.TrimSpace(stderr)
	var envelope struct {
		Errors []struct {
			Status string `json:"status"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("CONTRACT: destructive-no-yes stderr not valid JSON: %v; stderr=%q", err, raw)
	}
	if len(envelope.Errors) == 0 {
		t.Errorf("CONTRACT: destructive-no-yes error envelope empty; stderr=%q", raw)
	} else if envelope.Errors[0].Status != "412" {
		t.Errorf("CONTRACT: destructive-no-yes errors[0].status = %q, want 412; stderr=%q",
			envelope.Errors[0].Status, raw)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TTY vs non-TTY contracts
// ═══════════════════════════════════════════════════════════════════════════════

// TestContract_TTY_ErrorNotInStdout: errors must not bleed into stdout.
//
// CONTRACT: on error, stdout is empty; error goes to stderr only
func TestContract_TTY_ErrorNotInStdout(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Status: 404, Body: `{"errors":[{"status":"404","detail":"not found"}]}`},
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "retrieve", "ctt_missing")...)

	if res.Code == errs.ExitOK {
		t.Error("CONTRACT: expected non-zero exit for 404")
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("CONTRACT: error path must produce no stdout; got: %q", res.Stdout)
	}
}
