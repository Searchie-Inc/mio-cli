package cmd

// doc_examples_test.go — every `mio …` example the CLI ships must be a command
// the CLI would actually accept (MIO-4154).
//
// WHY
// README's "Products & prices" section documented
//
//	mio products create --name "Pro Plan" --description "Full access"
//
// long after --type became required, so the documented command exited 2. Nothing
// noticed: the docs are hand-maintained, and an agent executes them verbatim. A
// one-line doc fix would let the class recur the next time a flag becomes
// required, is renamed, or a verb moves.
//
// WHAT THIS GUARDS
// Every invocation in a fenced code block of the shipped doc surfaces, and in
// every command's cobra Example, is resolved against the REAL root command using
// cobra's own machinery — Find for the command path, the command's real flag set
// (local + inherited + help) for parsing, and cobra's ValidateArgs,
// ValidateRequiredFlags and ValidateFlagGroups. The oracle is cobra, not a list
// kept beside it: marking a flag required (MarkFlagRequired) immediately obliges
// every example of that command to pass it, and renaming or removing a flag
// fails every example that still uses the old spelling.
//
// A failure names the file and line of the example and cobra's own error.
//
// WHAT IT DOES NOT SEE
//   - A requirement enforced only inside a RunE body. cobra cannot report it, so
//     neither can this. Declare required flags with markFlagsRequired (products
//     create moved to it in MIO-4154 for exactly this reason). MIO-4154 moved
//     the `missing required flag(s)` checks, except `hubs policies gate
//     --enabled`, which stays in RunE to keep its usage hint. That one and 43
//     others, mostly phrased `--x is required[: hint]`, still live in RunE.
//     43 of those 44 are invisible here until they move; `pages publish
//     --if-match` is seen because it is also declared with MarkFlagRequired.
//     Some are conditional and cannot move.
//     List all 44 with: grep -nE 'ExitUsage, *"[^"]*(required|missing)' cmd/*.go
//   - Flag VALUES. Every flag is parsed into a stub that accepts anything, so
//     placeholders (<id>, "$HUB_ID") never false-positive; an invalid enum value
//     in a doc is out of scope.
//   - Prose and inline `code spans`. Only fenced blocks (blockquoted ones too)
//     and Example strings are read; the extractor reports any line of them that
//     mentions `mio <word>` but that it could not turn into an invocation,
//     including an unprompted line of a `$ ` console transcript
//     (TestDocExamples_* fail on it). So llms.txt and
//     docs/internal/api-surface.md, which have no fenced blocks, are not read.
//
// To sweep another repo's docs with the same checker (the docs site, say), set
// MIO_DOC_EXAMPLES_EXTRA to a list of files/globs separated by the OS path-list
// separator and run TestDocExamples_ShippedSurfaces.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/docexamples"
)

// shippedDocSurfaces are the hand-maintained documents that ship with (or are
// generated into) the CLI and that agents execute verbatim. Paths are relative
// to this package directory. Each must yield at least one checked invocation
// (TestDocExamples_ShippedSurfaces fails otherwise), so this list cannot claim a
// surface the guard never reads.
//
// Not listed, because they hold no fenced code block and so give this checker
// nothing to read: llms.txt (a one-line-per-command index) and
// docs/internal/api-surface.md (an endpoint reference). Neither is guarded.
// docs/superpowers/ is excluded on purpose: it holds dated design plans and
// specs describing the CLI as it was then, not docs.
var shippedDocSurfaces = []string{
	"../README.md",
	"../AGENTS.md",
	"skills/content/mio-skill.md",
}

// stubValue accepts any value. The guard checks which flags an example passes,
// never what it passes, so `--amount <amount>` and `--hub "$HUB_ID"` parse.
type stubValue struct{ typ string }

func (s *stubValue) String() string   { return "" }
func (s *stubValue) Set(string) error { return nil }
func (s *stubValue) Type() string     { return s.typ }

// stubOf clones f with a stub value, keeping everything cobra's validators and
// pflag's parser read: name, shorthand, NoOptDefVal (whether a value must
// follow) and annotations (required, flag groups).
func stubOf(f *pflag.Flag) *pflag.Flag {
	return &pflag.Flag{
		Name:        f.Name,
		Shorthand:   f.Shorthand,
		Usage:       f.Usage,
		Value:       &stubValue{typ: f.Value.Type()},
		DefValue:    f.DefValue,
		NoOptDefVal: f.NoOptDefVal,
		Hidden:      f.Hidden,
		Annotations: f.Annotations,
	}
}

// docExampleVerdict is the outcome of checking one invocation.
type docExampleVerdict struct {
	cmd     *cobra.Command // the resolved command (nil when resolution failed)
	problem string         // non-empty: the example would not run as written
	skipped string         // non-empty: deliberately not checked, with the reason
}

var placeholderArgRE = regexp.MustCompile(`^<[^>]+>$`)

// checkDocInvocation resolves args (the words after `mio`) against root exactly
// as cobra would at execution time, without executing anything and without
// touching the real commands' flag state.
func checkDocInvocation(root *cobra.Command, args []string, elided bool) docExampleVerdict {
	target, rest, _ := root.Find(args)
	if target == nil {
		return docExampleVerdict{problem: "does not resolve to a command"}
	}

	// The command's real flag set: its own flags plus every persistent flag it
	// inherits, plus the help (and, on root, version) flag cobra adds at execute
	// time. Each is cloned onto a throwaway command so parsing never mutates the
	// singleton tree the in-process contract tests share.
	tmp := &cobra.Command{Use: target.Name(), FParseErrWhitelist: target.FParseErrWhitelist}
	tmp.SetOut(io.Discard)
	tmp.SetErr(io.Discard)
	fs := tmp.Flags()
	fs.SetOutput(io.Discard)
	// Spellings the command accepts through a normalize func (contacts'
	// legacy --first_name) must resolve here exactly as they do at runtime.
	fs.SetNormalizeFunc(target.Flags().GetNormalizeFunc())
	var deprecated = map[string]string{}
	add := func(f *pflag.Flag) {
		if fs.Lookup(f.Name) != nil {
			return
		}
		fs.AddFlag(stubOf(f))
		if f.Deprecated != "" {
			deprecated[f.Name] = f.Deprecated
		}
	}
	target.LocalFlags().VisitAll(add)
	target.InheritedFlags().VisitAll(add)
	if fs.Lookup("help") == nil {
		sh := "h"
		if fs.ShorthandLookup("h") != nil {
			sh = ""
		}
		fs.AddFlag(&pflag.Flag{Name: "help", Shorthand: sh, Value: &stubValue{typ: "bool"}, NoOptDefVal: "true"})
	}
	if target == root && root.Version != "" && fs.Lookup("version") == nil {
		fs.AddFlag(&pflag.Flag{Name: "version", Value: &stubValue{typ: "bool"}, NoOptDefVal: "true"})
	}

	v := docExampleVerdict{cmd: target}
	if target.DisableFlagParsing {
		v.skipped = "command disables flag parsing"
		return v
	}
	if err := tmp.ParseFlags(rest); err != nil {
		v.problem = err.Error()
		return v
	}
	positional := fs.Args()
	help := fs.Changed("help") || fs.Changed("version")

	if target.HasSubCommands() {
		switch {
		case len(positional) > 0 && placeholderArgRE.MatchString(positional[0]):
			v.skipped = fmt.Sprintf("placeholder %s in the command path", positional[0])
		case len(positional) > 0:
			v.problem = fmt.Sprintf("unknown command %q for %q", positional[0], target.CommandPath())
		case !help:
			v.problem = fmt.Sprintf("%q is a command group, not a runnable command", target.CommandPath())
		}
		return v
	}
	if target.Deprecated != "" {
		v.problem = fmt.Sprintf("%q is deprecated: %s", target.CommandPath(), target.Deprecated)
		return v
	}
	var used []string
	fs.Visit(func(f *pflag.Flag) {
		if msg, ok := deprecated[f.Name]; ok {
			used = append(used, fmt.Sprintf("--%s is deprecated: %s", f.Name, msg))
		}
	})
	if len(used) > 0 {
		v.problem = strings.Join(used, "; ")
		return v
	}
	if help {
		return v // cobra validates nothing further when help is requested
	}
	if elided {
		v.skipped = "elided (…) — command path and flag names checked only"
		return v
	}

	var errsFound []string
	if err := target.ValidateArgs(positional); err != nil {
		errsFound = append(errsFound, err.Error())
	}
	if err := tmp.ValidateRequiredFlags(); err != nil {
		errsFound = append(errsFound, err.Error())
	}
	if err := tmp.ValidateFlagGroups(); err != nil {
		errsFound = append(errsFound, err.Error())
	}
	v.problem = strings.Join(errsFound, "; ")
	return v
}

// docRoot returns the real command tree with cobra's lazily-added commands
// (help, completion) in place, as they are at execute time.
func docRoot() *cobra.Command {
	root := RootCmd()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	return root
}

// docSurfaceFiles returns the shipped surfaces plus any MIO_DOC_EXAMPLES_EXTRA.
func docSurfaceFiles(t *testing.T) []string {
	t.Helper()
	files := append([]string(nil), shippedDocSurfaces...)
	for _, pat := range filepath.SplitList(os.Getenv("MIO_DOC_EXAMPLES_EXTRA")) {
		if pat == "" {
			continue
		}
		matches, err := filepath.Glob(pat)
		if err != nil || len(matches) == 0 {
			t.Fatalf("MIO_DOC_EXAMPLES_EXTRA entry %q matched no file (err=%v)", pat, err)
		}
		files = append(files, matches...)
	}
	return files
}

// transcriptHint explains an uncovered mention on an output line of a `$ `
// console transcript, which is read as output rather than parsed.
func transcriptHint(m docexamples.Mention) string {
	if !m.Output {
		return ""
	}
	return " (the block has `$ ` prompts, so this unprompted line is read as output; if it is a command, give it a `$ ` prompt)"
}

// displayPath renders a surface path relative to the repo root for messages.
func displayPath(p string) string {
	if strings.HasPrefix(p, "../") {
		return strings.TrimPrefix(p, "../")
	}
	if !filepath.IsAbs(p) {
		return "cmd/" + p
	}
	return p
}

// TestDocExamples_ShippedSurfaces fails for every documented invocation cobra
// would reject: unknown command or flag, a missing required flag, a flag-group
// violation, a wrong positional-argument count, or a deprecated spelling.
func TestDocExamples_ShippedSurfaces(t *testing.T) {
	root := docRoot()
	checked := 0
	perSurface := map[string]int{}
	for _, path := range docSurfaceFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		name := displayPath(path)
		res := docexamples.FromMarkdown(name, string(raw))
		for _, m := range res.Uncovered {
			t.Errorf("%s:%d: mentions mio but no invocation was extracted, so it is unguarded%s: %s", m.File, m.Line, transcriptHint(m), m.Text)
		}
		for _, inv := range res.Invocations {
			v := checkDocInvocation(root, inv.Args, inv.Elided)
			switch {
			case v.problem != "":
				t.Errorf("%s:%d: `%s` would not run: %s", inv.File, inv.Line, inv.Text(), v.problem)
			case v.skipped != "":
				t.Logf("%s:%d: skipped `%s`: %s", inv.File, inv.Line, inv.Text(), v.skipped)
			default:
				checked++
				perSurface[path]++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no documented invocation was checked — the extractor or the surface list is broken")
	}
	// Per surface, not in total: a listed surface that yields nothing would be
	// hidden by the others' counts while the list claims it is guarded.
	for _, path := range shippedDocSurfaces {
		if perSurface[path] == 0 {
			t.Errorf("%s: listed in shippedDocSurfaces but no invocation from it was checked, so listing it guards nothing", displayPath(path))
		}
	}
}

// exampleSourceIndex maps a trimmed source line to its "file:line" in cmd/*.go,
// so an Example failure names where to edit. Lines seen twice map to "".
func exampleSourceIndex(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	idx := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(string(raw), "\n") {
			key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "Example:"))
			key = strings.TrimSpace(strings.TrimPrefix(key, "`"))
			if key == "" {
				continue
			}
			if _, dup := idx[key]; dup {
				idx[key] = ""
				continue
			}
			idx[key] = fmt.Sprintf("cmd/%s:%d", f, i+1)
		}
	}
	return idx
}

// TestDocExamples_CommandExamples applies the same check to every command's
// cobra Example — what `mio <cmd> --help` prints and `mio gen-docs` publishes.
func TestDocExamples_CommandExamples(t *testing.T) {
	root := docRoot()
	src := exampleSourceIndex(t)
	checked := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c.Example == "" {
			return
		}
		label := fmt.Sprintf("`%s` Example", c.CommandPath())
		lines := strings.Split(c.Example, "\n")
		where := func(line int) string {
			if line-1 < len(lines) {
				if loc := src[strings.TrimSpace(lines[line-1])]; loc != "" {
					return loc
				}
			}
			return fmt.Sprintf("%s line %d", label, line)
		}
		res := docexamples.FromScript(label, 1, c.Example)
		for _, m := range res.Uncovered {
			t.Errorf("%s: mentions mio but no invocation was extracted, so it is unguarded%s: %s", where(m.Line), transcriptHint(m), m.Text)
		}
		for _, inv := range res.Invocations {
			v := checkDocInvocation(root, inv.Args, inv.Elided)
			switch {
			case v.problem != "":
				t.Errorf("%s: `%s` would not run: %s", where(inv.Line), inv.Text(), v.problem)
			case v.skipped != "":
				t.Logf("%s: skipped `%s`: %s", where(inv.Line), inv.Text(), v.skipped)
			default:
				checked++
			}
		}
	}
	walk(root)
	if checked == 0 {
		t.Fatal("no Example invocation was checked — the walk or the extractor is broken")
	}
}

// TestDocExamples_ExtractorSeesProductsCreate proves the guard actually reaches
// the example MIO-4154 was filed against: README's `mio products create` is
// extracted, resolved to productsCreateCmd, and CHECKED (not skipped). An
// example the extractor silently drops cannot be guarded, however good the
// checker is. The line is located by content, not hard-coded, so unrelated edits
// above it cannot void this test.
func TestDocExamples_ExtractorSeesProductsCreate(t *testing.T) {
	raw, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	wantLine := 0
	for i, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "mio products create ") {
			wantLine = i + 1
			break
		}
	}
	if wantLine == 0 {
		t.Fatal("README.md has no `mio products create` example to guard (MIO-4154)")
	}
	root := docRoot()
	for _, inv := range docexamples.FromMarkdown("README.md", string(raw)).Invocations {
		if inv.Line != wantLine {
			continue
		}
		v := checkDocInvocation(root, inv.Args, inv.Elided)
		if v.cmd != productsCreateCmd {
			t.Fatalf("README.md:%d resolved to %v, want productsCreateCmd", wantLine, v.cmd)
		}
		if v.skipped != "" {
			t.Fatalf("README.md:%d was skipped (%s), so the guard does not check it", wantLine, v.skipped)
		}
		return
	}
	t.Fatalf("README.md:%d `mio products create …` was not extracted as an invocation", wantLine)
}

// TestDocExamples_CheckerRejectsKnownBadInvocations pins the checker itself
// against the real tree: each case is an invocation cobra rejects, and the
// checker must say so, naming the property. Without this a regression that made
// checkDocInvocation always return "" would leave the two surface tests green.
func TestDocExamples_CheckerRejectsKnownBadInvocations(t *testing.T) {
	root := docRoot()
	cases := []struct {
		name string
		args []string
		want string // substring of the problem
	}{
		{"MIO-4154: products create without --type", []string{"products", "create", "--name", "Pro Plan", "--description", "x"}, `required flag(s) "type" not set`},
		{"required flag from MarkFlagRequired", []string{"pages", "publish", "<page-id>", "--hub", "<hub-id>"}, `required flag(s) "if-match" not set`},
		{"unknown flag", []string{"contacts", "list", "--frobnicate"}, "unknown flag: --frobnicate"},
		{"unknown subcommand", []string{"contacts", "frobnicate"}, `unknown command "frobnicate"`},
		{"group without a subcommand", []string{"pages", "catalog"}, "is a command group"},
		{"wrong positional count", []string{"contacts", "retrieve"}, "accepts 1 arg(s), received 0"},
		{"flag missing its value", []string{"contacts", "list", "--team"}, "flag needs an argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := checkDocInvocation(root, tc.args, false)
			if !strings.Contains(v.problem, tc.want) {
				t.Errorf("problem = %q (skipped=%q), want it to contain %q", v.problem, v.skipped, tc.want)
			}
		})
	}

	// And the accept side: a correct invocation, with placeholders and
	// inherited/global flags, must produce no problem — otherwise the guard would
	// be failing for reasons of its own.
	ok := [][]string{
		{"products", "create", "--name", "Pro Plan", "--type", "membership", "--description", "Full access"},
		{"--api-base", "https://api.example.com", "contacts", "list", "-o", "plain", "--jq", ".[0].id"},
		{"pages", "publish", "$PAGE_ID", "--hub", "<hub-id>", "--if-match", "<n>"},
		{"contacts", "--help"},
		{"--version"},
		// A flag spelling the command accepts through its normalize func (the
		// legacy underscore alias) is a flag cobra accepts.
		{"contacts", "create", "--email", "a@example.com", "--first_name", "Ada"},
	}
	for _, args := range ok {
		if v := checkDocInvocation(root, args, false); v.problem != "" || v.skipped != "" {
			t.Errorf("mio %s: problem=%q skipped=%q, want a clean pass", strings.Join(args, " "), v.problem, v.skipped)
		}
	}
}
