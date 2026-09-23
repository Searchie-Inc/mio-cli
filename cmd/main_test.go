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

// realUserHome is the home directory this test process started with, kept
// only so TestMain_IsolatesEveryUserStore can prove the tests are NOT using it.
var realUserHome string

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

	pinGoToolchainDirs()
	realUserHome, _ = os.UserHomeDir()

	home := filepath.Join(root, "home")
	cfgHome := filepath.Join(root, "config")
	for _, dir := range []string{home, cfgHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "TestMain: create isolated dirs:", err)
			return 1
		}
	}
	for k, v := range map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": cfgHome} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: set %s: %v\n", k, err)
			return 1
		}
	}
	restore := config.UseFileBackendOnly()
	defer restore()
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
// mio store can be derived from — HOME, USERPROFILE, XDG_CONFIG_HOME, and the
// config path they resolve to — must be a temp dir and not the developer's,
// and the keyring must be pinned to the file backend under it. During
// MIO-2995 a store path wrongly derived from $HOME replaced the real
// ~/.config/mio/keyring/api-key from a unit test; this is what stops that
// from reaching a real store again. TestMain runs the same checks before any
// test and refuses to start if one fails; this test names them in the report.
func TestMain_IsolatesEveryUserStore(t *testing.T) {
	for _, p := range isolationProblems() {
		t.Error(p)
	}
}

// isolationProblems lists every way the current process could still reach
// the developer's real config or credential stores; empty means isolated.
func isolationProblems() []string {
	var problems []string
	tmp := os.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		v := os.Getenv(k)
		if v == "" || !strings.HasPrefix(v, tmp) {
			problems = append(problems, fmt.Sprintf("%s = %q, want a temp dir under %s — TestMain must isolate it", k, v, tmp))
		}
		if realUserHome != "" && v == realUserHome {
			problems = append(problems, fmt.Sprintf("%s is the developer's real home %q", k, v))
		}
	}
	if home, err := os.UserHomeDir(); err != nil || home == realUserHome {
		problems = append(problems, fmt.Sprintf("os.UserHomeDir() = (%q, %v): still the developer's real home", home, err))
	}
	if p, err := config.Path(); err != nil || !strings.HasPrefix(p, tmp) {
		problems = append(problems, fmt.Sprintf("config.Path() = (%q, %v), want a path under %s", p, err, tmp))
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
