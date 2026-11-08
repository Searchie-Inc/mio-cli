package cmd

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

var selectorDocSurfaces = append(append([]string(nil), shippedDocSurfaces...),
	"../llms.txt",
	"../docs/internal/api-surface.md",
)

type selectorSource struct {
	file      string
	firstLine int
	text      string
}

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

const selectorSentinel = "sentinel-3413"

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
	if attrs == nil {
		attrs = map[string]any{}
	}
	return attrs, selectorSentinel
}

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
			continue
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

var rawContextRE = regexp.MustCompile(`--raw\b`)

func answersList(c *cobra.Command) bool {
	return c.Flags().Lookup("after") != nil
}

type selectorAbout struct {
	cmds  []*cobra.Command
	every bool
}

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

func commandHelpTexts(root *cobra.Command) map[*cobra.Command][]string {
	out := map[*cobra.Command][]string{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		texts := []string{c.Short, c.Long, c.Example}
		c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) { texts = append(texts, f.Usage) })
		out[c] = texts
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return out
}

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
	if probed == 0 {
		t.Fatal("no path reading `attributes` was found anywhere; the sweep is not reading the help or the docs")
	}

	problems, onPages := helpPageProblems(t, RootCmd())
	for _, p := range problems {
		t.Error(p)
	}
	if onPages == 0 {
		t.Fatal("no help page carries a path reading `attributes`; the per-page pass is not reading the command tree")
	}
}

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
		context []*cobra.Command
	}
	broken := []probe{
		{"the .attributes.contact_id field from 'mio contacts'", nil},
		{"mio contacts retrieve <id> -o plain --jq '.attributes.contact_id'", nil},
		{"mio contacts retrieve <id> --raw --jq '.attributes.contact_id'", nil},
		{"mio contacts list --jq '.[].attributes.contact_id'", nil},
		{"filter client-side on .attributes.status", nil},
		{`.attributes["contact_id"]`, nil},
		{"the raw path .data.attributes.contact_id", nil},
		{"mio hubs list --raw -o plain --jq .data.attributes.slug", nil},
		{"mio contacts list --raw --jq '.data.attributes.contact_id'", nil},
		{"CID=$(mio contacts list --raw -o plain --jq .data.attributes.contact_id)", nil},
		{".data.attributes.contact_id under --raw", contactsGroup},
		{".data.attributes.contact_id under --raw", []*cobra.Command{cmdAt("contacts", "list")}},
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
		{"mio contacts retrieve <id> --raw --jq '.[].attributes.contact_id'", nil},
		{"mio contacts retrieve <id> --raw -o plain --jq .data.attributes.contact_id", nil},
		{".data.attributes.contact_id under --raw", []*cobra.Command{cmdAt("contacts", "retrieve")}},
		{".data[].attributes.contact_id under --raw", []*cobra.Command{cmdAt("contacts", "list")}},
		{".data.attributes.contact_id under `mio contacts retrieve --raw`", contactsGroup},
	}
	for _, p := range working {
		sel := one(p.line)
		if msg := attributesSelectorProblem(t, sel, aboutSelector(t, sel, p.context)); msg != "" {
			t.Errorf("%q (context %d commands): the probe rejected a path the renderer answers: %s", p.line, len(p.context), msg)
		}
	}

	const onePath = "The id is .data.attributes.thing_id under --raw."
	noop := func(*cobra.Command, []string) error { return nil }
	fakeRoot := &cobra.Command{Use: "mio"}
	group := &cobra.Command{Use: "things", Long: onePath}
	list := &cobra.Command{Use: "list", Short: onePath, RunE: noop}
	addPaginationFlags(list)
	list.Flags().String("probe", "", onePath)
	get := &cobra.Command{Use: "retrieve", RunE: noop, Long: onePath}
	group.AddCommand(list, get)
	fakeRoot.AddCommand(group)
	problems, probed := helpPageProblems(t, fakeRoot)
	if probed != 4 {
		t.Errorf("the per-page pass read %d paths in the probe tree, want 4 (group page, list Short and flag, retrieve page)", probed)
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

var contactsCaptureRE = regexp.MustCompile(
	`mio contacts (?:retrieve|create|list)` +
		`(?:[ \t]+(?:'[^'\n]*'|"[^"\n]*"|[^\s'"` + "`" + `)]+))*?` +
		`[ \t]+--jq[ \t]+(?:'[^'\n]*'|"[^"\n]*"|[^\s'"` + "`" + `)]+)`)

var placeholderWordRE = regexp.MustCompile(`^(?:<[^>]+>|\$[A-Za-z_{][A-Za-z0-9_}]*)$`)

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

func TestContactIDHelp_EveryPageGivesAWorkingCapture(t *testing.T) {
	checked := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if !c.HasParent() {
			return
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
