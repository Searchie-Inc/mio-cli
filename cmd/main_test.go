package cmd

import (
	"fmt"
	"os"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/config"
)

// TestMain keeps every test in this package away from the developer's real
// credential stores and config (MIO-2995):
//
//   - XDG_CONFIG_HOME points at a fresh temp dir for the whole run, so a test
//     that does not set its own never reads or writes ~/.config/mio. (Before
//     this, a bare `go test ./...` read the real config.toml and stored key —
//     TestContract_ExitCodes_NoCredentials and TestWiring_SingleHubAutoDefault
//     failed on any machine that had run `mio login`.) It is overridden even
//     when already set, since a caller's XDG_CONFIG_HOME may BE the real one.
//   - The keyring is pinned to the encrypted FILE backend, under that config
//     dir. Otherwise a test that resolves or stores a key without MIO_API_KEY —
//     the login, register and no-credentials tests among them — reaches the
//     first OS store the build can open: on a macOS machine running a cgo
//     `go test`, the developer's real login Keychain, which no XDG_CONFIG_HOME
//     sandbox reaches.
//
// Subprocess tests (buildBinary/runBinary) are not covered by the keyring pin;
// runBinary gives them an isolated HOME instead.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	cfgHome, err := os.MkdirTemp("", "mio-cmd-test-config-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: create isolated config dir:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(cfgHome) }()
	if err := os.Setenv("XDG_CONFIG_HOME", cfgHome); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: set XDG_CONFIG_HOME:", err)
		return 1
	}
	restore := config.UseFileBackendOnly()
	defer restore()
	return m.Run()
}
