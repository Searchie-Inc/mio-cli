package cmd

// doc_selectors_test.go — MIO-3413.
//
// WHY
// Every member-shaped verb's help told an agent the global contact id was
// "the .attributes.contact_id field from 'mio contacts'". As a selector that
// path yields null in every output mode: the default output flattens a
// resource's attributes to the top level (the field is .contact_id), and under
// --raw the envelope needs .data.attributes.contact_id. An agent that ran
// `mio contacts retrieve <id> -o plain --jq '.attributes.contact_id'` captured
// an empty string, fed it to the verb, and read the 404 as a missing member.
//
// WHAT THIS GUARDS
//   - TestDocSelectors_AttributesPathsResolveInSomeOutputMode: every path that
//     reads `attributes` — in any Go string literal the CLI can print (help,
//     flag usage, error hints) and anywhere in the shipped docs, prose included
//     — must yield the attribute it names in an output mode of the REAL
//     renderer (internal/output.Render over a flattened and a raw resource and
//     collection), for the commands it is about: the command whose --jq it is,
//     one the line names, or every command a help page covers. A command with
//     the --after cursor prints a list, so a one-resource path fails there. The
//     oracle is the renderer, not a rule about spellings: `.attributes.x` fails
//     because Render never emits a top-level `attributes`, and would pass the
//     day it did.
//   - TestContactIDHelp_EveryPageGivesAWorkingCapture: every help page that
//     mentions the GLOBAL contact id must show a capture command
//     (`mio contacts retrieve … --jq …` reading contact_id), and each one is
//     RUN in-process against a contacts API stub; it must print exactly the
//     contact's global id — not null, not an empty line, not a JSON-quoted
//     string a shell capture would keep the quotes of (MIO-2792).
//   - requireWorkingContactIDCapture, shared with the 404 hint test in
//     contact_id_namespace_test.go, which applies the same run to the hint
//     every member verb appends to a 404.
//
// WHAT IT DOES NOT SEE
//   - Which shape a command without the --after cursor prints: the command tree
//     does not say, so a path for one only has to work for one shape. Nor
//     which command a path in docs prose is for, when its line names none.
//   - A path that reads a key the flattened output lacks for reasons other than
//     `attributes` (a misspelt attribute, a field this resource does not
//     have). Those need the resource's real attributes; the MIO-4156 list
//     guard does it for the hub display rows.
//   - Go comments (they are not printed) and docs/superpowers/ (dated plans).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/docexamples"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/output"
)

// selectorDocSurfaces are the docs whose every line — prose, tables and fences
// — is read for selectors: the shipped surfaces the doc-example guard reads,
// plus the two it cannot (they hold no fenced block).
var selectorDocSurfaces = append(append([]string(nil), shippedDocSurfaces...),
	"../llms.txt",
	"../docs/internal/api-surface.md",
)

// selectorSource is one text the selector sweep reads.
type selectorSource struct {
	file      string
	firstLine int
	text      string
}

// goStringLiterals returns every string literal in the module's non-test Go
// files (cmd/, internal/, main.go): help text, flag usage, error hints — all
// of it can reach a user. Comments are not literals and are not read.
func goStringLiterals(t *testing.T) []selectorSource {
	t.Helper()
	var out []selectorSource
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != ".." && (strings.HasPrefix(name, ".") || name == "testdata" || name == "docs" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if !strings.HasPrefix(lit.Value, "`") {
				// An interpreted literal is one source line; its \n escapes
				// must not shift the reported line.
				s = strings.ReplaceAll(s, "\n", " ")
			}
			out = append(out, selectorSource{
				file:      strings.TrimPrefix(filepath.ToSlash(path), "../"),
				firstLine: fset.Position(lit.Pos()).Line,
				text:      s,
			})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk Go sources: %v", err)
	}
	return out
}

// selectorSentinel is the attribute value the renderer probe plants.
const selectorSentinel = "sentinel-3413"

// attributesProbe builds the attributes object a path reading `attributes`
// asks for: the steps after `attributes`, nested, with the sentinel at the
// leaf. It returns the object and the value the path should yield.
func attributesProbe(sel docexamples.Selector) (attrs map[string]any, want any) {
	at := -1
	for i, st := range sel.Steps {
		if !st.Index && st.Key == "attributes" {
			at = i
			break
		}
	}
	rest := sel.Steps[at+1:]
	var build func(i int) any
	build = func(i int) any {
		if i == len(rest) {
			return selectorSentinel
		}
		if rest[i].Index {
			return []any{build(i + 1)}
		}
		return map[string]any{rest[i].Key: build(i + 1)}
	}
	if len(rest) == 0 {
		attrs = map[string]any{"probe": selectorSentinel}
		return attrs, attrs
	}
	attrs, _ = build(0).(map[string]any)
	if attrs == nil { // the step after `attributes` is an index: not an object
		attrs = map[string]any{}
	}
	return attrs, selectorSentinel
}

// selectorOutputModes renders the probe resource through the REAL renderer and
// reports which output modes yield want. The default (flattened) modes are
// always tried; the --raw modes only when the text names --raw on the same
// line, because a path that works only under --raw is broken for a reader who
// was not told to pass it. The raw envelopes are the API's own shapes (a
// single resource is {"data": …}; a list adds links and meta). listOnly drops
// the single-resource modes, for a command that prints a list.
func selectorOutputModes(t *testing.T, path string, attrs map[string]any, want any, raw, listOnly bool) []string {
	t.Helper()
	item := map[string]any{"type": "things", "id": "thing_1", "attributes": attrs}
	single, err := json.Marshal(map[string]any{"data": item})
	if err != nil {
		t.Fatal(err)
	}
	list, err := json.Marshal(map[string]any{
		"data":  []any{item},
		"links": map[string]any{"self": "/api/v1/things?page%5Bsize%5D=20"},
		"meta":  map[string]any{"page": map[string]any{"has_more": false, "size": 20}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := client.Resource{ID: "thing_1", Type: "things", Attributes: attrs, RawBody: single}
	col := client.Collection{Data: []client.Resource{res}, RawBody: list}

	type mode struct {
		name string
		v    any
		raw  bool
		list bool
	}
	modes := []mode{
		{"default output, one resource", &res, false, false},
		{"default output, a list", &col, false, true},
	}
	if raw {
		modes = append(modes, mode{"--raw, one resource", &res, true, false}, mode{"--raw, a list", &col, true, true})
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var yields []string
	for _, m := range modes {
		if listOnly && !m.list {
			continue
		}
		var buf bytes.Buffer
		if err := output.Render(&buf, m.v, output.Options{Format: output.FormatJSON, JQ: path, Raw: m.raw}); err != nil {
			continue // a path that errors in this mode does not yield
		}
		var got any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			continue
		}
		if gotJSON, _ := json.Marshal(got); bytes.Equal(gotJSON, wantJSON) {
			yields = append(yields, m.name)
		}
	}
	return yields
}

// rawContextRE: the line the path is on tells the reader to pass --raw.
var rawContextRE = regexp.MustCompile(`--raw\b`)

// answersList reports whether c prints a list, read from the real command:
// addPaginationFlags gives a command the --after cursor, and only a command
// reading a collection route has one. Any other command may print either
// shape as far as the command tree says, so a path for it only has to work
// for one of them.
func answersList(c *cobra.Command) bool {
	return c.Flags().Lookup("after") != nil
}

// selectorAbout is what a path is about: the commands a reader will run it
// against, and whether it must work for every one of them (true) or for one.
type selectorAbout struct {
	cmds  []*cobra.Command
	every bool
}

// aboutSelector decides which commands sel is about (the blind review of #129
// found the sweep accepted a single-resource path written on a list command,
// because it asked only whether SOME shape answered it):
//
//   - a `mio` invocation on the line whose words hold the path (its --jq
//     program): that command, and the path must work for it;
//   - otherwise any invocation the line names ("… under `mio contacts retrieve
//     --raw`"): the path must work for one of them;
//   - otherwise the commands whose help the text is (context): the path must
//     work for every one. A group's page is about every command beneath it,
//     so a path there cannot be one that only a single-resource verb answers;
//   - otherwise (docs prose naming no command) nothing: the path must work
//     for some shape.
func aboutSelector(t *testing.T, sel docexamples.Selector, context []*cobra.Command) selectorAbout {
	t.Helper()
	root := RootCmd()
	var holding, named []*cobra.Command
	for _, inv := range docexamples.FromScript(sel.File, sel.Line, sel.LineText).Invocations {
		c, _, err := root.Find(inv.Args)
		if err != nil || c == root {
			continue
		}
		named = append(named, c)
		for _, a := range inv.Args {
			if strings.Contains(a, sel.Path) {
				holding = append(holding, c)
				break
			}
		}
	}
	switch {
	case len(holding) > 0:
		return selectorAbout{cmds: holding, every: true}
	case len(named) > 0:
		return selectorAbout{cmds: named}
	default:
		return selectorAbout{cmds: context, every: true}
	}
}

// attributesSelectorProblem returns "" when sel does not read `attributes`, or
// when the renderer answers it in an output mode the text names, for the
// commands it is about; otherwise it says why sel is broken.
func attributesSelectorProblem(t *testing.T, sel docexamples.Selector, about selectorAbout) string {
	t.Helper()
	if !sel.Reads("attributes") {
		return ""
	}
	attrs, want := attributesProbe(sel)
	raw := rawContextRE.MatchString(sel.LineText)
	var failing []string
	if len(about.cmds) == 0 {
		if len(selectorOutputModes(t, sel.Path, attrs, want, raw, false)) > 0 {
			return ""
		}
	} else {
		answered := 0
		for _, c := range about.cmds {
			if len(selectorOutputModes(t, sel.Path, attrs, want, raw, answersList(c))) > 0 {
				answered++
				continue
			}
			what := "`" + c.CommandPath() + "`"
			if answersList(c) {
				what += " (it pages, so it prints a list)"
			}
			failing = append(failing, what)
		}
		if (about.every && answered == len(about.cmds)) || (!about.every && answered > 0) {
			return ""
		}
	}
	leaf := strings.TrimPrefix(sel.Path[strings.LastIndex(sel.Path, "attributes")+len("attributes"):], ".")
	modes := "the default output"
	if raw {
		modes = "the default output or under --raw"
	}
	on := ""
	if len(failing) > 0 {
		on = " for " + strings.Join(failing, ", ")
	}
	return fmt.Sprintf("selector %s yields nothing in %s%s — the default output flattens a resource's attributes "+
		"to the top level, so select .%s (capture a string with -o plain), or say --raw and use .data.attributes.%s "+
		"for one resource, .data[].attributes.%s for a list",
		sel.Path, modes, on, leaf, leaf, leaf)
}

// commandHelpTexts returns each command's own help text — Long, Example and
// the usage of every flag it defines — with the commands that text is about:
// the command itself, or for a group every runnable command beneath it.
func commandHelpTexts(root *cobra.Command) map[*cobra.Command][]string {
	out := map[*cobra.Command][]string{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		texts := []string{c.Long, c.Example}
		c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) { texts = append(texts, f.Usage) })
		out[c] = texts
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return out
}

// runnableUnder returns c if it runs, plus every runnable command beneath it.
func runnableUnder(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	if c.Runnable() {
		out = append(out, c)
	}
	for _, sub := range c.Commands() {
		out = append(out, runnableUnder(sub)...)
	}
	return out
}

// TestDocSelectors_AttributesPathsResolveInSomeOutputMode is the MIO-3413
// class guard: a path through `attributes` that the CLI prints or ships must be
// answered by the real renderer, in an output mode its line names, for the
// commands it is about (aboutSelector): the command whose --jq it is, one of
// the commands its line names, or — on a help page — every command the page
// covers. A path in docs prose that names no command only has to work for one
// shape, because nothing says which command it is for.
func TestDocSelectors_AttributesPathsResolveInSomeOutputMode(t *testing.T) {
	sources := goStringLiterals(t)
	fromCmd := 0
	for _, src := range sources {
		if strings.HasPrefix(src.file, "cmd/") {
			fromCmd++
		}
	}
	if fromCmd < 1000 {
		t.Fatalf("read only %d string literals from cmd/; the walk is not reaching the command sources", fromCmd)
	}
	for _, path := range selectorDocSurfaces {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sources = append(sources, selectorSource{file: displayPath(path), firstLine: 1, text: string(raw)})
	}

	probed := 0
	for _, src := range sources {
		for _, sel := range docexamples.Selectors(src.file, src.firstLine, src.text) {
			if !sel.Reads("attributes") {
				continue
			}
			probed++
			if p := attributesSelectorProblem(t, sel, aboutSelector(t, sel, nil)); p != "" {
				t.Errorf("%s:%d: %s: %s", sel.File, sel.Line, p, strings.TrimSpace(sel.LineText))
			}
		}
	}
	// The sweep must meet real `attributes` paths, or it proves nothing: the
	// contacts help names the --raw path on purpose.
	if probed == 0 {
		t.Fatal("no path reading `attributes` was found anywhere; the sweep is not reading the help or the docs")
	}

	// The help pages again, now knowing whose page each text is: a line that
	// names no command is about the page's own command(s).
	problems, onPages := helpPageProblems(t, RootCmd())
	for _, p := range problems {
		t.Error(p)
	}
	if onPages == 0 {
		t.Fatal("no help page carries a path reading `attributes`; the per-page pass is not reading the command tree")
	}
}

// helpPageProblems checks every path through `attributes` in the help of the
// command tree under root, each against the commands its page covers, and
// returns the problems and how many paths it checked.
func helpPageProblems(t *testing.T, root *cobra.Command) (problems []string, probed int) {
	t.Helper()
	for c, texts := range commandHelpTexts(root) {
		context := runnableUnder(c)
		page := "`" + c.CommandPath() + " --help`"
		for _, text := range texts {
			for _, sel := range docexamples.Selectors(page, 1, text) {
				if !sel.Reads("attributes") {
					continue
				}
				probed++
				if p := attributesSelectorProblem(t, sel, aboutSelector(t, sel, context)); p != "" {
					problems = append(problems, fmt.Sprintf("%s: %s: %s", page, p, strings.TrimSpace(sel.LineText)))
				}
			}
		}
	}
	return problems, probed
}

// TestDocSelectors_ProbeTellsBrokenFromWorking pins the probe on both sides, so
// a probe that answered "broken" (or "fine") for everything cannot hide.
func TestDocSelectors_ProbeTellsBrokenFromWorking(t *testing.T) {
	one := func(line string) docexamples.Selector {
		for _, s := range docexamples.Selectors("probe", 1, line) {
			if s.Reads("attributes") {
				return s
			}
		}
		t.Fatalf("no attributes path in %q", line)
		return docexamples.Selector{}
	}
	cmdAt := func(path ...string) *cobra.Command {
		c, _, err := RootCmd().Find(path)
		if err != nil || c.CommandPath() != "mio "+strings.Join(path, " ") {
			t.Fatalf("no command mio %s", strings.Join(path, " "))
		}
		return c
	}
	contactsGroup := runnableUnder(cmdAt("contacts"))
	type probe struct {
		line    string
		context []*cobra.Command // the help page the line is on; nil for docs prose
	}
	broken := []probe{
		{"the .attributes.contact_id field from 'mio contacts'", nil},              // MIO-3413 as shipped
		{"mio contacts retrieve <id> -o plain --jq '.attributes.contact_id'", nil}, // its capture
		{"mio contacts retrieve <id> --raw --jq '.attributes.contact_id'", nil},    // --raw needs .data
		{"mio contacts list --jq '.[].attributes.contact_id'", nil},
		{"filter client-side on .attributes.status", nil},
		{`.attributes["contact_id"]`, nil},
		{"the raw path .data.attributes.contact_id", nil}, // the right path, but --raw is never named
		// #129 blind review: a one-resource path on a command that prints a list.
		{"mio hubs list --raw -o plain --jq .data.attributes.slug", nil},
		{"mio contacts list --raw --jq '.data.attributes.contact_id'", nil},
		{"CID=$(mio contacts list --raw -o plain --jq .data.attributes.contact_id)", nil},
		// …and on a group page, which covers `contacts list` too.
		{".data.attributes.contact_id under --raw", contactsGroup},
		{".data.attributes.contact_id under --raw", []*cobra.Command{cmdAt("contacts", "list")}},
		// The line names only a list command.
		{".data.attributes.contact_id under `mio contacts list --raw`", nil},
	}
	for _, p := range broken {
		sel := one(p.line)
		if msg := attributesSelectorProblem(t, sel, aboutSelector(t, sel, p.context)); msg == "" {
			t.Errorf("%q (context %d commands): the probe accepted a path that yields nothing where the text says to use it", p.line, len(p.context))
		}
	}
	working := []probe{
		{".data.attributes.contact_id under --raw", nil},
		{"mio contacts list --raw --jq '.data[0].attributes.contact_id'", nil},
		{"mio contacts list --raw --jq '.data[].attributes.contact_id'", nil},
		{"mio hubs retrieve --raw --jq .data.attributes.settings.registration.enabled", nil},
		{"mio contacts retrieve <id> --raw --jq '.[].attributes.contact_id'", nil}, // .[] over {"data": …}
		{"mio contacts retrieve <id> --raw -o plain --jq .data.attributes.contact_id", nil},
		{".data.attributes.contact_id under --raw", []*cobra.Command{cmdAt("contacts", "retrieve")}},
		{".data[].attributes.contact_id under --raw", []*cobra.Command{cmdAt("contacts", "list")}},
		// The line names the command, so the page's context does not apply.
		{".data.attributes.contact_id under `mio contacts retrieve --raw`", contactsGroup},
	}
	for _, p := range working {
		sel := one(p.line)
		if msg := attributesSelectorProblem(t, sel, aboutSelector(t, sel, p.context)); msg != "" {
			t.Errorf("%q (context %d commands): the probe rejected a path the renderer answers: %s", p.line, len(p.context), msg)
		}
	}

	// The per-page pass, on a tree built here: a group whose page gives a
	// one-resource --raw path, over a paged list verb and a retrieve verb. The
	// path is wrong for the group's page and for a flag of the list verb, and
	// right on the retrieve verb's page.
	const onePath = "The id is .data.attributes.thing_id under --raw."
	noop := func(*cobra.Command, []string) error { return nil }
	fakeRoot := &cobra.Command{Use: "mio"}
	group := &cobra.Command{Use: "things", Long: onePath}
	list := &cobra.Command{Use: "list", RunE: noop}
	addPaginationFlags(list)
	list.Flags().String("probe", "", onePath)
	get := &cobra.Command{Use: "retrieve", RunE: noop, Long: onePath}
	group.AddCommand(list, get)
	fakeRoot.AddCommand(group)
	problems, probed := helpPageProblems(t, fakeRoot)
	if probed != 3 {
		t.Errorf("the per-page pass read %d paths in the probe tree, want 3 (group page, list flag, retrieve page)", probed)
	}
	var onGroup, onList, onGet bool
	for _, p := range problems {
		onGroup = onGroup || strings.HasPrefix(p, "`mio things --help`")
		onList = onList || strings.HasPrefix(p, "`mio things list --help`")
		onGet = onGet || strings.HasPrefix(p, "`mio things retrieve --help`")
	}
	if !onGroup || !onList || onGet {
		t.Errorf("the per-page pass flagged group=%v list=%v retrieve=%v, want true true false; problems:\n%s",
			onGroup, onList, onGet, strings.Join(problems, "\n"))
	}
}

// ─── the contact-id capture ─────────────────────────────────────────────────

// The contact the stub serves, shaped as the local stack's
// `mio contacts retrieve --raw` returned it. The team-contact id and the
// global contact id differ, so a capture that reads the wrong one cannot pass.
const (
	fixtureTeamContactID    = "01a0d321-c31a-72d2-b8bb-6eeb9fd70e07"
	fixtureGlobalContactID  = "01a0d321-c304-7332-801d-8e7d1cb5245c"
	fixtureTeamContactID2   = "01a0d321-c31a-72d2-b8bb-6eeb9fd70e08"
	fixtureGlobalContactID2 = "01a0d321-c304-7332-801d-8e7d1cb5245d"
)

func contactResourceJSON(id, contactID, email string) string {
	return fmt.Sprintf(`{"type":"team_contacts","id":%q,"attributes":{"city":null,"consent_source":null,`+
		`"consent_version":null,"contact_id":%q,"country":null,"created_at":"2026-09-24T11:16:40.346035Z",`+
		`"deleted_at":null,"email":%q,"email_verified_at":null,"first_name":null,"last_name":null,`+
		`"marketing_consent_at":null,"meta":{},"phone":null,"restored_at":null,"source":"admin_grant",`+
		`"state":null,"timezone":null,"updated_at":"2026-09-24T11:16:40.346042Z"}}`, id, contactID, email)
}

// contactsStub serves the three contacts routes a capture can call: retrieve
// and create answer one contact, list answers two.
func contactsStub(t *testing.T) *httptest.Server {
	t.Helper()
	one := contactResourceJSON(fixtureTeamContactID, fixtureGlobalContactID, "pat@example.com")
	two := contactResourceJSON(fixtureTeamContactID2, fixtureGlobalContactID2, "sam@example.com")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := strings.TrimSuffix(r.URL.Path, "/")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/contacts"):
			fmt.Fprintf(w, `{"data":[%s,%s],"links":{"self":%q},"meta":{"page":{"has_more":false,"size":20}}}`, one, two, r.URL.Path)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/contacts"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"data":%s}`, one)
		case r.Method == http.MethodGet && strings.Contains(path, "/contacts/"):
			fmt.Fprintf(w, `{"data":%s}`, one)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"errors":[{"status":"404","detail":"stub has no route %s %s"}]}`, r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// contactsCaptureRE finds a `mio contacts retrieve|create|list …` command up to
// the end of its first --jq program, wherever it sits: a code line, a
// `$( … )` capture, a backticked span in prose or a flag's usage.
var contactsCaptureRE = regexp.MustCompile(
	`mio contacts (?:retrieve|create|list)` +
		`(?:[ \t]+(?:'[^'\n]*'|"[^"\n]*"|[^\s'"` + "`" + `)]+))*?` +
		`[ \t]+--jq[ \t]+(?:'[^'\n]*'|"[^"\n]*"|[^\s'"` + "`" + `)]+)`)

// placeholderWordRE is a positional the doc leaves for the reader to fill:
// `<team-contact-id>`, `$ID`, `${ID}`.
var placeholderWordRE = regexp.MustCompile(`^(?:<[^>]+>|\$[A-Za-z_{][A-Za-z0-9_}]*)$`)

// contactIDCaptures returns the args (the words after `mio`) of every
// `mio contacts … --jq <program>` in text whose program reads contact_id
// (by jq's own parse tree, jqFieldReads from the MIO-4156 guard).
func contactIDCaptures(t *testing.T, text string) [][]string {
	t.Helper()
	var out [][]string
	for _, span := range contactsCaptureRE.FindAllString(text, -1) {
		invs := docexamples.FromScript("capture", 1, span).Invocations
		if len(invs) != 1 {
			t.Fatalf("capture %q did not parse to one invocation (got %d)", span, len(invs))
		}
		args := invs[0].Args
		prog := ""
		for i, a := range args {
			if a == "--jq" && i+1 < len(args) {
				prog = args[i+1]
			}
		}
		for _, k := range jqFieldReads(t, prog) {
			if k == "contact_id" {
				out = append(out, args)
				break
			}
		}
	}
	return out
}

// requireWorkingContactIDCapture fails unless text shows at least one
// `mio contacts … --jq …` capture of the global contact id, and every such
// capture, RUN against the contacts stub, prints what a shell capture needs:
// for retrieve/create exactly the global contact id and a newline; for list
// every row's global contact id and no null.
func requireWorkingContactIDCapture(t *testing.T, where, text string) {
	t.Helper()
	captures := contactIDCaptures(t, text)
	if len(captures) == 0 {
		t.Errorf("%s gives no capture of the GLOBAL contact id; show a working one, e.g. "+
			"`mio contacts retrieve <team-contact-id> -o plain --jq .contact_id` (MIO-3413):\n%s", where, text)
		return
	}
	srv := contactsStub(t)
	for _, args := range captures {
		run := make([]string, len(args))
		for i, a := range args {
			if placeholderWordRE.MatchString(a) {
				a = fixtureTeamContactID
			}
			run[i] = a
		}
		res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", run...)...)
		shown := "mio " + strings.Join(args, " ")
		if res.Code != errs.ExitOK {
			t.Errorf("%s: `%s` exited %d, want 0; stderr=%q", where, shown, res.Code, res.Stderr)
			continue
		}
		if args[1] == "list" {
			if !strings.Contains(res.Stdout, fixtureGlobalContactID) || !strings.Contains(res.Stdout, fixtureGlobalContactID2) ||
				strings.Contains(res.Stdout, "null") || strings.Contains(res.Stdout, fixtureTeamContactID) {
				t.Errorf("%s: `%s` printed %q; want every row's GLOBAL contact id (%s, %s) and no null",
					where, shown, res.Stdout, fixtureGlobalContactID, fixtureGlobalContactID2)
			}
			continue
		}
		if want := fixtureGlobalContactID + "\n"; res.Stdout != want {
			t.Errorf("%s: `%s` printed %q, want exactly %q — the GLOBAL contact id, unquoted, so a "+
				"CID=$(…) capture gets it (the flattened field is .contact_id; -o plain, since --jq alone "+
				"JSON-quotes a string)", where, shown, res.Stdout, want)
		}
	}
}

// memberVerbHelpPages are the verbs that route on the GLOBAL {contact_id}; each
// must be among the pages TestContactIDHelp_EveryPageGivesAWorkingCapture checks.
var memberVerbHelpPages = [][]string{
	{"hub-memberships", "add"},
	{"hub-memberships", "set-role"},
	{"hub-memberships", "ban"},
	{"hub-memberships", "unban"},
	{"hub-memberships", "warn"},
	{"activity", "contact"},
	{"community", "members", "ban"},
	{"community", "members", "unban"},
	{"community", "members", "warn"},
	{"community", "members", "soft-ban"},
	{"email", "enrollments", "create"},
	{"email", "enrollments", "list-by-contact"},
	{"access-rules", "overrides", "create"},
}

// TestContactIDHelp_EveryPageGivesAWorkingCapture: every help page that
// mentions the GLOBAL contact id — found by walking the real command tree, not
// listed — must show a capture of it that works when run.
func TestContactIDHelp_EveryPageGivesAWorkingCapture(t *testing.T) {
	checked := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if !c.HasParent() {
			return // root
		}
		page := strings.Fields(c.CommandPath())[1:]
		out := helpOutput(t, page...)
		if !strings.Contains(strings.ToLower(out), "global contact id") {
			return
		}
		checked[strings.Join(page, " ")] = true
		requireWorkingContactIDCapture(t, "`mio "+strings.Join(page, " ")+" --help`", out)
	}
	walk(RootCmd())

	for _, p := range memberVerbHelpPages {
		if !checked[strings.Join(p, " ")] {
			t.Errorf("`mio %s --help` routes on the GLOBAL contact id but was not checked — its help no longer says so", strings.Join(p, " "))
		}
	}
	for _, p := range []string{"contacts", "contacts retrieve", "contacts create", "contacts list"} {
		if !checked[p] {
			t.Errorf("`mio %s --help` was not checked; the contacts pages are where the capture is documented", p)
		}
	}
}
