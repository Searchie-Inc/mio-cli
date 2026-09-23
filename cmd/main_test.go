package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
// config path they resolve to — must be a temp dir and not the developer's.
// During MIO-2995 a store path wrongly derived from $HOME replaced the real
// ~/.config/mio/keyring/api-key from a unit test; this is what stops that
// from reaching a real store again.
func TestMain_IsolatesEveryUserStore(t *testing.T) {
	tmp := os.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		v := os.Getenv(k)
		if v == "" || !strings.HasPrefix(v, tmp) {
			t.Errorf("%s = %q, want a temp dir under %s — TestMain must isolate it", k, v, tmp)
		}
		if realUserHome != "" && v == realUserHome {
			t.Errorf("%s is the developer's real home %q", k, v)
		}
	}
	if home, err := os.UserHomeDir(); err != nil || home == realUserHome {
		t.Errorf("os.UserHomeDir() = (%q, %v): still the developer's real home", home, err)
	}
	if p, err := config.Path(); err != nil || !strings.HasPrefix(p, tmp) {
		t.Errorf("config.Path() = (%q, %v), want a path under %s", p, err, tmp)
	}
}
