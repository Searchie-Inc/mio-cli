package cmd

// more_rows_note_test.go — MIO-4174 part 2: when the API says a list has more
// rows than it returned, the CLI says so on STDERR and names the flag that
// fetches them. STDOUT is untouched: the flattened bare array is a documented
// contract (`--jq '.[0].id'`, `--jq '.[].id'`), so the signal cannot go there.
//
// The oracles are all observables the implementation cannot fake:
//   - stdout is compared byte for byte with the same command against the same
//     rows served as a last page;
//   - the cursor the note prints is fed back to the CLI and must reach the
//     wire as page[after] (or the body's page.after for segments search), and
//     the stub answers that request with the real next page;
//   - every command that registers --after is run against a has_more stub, so
//     a list command that stops going through the shared render path loses its
//     note by name;
//   - every such command is also run against its OWN route's envelope, as read
//     from the mio-backend handler (realAfterShapes), so a real shape the
//     reader cannot see loses its note by name, and an unpaged route that
//     gains one is caught too.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// pagedStub serves one list route in two pages. The first page's envelope is
// built by envelope (the backend shape under test) with more=true; the
// request that carries the right cursor gets the second, last page. It records
// every page[after] it was sent so a test can prove the note's cursor reached
// the wire.
type pagedStub struct {
	mu        sync.Mutex
	afters    []string
	bodyAfter []string // page.after from a POST body (segments search)
	srv       *httptest.Server
}

// envelope builds a list response in a named backend shape. Each shape is
// transcribed from the mio-backend origin/main (2dc04aab) handlers named
// beside it; realAfterShapes maps every list command to the one it gets.
func envelope(shape string, ids []string, more bool, cursor string) string {
	data := make([]string, len(ids))
	for i, id := range ids {
		data[i] = fmt.Sprintf(`{"id":%q,"type":"things","attributes":{"name":"n-%s"}}`, id, id)
	}
	d := "[" + strings.Join(data, ",") + "]"
	next := "null"
	if more {
		next = fmt.Sprintf(`"/api/x?page%%5Bafter%%5D=%s&page%%5Bsize%%5D=2"`, cursor)
	}
	nextCursor := "null"
	if more {
		nextCursor = fmt.Sprintf("%q", cursor)
	}
	switch shape {
	case "products": // meta.page.has_more; cursor only in links.next (products/router.py admin_list_products, coupons, contacts, content, pages, hubs, achievements)
		return fmt.Sprintf(`{"data":%s,"meta":{"page":{"size":2,"has_more":%v}},"links":{"self":"/api/x","next":%s}}`, d, more, next)
	case "tags": // top-level meta.has_more; cursor only in links.next (tags/router.py list_tags and the other has_more-only lists)
		return fmt.Sprintf(`{"data":%s,"meta":{"has_more":%v},"links":{"self":"/api/x","next":%s}}`, d, more, next)
	case "build_page_meta": // meta.page.{has_more,next_cursor} + links.next (infrastructure/pagination.py build_page_meta)
		return fmt.Sprintf(`{"data":%s,"meta":{"page":{"size":2,"has_more":%v,"next_cursor":%s}},"links":{"self":"/api/x","next":%s}}`, d, more, nextCursor, next)
	case "discussions": // top-level meta.{has_more,next_cursor} (discussions_admin._list_response, moderation, hub members, segment members)
		return fmt.Sprintf(`{"data":%s,"meta":{"has_more":%v,"next_cursor":%s}}`, d, more, nextCursor)
	case "discussions_null_cursor": // the same, when _build_cursor returns None (last row's last_activity_at is NULL): has_more with next_cursor null
		return fmt.Sprintf(`{"data":%s,"meta":{"next_cursor":null,"has_more":%v},"links":{"self":"/api/x"}}`, d, more)
	case "activity": // meta.{has_more,next_cursor} + links.next as a link OBJECT (activity/router.py list_top_engaged)
		nextObj := "null"
		if more {
			nextObj = fmt.Sprintf(`{"href":"/api/x?page%%5Bsize%%5D=2&page%%5Bafter%%5D=%s"}`, cursor)
		}
		return fmt.Sprintf(`{"data":%s,"meta":{"has_more":%v,"next_cursor":%s},"links":{"self":{"href":"/api/x"},"next":%s}}`, d, more, nextCursor, nextObj)
	case "page_only": // meta.page.has_more, no cursor, no links (achievements/admin_router.py list_hub_achievements)
		return fmt.Sprintf(`{"data":%s,"meta":{"page":{"size":2,"has_more":%v}}}`, d, more)
	case "media_links": // NO meta; links.next (raw brackets) is the only signal: list_files / list_attachments send it on ANY full page (len == page[size], no over-fetch), list_playlists / list_playlist_items / _hub_media_list_response after a size+1 probe (media/router.py)
		if !more {
			return fmt.Sprintf(`{"data":%s,"links":null}`, d)
		}
		return fmt.Sprintf(`{"data":%s,"links":{"next":"/api/teams/t/files?page[after]=%s&page[size]=2"}}`, d, cursor)
	case "admin_total": // pages by page[after] = last id, reports only meta.total = rows on THIS page (checkout/router.py _build_order/_subscription/_payment/_webhook_list_response)
		return fmt.Sprintf(`{"data":%s,"meta":{"total":%d}}`, d, len(ids))
	case "top_n": // media search: has_more, no cursor anywhere (media/router.py admin_search_media)
		return fmt.Sprintf(`{"data":%s,"meta":{"total":9,"has_more":%v,"is_capped":false,"cap":100,"pagination":"top_n"}}`, d, more)
	// Lists that take no page[after] or page[size] at all and return every
	// row, so they can never have more.
	case "unpaged_total": // teams/router.py list_teams, list_members; checkout list_payment_accounts
		return fmt.Sprintf(`{"data":%s,"meta":{"total":%d}}`, d, len(ids))
	case "unpaged_count": // products/router.py admin_list_prices, admin_list_deliverables, hub products/prices, coupon products; pages sections
		return fmt.Sprintf(`{"data":%s,"meta":{"count":%d}}`, d, len(ids))
	case "unpaged_empty_meta": // contact_attributes/router.py list_options, list_hub_configs
		return fmt.Sprintf(`{"data":%s,"meta":{}}`, d)
	case "unpaged_bare": // roles/router.py list_permissions; external_login list_providers, list_domains
		return fmt.Sprintf(`{"data":%s}`, d)
	}
	panic("unknown shape " + shape)
}

// lastPageShape is the envelope a control run serves: the same shape on its
// last page, except where a shape has no way to say "last page" (admin_total),
// which gets a has_more:false envelope instead. Flattened stdout depends only
// on the rows, so the control's stdout is the baseline either way.
func lastPageShape(shape string) string {
	if shape == "admin_total" {
		return "tags"
	}
	return shape
}

// newPagedStub answers every request with page 1 (more=true, cursor) unless
// the request carries that cursor, in which case it answers the last page.
// forceMore=false serves page 1's rows as a last page instead — the control
// run whose stdout the has_more run must match byte for byte.
func newPagedStub(t *testing.T, shape, cursor string, forceMore bool) *pagedStub {
	t.Helper()
	ps := &pagedStub{}
	ps.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("page[after]")
		if r.Method == http.MethodPost {
			var body struct {
				Data struct {
					Attributes struct {
						Page struct {
							After string `json:"after"`
						} `json:"page"`
					} `json:"attributes"`
				} `json:"data"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			after = body.Data.Attributes.Page.After
			ps.mu.Lock()
			ps.bodyAfter = append(ps.bodyAfter, after)
			ps.mu.Unlock()
		}
		ps.mu.Lock()
		ps.afters = append(ps.afters, r.URL.Query().Get("page[after]"))
		ps.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if after == cursor {
			_, _ = w.Write([]byte(envelope(shape, []string{"r3"}, false, "")))
			return
		}
		if !forceMore {
			_, _ = w.Write([]byte(envelope(lastPageShape(shape), []string{"r1", "r2"}, false, "")))
			return
		}
		_, _ = w.Write([]byte(envelope(shape, []string{"r1", "r2"}, true, cursor)))
	}))
	t.Cleanup(ps.srv.Close)
	return ps
}

// moreRowsCase is one list command in one backend shape.
type moreRowsCase struct {
	name    string
	shape   string
	args    []string // command args, without --team / -o
	flag    string   // the flag the note must name: "after", "page-after" or "limit"
	usesCur bool     // whether the note must carry a cursor
	cursor  string   // the cursor the stub hands out; "" = a plain token
	forbid  string   // text the note must NOT contain; "" = no check
	must    string   // text the note must contain; "" = no check
}

// discussionsCursor is the shape community/routers/discussions_admin.py
// _build_cursor emits (and comments, messages): "<isoformat>|<id>". The `|`
// makes an unquoted copy of it a shell pipe.
const discussionsCursor = "2026-09-24T11:00:00.123456+00:00|019f0000-0000-7000-8000-0000000000d9"

const testHub = "019f0000-0000-7000-8000-0000000000a1"

func moreRowsCases() []moreRowsCase {
	return []moreRowsCase{
		// The commands QA hit, each in its backend's real shape (mio-backend
		// origin/main 2dc04aab; see envelope for the handler behind each).
		{"products list (meta.page.has_more + links.next)", "products", []string{"products", "list"}, "after", true, "", "", ""},
		{"coupons list (meta.page.has_more + links.next)", "products", []string{"coupons", "list"}, "after", true, "", "", ""},
		{"tags list (top-level has_more + links.next)", "tags", []string{"tags", "list"}, "after", true, "", "", ""},
		// media/router.py list_files sends NO meta: links.next is its only
		// signal, so no has_more reader alone can see it.
		// Files and attachments send that link on ANY full page, so the
		// note must not claim more rows exist, only that the link came.
		{"media files list (links.next only, no meta)", "media_links", []string{"media", "files", "list"}, "after", true, "", "has more", "next-page link"},
		{"media folders list (build_page_meta)", "build_page_meta", []string{"media", "folders", "list"}, "after", true, "", "", ""},
		{"community discussions list (meta.next_cursor)", "discussions", []string{"community", "discussions", "list", "--hub", testHub}, "after", true, "", "", ""},
		// The backend's real discussions cursor: the note must survive a shell.
		{"community discussions list (<iso>|<id> cursor)", "discussions", []string{"community", "discussions", "list", "--hub", testHub}, "after", true, discussionsCursor, "", ""},
		// When the last row's last_activity_at is NULL the API says has_more
		// with next_cursor null. It owns this list's cursors (a bare id reads
		// as none, and serves page 1 again), so the note offers no row id.
		{"community discussions list (has_more, next_cursor null)", "discussions_null_cursor", []string{"community", "discussions", "list", "--hub", testHub}, "limit", false, "", "", ""},
		// checkout's admin hub lists report only meta.total (this page's row
		// count) and page by `id > page[after]`: a FULL page (--limit 2, two
		// rows) is the only hint, and the last row's id is the cursor.
		{"checkout payments list (meta.total only: a full page)", "admin_total", []string{"checkout", "payments", "list", "--hub", testHub, "--limit", "2"}, "after", true, "r2", "", ""},
		// achievements offerings (admin_router.list_hub_achievements) reports
		// has_more with NO cursor and no links, yet takes page[after] = the
		// last row's id (repository: id DESC, `AchievementHub.id < after`).
		// The stub's second page answers only page[after]=r2, the last row.
		{"achievements offerings list (has_more, no cursor: last row id)", "page_only", []string{"achievements", "offerings", "list", "--hub", testHub}, "after", true, "r2", "", ""},
		// activity top-engaged: links.next is a link object.
		{"activity top-engaged (links.next {href})", "activity", []string{"activity", "top-engaged", "--hub", testHub}, "after", true, "", "", ""},
		// segments search pages with --page-after, carried in the POST body.
		{"segments search (--page-after)", "discussions", []string{"segments", "search", "--conditions", `{"version":1,"groups":[]}`}, "page-after", true, "", "", ""},
		// segments search takes pages of up to 200 (schemas.py page.size
		// le=200), not the usual 100: 150 can still be raised, 200 cannot.
		{"segments search at --page-size 150 (cap 200)", "discussions", []string{"segments", "search", "--conditions", `{"version":1,"groups":[]}`, "--page-size", "150"}, "page-after", true, "", "", "(or raise --page-size)"},
		{"segments search at --page-size 200 (its cap)", "discussions", []string{"segments", "search", "--conditions", `{"version":1,"groups":[]}`, "--page-size", "200"}, "page-after", true, "", "raise --page-size", ""},
		// media search is top-N: has_more with no cursor, and no --after flag.
		{"media search (top_n, --limit only)", "top_n", []string{"media", "search", "--query", "x"}, "limit", false, "", "", ""},
		// At --limit 100 the API's page-size cap (le=100) is reached: a larger
		// --limit is a 422, not more rows, so the note must not offer one.
		{"media search at the page-size cap (--limit 100)", "top_n", []string{"media", "search", "--query", "x", "--limit", "100"}, "limit", false, "", "raise --limit", ""},
		{"tags list at the page-size cap (--limit 100)", "tags", []string{"tags", "list", "--limit", "100"}, "after", true, "", "raise --limit", ""},
	}
}

// followNote returns the argv a POSIX shell produces from the note's
// "fetch the next page with …" suggestion — the oracle for "an agent can paste
// it". printf prints one word per line, so an unquoted `|` in a cursor becomes
// a pipe and the words come back wrong (or not at all).
func followNote(t *testing.T, note string) []string {
	t.Helper()
	const lead = "fetch the next page with "
	i := strings.Index(note, lead)
	if i < 0 {
		t.Fatalf("the note offers no next-page command; got %q", note)
	}
	suggestion := note[i+len(lead):]
	if j := strings.Index(suggestion, " (or raise --"); j >= 0 {
		suggestion = suggestion[:j]
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX sh on this platform; CI (ubuntu) runs this")
	}
	out, err := exec.Command(sh, "-c", "printf '%s\\n' "+suggestion).CombinedOutput()
	if err != nil {
		t.Fatalf("the note's suggestion %q is not valid shell: %v; output=%q", suggestion, err, out)
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

func TestMoreRowsNote_StderrNamesTheFlag_StdoutUnchanged(t *testing.T) {
	for _, tc := range moreRowsCases() {
		cursor := tc.cursor
		if cursor == "" {
			cursor = "cur-page-2"
		}
		for _, format := range []string{"json", "table", "plain"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				more := newPagedStub(t, tc.shape, cursor, true)
				last := newPagedStub(t, tc.shape, cursor, false)
				args := withTeam("019f0000-0000-7000-8000-0000000000f1", append(append([]string{}, tc.args...), "-o", format)...)

				res := runContract(t, baseEnv(more.srv.URL), args...)
				ctl := runContract(t, baseEnv(last.srv.URL), args...)
				if res.Code != errs.ExitOK || ctl.Code != errs.ExitOK {
					t.Fatalf("exit = %d (control %d), want 0; stderr=%q", res.Code, ctl.Code, res.Stderr)
				}
				// STDOUT: byte-identical to the same rows served as the last page.
				if res.Stdout != ctl.Stdout {
					t.Errorf("stdout changed when the API reported more rows — the note must go to stderr only\n has_more: %q\n    last: %q", res.Stdout, ctl.Stdout)
				}
				if ctl.Stderr != "" {
					t.Errorf("no note is due on the last page; stderr=%q", ctl.Stderr)
				}
				// STDERR: exactly one line, naming the row count and the flag.
				lines := strings.Split(strings.TrimRight(res.Stderr, "\n"), "\n")
				if len(lines) != 1 || !strings.HasPrefix(lines[0], "note: ") {
					t.Fatalf("stderr must be ONE 'note: ' line; got %q", res.Stderr)
				}
				if !strings.Contains(lines[0], "2 rows") {
					t.Errorf("the note must say how many rows this page returned (2); got %q", lines[0])
				}
				if !strings.Contains(lines[0], "--"+tc.flag) {
					t.Errorf("the note must name --%s; got %q", tc.flag, lines[0])
				}
				if tc.forbid != "" && strings.Contains(lines[0], tc.forbid) {
					t.Errorf("the note must not say %q here; got %q", tc.forbid, lines[0])
				}
				if tc.must != "" && !strings.Contains(lines[0], tc.must) {
					t.Errorf("the note must say %q here; got %q", tc.must, lines[0])
				}
				if !tc.usesCur {
					if strings.Contains(lines[0], cursor) || strings.Contains(lines[0], "fetch the next page") {
						t.Errorf("this API returns no cursor, so the note must not offer one; got %q", lines[0])
					}
					return
				}
				// Follow the note literally, through a shell: it must yield
				// exactly `--<flag> <cursor>`, and that cursor must reach the
				// wire and fetch the next page.
				words := followNote(t, lines[0])
				if len(words) != 2 || words[0] != "--"+tc.flag || words[1] != cursor {
					t.Fatalf("pasted into a shell, the note's suggestion gives argv %q; want [--%s %q]; note=%q", words, tc.flag, cursor, lines[0])
				}
				next := runContract(t, baseEnv(more.srv.URL), append(args, words...)...)
				if next.Code != errs.ExitOK {
					t.Fatalf("following the note exited %d; stderr=%q", next.Code, next.Stderr)
				}
				if !strings.Contains(next.Stdout, "r3") || strings.Contains(next.Stdout, "r1") {
					t.Errorf("following the note did not fetch the next page; stdout=%q", next.Stdout)
				}
				if next.Stderr != "" {
					t.Errorf("the last page must carry no note; stderr=%q", next.Stderr)
				}
				more.mu.Lock()
				defer more.mu.Unlock()
				sent := more.afters
				if tc.flag == "page-after" {
					sent = more.bodyAfter
				}
				if len(sent) == 0 || sent[len(sent)-1] != cursor {
					t.Errorf("the note's cursor %q did not reach the wire as page[after]; sent %q", words[1], sent)
				}
			})
		}
	}
}

// --raw hands the caller the envelope itself, meta and links included, so the
// note would only repeat it.
func TestMoreRowsNote_NotUnderRaw(t *testing.T) {
	ps := newPagedStub(t, "products", "cur-raw", true)
	res := runContract(t, baseEnv(ps.srv.URL), withTeam("019f0000-0000-7000-8000-0000000000f1", "products", "list", "--raw")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if res.Stderr != "" {
		t.Errorf("--raw already shows meta.page.has_more and links.next; stderr must stay empty, got %q", res.Stderr)
	}
	if !strings.Contains(res.Stdout, `"has_more": true`) {
		t.Errorf("--raw must still carry has_more on stdout; got %q", res.Stdout)
	}
}

// afterCommands walks the command tree for every runnable command that
// registers --after (addPaginationFlags), sorted by path.
func afterCommands(t *testing.T) []*cobra.Command {
	t.Helper()
	var afterCmds []*cobra.Command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("after") != nil && c.Runnable() {
			afterCmds = append(afterCmds, c)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(RootCmd())
	sort.Slice(afterCmds, func(i, j int) bool { return afterCmds[i].CommandPath() < afterCmds[j].CommandPath() })
	// 65 list commands register --after today. A walk that found far fewer
	// would be a broken walk, not a passing test.
	if len(afterCmds) < 60 {
		t.Fatalf("found %d commands with --after; the walk is broken", len(afterCmds))
	}
	return afterCmds
}

// walkArgs is the argv that gets command c to its request: scope flags,
// placeholder positionals and required flags, then extra.
func walkArgs(c *cobra.Command, extra ...string) []string {
	path := strings.Fields(c.CommandPath())[1:] // drop "mio"
	args := append([]string{"--team", "019f0000-0000-7000-8000-0000000000f1", "--hub", testHub}, path...)
	args = append(args, positionalPlaceholders(c)...)
	args = append(args, requiredFlagPlaceholders(c)...)
	return append(args, extra...)
}

// walkEnv points at a stub; `mio events` pages only with a member token
// (MIO_CONTACT_TOKEN), which every other command ignores.
func walkEnv(url string) []string {
	return append(baseEnv(url), eventsContactTokenEnv+"=contact-token-walk")
}

// Every command that registers --after renders through the shared render path,
// and each must print the note when the API reports more rows. The hook lives
// in that path, so this is what catches a list command that renders some other
// way. The stub answers in the tags shape (top-level has_more, cursor only in
// links.next), which needs the links parse to find the cursor at all. Whether
// each command's REAL route sends that signal is TestMoreRowsNote_RealShapes'
// job.
func TestMoreRowsNote_EveryAfterCommand(t *testing.T) {
	const cursor = "cur-walk"
	for _, c := range afterCommands(t) {
		path := strings.Fields(c.CommandPath())[1:] // drop "mio"
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			ps := newPagedStub(t, "tags", cursor, true)
			args := walkArgs(c, "-o", "json")
			res := runContract(t, walkEnv(ps.srv.URL), args...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d; args=%q stderr=%q", res.Code, args, res.Stderr)
			}
			want := "--after " + cursor
			if !strings.Contains(res.Stderr, want) {
				t.Errorf("%s: the API reported more rows, but stderr does not name %q; stderr=%q", c.CommandPath(), want, res.Stderr)
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
				t.Errorf("%s: stdout must stay the bare flattened array; err=%v stdout=%q", c.CommandPath(), err, res.Stdout)
			}
		})
	}
}

// realAfterShapes maps every command that registers --after to the envelope
// its route answers with on mio-backend origin/main 2dc04aab, as read from the
// handler (routes found by running each command against a recording stub).
// The unpaged_* routes take neither page[after] nor page[size]: they return
// every row, so no note is ever due for them, and one that printed a cursor
// would send an agent round the same page for ever.
var realAfterShapes = map[string]string{
	"access-rules overrides list":        "tags",               // access_rules/router.py list_overrides
	"access-rules rules list":            "tags",               // access_rules/router.py list_rules
	"achievements list":                  "products",           // achievements/admin_router.py list_achievements
	"achievements offerings list":        "page_only",          // achievements/admin_router.py list_hub_achievements
	"activity top-engaged":               "activity",           // activity/router.py list_top_engaged
	"api-keys list":                      "tags",               // api_keys/router.py list_api_keys (build_cursor_links)
	"automations enrollments":            "tags",               // automations/router.py list_enrollments
	"automations list":                   "tags",               // automations/router.py list_automations
	"automations versions":               "tags",               // automations/router.py list_automation_versions
	"checkout accounts list":             "unpaged_total",      // checkout/router.py list_payment_accounts
	"checkout hub-prices list":           "unpaged_count",      // products/router.py admin_list_hub_price_displays
	"checkout hub-products list":         "unpaged_count",      // products/router.py admin_list_hub_products
	"checkout orders list":               "admin_total",        // checkout/router.py list_orders
	"checkout payments list":             "admin_total",        // checkout/router.py list_payments
	"checkout subscriptions list":        "admin_total",        // checkout/router.py list_subscriptions
	"checkout webhooks list":             "admin_total",        // checkout/router.py list_webhook_events
	"community comments list":            "discussions",        // comments/router.py admin_list_comments
	"community discussions list":         "discussions",        // community/routers/discussions_admin.py admin_list_discussions
	"community moderation audit-log":     "discussions",        // community/routers/moderation_admin.py list_audit_log
	"community moderation banned":        "discussions",        // moderation_admin.py list_banned_members
	"community moderation queue":         "discussions",        // moderation_admin.py list_moderation_queue
	"community moderation removed":       "discussions",        // moderation_admin.py list_removed_content
	"community spaces list":              "build_page_meta",    // community/routers/spaces_admin.py admin_list_spaces
	"contact-attributes hub-config list": "unpaged_empty_meta", // contact_attributes/router.py list_hub_configs
	"contact-attributes list":            "tags",               // contact_attributes/router.py list_definitions
	"contact-attributes options list":    "unpaged_empty_meta", // contact_attributes/router.py list_options
	"contacts list":                      "products",           // contacts_admin/router.py list_contacts
	"content children":                   "products",           // content/router.py admin_list_children
	"content list":                       "products",           // content/router.py admin_list_root_nodes
	"coupons list":                       "products",           // products/router.py admin_list_coupons
	"coupons products list":              "unpaged_count",      // products/router.py admin_list_coupon_products
	"email drip-campaigns list":          "build_page_meta",    // email/router.py list_drip_campaigns
	"email enrollments list":             "build_page_meta",    // email/router.py list_campaign_enrollments
	"email enrollments list-by-contact":  "build_page_meta",    // email/router.py list_contact_enrollments
	"email steps list":                   "build_page_meta",    // email/router.py list_drip_steps
	"email suppressions list":            "tags",               // email/suppression_router.py hub_list_suppressions
	"email templates list":               "build_page_meta",    // email/router.py list_email_templates
	"events list":                        "tags",               // hub_events/router.py list_events
	"events rsvps list":                  "tags",               // hub_events/router.py list_event_rsvps
	"external-login-providers list":      "unpaged_bare",       // external_login/router_admin.py list_providers
	"hub-memberships list":               "discussions",        // hub_memberships/admin_router.py list_hub_members
	"hubs list":                          "products",           // hubs/router.py admin_list_hubs
	"media attachments list":             "media_links",        // media/router.py list_attachments
	"media files list":                   "media_links",        // media/router.py list_files
	"media folders list":                 "build_page_meta",    // media/router.py list_folders
	"media hub-media list":               "media_links",        // media/router.py list_hub_media_files
	"media hub-playlists list":           "media_links",        // media/router.py list_hub_media_playlists
	"media playlists items list":         "media_links",        // media/router.py list_playlist_items
	"media playlists list":               "media_links",        // media/router.py list_playlists
	"oauth-clients list":                 "tags",               // oauth/router.py list_oauth_clients
	"pages list":                         "products",           // pages/router.py admin_list_pages
	"pages sections list":                "unpaged_count",      // pages/router.py admin_list_sections
	"products deliverables list":         "unpaged_count",      // products/router.py admin_list_deliverables
	"products list":                      "products",           // products/router.py admin_list_products
	"products prices list":               "unpaged_count",      // products/router.py admin_list_prices
	"roles list":                         "tags",               // roles/router.py list_roles
	"roles permissions list":             "unpaged_bare",       // roles/router.py list_permissions
	"segments list":                      "tags",               // segments/router.py list_segments
	"segments members":                   "discussions",        // segments/router.py list_members
	"tags list":                          "tags",               // tags/router.py list_tags
	"teams list":                         "unpaged_total",      // teams/router.py list_teams
	"teams members list":                 "unpaged_total",      // teams/router.py list_members
	"users list":                         "tags",               // users/router.py list_users
	"verified-domains list":              "unpaged_bare",       // external_login/router_domains.py list_domains
	"webhook-endpoints list":             "tags",               // automations/router.py list_webhook_endpoints
}

// Each list command against its OWN route's envelope. A paged route that has
// more rows must print a note whose cursor, pasted through a shell, reaches
// the wire as page[after] and fetches the next page, which carries no note.
// An unpaged route must print no note, however many rows it returns. So a
// shape the reader cannot see (the media lists' links-only signal, checkout's
// signal-less full page) loses its note by command name, and a command marked
// as paging without a signal when its route does not page gains one.
func TestMoreRowsNote_RealShapes(t *testing.T) {
	cmds := afterCommands(t)
	seen := map[string]bool{}
	for _, c := range cmds {
		seen[strings.Join(strings.Fields(c.CommandPath())[1:], " ")] = true
	}
	for name := range realAfterShapes {
		if !seen[name] {
			t.Errorf("realAfterShapes names %q, which is not a command that registers --after", name)
		}
	}
	for _, c := range cmds {
		name := strings.Join(strings.Fields(c.CommandPath())[1:], " ")
		shape, ok := realAfterShapes[name]
		if !ok {
			t.Errorf("%s registers --after but realAfterShapes has no entry for it: read its mio-backend handler and add the envelope it answers with", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			if strings.HasPrefix(shape, "unpaged_") {
				ids := make([]string, usualPageSizeCap)
				for i := range ids {
					ids[i] = fmt.Sprintf("u%03d", i)
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/vnd.api+json")
					_, _ = w.Write([]byte(envelope(shape, ids, false, "")))
				}))
				t.Cleanup(srv.Close)
				res := runContract(t, walkEnv(srv.URL), walkArgs(c, "-o", "json")...)
				if res.Code != errs.ExitOK {
					t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
				}
				if notes := noteLines(res.Stderr); len(notes) != 0 {
					t.Errorf("%s: this route returns every row and takes no page[after], so no note is due even for %d rows; got %q", name, len(ids), notes)
				}
				return
			}
			// A cursorless route pages by its last row's id; every other
			// route hands out its own cursor, which differs from any row id.
			cursor := "cur-real"
			if shape == "page_only" || shape == "admin_total" {
				cursor = "r2"
			}
			extra := []string{"-o", "json"}
			if shape == "admin_total" {
				extra = append(extra, "--limit", "2") // two rows is then a full page
			}
			ps := newPagedStub(t, shape, cursor, true)
			args := walkArgs(c, extra...)
			res := runContract(t, walkEnv(ps.srv.URL), args...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d; args=%q stderr=%q", res.Code, args, res.Stderr)
			}
			// Some commands also warn on stderr (media playlists list: --hub
			// does not scope it); the note is its own line.
			notes := noteLines(res.Stderr)
			if len(notes) != 1 {
				t.Fatalf("%s: its route (%s shape) reported more rows, but stderr does not carry ONE 'note: ' line; got %q", name, shape, res.Stderr)
			}
			words := followNote(t, notes[0])
			if len(words) != 2 || words[0] != "--after" || words[1] != cursor {
				t.Fatalf("%s: pasted into a shell, the note gives argv %q; want [--after %q]; note=%q", name, words, cursor, notes[0])
			}
			next := runContract(t, walkEnv(ps.srv.URL), append(args, words...)...)
			if next.Code != errs.ExitOK {
				t.Fatalf("following the note exited %d; stderr=%q", next.Code, next.Stderr)
			}
			if !strings.Contains(next.Stdout, "r3") || strings.Contains(next.Stdout, "r1") {
				t.Errorf("%s: following the note did not fetch the next page; stdout=%q", name, next.Stdout)
			}
			if notes := noteLines(next.Stderr); len(notes) != 0 {
				t.Errorf("%s: the last page must carry no note; got %q", name, notes)
			}
			ps.mu.Lock()
			defer ps.mu.Unlock()
			if len(ps.afters) == 0 || ps.afters[len(ps.afters)-1] != cursor {
				t.Errorf("%s: the note's cursor %q did not reach the wire as page[after]; sent %q", name, cursor, ps.afters)
			}
		})
	}
}

// noteLines returns the stderr lines that are more-rows notes.
func noteLines(stderr string) []string {
	var out []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "note: ") {
			out = append(out, l)
		}
	}
	return out
}

// The note's exact wording for each combination of what the API returned and
// which paging flags the command registers. The end-to-end tests above cover
// the combinations real commands produce; this pins the rest of the helper,
// including the flagless branch no list command reaches today (every command
// that renders a paginated route registers --after or --limit).
func TestMoreRowsNote_Wording(t *testing.T) {
	col := func(n int, body string) *client.Collection {
		c, err := client.DecodeCollection([]byte(body))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		c.Data = make([]client.Resource, n)
		return c
	}
	withIDs := func(c *client.Collection, ids ...string) *client.Collection {
		for i, id := range ids {
			c.Data[i].ID = id
		}
		return c
	}
	withLastID := func(c *client.Collection, id string) *client.Collection {
		c.Data[len(c.Data)-1].ID = id
		return c
	}
	withCursor := `{"data":[],"meta":{"has_more":true,"next_cursor":"CUR"}}`
	noCursor := `{"data":[],"meta":{"has_more":true}}`
	nullCursor := `{"data":[],"meta":{"next_cursor":null,"has_more":true},"links":{"self":"/x"}}`
	linkOnly := `{"data":[],"links":{"next":"/x?page[after]=LINKCUR&page[size]=3"}}`
	totalOnly := `{"data":[],"meta":{"total":20}}`
	last := `{"data":[],"meta":{"has_more":false,"next_cursor":null}}`
	// The page-size flags are ints, as on every real command.
	cmdWith := func(flags ...string) *cobra.Command {
		c := &cobra.Command{Use: "x"}
		for _, f := range flags {
			if f == "limit" || f == "page-size" {
				c.Flags().Int(f, 0, "")
				continue
			}
			c.Flags().String(f, "", "")
		}
		return c
	}
	set := func(c *cobra.Command, flag, v string) *cobra.Command {
		if err := c.Flags().Set(flag, v); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
		return c
	}
	unsignalled := func(c *cobra.Command) *cobra.Command {
		markUnsignalledPaging(c, 20)
		return c
	}
	capped := func(c *cobra.Command, rows int) *cobra.Command {
		markPageSizeCap(c, rows)
		return c
	}
	cases := []struct {
		name string
		cmd  *cobra.Command
		col  *client.Collection
		want string
	}{
		{"cursor, --after and --limit", cmdWith("limit", "after"), col(20, withCursor),
			"note: the API returned 20 rows and has more; fetch the next page with --after CUR (or raise --limit)"},
		{"cursor, --after only", cmdWith("after"), col(20, withCursor),
			"note: the API returned 20 rows and has more; fetch the next page with --after CUR"},
		{"cursor, segments-search flags", cmdWith("page-size", "page-after"), col(50, withCursor),
			"note: the API returned 50 rows and has more; fetch the next page with --page-after CUR (or raise --page-size)"},
		{"one row is singular", cmdWith("after"), col(1, withCursor),
			"note: the API returned 1 row and has more; fetch the next page with --after CUR"},
		{"no cursor, --limit only (media search)", cmdWith("limit"), col(20, noCursor),
			"note: the API returned 20 rows and has more; raise --limit to get more in one page"},
		// No cursor from the API but the command takes --after: the backend's
		// page[after] is the last row's id (infrastructure/pagination.py
		// get_pagination), so that is the cursor offered.
		{"no cursor, --after: last row id", cmdWith("limit", "after"), withIDs(col(3, noCursor), "a1", "a2", "a3"),
			"note: the API returned 3 rows and has more; fetch the next page with --after a3 (or raise --limit)"},
		{"no cursor, --after, no rows", cmdWith("limit", "after"), col(0, noCursor),
			"note: the API returned 0 rows and has more; raise --limit to get more in one page"},
		{"cursor but no after flag", cmdWith("limit"), col(20, withCursor),
			"note: the API returned 20 rows and has more; raise --limit to get more in one page"},
		{"no paging flag at all", cmdWith(), col(20, withCursor),
			"note: the API returned 20 rows and has more; this command has no paging flag, and --raw shows the API's meta and links"},
		{"last page", cmdWith("limit", "after"), col(20, last), ""},
		// A cursor a shell would split or reinterpret is single-quoted; a
		// base64url / UUID cursor is printed bare, as above.
		{"shell-unsafe cursor is quoted", cmdWith("after"), col(20, `{"data":[],"meta":{"has_more":true,"next_cursor":"2026-09-24T11:00:00+00:00|abc"}}`),
			"note: the API returned 20 rows and has more; fetch the next page with --after '2026-09-24T11:00:00+00:00|abc'"},
		{"single quote inside a cursor", cmdWith("after"), col(20, `{"data":[],"meta":{"has_more":true,"next_cursor":"a'b c"}}`),
			`note: the API returned 20 rows and has more; fetch the next page with --after 'a'\''b c'`},
		// links.next is the only signal the media lists send.
		{"links.next only (media lists)", cmdWith("limit", "after"), col(3, linkOnly),
			"note: the API returned 3 rows and a next-page link (the next page may be empty); fetch the next page with --after LINKCUR (or raise --limit)"},
		// A null next_cursor beside has_more (discussions, when the last row's
		// last_activity_at is NULL): the API owns this list's cursors and gave
		// none, so no row id is offered in its place.
		{"next_cursor null: no row id offered", cmdWith("limit", "after"), withIDs(col(3, nullCursor), "a1", "a2", "a3"),
			"note: the API returned 3 rows and has more; raise --limit to get more in one page"},
		// The page-size cap (MAX_PAGE_SIZE = 100): a larger --limit is a 422.
		{"--limit 99 can still be raised", set(cmdWith("limit", "after"), "limit", "99"), col(99, withCursor),
			"note: the API returned 99 rows and has more; fetch the next page with --after CUR (or raise --limit)"},
		{"--limit 100 is the cap", set(cmdWith("limit", "after"), "limit", "100"), col(100, withCursor),
			"note: the API returned 100 rows and has more; fetch the next page with --after CUR"},
		{"--limit 100 is the cap even for a short page", set(cmdWith("limit", "after"), "limit", "100"), col(40, withCursor),
			"note: the API returned 40 rows and has more; fetch the next page with --after CUR"},
		{"--page-size 150 is past the usual cap", set(cmdWith("page-size", "page-after"), "page-size", "150"), col(150, withCursor),
			"note: the API returned 150 rows and has more; fetch the next page with --page-after CUR"},
		// A route with its own cap (the segments routes: 200).
		{"--page-size 150 below a 200 cap", capped(set(cmdWith("page-size", "page-after"), "page-size", "150"), 200), col(150, withCursor),
			"note: the API returned 150 rows and has more; fetch the next page with --page-after CUR (or raise --page-size)"},
		{"--limit 200 at a 200 cap", capped(set(cmdWith("limit", "after"), "limit", "200"), 200), col(200, withCursor),
			"note: the API returned 200 rows and has more; fetch the next page with --after CUR"},
		{"no cursor at a 200 cap", capped(set(cmdWith("limit"), "limit", "200"), 200), col(200, noCursor),
			"note: the API returned 200 rows and has more; the API sent no cursor for the next page and --limit is already at its cap (200), so this command cannot fetch the rest"},
		{"no cursor at the cap (media search --limit 100)", set(cmdWith("limit"), "limit", "100"), col(100, noCursor),
			"note: the API returned 100 rows and has more; the API sent no cursor for the next page and --limit is already at its cap (100), so this command cannot fetch the rest"},
		{"next_cursor null at the cap", set(cmdWith("limit", "after"), "limit", "100"), withIDs(col(3, nullCursor), "a1", "a2", "a3"),
			"note: the API returned 3 rows and has more; the API sent no cursor for the next page and --limit is already at its cap (100), so this command cannot fetch the rest"},
		// A route marked as paging without any signal (checkout's admin hub
		// lists): only a full page earns a note, and it says what it knows.
		{"unsignalled: full default page", unsignalled(cmdWith("limit", "after")), withLastID(col(20, totalOnly), "z20"),
			"note: the API returned 20 rows, a full page, and this list does not report whether more exist; fetch the next page with --after z20 (or raise --limit)"},
		{"unsignalled: short default page", unsignalled(cmdWith("limit", "after")), withLastID(col(19, totalOnly), "z19"), ""},
		{"unsignalled: full --limit page", set(unsignalled(cmdWith("limit", "after")), "limit", "5"), withLastID(col(5, totalOnly), "z5"),
			"note: the API returned 5 rows, a full page, and this list does not report whether more exist; fetch the next page with --after z5 (or raise --limit)"},
		{"unsignalled: short --limit page", set(unsignalled(cmdWith("limit", "after")), "limit", "5"), withLastID(col(4, totalOnly), "z4"), ""},
		{"unsignalled: empty page", unsignalled(cmdWith("limit", "after")), col(0, totalOnly), ""},
		// The same envelope on an UNMARKED command is an unpaged list that
		// returned every row (teams list): no note however many.
		{"unmarked meta.total list: no note", cmdWith("limit", "after"), withLastID(col(20, totalOnly), "z20"), ""},
		// A real signal overrides the mark.
		{"unsignalled mark, but the API says last page", unsignalled(cmdWith("limit", "after")), col(20, last), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := moreRowsNote(tc.cmd, tc.col)
			if got != tc.want {
				t.Errorf("moreRowsNote\n got: %q\nwant: %q", got, tc.want)
			}
			// Whatever the spelling, a suggested cursor must come back out of a
			// real shell as the API's cursor, byte for byte.
			if p := tc.col.NextPage(); p.More && p.Cursor != "" && strings.Contains(got, "fetch the next page") {
				words := followNote(t, got)
				if len(words) != 2 || words[1] != p.Cursor {
					t.Errorf("through sh the suggestion gives argv %q; want the cursor %q intact", words, p.Cursor)
				}
			}
		})
	}
}

// positionalPlaceholders supplies one UUID-shaped value per <arg> in Use.
func positionalPlaceholders(c *cobra.Command) []string {
	var out []string
	for i, f := range strings.Fields(c.Use)[1:] {
		if strings.HasPrefix(f, "<") {
			out = append(out, fmt.Sprintf("019f0000-0000-7000-8000-0000000001%02d", i))
		}
	}
	return out
}

// requiredFlagPlaceholders supplies a value for every flag cobra marks
// required, so the command reaches its request.
func requiredFlagPlaceholders(c *cobra.Command) []string {
	var out []string
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if _, req := f.Annotations[cobra.BashCompOneRequiredFlag]; req {
			out = append(out, "--"+f.Name, "019f0000-0000-7000-8000-0000000002ff")
		}
	})
	return out
}
