package client

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// Resource is a single JSON:API resource object: a typed, identified bag of
// attributes. Relationships and links are intentionally not modelled — the CLI
// surfaces flattened attribute data to agents and does not traverse graphs.
//
// RawBody holds the original, unflattened response envelope bytes (the whole
// document, including top-level links/included/meta). It is populated by the
// decoders and used by the output layer when --raw is requested so the full
// JSON:API envelope round-trips instead of the flattened view. It is excluded
// from JSON marshalling so it never leaks into rendered output.
type Resource struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
	// Meta is the resource-level `meta` object. Most reads leave it nil; a few
	// writes return operational data here (e.g. the presigned `upload_url` on
	// files create, MIO-2267). Excluded from Flatten so it never leaks into the
	// agent-facing view.
	Meta    map[string]any `json:"meta,omitempty"`
	RawBody []byte         `json:"-"`
}

// Flatten merges id, type and every attribute into a single flat map suitable
// for clean agent-facing JSON, table rows, and plain key=value output.
//
// The envelope "id" is the resource identity and always wins over a same-named
// attribute. "type" is different: several mio schemas carry a business-level
// `type` attribute (products: course/membership/booking; contact-attributes:
// text/number/select) alongside the JSON:API document type ("products"). That
// business value is the meaningful one, so it MUST survive flattening — clobbering
// it with the transport discriminator hid it from default output (MIO-2647). The
// envelope type only fills in when the resource has no `type` attribute of its
// own; the document type is always available under --raw (.data.type). A present
// but null `type` attribute is preserved as null (the faithful business value),
// not backfilled with the document type.
func (r Resource) Flatten() map[string]any {
	out := make(map[string]any, len(r.Attributes)+2)
	for k, v := range r.Attributes {
		out[k] = v
	}
	if r.ID != "" {
		out["id"] = r.ID
	}
	if _, hasAttrType := r.Attributes["type"]; !hasAttrType && r.Type != "" {
		out["type"] = r.Type
	}
	return out
}

// Collection is a JSON:API collection document: a list of resources plus the
// top-level meta object (which carries pagination cursors such as `next`).
//
// RawBody holds the original, unflattened response envelope bytes (the whole
// document, including top-level links/included/meta). See Resource.RawBody. It
// is excluded from JSON marshalling so it never leaks into rendered output.
type Collection struct {
	Data    []Resource     `json:"data"`
	Meta    map[string]any `json:"meta"`
	RawBody []byte         `json:"-"`
}

// PageInfo is what a list response says about the rows past it (MIO-4174).
type PageInfo struct {
	// More is true when the API reported more rows past this page: has_more
	// true, or — on a list that sends no has_more at all — a links.next.
	More bool
	// LinkOnly is true when More rests on links.next alone. Such a link is
	// weaker than has_more: media files and attachments send one on every
	// FULL page (len == page[size], no over-fetch), so the page it points to
	// can be empty.
	LinkOnly bool
	// Cursor is the page[after] value the API handed out for the next page,
	// or "" when it handed out none.
	Cursor string
	// Signalled is true when the envelope carries any pagination signal at
	// all: a has_more flag, a next_cursor key or a links.next. A list whose
	// envelope carries none (checkout's admin hub lists, meta.total only)
	// cannot say whether a full page is the last one.
	Signalled bool
	// APICursors is true when the envelope has a place for the API's own
	// cursor — a next_cursor key (even a null one) or a links.next — so a
	// cursor must never be derived from the rows. A null next_cursor beside
	// has_more:true means the API has more rows it cannot page to.
	APICursors bool
}

// NextPage reads PageInfo out of a list response (MIO-4174).
//
// mio-backend has no single list envelope (origin/main 2dc04aab), so this
// reads every shape it emits:
//
//   - has_more sits at meta.page.has_more (build_page_meta, products, coupons,
//     contacts, content, pages, hubs, achievements) or at top-level
//     meta.has_more (tags, users, roles, api-keys, automations, segments,
//     events, discussions, moderation, hub members, activity, media search).
//   - the media lists (files, attachments, playlists, playlist items, hub
//     media, hub playlists) send no meta at all: links.next is their only
//     signal. Playlists, playlist items and the hub lists send it when a
//     size+1 probe found more; files and attachments send it on every full
//     page, so it can point at an empty one (LinkOnly).
//   - the cursor sits at meta.page.next_cursor, at meta.next_cursor, or only as
//     the page[after] parameter of links.next (products, coupons, tags, the
//     media lists and every other list that emits no next_cursor). links.next
//     is a URL string, or a JSON:API link object {"href": …} (activity).
//
// A present has_more is authoritative; links.next decides only where no
// has_more is sent. A top-N list (media search) reports has_more with no
// cursor at all: More is true and Cursor is empty. links is not modelled on
// Collection, so it is read from the retained RawBody.
func (c *Collection) NextPage() PageInfo {
	if c == nil {
		return PageInfo{}
	}
	page, _ := c.Meta["page"].(map[string]any)
	pageMore, pageHas := page["has_more"].(bool)
	topMore, topHas := c.Meta["has_more"].(bool)
	pageCur, pageCurKey := page["next_cursor"]
	topCur, topCurKey := c.Meta["next_cursor"]
	nextLink := nextLinkOf(c.RawBody)

	info := PageInfo{
		Signalled:  pageHas || topHas || pageCurKey || topCurKey || nextLink != "",
		APICursors: pageCurKey || topCurKey || nextLink != "",
	}
	if pageHas || topHas {
		info.More = pageMore || topMore
	} else {
		info.More = nextLink != ""
		info.LinkOnly = info.More
	}
	if !info.More {
		return info
	}
	if cur, _ := pageCur.(string); cur != "" {
		info.Cursor = cur
	} else if cur, _ := topCur.(string); cur != "" {
		info.Cursor = cur
	} else {
		info.Cursor = afterParam(nextLink)
	}
	return info
}

// nextLinkOf returns the envelope's links.next URL — a plain string, or the
// href of a link object — or "" when there is none.
func nextLinkOf(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var doc struct {
		Links struct {
			Next json.RawMessage `json:"next"`
		} `json:"links"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Links.Next) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(doc.Links.Next, &s) == nil {
		return s
	}
	var obj struct {
		Href string `json:"href"`
	}
	if json.Unmarshal(doc.Links.Next, &obj) == nil {
		return obj.Href
	}
	return ""
}

// afterParam returns the page[after] parameter of a next-page URL, or "".
func afterParam(link string) string {
	if link == "" {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Query().Get("page[after]")
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
	// Retain the original envelope bytes so --raw can preserve top-level
	// links/included/meta that the flattened Resource does not model.
	doc.Data.RawBody = append([]byte(nil), body...)
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

	// Retain the original envelope bytes so --raw can preserve top-level
	// links/included/meta that the flattened Collection does not model.
	col := &Collection{Meta: doc.Meta, RawBody: append([]byte(nil), body...)}
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
