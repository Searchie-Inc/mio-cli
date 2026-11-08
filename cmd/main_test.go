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

var isolationRoot string

var moduleRoot string

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
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	isolationRoot = root
	if wd, err := os.Getwd(); err == nil {
		if resolved, err := filepath.EvalSymlinks(wd); err == nil {
			wd = resolved
		}
		moduleRoot = filepath.Dir(wd)
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
	if problems := isolationProblems(); len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "TestMain: refusing to run: the cmd tests are not isolated from the developer's real stores:")
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
	if b := config.KeyringBackends(); len(b) != 1 || b[0] != keyring.FileBackend {
		problems = append(problems, fmt.Sprintf("keyring backends = %v, want only %q: without config.UseFileBackendOnly a test that "+
			"stores or reads a key reaches the first OS store this build can open (the macOS Keychain in a cgo build, "+
			"a desktop Secret Service or KWallet)", b, keyring.FileBackend))
	}
	return problems
}

func requireSkillPathInSandbox(path string) {
	if problem := skillPathProblem(path); problem != "" {
		panic(problem)
	}
}

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
