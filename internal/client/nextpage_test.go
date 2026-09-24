package client

// nextpage_test.go — MIO-4174: Collection.NextPage reads "more rows exist" and
// the next cursor out of every pagination envelope mio-backend emits.
//
// The backend has no single list envelope. Each body below is transcribed from
// the handler named in its case (mio-backend origin/main 4215a46b), and each
// goes through DecodeCollection — the decoder production uses — so the
// retained RawBody that links.next is parsed from is the real one.

import "testing"

func TestCollectionNextPage_BackendShapes(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantMore   bool
		wantCursor string
	}{
		{
			// infrastructure/pagination.py build_page_meta: media files, spaces,
			// drip campaigns and the other "canonical" lists.
			name: "build_page_meta: meta.page.{has_more,next_cursor} + links.next",
			body: `{"data":[{"id":"f1","type":"files","attributes":{}}],
			        "meta":{"page":{"size":1,"has_more":true,"next_cursor":"Y3VyLWZpbGVz"}},
			        "links":{"self":"/api/teams/t/files?page%5Bsize%5D=1","next":"/api/teams/t/files?page%5Bafter%5D=Y3VyLWZpbGVz&page%5Bsize%5D=1"}}`,
			wantMore: true, wantCursor: "Y3VyLWZpbGVz",
		},
		{
			// products/router.py list_products (also coupons, contacts, content,
			// pages, hubs, achievements): meta.page.has_more but NO next_cursor —
			// the cursor exists only in links.next.
			name: "products: meta.page.has_more, cursor only in links.next",
			body: `{"data":[{"id":"p1","type":"products","attributes":{}}],
			        "meta":{"page":{"size":1,"has_more":true}},
			        "links":{"self":"/api/teams/t/products?page%5Bsize%5D=1","next":"/api/teams/t/products?page%5Bsize%5D=1&page%5Bafter%5D=cur-products"}}`,
			wantMore: true, wantCursor: "cur-products",
		},
		{
			// tags/router.py list_tags (also users, roles, api-keys, oauth
			// clients, access rules, automations, segments, events, contact
			// attributes, suppressions): TOP-LEVEL meta.has_more, cursor only in
			// links.next.
			name: "tags: top-level meta.has_more, cursor only in links.next",
			body: `{"data":[{"id":"tag1","type":"tags","attributes":{}}],
			        "meta":{"has_more":true},
			        "links":{"self":"/api/teams/t/tags","next":"/api/teams/t/tags?page%5Bafter%5D=tag1&page%5Bsize%5D=1"}}`,
			wantMore: true, wantCursor: "tag1",
		},
		{
			// community/routers/discussions_admin.py (also moderation queues,
			// hub members, activity, segment members): top-level
			// meta.{has_more,next_cursor}, no links.next.
			name: "discussions: top-level meta.{has_more,next_cursor}",
			body: `{"data":[{"id":"d1","type":"discussions","attributes":{}}],
			        "meta":{"has_more":true,"next_cursor":"cur-discussions"}}`,
			wantMore: true, wantCursor: "cur-discussions",
		},
		{
			// checkout/router.py (payments et al.): meta.page nested beside a
			// total.
			name: "checkout: meta.{total,page.{has_more,next_cursor}}",
			body: `{"data":[{"id":"pay1","type":"payments","attributes":{}}],
			        "meta":{"total":40,"page":{"size":1,"has_more":true,"next_cursor":"cur-payments"}}}`,
			wantMore: true, wantCursor: "cur-payments",
		},
		{
			// media/router.py admin_search_media: top-N search, so has_more with
			// no cursor at all — the only way to more rows is a larger page.
			name: "media search: top_n has_more, no cursor",
			body: `{"data":[{"id":"s1","type":"media_search_results","attributes":{}}],
			        "meta":{"total":57,"has_more":true,"is_capped":false,"cap":100,"pagination":"top_n"}}`,
			wantMore: true, wantCursor: "",
		},
		{
			// build_page_meta on the last page: has_more false, next null.
			name: "last page: has_more false",
			body: `{"data":[{"id":"f9","type":"files","attributes":{}}],
			        "meta":{"page":{"size":20,"has_more":false,"next_cursor":null}},
			        "links":{"self":"/api/teams/t/files?page%5Bsize%5D=20","next":null}}`,
			wantMore: false, wantCursor: "",
		},
		{
			name:     "top-level has_more false",
			body:     `{"data":[],"meta":{"has_more":false},"links":{"self":"/api/teams/t/tags","next":null}}`,
			wantMore: false, wantCursor: "",
		},
		{
			// An unpaginated list (teams list: meta.total only).
			name:     "no pagination meta at all",
			body:     `{"data":[{"id":"t1","type":"teams","attributes":{}}],"meta":{"total":1}}`,
			wantMore: false, wantCursor: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			col, err := DecodeCollection([]byte(tc.body))
			if err != nil {
				t.Fatalf("DecodeCollection: %v", err)
			}
			more, cursor := col.NextPage()
			if more != tc.wantMore {
				t.Errorf("NextPage() more = %v, want %v — the has_more flag in this envelope was misread", more, tc.wantMore)
			}
			if cursor != tc.wantCursor {
				t.Errorf("NextPage() cursor = %q, want %q — the next-page cursor in this envelope was misread", cursor, tc.wantCursor)
			}
		})
	}
}

// A collection with no retained body (built in-process) and no meta reports no
// more rows rather than panicking.
func TestCollectionNextPage_NilSafe(t *testing.T) {
	var nilCol *Collection
	if more, cur := nilCol.NextPage(); more || cur != "" {
		t.Errorf("nil collection NextPage() = (%v, %q), want (false, \"\")", more, cur)
	}
	if more, cur := (&Collection{}).NextPage(); more || cur != "" {
		t.Errorf("empty collection NextPage() = (%v, %q), want (false, \"\")", more, cur)
	}
}
