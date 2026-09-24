package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99designs/keyring"

	"github.com/Searchie-Inc/mio-cli/internal/config"
)

// isolationRoot is the temp dir TestMain created for this run. Every location a
// store can be derived from must resolve under it; see isolationProblems.
var isolationRoot string

// moduleRoot is this repo's checkout, recorded before any test runs. No test
// may resolve an agent skill path inside it: the project scope is relative to
// the working directory, and `go test` starts in the package dir (MIO-4178).
var moduleRoot string

// TestMain keeps every test in this package away from the developer's real
// credential stores and config (MIO-2995):
//
//   - HOME and USERPROFILE point at a fresh temp dir, and XDG_CONFIG_HOME at
//     another, for the whole run — overridden even when already set, since a
//     caller's XDG_CONFIG_HOME may BE the real one. So a test that does not set
//     its own can never read or write ~/.config/mio, and neither can a bug that
//     derives a store path from HOME instead of XDG_CONFIG_HOME. (Before this, a
//     bare `go test ./...` read the real config.toml and stored key:
//     TestContract_ExitCodes_NoCredentials then sent that key to the production
//     default API base, and it and TestWiring_SingleHubAutoDefault failed on any
//     machine that had run `mio login`.)
//   - CODEX_HOME points at a third temp dir, so the Codex user-scope skill
//     can never resolve to the developer's real one (MIO-4178).
//   - Every agent skill path is checked as it is resolved: one outside the temp
//     root, or inside this checkout, panics before anything reads or writes it.
//     A test that forgets to move its working directory with
//     isolateSkillSandbox would otherwise resolve the project-scope skill in
//     the repo itself (MIO-4178).
//   - The keyring is pinned to the encrypted FILE backend, under that config
//     dir. Otherwise a test that resolves or stores a key without MIO_API_KEY —
//     the login, register and no-credentials tests among them — reaches the
//     first OS store the build can open: on a macOS machine running a cgo
//     `go test`, the developer's real login Keychain, which no HOME or
//     XDG_CONFIG_HOME sandbox reaches.
//
// Moving HOME would also move the Go toolchain's own caches for the `go build`
// that buildBinary runs, so their current locations are pinned first.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	root, err := os.MkdirTemp("", "mio-cmd-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: create isolated dirs:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()
	// Resolve symlinks (macOS's /var -> /private/var) so the prefix checks in
	// isolationProblems compare like with like.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	isolationRoot = root
	if wd, err := os.Getwd(); err == nil {
		if resolved, err := filepath.EvalSymlinks(wd); err == nil {
			wd = resolved
		}
		moduleRoot = filepath.Dir(wd) // go test runs in <module>/cmd
	}

	pinGoToolchainDirs()

	home := filepath.Join(root, "home")
	cfgHome := filepath.Join(root, "config")
	codexHomeDir := filepath.Join(root, "codex")
	for _, dir := range []string{home, cfgHome, codexHomeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "TestMain: create isolated dirs:", err)
			return 1
		}
	}
	for k, v := range map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": cfgHome, "CODEX_HOME": codexHomeDir} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: set %s: %v\n", k, err)
			return 1
		}
	}
	restore := config.UseFileBackendOnly()
	defer restore()
	skillPathResolved = requireSkillPathInSandbox
	// Check the isolation BEFORE any test runs: TestMain_IsolatesEveryUserStore
	// reports the same problems, but only after every test sorted ahead of it
	// has already had its chance to write a real store.
	if problems := isolationProblems(); len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "TestMain: refusing to run: the cmd tests are not isolated from the developer's real stores:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  - "+p)
		}
		return 1
	}
	return m.Run()
}

// pinGoToolchainDirs exports the Go build cache, module cache, GOPATH and
// go-env file locations as they are NOW, so a `go build` launched after HOME
// and XDG_CONFIG_HOME move keeps using the developer's real caches and
// `go env -w` settings instead of re-downloading every module into a temp dir.
// A value already set in the environment is left alone.
func pinGoToolchainDirs() {
	out, err := exec.Command("go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV").Output()
	if err != nil {
		return
	}
	var vals map[string]string
	if json.Unmarshal(out, &vals) != nil {
		return
	}
	for k, v := range vals {
		if v != "" && os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
}

// TestMain_IsolatesEveryUserStore pins TestMain's isolation: every location a
// mio store can be derived from — HOME, USERPROFILE, XDG_CONFIG_HOME, CODEX_HOME, the home
// directory os.UserHomeDir derives from them, and the config path they resolve
// to — must lie inside the temp dir TestMain created, and the keyring must be
// pinned to the file backend under it. Inside TestMain's own dir, not merely
// inside os.TempDir(): a caller that already runs `go test` under a temp
// XDG_CONFIG_HOME would otherwise satisfy the check with TestMain's pinning
// deleted. During MIO-2995 a store path wrongly derived from $HOME replaced
// the real ~/.config/mio/keyring/api-key from a unit test; this is what stops
// that from reaching a real store again. TestMain runs the same checks before
// any test and refuses to start if one fails; this test names them in the
// report.
func TestMain_IsolatesEveryUserStore(t *testing.T) {
	for _, p := range isolationProblems() {
		t.Error(p)
	}
}

// isolationProblems lists every way the current process could still reach
// the developer's real config or credential stores; empty means isolated.
func isolationProblems() []string {
	var problems []string
	inRoot := func(p string) bool {
		return isolationRoot != "" && (p == isolationRoot || strings.HasPrefix(p, isolationRoot+string(filepath.Separator)))
	}
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "CODEX_HOME"} {
		if v := os.Getenv(k); !inRoot(v) {
			problems = append(problems, fmt.Sprintf("%s = %q, want a dir under TestMain's %q — TestMain must pin it", k, v, isolationRoot))
		}
	}
	if home, err := os.UserHomeDir(); err != nil || !inRoot(home) {
		problems = append(problems, fmt.Sprintf("os.UserHomeDir() = (%q, %v), want a dir under %q", home, err, isolationRoot))
	}
	if p, err := config.Path(); err != nil || !inRoot(p) {
		problems = append(problems, fmt.Sprintf("config.Path() = (%q, %v), want a path under %q", p, err, isolationRoot))
	}
	// On a runner with no OS store the file backend is chosen whether or not
	// it is pinned, so no test's behaviour shows a missing pin there; on a
	// macOS cgo build or a Linux desktop, the first test that stores a key
	// would write the developer's real Keychain or Secret Service.
	if b := config.KeyringBackends(); len(b) != 1 || b[0] != keyring.FileBackend {
		problems = append(problems, fmt.Sprintf("keyring backends = %v, want only %q: without config.UseFileBackendOnly a test that "+
			"stores or reads a key reaches the first OS store this build can open (the macOS Keychain in a cgo build, "+
			"a desktop Secret Service or KWallet)", b, keyring.FileBackend))
	}
	return problems
}

// requireSkillPathInSandbox is skillPathResolved for this whole test binary: it
// panics — before the caller reads or writes anything — when a test resolves an
// agent skill path outside the temp root or inside this checkout. That is what
// a test does when it forgets isolateSkillSandbox: the user scope follows HOME
// and CODEX_HOME, and the project scope follows the working directory, which
// `go test` sets to the package dir in the repo (MIO-4178).
func requireSkillPathInSandbox(path string) {
	if problem := skillPathProblem(path); problem != "" {
		panic(problem)
	}
}

// skillPathProblem describes why path is not a safe place for a test to put an
// agent skill, or returns "" when it is.
func skillPathProblem(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Sprintf("skill test sandbox (MIO-4178): cannot resolve skill path %q: %v", path, err)
	}
	within := func(p, dir string) bool {
		return dir != "" && (p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)))
	}
	inTemp := false
	for _, tmp := range []string{os.TempDir(), resolvedOrSelf(os.TempDir())} {
		if within(abs, filepath.Clean(tmp)) {
			inTemp = true
		}
	}
	if inTemp && !within(abs, moduleRoot) {
		return ""
	}
	wd, _ := os.Getwd()
	return fmt.Sprintf("skill test sandbox violated (MIO-4178): a test resolved the agent skill path %s, which is outside the temp root %s "+
		"or inside the checkout %s (cwd=%s HOME=%s CODEX_HOME=%s) — call isolateSkillSandbox(t) before anything reads or writes a skill",
		abs, os.TempDir(), moduleRoot, wd, os.Getenv("HOME"), os.Getenv("CODEX_HOME"))
}

func resolvedOrSelf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
