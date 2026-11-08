package cmd

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestAuthTokenExportIdiom_StopsASetEScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the idiom is POSIX shell")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	const key = "mio_sk_live_idiom"
	stubs := []struct{ name, body string }{
		{"no stored key", "#!/bin/sh\necho 'no API key stored' >&2\nexit 3\n"},
		{"stored key", "#!/bin/sh\necho " + key + "\n"},
	}
	for _, form := range []struct{ name, snippet string }{
		{"two statements", authTokenExport},
		{"one line", authTokenExportOneLine},
	} {
		for _, stub := range stubs {
			t.Run(form.name+"/"+stub.name, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "mio"), []byte(stub.body), 0o755); err != nil {
					t.Fatalf("write the mio stub: %v", err)
				}
				script := "set -euo pipefail\n" + form.snippet + "\n" +
					`printf 'child sees [%s]\n' "$(sh -c 'printf %s "${MIO_API_KEY-unset}"')"` + "\n"
				run := exec.Command(bash, "-c", script)
				run.Env = []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}
				out, runErr := run.Output()
				code := 0
				var exitErr *exec.ExitError
				switch {
				case errors.As(runErr, &exitErr):
					code = exitErr.ExitCode()
				case runErr != nil:
					t.Fatalf("run bash: %v", runErr)
				}

				if stub.name == "no stored key" {
					if code != errs.ExitAuth || strings.Contains(string(out), "child sees") {
						t.Errorf("the documented idiom %q did not stop a set -e script when no key is stored: "+
							"exit = %d, want %d, and stdout = %q — the script ran on past it without a usable key",
							form.snippet, code, errs.ExitAuth, out)
					}
					return
				}
				if want := "child sees [" + key + "]\n"; code != 0 || string(out) != want {
					t.Errorf("the documented idiom %q did not export the stored key to a child process: exit = %d, stdout = %q, want %q",
						form.snippet, code, out, want)
				}
			})
		}
	}
}

var authTokenCapture = regexp.MustCompile("(\\$\\(|=[\"']?`)[^)`]*\\bmio\\b[^)`]*\\bauth\\s+token\\b")

var exportsTheKey = regexp.MustCompile(`^\s*export MIO_API_KEY\s*(#.*)?$`)

func TestAuthTokenExportIdiom_EveryDocUsesIt(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	surfaces := map[string]string{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || path == filepath.Join(root, ".claude", "worktrees") {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".md" && ext != ".txt" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		surfaces[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
	var addHelp func(c *cobra.Command)
	addHelp = func(c *cobra.Command) {
		surfaces["`"+c.CommandPath()+" --help`"] = c.Long + "\n" + c.Example
		for _, sub := range c.Commands() {
			addHelp(sub)
		}
	}
	addHelp(RootCmd())

	first, second, _ := strings.Cut(authTokenExport, "\n")
	if !exportsTheKey.MatchString(second) {
		t.Fatalf("authTokenExport's second statement %q is not what this scan accepts on the next line", second)
	}
	captures := map[string]int{}
	for name, text := range surfaces {
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if !authTokenCapture.MatchString(line) {
				continue
			}
			captures[name]++
			rest, prefixed := stripOneLineIdiom(line)
			if prefixed {
				t.Errorf("%s:%d does not use the one-line idiom as a statement of its own: a word before it "+
					"(`local`, `readonly`, `declare`, `!`, …) hides its exit 3 from set -e, and text run onto its end "+
					"changes what is exported. Only a code-span backtick, `(`, `:`, `;` or `{` may precede it:\n  %s",
					name, i+1, strings.TrimSpace(line))
				continue
			}
			if !authTokenCapture.MatchString(rest) {
				continue
			}
			if after, ok := strings.CutPrefix(strings.TrimSpace(rest), first); ok && isBlankOrComment(after) &&
				i+1 < len(lines) && exportsTheKey.MatchString(lines[i+1]) {
				continue
			}
			t.Errorf("%s:%d captures `mio auth token` outside the documented idiom, so its exit 3 may never reach set -e:\n"+
				"  %s\nuse the two statements\n  %s\nor, on one line, %s",
				name, i+1, strings.TrimSpace(line), strings.ReplaceAll(authTokenExport, "\n", "\n  "), authTokenExportOneLine)
		}
	}
	for _, must := range []string{"AGENTS.md", "README.md", "llms.txt", "cmd/skills/content/mio-skill.md", "`mio auth token --help`"} {
		if captures[must] == 0 {
			t.Errorf("%s never captures `mio auth token`: the scan is not reading it, or it lost its export instructions", must)
		}
	}
}

func stripOneLineIdiom(line string) (rest string, prefixed bool) {
	var b strings.Builder
	for {
		i := strings.Index(line, authTokenExportOneLine)
		if i < 0 {
			b.WriteString(line)
			return b.String(), prefixed
		}
		before := strings.TrimRight(line[:i], " \t")
		after := line[i+len(authTokenExportOneLine):]
		if before != "" && !strings.ContainsRune("`(:;{", rune(before[len(before)-1])) {
			prefixed = true
		}
		if after != "" && (after[0] == '_' || after[0] == '=' ||
			'a' <= after[0] && after[0] <= 'z' || 'A' <= after[0] && after[0] <= 'Z' || '0' <= after[0] && after[0] <= '9') {
			prefixed = true
		}
		b.WriteString(line[:i])
		line = after
	}
}

func isBlankOrComment(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.HasPrefix(s, "#")
}
