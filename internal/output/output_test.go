package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/client"
)

func render(t *testing.T, v any, opts Options) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Render(&buf, v, opts); err != nil {
		t.Fatalf("Render error: %v", err)
	}
	return buf.String()
}

func sampleResource() *client.Resource {
	return &client.Resource{
		ID:         "prod_1",
		Type:       "products",
		Attributes: map[string]any{"name": "Pro", "published": true, "price": float64(4900)},
	}
}

func sampleCollection() *client.Collection {
	return &client.Collection{
		Data: []client.Resource{
			{ID: "1", Type: "products", Attributes: map[string]any{"name": "A"}},
			{ID: "2", Type: "products", Attributes: map[string]any{"name": "B"}},
		},
		Meta: map[string]any{"next": "cur"},
	}
}

// Golden: flattened JSON for a single resource. Keys sorted by encoding/json.
func TestRender_JSONResource_Golden(t *testing.T) {
	got := render(t, sampleResource(), Options{Format: FormatJSON})
	want := `{
  "id": "prod_1",
  "name": "Pro",
  "price": 4900,
  "published": true,
  "type": "products"
}
`
	if got != want {
		t.Errorf("JSON resource golden mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Golden: raw JSON keeps the JSON:API envelope. With no retained RawBody the
// renderer falls back to re-encoding the modelled resource under `data`.
func TestRender_JSONRaw_Golden(t *testing.T) {
	got := render(t, sampleResource(), Options{Format: FormatJSON, Raw: true})
	want := `{
  "attributes": {
    "name": "Pro",
    "price": 4900,
    "published": true
  },
  "id": "prod_1",
  "type": "products"
}
`
	if got != want {
		t.Errorf("raw JSON golden mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// --raw must preserve the ORIGINAL response envelope, including top-level
// links/included/meta that the flattened Resource view drops. When the client
// retained the raw bytes, those are emitted verbatim.
func TestRender_JSONRaw_PreservesEnvelope(t *testing.T) {
	raw := []byte(`{
	  "data": {"id":"prod_1","type":"products","attributes":{"name":"Pro"}},
	  "included": [{"id":"pr_1","type":"prices","attributes":{"amount":4900}}],
	  "links": {"self":"/api/teams/t1/products/prod_1"},
	  "meta": {"request_id":"req_abc"}
	}`)
	res := &client.Resource{
		ID:         "prod_1",
		Type:       "products",
		Attributes: map[string]any{"name": "Pro"},
		RawBody:    raw,
	}

	got := render(t, res, Options{Format: FormatJSON, Raw: true})

	for _, must := range []string{`"included"`, `"pr_1"`, `"links"`, `"/api/teams/t1/products/prod_1"`, `"meta"`, `"req_abc"`} {
		if !strings.Contains(got, must) {
			t.Errorf("--raw output dropped %s; got:\n%s", must, got)
		}
	}
}

// Same guarantee for collections: top-level links/meta round-trip under --raw.
func TestRender_JSONRawCollection_PreservesEnvelope(t *testing.T) {
	raw := []byte(`{
	  "data": [{"id":"1","type":"products","attributes":{"name":"A"}}],
	  "links": {"self":"/api/teams/t1/products","next":"/api/teams/t1/products?page[after]=cur"},
	  "meta": {"page":{"has_more":true}}
	}`)
	col := &client.Collection{
		Data:    []client.Resource{{ID: "1", Type: "products", Attributes: map[string]any{"name": "A"}}},
		Meta:    map[string]any{"page": map[string]any{"has_more": true}},
		RawBody: raw,
	}

	got := render(t, col, Options{Format: FormatJSON, Raw: true})

	for _, must := range []string{`"links"`, `"next"`, `page[after]=cur`, `"has_more"`} {
		if !strings.Contains(got, must) {
			t.Errorf("--raw collection output dropped %s; got:\n%s", must, got)
		}
	}
}

// Golden: table output for a collection. id and type lead, then alpha columns.
func TestRender_TableCollection_Golden(t *testing.T) {
	got := render(t, sampleCollection(), Options{Format: FormatTable})
	want := "ID  TYPE      NAME\n" +
		"1   products  A\n" +
		"2   products  B\n"
	if got != want {
		t.Errorf("table golden mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Golden: plain key=value for a single resource (keys sorted).
func TestRender_PlainResource_Golden(t *testing.T) {
	got := render(t, sampleResource(), Options{Format: FormatPlain})
	want := "id=prod_1\n" +
		"name=Pro\n" +
		"price=4900\n" +
		"published=true\n" +
		"type=products\n"
	if got != want {
		t.Errorf("plain golden mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestRender_JQFilter(t *testing.T) {
	got := render(t, sampleResource(), Options{Format: FormatJSON, JQ: ".name"})
	if strings.TrimSpace(got) != `"Pro"` {
		t.Errorf("jq .name = %q, want \"Pro\"", strings.TrimSpace(got))
	}
}

func TestRender_JQOverCollection(t *testing.T) {
	got := render(t, sampleCollection(), Options{Format: FormatJSON, JQ: ".[].name"})
	// Two outputs → slice ["A","B"].
	norm := strings.Join(strings.Fields(got), "")
	if norm != `["A","B"]` {
		t.Errorf("jq over collection = %q, want [\"A\",\"B\"]", norm)
	}
}

func TestRender_JQInvalid(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, sampleResource(), Options{Format: FormatJSON, JQ: ".["})
	if err == nil {
		t.Fatal("expected error for invalid jq expression")
	}
}

func TestParseFormat(t *testing.T) {
	for _, f := range []string{"json", "table", "plain"} {
		if _, err := ParseFormat(f); err != nil {
			t.Errorf("ParseFormat(%q) error: %v", f, err)
		}
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Error("ParseFormat(yaml) should error")
	}
}

func TestRender_EmptyCollectionTable(t *testing.T) {
	got := render(t, &client.Collection{Data: []client.Resource{}}, Options{Format: FormatTable})
	if !strings.Contains(got, "no results") {
		t.Errorf("empty table = %q, want a no-results notice", got)
	}
}

// ---- MIO-4174: -o plain over a --jq stream ----------------------------------
//
// Before MIO-4174 the SHAPE of plain output depended on how many values the
// filter produced: one scalar printed bare, two or more printed as
// "value=X" records separated by blank lines, and zero printed a blank line. A
// capture loop that worked on a team with one row broke on a team with two.
// The contract now: a stream or array of scalars is one bare value per line,
// whatever the count (a value that itself holds a newline spans lines);
// objects and mixed arrays keep their key=value blocks.

// plainJQ renders the two-row sample collection in plain mode through a jq
// program. Every case goes through the real Render entry point, so the
// stream-collapsing in the jq step and the plain formatter are both exercised.
func plainJQ(t *testing.T, program string) string {
	t.Helper()
	return render(t, sampleCollection(), Options{Format: FormatPlain, JQ: program})
}

func TestRender_PlainScalarStream_OneBareValuePerLine(t *testing.T) {
	cases := []struct {
		name, jq, want string
	}{
		// The reported case: a stream of 2+ strings.
		{"stream of two strings", `.[].name`, "A\nB\n"},
		// Same values as an explicit array: identical output, because a jq
		// stream of 2+ and a single array result reach the formatter as the
		// same []any.
		{"explicit array of two strings", `[.[].name]`, "A\nB\n"},
		// One value, as a stream and as an array: the array used to print
		// "value=A".
		{"stream of one string", `.[0].name`, "A\n"},
		{"array of one string", `[.[0].name]`, "A\n"},
		// Numbers and booleans are scalars too; integers print without a
		// decimal exactly as a single scalar always did.
		{"stream of numbers", `.[] | .id | tonumber`, "1\n2\n"},
		{"stream of mixed scalars", `"x", 3, true, 1.5`, "x\n3\ntrue\n1.5\n"},
		// gojq emits *big.Int for an integer too large for int.
		{"stream with a big integer", `1, 100000000000000000000`, "1\n100000000000000000000\n"},
		// null is a scalar with an empty rendering. It keeps its own line, so a
		// null does not shift the values after it. (A string that itself holds
		// a newline does: see TestRender_PlainScalarStream_NewlineInAValue.)
		{"null keeps its line", `.[].id, null, "z"`, "1\n2\n\nz\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := plainJQ(t, tc.jq)
			if got != tc.want {
				t.Errorf("-o plain --jq %s\n got: %q\nwant: %q (one bare value per line, no value= prefix, no blank separators)", tc.jq, got, tc.want)
			}
			if strings.Contains(got, "value=") {
				t.Errorf("-o plain --jq %s printed a value= record for a scalar: %q", tc.jq, got)
			}
		})
	}
}

// A string is printed bare and VERBATIM, exactly as a single scalar always was
// (`-o plain --jq .content` must hand back multi-line policy text intact). So a
// value that holds a newline spans several lines, and in a stream it shifts the
// values after it: "one value per line" holds only for values without one
// (ids, slugs, numbers). The docs say so, and point at -o json for the rest.
func TestRender_PlainScalarStream_NewlineInAValue(t *testing.T) {
	cases := []struct {
		name, jq, want string
	}{
		{"single multi-line value", `"line1\nline2"`, "line1\nline2\n"},
		{"stream with a multi-line value", `"line1\nline2", "c"`, "line1\nline2\nc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := plainJQ(t, tc.jq); got != tc.want {
				t.Errorf("-o plain --jq %s\n got: %q\nwant: %q (the value printed verbatim, its newline included)", tc.jq, got, tc.want)
			}
		})
	}
	// -o json is the documented way to keep such values one per line: each
	// is one JSON string, its newline escaped.
	got := render(t, sampleCollection(), Options{Format: FormatJSON, JQ: `"line1\nline2", "c"`})
	if want := "[\n  \"line1\\nline2\",\n  \"c\"\n]\n"; got != want {
		t.Errorf("-o json --jq stream\n got: %q\nwant: %q", got, want)
	}
}

// Zero results print NOTHING in plain mode. Before MIO-4174 they printed one
// empty line, which `while read` turns into one empty iteration.
func TestRender_PlainZeroResults_PrintsNothing(t *testing.T) {
	for _, jq := range []string{`.[] | select(.name == "nope") | .name`, `empty`, `.[] | select(false)`} {
		if got := plainJQ(t, jq); got != "" {
			t.Errorf("-o plain --jq %s = %q, want empty output for a filter that yields no values", jq, got)
		}
	}
	// An explicit empty array was already silent; it stays that way.
	if got := plainJQ(t, `[]`); got != "" {
		t.Errorf("-o plain --jq [] = %q, want empty output", got)
	}
}

// Objects and mixed arrays keep the key=value record format, byte for byte.
func TestRender_PlainObjectsAndMixedArrays_KeepKeyValueBlocks(t *testing.T) {
	cases := []struct {
		name, jq, want string
	}{
		{"stream of objects", `.[]`, "id=1\nname=A\ntype=products\n\nid=2\nname=B\ntype=products\n"},
		{"single object", `.[0]`, "id=1\nname=A\ntype=products\n"},
		// A mixed array is not a scalar list: its scalars stay value= records so
		// they cannot be mistaken for the objects' key=value lines.
		{"mixed object and scalar", `[.[0] | {name}, "x"]`, "name=A\n\nvalue=x\n"},
		// Nested arrays are not scalars either.
		{"array of arrays", `[[1, 2], [3]]`, "value=[1,2]\n\nvalue=[3]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := plainJQ(t, tc.jq); got != tc.want {
				t.Errorf("-o plain --jq %s\n got: %q\nwant: %q", tc.jq, got, tc.want)
			}
		})
	}
}

// JSON and table output are NOT part of MIO-4174: a stream of 2+ still renders
// as one pretty-printed JSON array, one value bare, and zero values as null.
// Pinned here so the plain-mode change cannot leak into the other formats.
func TestRender_JSONAndTable_UnchangedForStreams(t *testing.T) {
	cases := []struct {
		name   string
		format Format
		jq     string
		want   string
	}{
		{"json stream", FormatJSON, `.[].name`, "[\n  \"A\",\n  \"B\"\n]\n"},
		{"json one", FormatJSON, `.[0].name`, "\"A\"\n"},
		{"json zero", FormatJSON, `empty`, "null\n"},
		{"table stream", FormatTable, `.[].name`, "VALUE\nA\nB\n"},
		{"table zero", FormatTable, `empty`, "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render(t, sampleCollection(), Options{Format: tc.format, JQ: tc.jq})
			if got != tc.want {
				t.Errorf("%s --jq %s\n got: %q\nwant: %q", tc.format, tc.jq, got, tc.want)
			}
		})
	}
}
