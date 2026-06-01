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
