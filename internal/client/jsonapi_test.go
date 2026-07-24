package client

import (
	"reflect"
	"testing"
)

func TestDecodeResource(t *testing.T) {
	body := []byte(`{
	  "data": {
	    "id": "prod_123",
	    "type": "products",
	    "attributes": {"name": "Pro", "published": true, "price": 4900}
	  }
	}`)

	res, err := DecodeResource(body)
	if err != nil {
		t.Fatalf("DecodeResource returned error: %v", err)
	}
	if res.ID != "prod_123" {
		t.Errorf("ID = %q, want prod_123", res.ID)
	}
	if res.Type != "products" {
		t.Errorf("Type = %q, want products", res.Type)
	}
	if got := res.Attributes["name"]; got != "Pro" {
		t.Errorf("name = %v, want Pro", got)
	}
}

func TestDecodeResource_ErrorsArray(t *testing.T) {
	body := []byte(`{"errors":[{"status":"404","detail":"product not found"}]}`)
	if _, err := DecodeResource(body); err == nil {
		t.Fatal("expected error for body with errors array, got nil")
	}
}

func TestDecodeResource_NoData(t *testing.T) {
	body := []byte(`{"meta":{"foo":"bar"}}`)
	if _, err := DecodeResource(body); err == nil {
		t.Fatal("expected error for body without data, got nil")
	}
}

func TestResourceFlatten(t *testing.T) {
	r := Resource{
		ID:         "con_1",
		Type:       "contacts",
		Attributes: map[string]any{"email": "a@example.com", "first_name": "Ada"},
	}
	got := r.Flatten()
	want := map[string]any{
		"id":         "con_1",
		"type":       "contacts",
		"email":      "a@example.com",
		"first_name": "Ada",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Flatten() = %#v, want %#v", got, want)
	}
}

func TestResourceFlatten_IDWinsOverAttributes(t *testing.T) {
	// The envelope id is the resource identity — it always wins over a same-named
	// attribute (JSON:API forbids `id` in attributes anyway).
	r := Resource{
		ID:         "real_id",
		Type:       "products",
		Attributes: map[string]any{"id": "bogus", "x": 1},
	}
	got := r.Flatten()
	if got["id"] != "real_id" {
		t.Errorf("id = %v, want real_id", got["id"])
	}
}

func TestResourceFlatten_BusinessTypeSurvives(t *testing.T) {
	// MIO-2647: a business-level `type` attribute (products.type=course,
	// contact-attributes.type=text) must survive flattening and win over the
	// JSON:API document type ("products"), which is only a transport discriminator.
	r := Resource{
		ID:         "prod_1",
		Type:       "products",
		Attributes: map[string]any{"type": "course", "name": "X"},
	}
	got := r.Flatten()
	if got["type"] != "course" {
		t.Errorf("type = %v, want course (business type must survive — MIO-2647)", got["type"])
	}
}

func TestResourceFlatten_EnvelopeTypeWhenNoAttributeType(t *testing.T) {
	// When the schema has no business `type`, the JSON:API document type fills in.
	r := Resource{
		ID:         "con_1",
		Type:       "team_contacts",
		Attributes: map[string]any{"email": "a@example.com"},
	}
	got := r.Flatten()
	if got["type"] != "team_contacts" {
		t.Errorf("type = %v, want team_contacts (envelope type fills in)", got["type"])
	}
}

func TestDecodeCollection(t *testing.T) {
	body := []byte(`{
	  "data": [
	    {"id":"1","type":"products","attributes":{"name":"A"}},
	    {"id":"2","type":"products","attributes":{"name":"B"}}
	  ],
	  "meta": {"next": "cursor_abc"}
	}`)

	col, err := DecodeCollection(body)
	if err != nil {
		t.Fatalf("DecodeCollection returned error: %v", err)
	}
	if len(col.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(col.Data))
	}
	if col.Meta["next"] != "cursor_abc" {
		t.Errorf("meta.next = %v, want cursor_abc", col.Meta["next"])
	}
	flat := col.Flatten()
	if flat[0]["name"] != "A" || flat[1]["name"] != "B" {
		t.Errorf("flattened collection = %#v", flat)
	}
}

func TestDecodeCollection_Empty(t *testing.T) {
	col, err := DecodeCollection([]byte(`{"data":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(col.Data) != 0 {
		t.Errorf("len(Data) = %d, want 0", len(col.Data))
	}
}

func TestDecodeCollection_SingleObjectPromoted(t *testing.T) {
	// An endpoint that returns a single object where a list is expected should
	// be tolerated as a one-element collection.
	body := []byte(`{"data":{"id":"1","type":"products","attributes":{"name":"Solo"}}}`)
	col, err := DecodeCollection(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(col.Data) != 1 || col.Data[0].ID != "1" {
		t.Errorf("expected single promoted element, got %#v", col.Data)
	}
}
