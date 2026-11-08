package cmd

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

var shippedDocSurfaces = []string{
	"../README.md",
	"../AGENTS.md",
	"skills/content/mio-skill.md",
}

type stubValue struct{ typ string }

func (s *stubValue) String() string   { return "" }
func (s *stubValue) Set(string) error { return nil }
func (s *stubValue) Type() string     { return s.typ }

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

type docExampleVerdict struct {
	cmd     *cobra.Command
	problem string
	skipped string
}

var placeholderArgRE = regexp.MustCompile(`^<[^>]+>$`)

func checkDocInvocation(root *cobra.Command, args []string, elided bool) docExampleVerdict {
	target, rest, _ := root.Find(args)
	if target == nil {
		return docExampleVerdict{problem: "does not resolve to a command"}
	}

	tmp := &cobra.Command{Use: target.Name(), FParseErrWhitelist: target.FParseErrWhitelist}
	tmp.SetOut(io.Discard)
	tmp.SetErr(io.Discard)
	fs := tmp.Flags()
	fs.SetOutput(io.Discard)
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
		return v
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

func docRoot() *cobra.Command {
	root := RootCmd()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	return root
}

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

func transcriptHint(m docexamples.Mention) string {
	if !m.Output {
		return ""
	}
	return " (the block has `$ ` prompts, so this unprompted line is read as output; if it is a command, give it a `$ ` prompt)"
}

func displayPath(p string) string {
	if strings.HasPrefix(p, "../") {
		return strings.TrimPrefix(p, "../")
	}
	if !filepath.IsAbs(p) {
		return "cmd/" + p
	}
	return p
}

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
	for _, path := range shippedDocSurfaces {
		if perSurface[path] == 0 {
			t.Errorf("%s: listed in shippedDocSurfaces but no invocation from it was checked, so listing it guards nothing", displayPath(path))
		}
	}
}

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

func TestDocExamples_CheckerRejectsKnownBadInvocations(t *testing.T) {
	root := docRoot()
	cases := []struct {
		name string
		args []string
		want string
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

	ok := [][]string{
		{"products", "create", "--name", "Pro Plan", "--type", "membership", "--description", "Full access"},
		{"--api-base", "https://api.example.com", "contacts", "list", "-o", "plain", "--jq", ".[0].id"},
		{"pages", "publish", "$PAGE_ID", "--hub", "<hub-id>", "--if-match", "<n>"},
		{"contacts", "--help"},
		{"--version"},
		{"contacts", "create", "--email", "a@example.com", "--first_name", "Ada"},
	}
	for _, args := range ok {
		if v := checkDocInvocation(root, args, false); v.problem != "" || v.skipped != "" {
			t.Errorf("mio %s: problem=%q skipped=%q, want a clean pass", strings.Join(args, " "), v.problem, v.skipped)
		}
	}
}
