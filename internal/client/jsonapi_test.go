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

func TestResourceFlatten_IDTypeWinOverAttributes(t *testing.T) {
	// id/type in attributes must never override the real id/type.
	r := Resource{
		ID:         "real_id",
		Type:       "real_type",
		Attributes: map[string]any{"id": "bogus", "type": "bogus", "x": 1},
	}
	got := r.Flatten()
	if got["id"] != "real_id" {
		t.Errorf("id = %v, want real_id", got["id"])
	}
	if got["type"] != "real_type" {
		t.Errorf("type = %v, want real_type", got["type"])
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
