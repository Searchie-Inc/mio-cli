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

func TestRender_TableCollection_Golden(t *testing.T) {
	got := render(t, sampleCollection(), Options{Format: FormatTable})
	want := "ID  TYPE      NAME\n" +
		"1   products  A\n" +
		"2   products  B\n"
	if got != want {
		t.Errorf("table golden mismatch:\n got: %q\nwant: %q", got, want)
	}
}

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

func plainJQ(t *testing.T, program string) string {
	t.Helper()
	return render(t, sampleCollection(), Options{Format: FormatPlain, JQ: program})
}

func TestRender_PlainScalarStream_OneBareValuePerLine(t *testing.T) {
	cases := []struct {
		name, jq, want string
	}{
		{"stream of two strings", `.[].name`, "A\nB\n"},
		{"explicit array of two strings", `[.[].name]`, "A\nB\n"},
		{"stream of one string", `.[0].name`, "A\n"},
		{"array of one string", `[.[0].name]`, "A\n"},
		{"stream of numbers", `.[] | .id | tonumber`, "1\n2\n"},
		{"stream of mixed scalars", `"x", 3, true, 1.5`, "x\n3\ntrue\n1.5\n"},
		{"stream with a big integer", `1, 100000000000000000000`, "1\n100000000000000000000\n"},
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
	got := render(t, sampleCollection(), Options{Format: FormatJSON, JQ: `"line1\nline2", "c"`})
	if want := "[\n  \"line1\\nline2\",\n  \"c\"\n]\n"; got != want {
		t.Errorf("-o json --jq stream\n got: %q\nwant: %q", got, want)
	}
}

func TestRender_PlainZeroResults_PrintsNothing(t *testing.T) {
	for _, jq := range []string{`.[] | select(.name == "nope") | .name`, `empty`, `.[] | select(false)`} {
		if got := plainJQ(t, jq); got != "" {
			t.Errorf("-o plain --jq %s = %q, want empty output for a filter that yields no values", jq, got)
		}
	}
	if got := plainJQ(t, `[]`); got != "" {
		t.Errorf("-o plain --jq [] = %q, want empty output", got)
	}
}

func TestRender_PlainObjectsAndMixedArrays_KeepKeyValueBlocks(t *testing.T) {
	cases := []struct {
		name, jq, want string
	}{
		{"stream of objects", `.[]`, "id=1\nname=A\ntype=products\n\nid=2\nname=B\ntype=products\n"},
		{"single object", `.[0]`, "id=1\nname=A\ntype=products\n"},
		{"mixed object and scalar", `[.[0] | {name}, "x"]`, "name=A\n\nvalue=x\n"},
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
