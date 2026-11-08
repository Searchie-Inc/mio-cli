package client

import (
	"encoding/json"
	"fmt"
	"net/url"
)

type Resource struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
	Meta       map[string]any `json:"meta,omitempty"`
	RawBody    []byte         `json:"-"`
}

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

type Collection struct {
	Data    []Resource     `json:"data"`
	Meta    map[string]any `json:"meta"`
	RawBody []byte         `json:"-"`
}

type PageInfo struct {
	More       bool
	LinkOnly   bool
	Cursor     string
	Signalled  bool
	APICursors bool
}

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

func (c Collection) Flatten() []map[string]any {
	out := make([]map[string]any, 0, len(c.Data))
	for _, r := range c.Data {
		out = append(out, r.Flatten())
	}
	return out
}

type singleDoc struct {
	Data   *Resource      `json:"data"`
	Errors []apiError     `json:"errors"`
	Meta   map[string]any `json:"meta"`
}

type collectionDoc struct {
	Data   json.RawMessage `json:"data"`
	Errors []apiError      `json:"errors"`
	Meta   map[string]any  `json:"meta"`
}

func DecodeResource(body []byte) (*Resource, error) {
	var doc singleDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode resource: %w", err)
	}
	if len(doc.Errors) > 0 {
		return nil, newAPIErrorList(doc.Errors, body)
	}
	if doc.Data == nil {
		return nil, fmt.Errorf("decode resource: response had no `data` member")
	}
	doc.Data.RawBody = append([]byte(nil), body...)
	return doc.Data, nil
}

func DecodeCollection(body []byte) (*Collection, error) {
	var doc collectionDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode collection: %w", err)
	}
	if len(doc.Errors) > 0 {
		return nil, newAPIErrorList(doc.Errors, body)
	}

	col := &Collection{Meta: doc.Meta, RawBody: append([]byte(nil), body...)}
	if len(doc.Data) == 0 || string(doc.Data) == "null" {
		col.Data = []Resource{}
		return col, nil
	}

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
