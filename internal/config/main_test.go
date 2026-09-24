package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99designs/keyring"
)

// isolationRoot is the temp dir TestMain created for this run. Every location a
// store can be derived from must resolve under it; see isolationProblems.
var isolationRoot string

// TestMain keeps every test in this package away from the developer's real
// config and credential stores, whether or not the test remembers withXDG and
// withFileBackendOnly (MIO-2995). It mirrors cmd's TestMain (cmd/main_test.go);
// the helper cannot be shared, because a package that imports config cannot be
// imported by config's own tests.
//
//   - HOME and USERPROFILE point at a fresh temp dir, and XDG_CONFIG_HOME at
//     another, for the whole run — overridden even when already set, since a
//     caller's XDG_CONFIG_HOME may BE the real one. withXDG still moves them
//     per test; this is the floor for a test that forgets it, and for a bug
//     that derives a path some other way (during MIO-2995 a store path wrongly
//     derived from $HOME replaced the real ~/.config/mio/keyring/api-key from a
//     unit test in this package).
//   - The keyring is pinned to the encrypted FILE backend. Resolve reads the
//     stored key whenever no flag or MIO_API_KEY supplies one, so every Resolve
//     test that calls withXDG but not withFileBackendOnly would otherwise open
//     the first OS store the build can reach: the developer's Secret Service on
//     a Linux desktop, or the login Keychain in a macOS cgo `go test` — stores
//     no HOME or XDG_CONFIG_HOME sandbox redirects.
//
// Every home-directory lookup on the store path goes through os.UserHomeDir,
// which reads these variables, so pinning them redirects it; nothing here or in
// 99designs/keyring asks os/user (the passwd database) for a home directory.
//
// No test here launches the Go toolchain today, but moving HOME would also move
// its caches for one that did, so their current locations are pinned first.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	root, err := os.MkdirTemp("", "mio-config-test-")
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

	pinGoToolchainDirs()

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
	restore := UseFileBackendOnly()
	defer restore()
	// Check BEFORE any test runs: TestMain_IsolatesEveryUserStore reports the
	// same problems, but only after every test sorted ahead of it has already
	// had its chance to reach a real store.
	if problems := isolationProblems(); len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "TestMain: refusing to run: the config tests are not isolated from the developer's real stores:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  - "+p)
		}
		return 1
	}
	return m.Run()
}

// pinGoToolchainDirs exports the Go build cache, module cache, GOPATH and
// go-env file locations as they are NOW, so a `go` command launched after HOME
// and XDG_CONFIG_HOME move keeps using the developer's real caches and
// `go env -w` settings. A value already set in the environment is left alone.
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

// TestMain_IsolatesEveryUserStore pins TestMain's isolation: HOME,
// USERPROFILE, XDG_CONFIG_HOME, the home directory os.UserHomeDir derives from
// them, the config path, and the credential store this package actually opens
// must all lie inside the temp dir TestMain created, and the keyring must be
// pinned to the file backend. Inside TestMain's own dir, not merely inside
// os.TempDir(): a caller that already runs `go test` under a temp HOME would
// otherwise satisfy a temp-dir check with TestMain's pinning deleted. TestMain
// runs the same checks before any test and refuses to start if one fails; this
// test names them in the report.
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
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		if v := os.Getenv(k); !inRoot(v) {
			problems = append(problems, fmt.Sprintf("%s = %q, want a dir under TestMain's %q — TestMain must pin it", k, v, isolationRoot))
		}
	}
	if home, err := os.UserHomeDir(); err != nil || !inRoot(home) {
		problems = append(problems, fmt.Sprintf("os.UserHomeDir() = (%q, %v), want a dir under %q", home, err, isolationRoot))
	}
	if p, err := Path(); err != nil || !inRoot(p) {
		problems = append(problems, fmt.Sprintf("Path() = (%q, %v), want a path under %q", p, err, isolationRoot))
	}
	// On a machine with no OS store the file backend is chosen whether or not
	// it is pinned, so the store actually opened can look right with the pin
	// missing; the allowed list is what shows it.
	if b := KeyringBackends(); len(b) != 1 || b[0] != keyring.FileBackend {
		problems = append(problems, fmt.Sprintf("keyring backends = %v, want only %q: without UseFileBackendOnly a test that "+
			"resolves or stores a key reaches the first OS store this build can open (a desktop Secret Service or KWallet, "+
			"the macOS Keychain in a cgo build)", b, keyring.FileBackend))
	}
	if _, store, err := openKeyring(); err != nil || store.Backend != keyring.FileBackend || !inRoot(store.Path) {
		problems = append(problems, fmt.Sprintf("openKeyring() = (%s %q, %v), want the file backend under %q",
			store.Backend, store.Path, err, isolationRoot))
	}
	return problems
}
