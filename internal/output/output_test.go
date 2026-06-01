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

// Golden: raw JSON keeps the JSON:API envelope.
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
