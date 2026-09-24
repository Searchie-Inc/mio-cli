package client

// nextpage_test.go — MIO-4174: Collection.NextPage reads "more rows exist" and
// the next cursor out of every pagination envelope mio-backend emits.
//
// The backend has no single list envelope. Each body below is transcribed from
// the handler named in its case (mio-backend origin/main 2dc04aab), and each
// goes through DecodeCollection — the decoder production uses — so the
// retained RawBody that links.next is parsed from is the real one.

import "testing"

func TestCollectionNextPage_BackendShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want PageInfo
	}{
		{
			// infrastructure/pagination.py build_page_meta: media folders
			// (list_folders), email drip campaigns / steps / templates /
			// enrollments, community spaces (admin_list_spaces).
			name: "build_page_meta: meta.page.{has_more,next_cursor} + links.next",
			body: `{"data":[{"id":"f1","type":"folders","attributes":{}}],
			        "meta":{"page":{"size":1,"has_more":true,"next_cursor":"Y3VyLWZvbGRlcnM"},"count":1},
			        "links":{"self":"/api/teams/t/folders?page%5Bsize%5D=1","next":"/api/teams/t/folders?page%5Bafter%5D=Y3VyLWZvbGRlcnM&page%5Bsize%5D=1"}}`,
			want: PageInfo{More: true, Cursor: "Y3VyLWZvbGRlcnM", Signalled: true, APICursors: true},
		},
		{
			// products/router.py admin_list_products (also coupons, contacts,
			// content, pages, hubs): meta.page.has_more but NO next_cursor —
			// the cursor exists only in links.next.
			name: "products: meta.page.has_more, cursor only in links.next",
			body: `{"data":[{"id":"p1","type":"products","attributes":{}}],
			        "meta":{"page":{"size":1,"has_more":true}},
			        "links":{"self":"/api/teams/t/products?page%5Bsize%5D=1","next":"/api/teams/t/products?page%5Bsize%5D=1&page%5Bafter%5D=cur-products"}}`,
			want: PageInfo{More: true, Cursor: "cur-products", Signalled: true, APICursors: true},
		},
		{
			// tags/router.py list_tags (also users, roles, api-keys, oauth
			// clients, access rules, automations, segments, events, contact
			// attributes, suppressions, webhook endpoints): TOP-LEVEL
			// meta.has_more, cursor only in links.next.
			name: "tags: top-level meta.has_more, cursor only in links.next",
			body: `{"data":[{"id":"tag1","type":"tags","attributes":{}}],
			        "meta":{"has_more":true},
			        "links":{"self":"/api/teams/t/tags","next":"/api/teams/t/tags?page%5Bafter%5D=tag1&page%5Bsize%5D=1"}}`,
			want: PageInfo{More: true, Cursor: "tag1", Signalled: true, APICursors: true},
		},
		{
			// community/routers/discussions_admin.py _list_response (also
			// moderation queues, hub members, segment members): top-level
			// meta.{next_cursor,has_more}. Its links.next carries the raw
			// "<iso>|<id>" cursor unencoded, so the "+00:00" in it would read
			// back as " 00:00" from the query string: meta.next_cursor wins.
			name: "discussions: top-level meta.{has_more,next_cursor}",
			body: `{"data":[{"id":"d1","type":"discussions","attributes":{}}],
			        "meta":{"next_cursor":"2026-09-24T11:00:00+00:00|d1","has_more":true},
			        "links":{"self":"/api/admin/teams/t/hubs/h/discussions?page[size]=1","next":"/api/admin/teams/t/hubs/h/discussions?page[size]=1&page[after]=2026-09-24T11:00:00+00:00|d1"}}`,
			want: PageInfo{More: true, Cursor: "2026-09-24T11:00:00+00:00|d1", Signalled: true, APICursors: true},
		},
		{
			// discussions_admin._build_cursor returns None when the last row's
			// last_activity_at is NULL (the column is nullable), so the page
			// says has_more with next_cursor explicitly null and no next link.
			// The API owns this list's cursors (its _parse_cursor treats a bare
			// id as no cursor at all), so none may be derived from the rows.
			name: "discussions: has_more with next_cursor null",
			body: `{"data":[{"id":"d1","type":"discussions","attributes":{}}],
			        "meta":{"next_cursor":null,"has_more":true},
			        "links":{"self":"/api/admin/teams/t/hubs/h/discussions?page[size]=1"}}`,
			want: PageInfo{More: true, Cursor: "", Signalled: true, APICursors: true},
		},
		{
			// activity/router.py list_top_engaged: meta.{has_more,next_cursor}
			// and a links.next that is a JSON:API link OBJECT ({"href": …}).
			name: "activity: links.next as a link object",
			body: `{"data":[{"id":"c1","type":"top_engaged","attributes":{}}],
			        "meta":{"has_more":true,"next_cursor":"cur-activity"},
			        "links":{"self":{"href":"/api/teams/t/hubs/h/activity/top-engaged?page%5Bsize%5D=1"},"next":{"href":"/api/teams/t/hubs/h/activity/top-engaged?page%5Bsize%5D=1&page%5Bafter%5D=cur-activity"}}}`,
			want: PageInfo{More: true, Cursor: "cur-activity", Signalled: true, APICursors: true},
		},
		{
			// The same link-object shape read for its cursor alone, so the
			// href form cannot pass only because meta.next_cursor covered it.
			name: "links.next link object carries the cursor",
			body: `{"data":[{"id":"c1","type":"top_engaged","attributes":{}}],
			        "meta":{"has_more":true},
			        "links":{"next":{"href":"/x?page%5Bafter%5D=cur-href&page%5Bsize%5D=1"}}}`,
			want: PageInfo{More: true, Cursor: "cur-href", Signalled: true, APICursors: true},
		},
		{
			// achievements/admin_router.py list_hub_achievements (offerings):
			// meta.page.has_more with NO cursor and no links, yet the route
			// takes page[after] = the last row's id.
			name: "offerings: meta.page.has_more, no cursor field at all",
			body: `{"data":[{"id":"o1","type":"achievement_hubs","attributes":{}}],
			        "meta":{"page":{"size":1,"has_more":true}},"links":null}`,
			want: PageInfo{More: true, Cursor: "", Signalled: true, APICursors: false},
		},
		{
			// media/router.py list_files (also list_attachments, list_playlists,
			// list_playlist_items and _hub_media_list_response for hub media /
			// hub playlists): NO meta at all — the only pagination signal is
			// links.next, whose page[after] is spelled with raw brackets.
			name: "media files: links.next only, no meta",
			body: `{"data":[{"id":"file1","type":"files","attributes":{}}],
			        "links":{"next":"/api/teams/t/files?page[after]=file1&page[size]=1"}}`,
			want: PageInfo{More: true, Cursor: "file1", Signalled: true, APICursors: true},
		},
		{
			// The same media lists on their last page: links is null.
			name: "media files: last page, links null",
			body: `{"data":[{"id":"file9","type":"files","attributes":{}}],"links":null}`,
			want: PageInfo{},
		},
		{
			// checkout/router.py _build_order_list_response (also
			// _build_subscription_list_response, _build_payment_list_response,
			// _build_webhook_list_response): the admin hub lists page by
			// page[after] = the last row's id, but report only
			// meta.total = the length of THIS page. No signal either way.
			name: "checkout admin hub lists: meta.total only",
			body: `{"data":[{"id":"pay1","type":"payments","attributes":{}}],"meta":{"total":1}}`,
			want: PageInfo{},
		},
		{
			// checkout/router.py _build_member_payment_list_response: the
			// member-facing /me payments list (no CLI command reads it today)
			// nests meta.page beside a total.
			name: "checkout member payments: meta.{total,page.{has_more,next_cursor}}",
			body: `{"data":[{"id":"pay1","type":"payments","attributes":{}}],
			        "meta":{"total":40,"page":{"size":1,"has_more":true,"next_cursor":"cur-payments"}}}`,
			want: PageInfo{More: true, Cursor: "cur-payments", Signalled: true, APICursors: true},
		},
		{
			// media/router.py admin_search_media: top-N search, so has_more with
			// no cursor at all — the only way to more rows is a larger page.
			name: "media search: top_n has_more, no cursor",
			body: `{"data":[{"id":"s1","type":"media_search_results","attributes":{}}],
			        "meta":{"total":57,"has_more":true,"is_capped":false,"cap":100,"pagination":"top_n"}}`,
			want: PageInfo{More: true, Cursor: "", Signalled: true, APICursors: false},
		},
		{
			// build_page_meta on the last page: has_more false, next null.
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
			// An unpaginated list (teams/router.py list_teams: meta.total only).
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
			if got.APICursors != tc.want.APICursors {
				t.Errorf("NextPage().APICursors = %v, want %v — whether this API hands out its own cursors was misread", got.APICursors, tc.want.APICursors)
			}
		})
	}
}

// A collection with no retained body (built in-process) and no meta reports no
// more rows rather than panicking.
func TestCollectionNextPage_NilSafe(t *testing.T) {
	var nilCol *Collection
	if got := nilCol.NextPage(); got != (PageInfo{}) {
		t.Errorf("nil collection NextPage() = %+v, want the zero PageInfo", got)
	}
	if got := (&Collection{}).NextPage(); got != (PageInfo{}) {
		t.Errorf("empty collection NextPage() = %+v, want the zero PageInfo", got)
	}
}
