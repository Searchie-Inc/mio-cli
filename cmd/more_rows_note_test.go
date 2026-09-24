package cmd

// more_rows_note_test.go — MIO-4174 part 2: when the API says a list has more
// rows than it returned, the CLI says so on STDERR and names the flag that
// fetches them. STDOUT is untouched: the flattened bare array is a documented
// contract (`--jq '.[0].id'`, `--jq '.[].id'`), so the signal cannot go there.
//
// The oracles are all observables the implementation cannot fake:
//   - stdout is compared byte for byte with the same command against the same
//     rows served with has_more:false;
//   - the cursor the note prints is fed back to the CLI and must reach the
//     wire as page[after] (or the body's page.after for segments search), and
//     the stub answers that request with the real next page;
//   - every command that registers --after is run against a has_more stub, so
//     a list command that stops going through the shared render path loses its
//     note by name.

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
// produced by firstMeta (the backend shape under test) with more=true; the
// request that carries the right cursor gets the second, last page. It records
// every page[after] it was sent so a test can prove the note's cursor reached
// the wire.
type pagedStub struct {
	mu        sync.Mutex
	afters    []string
	bodyAfter []string // page.after from a POST body (segments search)
	srv       *httptest.Server
}

// envelope builds a list response in a named backend shape.
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
	case "products": // meta.page.has_more; cursor only in links.next
		return fmt.Sprintf(`{"data":%s,"meta":{"page":{"size":2,"has_more":%v}},"links":{"self":"/api/x","next":%s}}`, d, more, next)
	case "tags": // top-level meta.has_more; cursor only in links.next
		return fmt.Sprintf(`{"data":%s,"meta":{"has_more":%v},"links":{"self":"/api/x","next":%s}}`, d, more, next)
	case "build_page_meta": // meta.page.{has_more,next_cursor} + links.next
		return fmt.Sprintf(`{"data":%s,"meta":{"page":{"size":2,"has_more":%v,"next_cursor":%s}},"links":{"self":"/api/x","next":%s}}`, d, more, nextCursor, next)
	case "checkout": // meta.{total,page.{has_more,next_cursor}}, no links
		return fmt.Sprintf(`{"data":%s,"meta":{"total":9,"page":{"size":2,"has_more":%v,"next_cursor":%s}}}`, d, more, nextCursor)
	case "discussions": // top-level meta.{has_more,next_cursor}, no links
		return fmt.Sprintf(`{"data":%s,"meta":{"has_more":%v,"next_cursor":%s}}`, d, more, nextCursor)
	case "top_n": // media search: has_more, no cursor anywhere
		return fmt.Sprintf(`{"data":%s,"meta":{"total":9,"has_more":%v,"is_capped":false,"cap":100,"pagination":"top_n"}}`, d, more)
	}
	panic("unknown shape " + shape)
}

// newPagedStub answers every request with page 1 (more=true, cursor) unless
// the request carries that cursor, in which case it answers the last page.
// forceMore=false serves page 1's rows with more=false instead — the control
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
		_, _ = w.Write([]byte(envelope(shape, []string{"r1", "r2"}, forceMore, cursor)))
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
}

// discussionsCursor is the shape community/routers/discussions_admin.py
// _build_cursor emits (and comments, messages): "<isoformat>|<id>". The `|`
// makes an unquoted copy of it a shell pipe.
const discussionsCursor = "2026-09-24T11:00:00.123456+00:00|019f0000-0000-7000-8000-0000000000d9"

func moreRowsCases() []moreRowsCase {
	return []moreRowsCase{
		// The four commands QA hit, each in its backend's real shape.
		{"products list (meta.page.has_more + links.next)", "products", []string{"products", "list"}, "after", true, ""},
		{"coupons list (meta.page.has_more + links.next)", "products", []string{"coupons", "list"}, "after", true, ""},
		{"tags list (top-level has_more + links.next)", "tags", []string{"tags", "list"}, "after", true, ""},
		{"media files list (build_page_meta)", "build_page_meta", []string{"media", "files", "list"}, "after", true, ""},
		{"community discussions list (meta.next_cursor)", "discussions", []string{"community", "discussions", "list", "--hub", "019f0000-0000-7000-8000-0000000000a1"}, "after", true, ""},
		// The backend's real discussions cursor: the note must survive a shell.
		{"community discussions list (<iso>|<id> cursor)", "discussions", []string{"community", "discussions", "list", "--hub", "019f0000-0000-7000-8000-0000000000a1"}, "after", true, discussionsCursor},
		{"checkout payments list (meta.page.next_cursor, no links)", "checkout", []string{"checkout", "payments", "list", "--hub", "019f0000-0000-7000-8000-0000000000a1"}, "after", true, ""},
		// segments search pages with --page-after, carried in the POST body.
		{"segments search (--page-after)", "discussions", []string{"segments", "search", "--conditions", `{"version":1,"groups":[]}`}, "page-after", true, ""},
		// media search is top-N: has_more with no cursor, and no --after flag.
		{"media search (top_n, --limit only)", "top_n", []string{"media", "search", "--query", "x"}, "limit", false, ""},
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

// Every command that registers --after is a paged list command, and each must
// print the note when the API reports more rows. The hook lives in the shared
// render path, so this is what catches a list command that renders some other
// way. The stub answers in the tags shape (top-level has_more, cursor only in
// links.next), which needs the links parse to find the cursor at all.
func TestMoreRowsNote_EveryAfterCommand(t *testing.T) {
	const cursor = "cur-walk"
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
	// 65 list commands register --after today (addPaginationFlags). A walk
	// that found far fewer would be a broken walk, not a passing test.
	if len(afterCmds) < 60 {
		t.Fatalf("found %d commands with --after; the walk is broken", len(afterCmds))
	}

	for _, c := range afterCmds {
		path := strings.Fields(c.CommandPath())[1:] // drop "mio"
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			ps := newPagedStub(t, "tags", cursor, true)
			args := append([]string{"--team", "019f0000-0000-7000-8000-0000000000f1", "--hub", "019f0000-0000-7000-8000-0000000000a1"}, path...)
			args = append(args, positionalPlaceholders(c)...)
			args = append(args, requiredFlagPlaceholders(c)...)
			args = append(args, "-o", "json")

			// `mio events` pages only with a member token (MIO_CONTACT_TOKEN);
			// every other command ignores it.
			env := append(baseEnv(ps.srv.URL), eventsContactTokenEnv+"=contact-token-walk")
			res := runContract(t, env, args...)
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
	withCursor := `{"data":[],"meta":{"has_more":true,"next_cursor":"CUR"}}`
	noCursor := `{"data":[],"meta":{"has_more":true}}`
	last := `{"data":[],"meta":{"has_more":false,"next_cursor":null}}`
	cmdWith := func(flags ...string) *cobra.Command {
		c := &cobra.Command{Use: "x"}
		for _, f := range flags {
			c.Flags().String(f, "", "")
		}
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
		{"no cursor, --limit", cmdWith("limit", "after"), col(20, noCursor),
			"note: the API returned 20 rows and has more; raise --limit to get more in one page"},
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := moreRowsNote(tc.cmd, tc.col)
			if got != tc.want {
				t.Errorf("moreRowsNote\n got: %q\nwant: %q", got, tc.want)
			}
			// Whatever the spelling, a suggested cursor must come back out of a
			// real shell as the API's cursor, byte for byte.
			if more, cursor := tc.col.NextPage(); more && cursor != "" && strings.Contains(got, "fetch the next page") {
				words := followNote(t, got)
				if len(words) != 2 || words[1] != cursor {
					t.Errorf("through sh the suggestion gives argv %q; want the cursor %q intact", words, cursor)
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
