package client

import (
	"encoding/json"
	"fmt"
)

// Resource is a single JSON:API resource object: a typed, identified bag of
// attributes. Relationships and links are intentionally not modelled — the CLI
// surfaces flattened attribute data to agents and does not traverse graphs.
type Resource struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
}

// Flatten merges id, type and every attribute into a single flat map suitable
// for clean agent-facing JSON, table rows, and plain key=value output.
//
// "id" and "type" always win over a same-named attribute (the spec forbids
// id/type inside attributes, but we guard anyway so output is deterministic).
func (r Resource) Flatten() map[string]any {
	out := make(map[string]any, len(r.Attributes)+2)
	for k, v := range r.Attributes {
		out[k] = v
	}
	if r.ID != "" {
		out["id"] = r.ID
	}
	if r.Type != "" {
		out["type"] = r.Type
	}
	return out
}

// Collection is a JSON:API collection document: a list of resources plus the
// top-level meta object (which carries pagination cursors such as `next`).
type Collection struct {
	Data []Resource     `json:"data"`
	Meta map[string]any `json:"meta"`
}

// Flatten returns one flattened map per resource, preserving order.
func (c Collection) Flatten() []map[string]any {
	out := make([]map[string]any, 0, len(c.Data))
	for _, r := range c.Data {
		out = append(out, r.Flatten())
	}
	return out
}

// singleDoc is the wire shape of a JSON:API single-resource document.
type singleDoc struct {
	Data   *Resource      `json:"data"`
	Errors []apiError     `json:"errors"`
	Meta   map[string]any `json:"meta"`
}

// collectionDoc is the wire shape of a JSON:API collection document. Data is a
// RawMessage first so we can tolerate a single object where a list is expected.
type collectionDoc struct {
	Data   json.RawMessage `json:"data"`
	Errors []apiError      `json:"errors"`
	Meta   map[string]any  `json:"meta"`
}

// DecodeResource parses a JSON:API single-resource document. It returns an
// error if the body carries a top-level `errors` array or lacks `data`.
func DecodeResource(body []byte) (*Resource, error) {
	var doc singleDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode resource: %w", err)
	}
	if len(doc.Errors) > 0 {
		return nil, &apiErrorList{Errors: doc.Errors}
	}
	if doc.Data == nil {
		return nil, fmt.Errorf("decode resource: response had no `data` member")
	}
	return doc.Data, nil
}

// DecodeCollection parses a JSON:API collection document. A single object in
// `data` is tolerated and promoted to a one-element collection so callers that
// hit an endpoint returning either shape stay simple.
func DecodeCollection(body []byte) (*Collection, error) {
	var doc collectionDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode collection: %w", err)
	}
	if len(doc.Errors) > 0 {
		return nil, &apiErrorList{Errors: doc.Errors}
	}

	col := &Collection{Meta: doc.Meta}
	if len(doc.Data) == 0 || string(doc.Data) == "null" {
		col.Data = []Resource{}
		return col, nil
	}

	// Try a list first; fall back to a single object.
	var list []Resource
	if err := json.Unmarshal(doc.Data, &list); err == nil {
		col.Data = list
		return col, nil
	}
	var one Resource
	if err := json.Unmarshal(doc.Data, &one); err != nil {
		return nil, fmt.Errorf("decode collection: `data` was neither a list nor an object: %w", err)
	}
	col.Data = []Resource{one}
	return col, nil
}
