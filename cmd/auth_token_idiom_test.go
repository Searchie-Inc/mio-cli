package cmd

// auth_token_idiom_test.go — MIO-2995 blind review: the docs told agents to
// run `export MIO_API_KEY="$(mio auth token)"`, which returns export's own
// status, so under `set -e` a missing key exported an EMPTY MIO_API_KEY and the
// script ran on. These tests hold the documented idiom to what bash actually
// does, and every doc surface to that idiom.

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

// TestAuthTokenExportIdiom_StopsASetEScript runs the documented idiom under
// bash with a `mio` on PATH that answers the way `mio auth token` is pinned to
// answer — exit 3 and empty stdout with no stored key (TestAuthToken_NoStoredKey),
// the key and a newline with one (TestAuthToken_PrintsTheStoredKeyAndNothingElse).
// The oracle is bash itself: with no key a `set -e` script must stop AT the
// idiom with exit 3, and with one the key must reach a child process.
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
				// Nothing inherited but PATH: no MIO_API_KEY can leak in.
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

// authTokenCapture finds a shell capture of `mio auth token`'s stdout: a
// $(…) substitution, or an old-style backtick one right after an assignment
// (elsewhere a backticked `mio auth token` is a Markdown code span, not a
// capture). Anything may sit between the opener and `mio`, and between `mio`
// and `auth token`: an env assignment, `command`, a path, global flags.
var authTokenCapture = regexp.MustCompile("(\\$\\(|=[\"']?`)[^)`]*\\bmio\\b[^)`]*\\bauth\\s+token\\b")

// exportsTheKey matches the second statement of authTokenExport alone on its
// line, optionally followed by a comment.
var exportsTheKey = regexp.MustCompile(`^\s*export MIO_API_KEY\s*(#.*)?$`)

// TestAuthTokenExportIdiom_EveryDocUsesIt: wherever the docs capture `mio auth
// token` (authTokenCapture) — every Markdown and text file in the repo, and
// every command's help — it must be as authTokenExport (its first statement
// alone on its line, `export MIO_API_KEY` alone on the next) or
// authTokenExportOneLine, the forms the test above runs. Any other shape,
// `export MIO_API_KEY="$(mio auth token)"` above all, is named by file and
// line. A capture it does not recognise as one (a `mio` reached through an
// alias or a variable) is outside what it can see.
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
			// .claude/worktrees holds OTHER branches' checkouts.
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
			// The two-statement form: the first statement ALONE on its line (a
			// trailing comment aside — anything else, `|| true` say, can swallow
			// the exit 3), and exactly `export MIO_API_KEY` on the next.
			if after, ok := strings.CutPrefix(strings.TrimSpace(rest), first); ok && isBlankOrComment(after) &&
				i+1 < len(lines) && exportsTheKey.MatchString(lines[i+1]) {
				continue
			}
			t.Errorf("%s:%d captures `mio auth token` outside the documented idiom, so its exit 3 may never reach set -e:\n"+
				"  %s\nuse the two statements\n  %s\nor, on one line, %s",
				name, i+1, strings.TrimSpace(line), strings.ReplaceAll(authTokenExport, "\n", "\n  "), authTokenExportOneLine)
		}
	}
	// A floor, so a scan that silently stopped reading the surfaces agents
	// execute cannot pass: each of these documents the export today.
	for _, must := range []string{"AGENTS.md", "README.md", "llms.txt", "cmd/skills/content/mio-skill.md", "`mio auth token --help`"} {
		if captures[must] == 0 {
			t.Errorf("%s never captures `mio auth token`: the scan is not reading it, or it lost its export instructions", must)
		}
	}
}

// stripOneLineIdiom removes every occurrence of authTokenExportOneLine from
// line and reports whether any of them was not a statement of its own. A
// prefix such as `local` or `readonly` returns its own status (0), which hides
// the exit 3 from set -e exactly like `export` does, so the only thing allowed
// just before the idiom is the start of the line, the backtick opening a
// Markdown code span, or `(`, `:`, `;` or `{`. The idiom must also end its
// statement: `export MIO_API_KEY_OTHER` is not an export of the key.
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

// isBlankOrComment reports whether what follows a shell statement on its line
// is nothing but whitespace or a comment.
func isBlankOrComment(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.HasPrefix(s, "#")
}
