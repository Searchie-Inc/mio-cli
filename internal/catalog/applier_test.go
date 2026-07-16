package catalog

// applier_test.go — Go port parity of mio-page-catalog src/applier.test.ts
// (the applier UNIT invariants) plus the NormalizeIDs / CanonicalJSON helpers
// the cross-language golden parity (parity_test.go) leans on. The reference
// algorithm is mio-page-catalog@f75ddf4 src/applier.ts (instantiateTemplate /
// cloneWithFreshIds).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// uuidV7Re mirrors the TS test's UUID_V7 regex: version nibble 7, variant nibble
// in [89ab]. Case-insensitive (our generator emits lowercase).
var uuidV7Re = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-7[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// counterGen returns a deterministic IDGen yielding id-0, id-1, … — the Go
// analogue of the TS test's injected `() => `id-${i++}“.
func counterGen() IDGen {
	i := 0
	return func() (string, error) {
		s := "id-" + itoa(i)
		i++
		return s, nil
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// allIDs collects node ids in pre-order over the children-tree (the same nodes
// the applier re-ids).
func allIDs(n Node) []string {
	out := []string{}
	if id, ok := n["id"].(string); ok {
		out = append(out, id)
	}
	if children, ok := n["children"].([]any); ok {
		for _, c := range children {
			if cm, ok := c.(map[string]any); ok {
				out = append(out, allIDs(cm)...)
			}
		}
	}
	return out
}

// readJSONNumber decodes a JSON file preserving numeric literals as json.Number
// (so canonical comparison is byte-faithful, matching TS JSON.stringify).
func readJSONNumber(t *testing.T, path string) any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return v
}

// mustCanonical canonicalizes or fails the test.
func mustCanonical(t *testing.T, v any) string {
	t.Helper()
	b, err := CanonicalJSON(v)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	return string(b)
}

// sampleTemplate mirrors the inline SAMPLE fixture in applier.test.ts.
func sampleTemplate() Template {
	return Template{
		ID: "sample",
		Starter: Node{
			"kind":     "stack",
			"id":       "root",
			"settings": Node{"slot": "root", "gap": json.Number("8")},
			"children": []any{
				Node{"kind": "field", "id": "f", "settings": Node{"role": "title", "name": "title"}},
			},
		},
		Variants: map[string]Node{
			"bound": {"kind": "container", "id": "root", "dataSource": Node{"type": "playlist"}, "children": []any{}},
		},
	}
}

func loadForTest(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func heroTemplate(t *testing.T) Template {
	t.Helper()
	c := loadForTest(t)
	tmpl, ok := c.TemplateByID("hero")
	if !ok {
		t.Fatal("catalog has no 'hero' template")
	}
	return tmpl
}

func TestInstantiate_MintsFreshUUIDv7_ReplacingPlaceholders(t *testing.T) {
	tree, err := InstantiateTemplate(heroTemplate(t), "", NewUUIDv7Gen())
	if err != nil {
		t.Fatalf("InstantiateTemplate: %v", err)
	}
	ids := allIDs(tree)
	if len(ids) == 0 {
		t.Fatal("no ids collected")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !uuidV7Re.MatchString(id) {
			t.Errorf("id %q is not a UUIDv7", id)
		}
		if id == "root" {
			t.Error("placeholder id 'root' was not replaced")
		}
		if seen[id] {
			t.Errorf("duplicate id %q within tree", id)
		}
		seen[id] = true
	}
}

func TestInstantiate_PreservesStructuralSettings(t *testing.T) {
	tree, err := InstantiateTemplate(sampleTemplate(), "", counterGen())
	if err != nil {
		t.Fatalf("InstantiateTemplate: %v", err)
	}
	settings, _ := tree["settings"].(Node)
	if settings["slot"] != "root" {
		t.Errorf("settings.slot = %v, want root", settings["slot"])
	}
	if settings["gap"] != json.Number("8") {
		t.Errorf("settings.gap = %v, want 8", settings["gap"])
	}
	children, _ := tree["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children len = %d, want 1", len(children))
	}
	field, _ := children[0].(map[string]any)
	fs, _ := field["settings"].(Node)
	if fs["role"] != "title" || fs["name"] != "title" {
		t.Errorf("field settings role/name not preserved: %#v", fs)
	}
}

func TestInstantiate_DoesNotMutateSourceRecipe(t *testing.T) {
	tmpl := heroTemplate(t)
	before := mustCanonical(t, tmpl.Starter)
	if _, err := InstantiateTemplate(tmpl, "", NewUUIDv7Gen()); err != nil {
		t.Fatalf("InstantiateTemplate: %v", err)
	}
	after := mustCanonical(t, tmpl.Starter)
	if before != after {
		t.Errorf("source recipe mutated:\n before=%s\n after =%s", before, after)
	}
}

func TestInstantiate_VariantSelection_GracefulFallback(t *testing.T) {
	sample := sampleTemplate()

	bound, err := InstantiateTemplate(sample, "bound", counterGen())
	if err != nil {
		t.Fatalf("InstantiateTemplate bound: %v", err)
	}
	ds, ok := bound["dataSource"].(Node)
	if !ok || ds["type"] != "playlist" {
		t.Errorf("variant 'bound' not selected: dataSource=%#v", bound["dataSource"])
	}

	missing, err := InstantiateTemplate(sample, "nope", counterGen())
	if err != nil {
		t.Fatalf("InstantiateTemplate nope: %v", err)
	}
	if _, present := missing["dataSource"]; present {
		t.Error("unknown variant should fall back to base starter (no dataSource)")
	}
}

func TestInstantiate_DeterministicUnderInjectedGen(t *testing.T) {
	tmpl := heroTemplate(t)
	a, err := CloneWithFreshIDs(tmpl.Starter, counterGen())
	if err != nil {
		t.Fatalf("clone a: %v", err)
	}
	b, err := CloneWithFreshIDs(tmpl.Starter, counterGen())
	if err != nil {
		t.Fatalf("clone b: %v", err)
	}
	if mustCanonical(t, a) != mustCanonical(t, b) {
		t.Error("clone is not deterministic under an injected id generator")
	}
}

func TestInstantiate_DuplicateIDErrors(t *testing.T) {
	constGen := func() (string, error) { return "same", nil }
	_, err := CloneWithFreshIDs(heroTemplate(t).Starter, constGen)
	if err == nil {
		t.Fatal("expected an error for a colliding id generator")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("unique")) {
		t.Errorf("error %q should mention the uniqueness contract", err)
	}
}

func TestInstantiate_MissingStarterErrors(t *testing.T) {
	_, err := InstantiateTemplate(Template{ID: "empty"}, "", counterGen())
	if err == nil {
		t.Fatal("expected an error for a template with no starter subtree")
	}
}

func TestNormalizeIDs_PreorderPlaceholders(t *testing.T) {
	tree := Node{
		"id":   "a",
		"kind": "stack",
		"children": []any{
			map[string]any{"id": "b", "kind": "text"},
			map[string]any{"kind": "spacer"}, // no id — must stay untouched
			map[string]any{"id": "c", "kind": "row", "children": []any{
				map[string]any{"id": "d", "kind": "button"},
			}},
		},
	}
	got := NormalizeIDs(tree).(map[string]any)
	want := []string{"#0", "#1", "#2", "#3"} // pre-order over id-bearing nodes
	ids := allIDs(got)
	if len(ids) != len(want) {
		t.Fatalf("id count = %d, want %d (%v)", len(ids), len(want), ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("id[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
	// The id-less spacer must remain id-less.
	children := got["children"].([]any)
	spacer := children[1].(map[string]any)
	if _, ok := spacer["id"]; ok {
		t.Error("NormalizeIDs added an id to a node that had none")
	}
}

func TestNormalizeIDs_DoesNotMutateInput(t *testing.T) {
	tree := Node{"id": "keep", "kind": "stack"}
	_ = NormalizeIDs(tree)
	if tree["id"] != "keep" {
		t.Errorf("NormalizeIDs mutated its input: id=%v", tree["id"])
	}
}

func TestCanonicalJSON_SortsKeysAndPreservesUnknowns(t *testing.T) {
	v := Node{"b": json.Number("2"), "a": json.Number("1"), "z": Node{"y": true, "x": "keep"}}
	got := mustCanonical(t, v)
	want := `{"a":1,"b":2,"z":{"x":"keep","y":true}}`
	if got != want {
		t.Errorf("CanonicalJSON = %s, want %s", got, want)
	}
}

func TestCanonicalJSON_NoHTMLEscaping(t *testing.T) {
	// TS JSON.stringify does NOT escape <, >, &; Go's default encoder does. The
	// digest parity depends on matching TS, so CanonicalJSON must disable it.
	v := Node{"label": "A & B <tag>"}
	got := mustCanonical(t, v)
	want := `{"label":"A & B <tag>"}`
	if got != want {
		t.Errorf("CanonicalJSON = %s, want %s (HTML escaping must be off)", got, want)
	}
}

// TestCanonicalJSON_MatchesTSReference_UnicodeEdges pins the exact byte output
// against the TS canonicalizer (JSON.stringify(sortKeys(v))) for the cases where
// Go's encoding/json would diverge — proven equal to node's output. If these
// diverged, a valid upstream catalog carrying such characters would be wrongly
// rejected as a digest mismatch.
func TestCanonicalJSON_MatchesTSReference_UnicodeEdges(t *testing.T) {
	ls := string(rune(0x2028))      // U+2028 line separator
	ps := string(rune(0x2029))      // U+2029 paragraph separator
	pua := string(rune(0xE000))     // U+E000 private-use (BMP)
	astral := string(rune(0x10000)) // U+10000 (astral plane)
	cases := []struct {
		name string
		in   any
		want string
	}{
		{
			// JS emits U+2028/U+2029 raw; Go's encoding/json escapes them. The
			// runes are built explicitly here to keep this source pure ASCII.
			name: "line/paragraph separators emitted raw",
			in:   Node{"s": "a" + ls + "b" + ps + "c"},
			want: `{"s":"a` + ls + "b" + ps + `c"}`,
		},
		{
			// UTF-16 key sort: the astral char's lead surrogate (0xD800) sorts
			// BELOW the BMP char U+E000, so it comes first — the opposite of Go's
			// UTF-8/codepoint ordering.
			name: "astral key sorts before high-BMP key (UTF-16 order)",
			in:   Node{pua: json.Number("1"), astral: json.Number("2")},
			want: `{"` + astral + `":2,"` + pua + `":1}`,
		},
		{
			name: "C0 controls use short forms then backslash-u00XX",
			in:   Node{"s": "\b\t\n\f\r" + string(rune(0x01))},
			want: `{"s":"\b\t\n\f\r\u0001"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustCanonical(t, tc.in); got != tc.want {
				t.Errorf("CanonicalJSON =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestFixturesDirExists is a canary: the golden fixtures must be vendored.
func TestFixturesDirExists(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "fixtures"))
	if err != nil {
		t.Fatalf("read fixtures dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no golden fixtures vendored")
	}
}
