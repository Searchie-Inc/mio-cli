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

var isolationRoot string

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
	if problems := isolationProblems(); len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "TestMain: refusing to run: the config tests are not isolated from the developer's real stores:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  - "+p)
		}
		return 1
	}
	return m.Run()
}

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

func TestMain_IsolatesEveryUserStore(t *testing.T) {
	for _, p := range isolationProblems() {
		t.Error(p)
	}
}

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
