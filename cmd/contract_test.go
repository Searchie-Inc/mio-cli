package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/99designs/keyring"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type contractResult struct {
	Stdout string
	Stderr string
	Code   int
}

func runContract(t *testing.T, env []string, args ...string) contractResult {
	t.Helper()

	restore := overlayEnv(t, env)
	defer restore()

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

func resetGlobalFlags() {
	flags = globalFlags{}
	resetCommandFlags(rootCmd)
}

func resetCommandFlags(cmd *cobra.Command) {
	reset := func(fs *pflag.FlagSet) {
		fs.VisitAll(func(f *pflag.Flag) {
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
				f.Changed = false
				return
			}
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

type mockHandler struct {
	Method  string
	PathPfx string
	Status  int
	Body    string
}

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

func baseEnv(apiBase string) []string {
	return []string{
		"MIO_API_KEY=test-key-contract",
		"MIO_API_BASE_URL=" + apiBase,
	}
}

func withTeam(teamID string, args ...string) []string {
	return append([]string{"--team", teamID}, args...)
}

func mustReadTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return string(b)
}

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

func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mio")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	cmdDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	moduleRoot := filepath.Dir(cmdDir)

	cmd := exec.Command("go", "build", "-tags", childKeyringTag, "-o", bin, ".")
	cmd.Dir = moduleRoot
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		t.Fatalf("build failed: %v\n%s", buildErr, out)
	}
	requireChildOnFileKeyring(t, bin)
	return bin
}

const (
	childKeyringTag  = "mio_test_file_keyring"
	childBackendsCmd = "__test-keyring-backends"
)

func requireChildOnFileKeyring(t *testing.T, bin string) {
	t.Helper()
	probe := exec.Command(bin, childBackendsCmd)
	probe.Env = childBaseEnv(t.TempDir())
	var stdout, stderr bytes.Buffer
	probe.Stdout, probe.Stderr = &stdout, &stderr
	if err := probe.Run(); err != nil {
		t.Fatalf("the child mio binary cannot report its keyring backends (%v; is it built with -tags %s?): "+
			"unpinned, a child reads the developer's real OS credential store. stderr=%q", err, childKeyringTag, stderr.String())
	}
	var backends []string
	if err := json.Unmarshal(stdout.Bytes(), &backends); err != nil || len(backends) != 1 || backends[0] != string(keyring.FileBackend) {
		t.Fatalf("the child mio binary may open keyring backends %s (%v), want only [%q]: "+
			"it would read the developer's real Keychain, Credential Manager or Secret Service", strings.TrimSpace(stdout.String()), err, keyring.FileBackend)
	}
}

func TestBuildBinary_ChildReadsOnlyAnIsolatedFileKeyring(t *testing.T) {
	bin := buildBinary(t)
	requireChildOnFileKeyring(t, bin)

	stdout, stderr, code := runBinary(t, bin, nil, "auth", "token")
	if code != errs.ExitAuth || stdout != "" {
		t.Fatalf("auth token with nothing stored: exit %d, stdout %q; want exit %d and no stdout. stderr=%q", code, stdout, errs.ExitAuth, stderr)
	}
	var envelope struct {
		Errors []struct {
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil || len(envelope.Errors) == 0 {
		t.Fatalf("auth token stderr is not a JSON:API error envelope (%v): %q", err, stderr)
	}
	detail := envelope.Errors[0].Detail
	homeVar := "HOME"
	if runtime.GOOS == "windows" {
		homeVar = "USERPROFILE"
	}
	want := "the file keyring at " + os.TempDir()
	if !strings.Contains(detail, want) || !strings.Contains(detail, "config dir from $"+homeVar+"; XDG_CONFIG_HOME is unset") {
		t.Errorf("the child did not read a file keyring under the temp $%s runBinary gives it; want %q in %q", homeVar, want, detail)
	}
}

func childBaseEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"USERPROFILE=" + home,
	}
}

func runBinary(t *testing.T, bin string, envPairs []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	env := childBaseEnv(t.TempDir())
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

func TestContract_ExitCodes_UnknownFlag(t *testing.T) {
	res := runContract(t, nil, "version", "--definitely-not-a-real-flag")
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: unknown-flag exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
}

func TestContract_ExitCodes_MissingRequiredFlagSemantics(t *testing.T) {
	srv := newMockServer(t, nil)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "create")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: missing-required-field exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
}

func TestContract_ExitCodes_WrongArgCount(t *testing.T) {
	res := runContract(t, nil, "contacts", "retrieve")
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: wrong-arg-count exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
}

func TestContract_ExitCodes_UnknownRootCommand(t *testing.T) {
	res := runContract(t, nil, "frobnicate-this-does-not-exist")
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: unknown-root-command exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
}

func TestContract_ExitCodes_UnknownSubcommandOnResourceGroup(t *testing.T) {
	res := runContract(t, nil, "contacts", "frobnicate-unknown-subcommand")
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: unknown-subcommand-on-group exit code = %d, want %d (ExitUsage)",
			res.Code, errs.ExitUsage)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("CONTRACT: unknown-subcommand-on-group must produce no stdout; got: %q", res.Stdout)
	}
}

func TestContract_ExitCodes_NoCredentials(t *testing.T) {
	restore := overlayEnv(t, []string{
		"MIO_API_KEY=",
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

func TestContract_ExitCodes_NonTTYDestructiveWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "delete", "ctt_any")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("CONTRACT: non-TTY-no-yes exit code = %d, want %d (ExitNeedsConfir=5)",
			res.Code, errs.ExitNeedsConfir)
	}
}

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
		if _, hasAttrs := obj["attributes"]; hasAttrs {
			t.Errorf("CONTRACT: list[%d] must NOT have nested 'attributes' (must be flattened)", i)
		}
		if _, hasEmail := obj["email"]; !hasEmail {
			t.Errorf("CONTRACT: list[%d] missing top-level 'email' (not flattened)", i)
		}
	}
}

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
	if obj["type"] != "team_contacts" {
		t.Errorf("CONTRACT: retrieve.type = %v, want team_contacts", obj["type"])
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

func TestContract_JSON_NonTTYDefaultIsJSON(t *testing.T) {
	body := mustReadTestdata(t, "list_response.json")
	srv := newMockServer(t, []mockHandler{{Status: 200, Body: body}})

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
	if len(lines) < 3 {
		t.Errorf("CONTRACT: table expected ≥3 lines (header + 2 rows); got %d", len(lines))
	}
}

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

func TestContract_OutputFormats_InvalidFormat(t *testing.T) {
	srv := newMockServer(t, nil)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--output", "yaml", "contacts", "list")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("CONTRACT: invalid-format exit code = %d, want %d (ExitUsage); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
}

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

		if strings.TrimSpace(res.Stdout) != "" {
			t.Errorf("CONTRACT: teams switch (no body) must produce empty stdout; got: %q", res.Stdout)
		}
		if strings.Contains(res.Stderr, "Switched to team") {
			t.Errorf("CONTRACT: non-TTY stderr should not carry the confirmation; got: %q", res.Stderr)
		}
	})
}

func TestContract_ErrorEnvelope_Shape(t *testing.T) {
	bin := buildBinary(t)

	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_TEAM_IS_NOT_A_REAL_VAR=t_team1",
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

func TestContract_ErrorEnvelope_NonTTYDestructiveNoYes(t *testing.T) {
	srv := newMockServer(t, nil)

	bin := buildBinary(t)
	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	}, "--team", "t_team1", "contacts", "delete", "ctt_any")

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

func TestContract_ErrorEnvelope_RealHTTPStatus(t *testing.T) {
	bin := buildBinary(t)

	cases := []struct {
		name       string
		httpStatus int
		body       string
		wantStatus string
		wantExit   int
	}{
		{
			name:       "403 Forbidden is not reported as 401",
			httpStatus: 403,
			body:       `{"errors":[{"status":"403","detail":"API key principals cannot manage API keys"}]}`,
			wantStatus: "403",
			wantExit:   errs.ExitAuth,
		},
		{
			name:       "409 Conflict is not reported as 400",
			httpStatus: 409,
			body:       `{"errors":[{"status":"409","detail":"Email 'a@b.c' is already registered."}]}`,
			wantStatus: "409",
			wantExit:   errs.ExitUsage,
		},
		{
			name:       "422 Unprocessable is not reported as 400",
			httpStatus: 422,
			body:       `{"errors":[{"status":"422","detail":"String should have at least 8 characters"}]}`,
			wantStatus: "422",
			wantExit:   errs.ExitUsage,
		},
		{
			name:       "5xx variants no longer all report 500",
			httpStatus: 503,
			body:       `{"errors":[{"status":"503","detail":"Service temporarily unavailable"}]}`,
			wantStatus: "503",
			wantExit:   errs.ExitServer,
		},
		{
			name:       "other 4xx no longer reports 500",
			httpStatus: 405,
			body:       `{"errors":[{"status":"405","detail":"Method Not Allowed"}]}`,
			wantStatus: "405",
			wantExit:   errs.ExitGeneric,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMockServer(t, []mockHandler{
				{Status: tc.httpStatus, Body: tc.body},
			})

			_, stderr, exitCode := runBinary(t, bin, []string{
				"MIO_API_KEY=test-key",
				"MIO_API_BASE_URL=" + srv.URL,
			}, "--team", "t_team1", "contacts", "retrieve", "ctt_any")

			raw := strings.TrimSpace(stderr)
			var envelope struct {
				Errors []struct {
					Status string         `json:"status"`
					Detail string         `json:"detail"`
					Meta   map[string]any `json:"meta"`
				} `json:"errors"`
			}
			if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
				t.Fatalf("CONTRACT: %d stderr not valid JSON:API envelope: %v; stderr=%q",
					tc.httpStatus, err, raw)
			}
			if len(envelope.Errors) == 0 {
				t.Fatalf("CONTRACT: %d error envelope empty; stderr=%q", tc.httpStatus, raw)
			}

			e := envelope.Errors[0]
			if e.Status != tc.wantStatus {
				t.Errorf("CONTRACT: HTTP %d → errors[0].status = %q, want %q; stderr=%q",
					tc.httpStatus, e.Status, tc.wantStatus, raw)
			}
			if got, ok := e.Meta["exit_code"].(float64); !ok || int(got) != tc.wantExit {
				t.Errorf("CONTRACT: HTTP %d → meta.exit_code = %v, want %d; stderr=%q",
					tc.httpStatus, e.Meta["exit_code"], tc.wantExit, raw)
			}
			if exitCode != tc.wantExit {
				t.Errorf("CONTRACT: HTTP %d → process exit code = %d, want %d; stderr=%q",
					tc.httpStatus, exitCode, tc.wantExit, raw)
			}
			if e.Detail == "" {
				t.Errorf("CONTRACT: HTTP %d → errors[0].detail empty; stderr=%q", tc.httpStatus, raw)
			}
		})
	}
}

func TestContract_ErrorEnvelope_LocalErrorKeepsDerivedStatus(t *testing.T) {
	bin := buildBinary(t)

	_, stderr, exitCode := runBinary(t, bin, nil,
		"version", "--definitely-not-a-real-flag")

	raw := strings.TrimSpace(stderr)
	var envelope struct {
		Errors []struct {
			Status string `json:"status"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("CONTRACT: local-error stderr not valid JSON: %v; stderr=%q", err, raw)
	}
	if len(envelope.Errors) == 0 {
		t.Fatalf("CONTRACT: local-error envelope empty; stderr=%q", raw)
	}
	if envelope.Errors[0].Status != "400" {
		t.Errorf("CONTRACT: local usage error → errors[0].status = %q, want 400; stderr=%q",
			envelope.Errors[0].Status, raw)
	}
	if exitCode != errs.ExitUsage {
		t.Errorf("CONTRACT: local usage error exit code = %d, want %d (ExitUsage)",
			exitCode, errs.ExitUsage)
	}
}

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
