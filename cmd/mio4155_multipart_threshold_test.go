package cmd

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

var staleMultipartClaims = []struct {
	name string
	re   *regexp.Regexp
}{
	{"claims uploads are single-part only", regexp.MustCompile(`(?i)single[- ]part(?:\s+uploads?)?\s+only`)},
	{"calls multipart a follow-on", regexp.MustCompile(`(?i)multipart[^.]*\bfollow[- ]on\b`)},
	{"describes the command as single-part, unconditionally", regexp.MustCompile(`(?i)(?:^|[.!?]\s+)single[- ]part(?:\s+upload)?\.`)},
}

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
		line := anchoredDocLine(t, s.file, s.anchor)
		if line == "" {
			continue
		}
		if !strings.Contains(line, want) {
			t.Errorf("%s: the %q line does not state the multipart threshold (%q) — agents execute this text",
				s.file, s.anchor, want)
		}
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

func anchoredDocLine(t *testing.T, file, anchor string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, anchor) {
			return l
		}
	}
	t.Errorf("%s has no line containing %q to check", file, anchor)
	return ""
}

func TestMultipartThreshold_StatedBoundaryIsTheWire(t *testing.T) {
	threshold := int64(autoMultipartThreshold)

	var atThresholdSingle []bool
	for _, path := range []string{"mio media files upload", "mio media files replace"} {
		mc := multipartCapableCommands[path]
		switch got, code := dispatchFirstRequest(t, mc, threshold); got {
		case mc.singleInit:
			atThresholdSingle = append(atThresholdSingle, true)
		case mc.multiInit:
			atThresholdSingle = append(atThresholdSingle, false)
		default:
			t.Fatalf("%s on a %d-byte file sent %q first (exit %d), neither init route", path, threshold, got, code)
		}
	}
	if atThresholdSingle[0] != atThresholdSingle[1] {
		t.Fatalf("upload and replace disagree about a file of exactly %d bytes (upload single-PUT: %v, "+
			"replace single-PUT: %v); every surface says they share one threshold", threshold,
			atThresholdSingle[0], atThresholdSingle[1])
	}
	wireSingle := atThresholdSingle[0]
	statedBytes := regexp.MustCompile(`\b(\d+) bytes\b`)

	surfaces := map[string]string{}
	for path, c := range commandsWithMultipartFlag() {
		surfaces[path+" (Long)"] = c.Long
		c.LocalFlags().VisitAll(func(f *pflag.Flag) { surfaces[path+" --"+f.Name] = f.Usage })
	}
	surfaces["mio media (Long)"] = mediaCmd.Long
	surfaces["mio media files (Long)"] = mediaFilesCmd.Long
	for _, s := range multipartDocSurfaces {
		surfaces[fmt.Sprintf("%s (the %q line)", s.file, s.anchor)] = anchoredDocLine(t, s.file, s.anchor)
	}
	keys := make([]string, 0, len(surfaces))
	for k := range surfaces {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var docLinesStatingBytes, canonicalClauses int
	for _, where := range keys {
		text := surfaces[where]
		bytesMatches := statedBytes.FindAllStringSubmatch(text, -1)
		for _, m := range bytesMatches {
			if m[1] != strconv.FormatInt(threshold, 10) {
				t.Errorf("%s states %q, but the multipart threshold is %d bytes", where, m[0], threshold)
			}
		}
		if len(bytesMatches) > 0 && strings.HasPrefix(where, "../") {
			docLinesStatingBytes++
		}
		accepted := checkBoundaryWording(text, threshold, wireSingle, func(format string, args ...any) {
			t.Errorf("%s %s", where, fmt.Sprintf(format, args...))
		})
		canonicalClauses += accepted
		if len(bytesMatches) > 0 && accepted == 0 {
			t.Errorf("%s states the threshold in bytes but not where a file of exactly that size goes; "+
				"add one of %q", where, canonicalBoundaryClauses(wireSingle))
		}
	}
	if docLinesStatingBytes == 0 {
		t.Errorf("no doc line states the multipart threshold in bytes — the byte and boundary checks above are vacuous")
	}
	if canonicalClauses == 0 {
		t.Errorf("no surface carries a canonical boundary clause %q — the boundary check above is vacuous",
			canonicalBoundaryClauses(wireSingle))
	}
}

func canonicalBoundaryClauses(wireSingle bool) []string {
	if wireSingle {
		return []string{"at or below it is one presigned PUT", "of that size or smaller goes up as one presigned PUT"}
	}
	return []string{"at or above it goes multipart", "of that size or larger goes multipart"}
}

func thresholdEdgeWording(threshold int64) *regexp.Regexp {
	digits := strconv.FormatInt(threshold, 10)
	var grouped strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteString(",?")
		}
		grouped.WriteRune(d)
	}
	num := fmt.Sprintf(`(?:%d ?Mi?B|%s bytes)`, threshold/(1024*1024), grouped.String())
	named := `(?:` + num + `|that size|the threshold)`
	paren := `(?:\s*\([^)]*\))?`
	return regexp.MustCompile(`(?i)` + strings.Join([]string{
		`\bexactly ` + named + `\b`,
		`\bat or (?:above|below|over|under) (?:` + named + `|it)\b`,
		`\b` + named + `\b` + paren + ` (?:or|and) (?:more|less|larger|smaller|bigger|greater|fewer|over|under|above|below|up)\b`,
		`\b(?:at least|at most|up to and including|no (?:more|less|larger|smaller|bigger|greater) than) ` + named + `\b`,
		`\bfrom ` + num + `\b` + paren + ` (?:up|upwards?|onwards?)\b`,
		`\ba ` + num + `\b` + paren + ` (?:file|upload|replacement)\b`,
		`(?:>=|<=|≥|≤) ?` + num + `\b`,
	}, "|"))
}

func checkBoundaryWording(text string, threshold int64, wireSingle bool, errorf func(format string, args ...any)) (accepted int) {
	text = strings.Join(strings.Fields(text), " ")
	var allowed [][2]int
	for _, c := range canonicalBoundaryClauses(wireSingle) {
		for from := 0; ; {
			i := strings.Index(text[from:], c)
			if i < 0 {
				break
			}
			allowed = append(allowed, [2]int{from + i, from + i + len(c)})
			from += i + len(c)
		}
	}
	wireSays := map[bool]string{true: "goes up as one presigned PUT", false: "goes multipart"}[wireSingle]
	for _, m := range thresholdEdgeWording(threshold).FindAllStringIndex(text, -1) {
		inside := false
		for _, a := range allowed {
			if m[0] >= a[0] && m[1] <= a[1] {
				inside = true
				break
			}
		}
		if inside {
			accepted++
			continue
		}
		errorf("says %q about a file at the multipart threshold. A file of exactly %d bytes %s, and the only "+
			"wording accepted for that is one of %q", text[m[0]:m[1]], threshold, wireSays, canonicalBoundaryClauses(wireSingle))
	}
	return accepted
}

func TestMultipartBoundaryWording_Cases(t *testing.T) {
	threshold := int64(autoMultipartThreshold)
	check := func(text string, wireSingle bool) (msgs []string, accepted int) {
		accepted = checkBoundaryWording(text, threshold, wireSingle, func(format string, args ...any) {
			msgs = append(msgs, fmt.Sprintf(format, args...))
		})
		return msgs, accepted
	}
	flagged := []string{
		"a file of exactly 100 MB also goes multipart",
		"a file of exactly 100 MB (104857600 bytes) goes multipart",
		"at exactly 100 MB, uploads go multipart",
		"a file of exactly 100 MB is split into parts",
		"exactly 100 MiB goes multipart",
		"a file of exactly 100 MB is not one PUT",
		"at or above it goes multipart",
		"a file of that size or larger goes multipart",
		"100 MB or more goes multipart",
		"100 MB (104857600 bytes) or more goes multipart",
		"104,857,600 bytes and up go multipart",
		"at least 100 MB is sent in parts",
		"a 100 MB file goes multipart",
		"from 100 MB up, uploads go multipart",
		">= 100 MB",
		"a file of exactly 100 MB (104857600 bytes) goes up as one presigned PUT",
		"a file of exactly 100 MB does not go multipart",
		"100 MB or less is one PUT",
		"up to and including 104857600 bytes is one PUT",
	}
	for _, text := range flagged {
		if msgs, _ := check(text, true); len(msgs) == 0 {
			t.Errorf("wire=single-PUT at the threshold: %q says something about the threshold's edge but was not flagged", text)
		}
	}
	unflagged := []string{
		"multipart above 100 MB (104857600 bytes) — at or below it is one presigned PUT — or at any size with `--multipart`",
		"a file larger than 104857600 bytes is sent in parts; a file of that size or smaller goes up as one presigned PUT.",
		"a file of that size or smaller goes up as one\npresigned PUT",
		"multipart above 100 MB or at any size with --multipart, exactly as upload",
		"above 100 MB (104857600 bytes) or with `--multipart`",
		"--multipart forces the multipart path at any size",
		"`--part-size-mb` default 16, min 5",
		"reads the upload id from it, at least once",
	}
	for _, text := range unflagged {
		if msgs, _ := check(text, true); len(msgs) != 0 {
			t.Errorf("wire=single-PUT at the threshold: %q was flagged but is canonical or says nothing about the edge: %v", text, msgs)
		}
	}
	for _, wireSingle := range []bool{true, false} {
		for _, clause := range canonicalBoundaryClauses(wireSingle) {
			if msgs, accepted := check(clause, wireSingle); len(msgs) != 0 || accepted != 1 {
				t.Errorf("wire single-PUT=%v: canonical clause %q gave %d accepted, messages %v; want 1 accepted, none",
					wireSingle, clause, accepted, msgs)
			}
			if msgs, accepted := check(clause, !wireSingle); len(msgs) == 0 || accepted != 0 {
				t.Errorf("wire single-PUT=%v: the other direction's clause %q was accepted (%d accepted, messages %v)",
					!wireSingle, clause, accepted, msgs)
			}
		}
	}
}
