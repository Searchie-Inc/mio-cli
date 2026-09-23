package cmd

// mio4155_multipart_threshold_test.go — MIO-4155: the multipart story has to be
// ONE statement everywhere, and that statement has to be what the wire does.
//
// Before this, `media files upload --help` said "Single-part upload only
// (multipart for very large files is a follow-on)" in the same help that listed
// --multipart and --part-size-mb, `files replace` called itself "Single-part
// upload.", and the group help and the docs said "auto-multipart for large
// files" with no number — while the code has switched to multipart above
// autoMultipartThreshold (100 MB) since MIO-2267.
//
// The guards below take the threshold FROM THE HELP TEXT and drive the real
// command across it, so the oracle is the request the CLI actually sends, not a
// second copy of the number.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// multipartCapable describes one command that can take the multipart path: how
// to invoke it on a local file, and the first request each path sends. Those
// init routes are the only thing that distinguishes the two paths before any
// byte moves, so the stub can refuse them and the test never streams a file.
type multipartCapable struct {
	args       func(path string) []string
	singleInit string
	multiInit  string
}

var multipartCapableCommands = map[string]multipartCapable{
	"mio media files upload": {
		args:       func(p string) []string { return []string{"media", "files", "upload", p} },
		singleInit: "POST /api/v1/teams/t_team1/files",
		multiInit:  "POST /api/v1/teams/t_team1/files/multipart",
	},
	"mio media files replace": {
		args:       func(p string) []string { return []string{"media", "files", "replace", "file_x", p} },
		singleInit: "POST /api/v1/teams/t_team1/files/file_x/replace",
		multiInit:  "POST /api/v1/teams/t_team1/files/file_x/replace/multipart",
	},
}

// commandsWithMultipartFlag walks the real command tree for every command that
// registers a --multipart flag — the set of commands that can dispatch either
// way, found from the tree rather than from a list.
func commandsWithMultipartFlag() map[string]*cobra.Command {
	out := map[string]*cobra.Command{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.LocalFlags().Lookup("multipart") != nil {
			out[c.CommandPath()] = c
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(RootCmd())
	return out
}

// firstRequestRecorder answers every request 422 and records them all, so a
// dispatch run stops at its first request and the test reads which init route
// that was.
func firstRequestRecorder(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"status":"422","detail":"stopped by test after the init request"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// sparseFile creates a file of exactly size bytes without writing them — the
// CLI only stats it before the init request, and the stub refuses the init.
func sparseFile(t *testing.T, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("f-%d.bin", size))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// dispatchFirstRequest runs the real command on a file of the given size and
// returns the first request it sent ("" when none fired) plus the exit code.
func dispatchFirstRequest(t *testing.T, mc multipartCapable, size int64, extra ...string) (string, int) {
	t.Helper()
	srv, seen := firstRequestRecorder(t)
	args := append(mc.args(sparseFile(t, size)), extra...)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
	reqs := seen()
	if len(reqs) == 0 {
		return "", res.Code
	}
	return reqs[0], res.Code
}

var helpBytesThreshold = regexp.MustCompile(`\b(\d+) bytes\b`)
var helpPartMinimum = regexp.MustCompile(`\bminimum (\d+)\b`)
var helpPartDefault = regexp.MustCompile(`\bdefault (\d+)\b`)

// statedNumber returns the single number re captures in text. Zero matches, or
// two different numbers, is a help text that does not make one statement.
func statedNumber(t *testing.T, re *regexp.Regexp, text, what string) int64 {
	t.Helper()
	vals := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		vals[m[1]] = true
	}
	if len(vals) != 1 {
		t.Fatalf("help must state exactly one %s (matching %s); found %d: %v\nhelp:\n%s",
			what, re, len(vals), vals, text)
	}
	for v := range vals {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	return 0
}

// TestMultipartThreshold_HelpIsWhatTheWireDoes: for every command that can go
// multipart, read the threshold its --help states and drive the real command on
// a file exactly that size (must take the single-PUT path) and one byte larger
// (must take the multipart path). Changing the threshold in code without the
// help — or the help without the code — sends the other init request.
func TestMultipartThreshold_HelpIsWhatTheWireDoes(t *testing.T) {
	found := commandsWithMultipartFlag()
	for path := range found {
		if _, ok := multipartCapableCommands[path]; !ok {
			t.Errorf("%s registers --multipart but has no dispatch case here — add it, so its "+
				"help's threshold is checked against what it sends", path)
		}
	}
	for path := range multipartCapableCommands {
		if _, ok := found[path]; !ok {
			t.Errorf("%s has a dispatch case but no --multipart flag in the command tree", path)
		}
	}

	for path, mc := range multipartCapableCommands {
		c := found[path]
		if c == nil {
			continue
		}
		t.Run(path, func(t *testing.T) {
			n := statedNumber(t, helpBytesThreshold, c.Long, "multipart threshold in bytes")

			if got, code := dispatchFirstRequest(t, mc, n); got != mc.singleInit {
				t.Errorf("--help says a file of %d bytes goes up in one presigned PUT, but the command "+
					"sent %q first (exit %d), want %q", n, got, code, mc.singleInit)
			}
			if got, code := dispatchFirstRequest(t, mc, n+1); got != mc.multiInit {
				t.Errorf("--help says a file larger than %d bytes goes multipart automatically, but a "+
					"%d-byte file sent %q first (exit %d), want %q", n, n+1, got, code, mc.multiInit)
			}
		})
	}
}

// TestMultipartPartSize_HelpIsWhatTheCommandEnforces: the minimum part size the
// help states is the one the command enforces before any request — on the forced
// AND the automatic multipart path — and the flag is read only on the multipart
// path (a small file without --multipart ignores it rather than rejecting it).
// The default the help states is the flag's real default.
func TestMultipartPartSize_HelpIsWhatTheCommandEnforces(t *testing.T) {
	found := commandsWithMultipartFlag()
	for path, mc := range multipartCapableCommands {
		c := found[path]
		if c == nil {
			t.Errorf("%s not found in the command tree", path)
			continue
		}
		t.Run(path, func(t *testing.T) {
			minMB := statedNumber(t, helpPartMinimum, c.Long, "minimum part size")
			below := strconv.FormatInt(minMB-1, 10)

			if def := statedNumber(t, helpPartDefault, c.Long, "default part size"); strconv.FormatInt(def, 10) != c.Flags().Lookup("part-size-mb").DefValue {
				t.Errorf("--help says the part size default is %d, but --part-size-mb defaults to %s",
					def, c.Flags().Lookup("part-size-mb").DefValue)
			}

			if got, code := dispatchFirstRequest(t, mc, 1, "--multipart", "--part-size-mb", below); got != "" || code != errs.ExitUsage {
				t.Errorf("--help says the part size minimum is %d, but --multipart --part-size-mb %s "+
					"sent %q (exit %d), want no request and exit %d", minMB, below, got, code, errs.ExitUsage)
			}
			// The automatic path must enforce the same floor: a file one byte over the
			// threshold the help states goes multipart with no flag, so a part size
			// under the minimum has to stop it before the init request too.
			over := statedNumber(t, helpBytesThreshold, c.Long, "multipart threshold in bytes") + 1
			if got, code := dispatchFirstRequest(t, mc, over, "--part-size-mb", below); got != "" || code != errs.ExitUsage {
				t.Errorf("--help says the part size minimum is %d, but a %d-byte file (multipart automatically, "+
					"no --multipart) with --part-size-mb %s sent %q (exit %d), want no request and exit %d",
					minMB, over, below, got, code, errs.ExitUsage)
			}
			if got, _ := dispatchFirstRequest(t, mc, 1, "--multipart", "--part-size-mb", strconv.FormatInt(minMB, 10)); got != mc.multiInit {
				t.Errorf("--help says the part size minimum is %d, but --multipart --part-size-mb %d "+
					"sent %q first, want %q", minMB, minMB, got, mc.multiInit)
			}
			if got, _ := dispatchFirstRequest(t, mc, 1, "--part-size-mb", below); got != mc.singleInit {
				t.Errorf("--help says --part-size-mb is read only on the multipart path, but a 1-byte "+
					"file with --part-size-mb %s and no --multipart sent %q first, want %q",
					below, got, mc.singleInit)
			}
		})
	}
}

// staleMultipartClaims are the phrasings MIO-4155 found in shipped help. Each
// contradicts the multipart dispatch that has existed since MIO-2267.
var staleMultipartClaims = []struct {
	name string
	re   *regexp.Regexp
}{
	{"claims uploads are single-part only", regexp.MustCompile(`(?i)single[- ]part(?:\s+uploads?)?\s+only`)},
	{"calls multipart a follow-on", regexp.MustCompile(`(?i)multipart[^.]*\bfollow[- ]on\b`)},
	{"describes the command as single-part, unconditionally", regexp.MustCompile(`(?i)(?:^|[.!?]\s+)single[- ]part(?:\s+upload)?\.`)},
}

// helpTexts returns every piece of help the command tree renders, keyed by where
// it lives.
func helpTexts() map[string]string {
	out := map[string]string{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		p := c.CommandPath()
		out[p+" (Short)"] = c.Short
		out[p+" (Long)"] = c.Long
		out[p+" (Example)"] = c.Example
		visit := func(f *pflag.Flag) { out[p+" --"+f.Name] = f.Usage }
		c.LocalFlags().VisitAll(visit)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(RootCmd())
	return out
}

// multipartDocSurfaces are the hand-maintained documents that describe
// `media files upload` / `replace`, and the line in each that does.
var multipartDocSurfaces = []struct {
	file   string
	anchor string
}{
	{"../README.md", "| `media files` |"},
	{"../AGENTS.md", "| `media files` |"},
	{"../llms.txt", "mio media files upload —"},
	{"../llms.txt", "mio media files replace —"},
	{"../docs/internal/api-surface.md", "- upload   POST"},
	{"../docs/internal/api-surface.md", "- replace  single-part"},
	{"../cmd/skills/content/mio-skill.md", "- **Media:** `mio media files upload"},
}

// TestMultipartHelp_NoSinglePartOnlyClaim: no help anywhere in the tree — and
// no hand-maintained doc surface — may claim single-part only.
func TestMultipartHelp_NoSinglePartOnlyClaim(t *testing.T) {
	texts := helpTexts()
	for _, s := range multipartDocSurfaces {
		if _, done := texts[s.file]; done {
			continue
		}
		b, err := os.ReadFile(s.file)
		if err != nil {
			t.Fatalf("read %s: %v", s.file, err)
		}
		texts[s.file] = string(b)
	}
	keys := make([]string, 0, len(texts))
	for k := range texts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, where := range keys {
		for _, claim := range staleMultipartClaims {
			if m := claim.re.FindString(texts[where]); m != "" {
				t.Errorf("%s %s (%q) — upload and replace go multipart above %d bytes or with --multipart",
					where, claim.name, strings.TrimSpace(m), autoMultipartThreshold)
			}
		}
	}
}

// TestMultipartThreshold_EverySurfaceStatesIt: the group help, the flag help and
// every hand-maintained doc line about upload/replace state the threshold the
// code dispatches on. Before MIO-4155 they said "auto-multipart for large files"
// and the reader had to guess.
func TestMultipartThreshold_EverySurfaceStatesIt(t *testing.T) {
	const mib = 1024 * 1024
	if autoMultipartThreshold%mib != 0 {
		t.Fatalf("autoMultipartThreshold (%d) is not a whole number of MB; the surfaces below state it in MB",
			autoMultipartThreshold)
	}
	want := fmt.Sprintf("above %d MB", autoMultipartThreshold/mib)

	help := map[string]string{
		"mio media (Long)":       mediaCmd.Long,
		"mio media files (Long)": mediaFilesCmd.Long,
	}
	for path, c := range commandsWithMultipartFlag() {
		help[path+" --multipart"] = c.LocalFlags().Lookup("multipart").Usage
	}
	for where, text := range help {
		if !strings.Contains(text, want) {
			t.Errorf("%s does not state the multipart threshold (%q); it reads:\n%s", where, want, text)
		}
	}

	var statedDefaults, statedMinimums int
	for _, s := range multipartDocSurfaces {
		b, err := os.ReadFile(s.file)
		if err != nil {
			t.Fatalf("read %s: %v", s.file, err)
		}
		var line string
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, s.anchor) {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("%s has no line containing %q to check", s.file, s.anchor)
			continue
		}
		if !strings.Contains(line, want) {
			t.Errorf("%s: the %q line does not state the multipart threshold (%q) — agents execute this text",
				s.file, s.anchor, want)
		}
		// A part-size default or minimum a doc line states must be the one the
		// flag and the preflight use; the help is rendered from the constants, the
		// docs are not.
		for _, check := range []struct {
			re   *regexp.Regexp
			want int
			seen *int
		}{
			{docPartDefault, defaultPartSizeMB, &statedDefaults},
			{docPartMinimum, minPartSizeMB, &statedMinimums},
		} {
			for _, m := range check.re.FindAllStringSubmatch(line, -1) {
				*check.seen++
				if m[1] != strconv.Itoa(check.want) {
					t.Errorf("%s: the %q line states %q, but the code uses %d", s.file, s.anchor, m[0], check.want)
				}
			}
		}
	}
	if statedDefaults == 0 || statedMinimums == 0 {
		t.Errorf("no doc line states the --part-size-mb default (%d lines) or minimum (%d lines) — the check above "+
			"is vacuous", statedDefaults, statedMinimums)
	}
}

var docPartDefault = regexp.MustCompile(`\bdefault (\d+)\b`)
var docPartMinimum = regexp.MustCompile(`\bmin(?:imum)? (\d+)\b`)
