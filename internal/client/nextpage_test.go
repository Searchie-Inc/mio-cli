package client

import "testing"

func TestCollectionNextPage_BackendShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want PageInfo
	}{
		{
			name: "build_page_meta: meta.page.{has_more,next_cursor} + links.next",
			body: `{"data":[{"id":"f1","type":"folders","attributes":{}}],
			        "meta":{"page":{"size":1,"has_more":true,"next_cursor":"Y3VyLWZvbGRlcnM"},"count":1},
			        "links":{"self":"/api/teams/t/folders?page%5Bsize%5D=1","next":"/api/teams/t/folders?page%5Bafter%5D=Y3VyLWZvbGRlcnM&page%5Bsize%5D=1"}}`,
			want: PageInfo{More: true, Cursor: "Y3VyLWZvbGRlcnM", Signalled: true, APICursors: true},
		},
		{
			name: "products: meta.page.has_more, cursor only in links.next",
			body: `{"data":[{"id":"p1","type":"products","attributes":{}}],
			        "meta":{"page":{"size":1,"has_more":true}},
			        "links":{"self":"/api/teams/t/products?page%5Bsize%5D=1","next":"/api/teams/t/products?page%5Bsize%5D=1&page%5Bafter%5D=cur-products"}}`,
			want: PageInfo{More: true, Cursor: "cur-products", Signalled: true, APICursors: true},
		},
		{
			name: "tags: top-level meta.has_more, cursor only in links.next",
			body: `{"data":[{"id":"tag1","type":"tags","attributes":{}}],
			        "meta":{"has_more":true},
			        "links":{"self":"/api/teams/t/tags","next":"/api/teams/t/tags?page%5Bafter%5D=tag1&page%5Bsize%5D=1"}}`,
			want: PageInfo{More: true, Cursor: "tag1", Signalled: true, APICursors: true},
		},
		{
			name: "discussions: top-level meta.{has_more,next_cursor}",
			body: `{"data":[{"id":"d1","type":"discussions","attributes":{}}],
			        "meta":{"next_cursor":"2026-09-24T11:00:00+00:00|d1","has_more":true},
			        "links":{"self":"/api/admin/teams/t/hubs/h/discussions?page[size]=1","next":"/api/admin/teams/t/hubs/h/discussions?page[size]=1&page[after]=2026-09-24T11:00:00+00:00|d1"}}`,
			want: PageInfo{More: true, Cursor: "2026-09-24T11:00:00+00:00|d1", Signalled: true, APICursors: true},
		},
		{
			name: "discussions: has_more with next_cursor null",
			body: `{"data":[{"id":"d1","type":"discussions","attributes":{}}],
			        "meta":{"next_cursor":null,"has_more":true},
			        "links":{"self":"/api/admin/teams/t/hubs/h/discussions?page[size]=1"}}`,
			want: PageInfo{More: true, Cursor: "", Signalled: true, APICursors: true},
		},
		{
			name: "activity: links.next as a link object",
			body: `{"data":[{"id":"c1","type":"top_engaged","attributes":{}}],
			        "meta":{"has_more":true,"next_cursor":"cur-activity"},
			        "links":{"self":{"href":"/api/teams/t/hubs/h/activity/top-engaged?page%5Bsize%5D=1"},"next":{"href":"/api/teams/t/hubs/h/activity/top-engaged?page%5Bsize%5D=1&page%5Bafter%5D=cur-activity"}}}`,
			want: PageInfo{More: true, Cursor: "cur-activity", Signalled: true, APICursors: true},
		},
		{
			name: "links.next link object carries the cursor",
			body: `{"data":[{"id":"c1","type":"top_engaged","attributes":{}}],
			        "meta":{"has_more":true},
			        "links":{"next":{"href":"/x?page%5Bafter%5D=cur-href&page%5Bsize%5D=1"}}}`,
			want: PageInfo{More: true, Cursor: "cur-href", Signalled: true, APICursors: true},
		},
		{
			name: "offerings: meta.page.has_more, no cursor field at all",
			body: `{"data":[{"id":"o1","type":"achievement_hubs","attributes":{}}],
			        "meta":{"page":{"size":1,"has_more":true}},"links":null}`,
			want: PageInfo{More: true, Cursor: "", Signalled: true, APICursors: false},
		},
		{
			name: "media files: links.next only, no meta",
			body: `{"data":[{"id":"file1","type":"files","attributes":{}}],
			        "links":{"next":"/api/teams/t/files?page[after]=file1&page[size]=1"}}`,
			want: PageInfo{More: true, LinkOnly: true, Cursor: "file1", Signalled: true, APICursors: true},
		},
		{
			name: "media files: last page, links null",
			body: `{"data":[{"id":"file9","type":"files","attributes":{}}],"links":null}`,
			want: PageInfo{},
		},
		{
			name: "checkout admin hub lists: meta.total only",
			body: `{"data":[{"id":"pay1","type":"payments","attributes":{}}],"meta":{"total":1}}`,
			want: PageInfo{},
		},
		{
			name: "checkout member payments: meta.{total,page.{has_more,next_cursor}}",
			body: `{"data":[{"id":"pay1","type":"payments","attributes":{}}],
			        "meta":{"total":40,"page":{"size":1,"has_more":true,"next_cursor":"cur-payments"}}}`,
			want: PageInfo{More: true, Cursor: "cur-payments", Signalled: true, APICursors: true},
		},
		{
			name: "media search: top_n has_more, no cursor",
			body: `{"data":[{"id":"s1","type":"media_search_results","attributes":{}}],
			        "meta":{"total":57,"has_more":true,"is_capped":false,"cap":100,"pagination":"top_n"}}`,
			want: PageInfo{More: true, Cursor: "", Signalled: true, APICursors: false},
		},
		{
			name: "last page: has_more false",
			body: `{"data":[{"id":"f9","type":"folders","attributes":{}}],
			        "meta":{"page":{"size":20,"has_more":false,"next_cursor":null}},
			        "links":{"self":"/api/teams/t/folders?page%5Bsize%5D=20","next":null}}`,
			want: PageInfo{Signalled: true, APICursors: true},
		},
		{
			name: "top-level has_more false",
			body: `{"data":[],"meta":{"has_more":false},"links":{"self":"/api/teams/t/tags","next":null}}`,
			want: PageInfo{Signalled: true},
		},
		{
			name: "no pagination meta at all",
			body: `{"data":[{"id":"t1","type":"teams","attributes":{}}],"meta":{"total":1}}`,
			want: PageInfo{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			col, err := DecodeCollection([]byte(tc.body))
			if err != nil {
				t.Fatalf("DecodeCollection: %v", err)
			}
			got := col.NextPage()
			if got.More != tc.want.More {
				t.Errorf("NextPage().More = %v, want %v — whether this envelope reports more rows was misread", got.More, tc.want.More)
			}
			if got.Cursor != tc.want.Cursor {
				t.Errorf("NextPage().Cursor = %q, want %q — the next-page cursor in this envelope was misread", got.Cursor, tc.want.Cursor)
			}
			if got.Signalled != tc.want.Signalled {
				t.Errorf("NextPage().Signalled = %v, want %v — whether this envelope carries any pagination signal was misread", got.Signalled, tc.want.Signalled)
			}
			if got.LinkOnly != tc.want.LinkOnly {
				t.Errorf("NextPage().LinkOnly = %v, want %v — whether \"more\" rests on links.next alone was misread", got.LinkOnly, tc.want.LinkOnly)
			}
			if got.APICursors != tc.want.APICursors {
				t.Errorf("NextPage().APICursors = %v, want %v — whether this API hands out its own cursors was misread", got.APICursors, tc.want.APICursors)
			}
		})
	}
}

func TestCollectionNextPage_NilSafe(t *testing.T) {
	var nilCol *Collection
	if got := nilCol.NextPage(); got != (PageInfo{}) {
		t.Errorf("nil collection NextPage() = %+v, want the zero PageInfo", got)
	}
	if got := (&Collection{}).NextPage(); got != (PageInfo{}) {
		t.Errorf("empty collection NextPage() = %+v, want the zero PageInfo", got)
	}
}
