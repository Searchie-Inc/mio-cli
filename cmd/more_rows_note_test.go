package cmd

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

type pagedStub struct {
	mu        sync.Mutex
	afters    []string
	bodyAfter []string
	srv       *httptest.Server
}

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
	case "products":
		return fmt.Sprintf(`{"data":%s,"meta":{"page":{"size":2,"has_more":%v}},"links":{"self":"/api/x","next":%s}}`, d, more, next)
	case "tags":
		return fmt.Sprintf(`{"data":%s,"meta":{"has_more":%v},"links":{"self":"/api/x","next":%s}}`, d, more, next)
	case "build_page_meta":
		return fmt.Sprintf(`{"data":%s,"meta":{"page":{"size":2,"has_more":%v,"next_cursor":%s}},"links":{"self":"/api/x","next":%s}}`, d, more, nextCursor, next)
	case "discussions":
		return fmt.Sprintf(`{"data":%s,"meta":{"has_more":%v,"next_cursor":%s}}`, d, more, nextCursor)
	case "discussions_null_cursor":
		return fmt.Sprintf(`{"data":%s,"meta":{"next_cursor":null,"has_more":%v},"links":{"self":"/api/x"}}`, d, more)
	case "activity":
		nextObj := "null"
		if more {
			nextObj = fmt.Sprintf(`{"href":"/api/x?page%%5Bsize%%5D=2&page%%5Bafter%%5D=%s"}`, cursor)
		}
		return fmt.Sprintf(`{"data":%s,"meta":{"has_more":%v,"next_cursor":%s},"links":{"self":{"href":"/api/x"},"next":%s}}`, d, more, nextCursor, nextObj)
	case "page_only":
		return fmt.Sprintf(`{"data":%s,"meta":{"page":{"size":2,"has_more":%v}}}`, d, more)
	case "media_links":
		if !more {
			return fmt.Sprintf(`{"data":%s,"links":null}`, d)
		}
		return fmt.Sprintf(`{"data":%s,"links":{"next":"/api/teams/t/files?page[after]=%s&page[size]=2"}}`, d, cursor)
	case "admin_total":
		return fmt.Sprintf(`{"data":%s,"meta":{"total":%d}}`, d, len(ids))
	case "top_n":
		return fmt.Sprintf(`{"data":%s,"meta":{"total":9,"has_more":%v,"is_capped":false,"cap":100,"pagination":"top_n"}}`, d, more)
	case "unpaged_total":
		return fmt.Sprintf(`{"data":%s,"meta":{"total":%d}}`, d, len(ids))
	case "unpaged_count":
		return fmt.Sprintf(`{"data":%s,"meta":{"count":%d}}`, d, len(ids))
	case "unpaged_empty_meta":
		return fmt.Sprintf(`{"data":%s,"meta":{}}`, d)
	case "unpaged_bare":
		return fmt.Sprintf(`{"data":%s}`, d)
	}
	panic("unknown shape " + shape)
}

func lastPageShape(shape string) string {
	if shape == "admin_total" {
		return "tags"
	}
	return shape
}

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

type moreRowsCase struct {
	name    string
	shape   string
	args    []string
	flag    string
	usesCur bool
	cursor  string
	forbid  string
	must    string
}

const discussionsCursor = "2026-09-24T11:00:00.123456+00:00|019f0000-0000-7000-8000-0000000000d9"

const testHub = "019f0000-0000-7000-8000-0000000000a1"

func moreRowsCases() []moreRowsCase {
	return []moreRowsCase{
		{"products list (meta.page.has_more + links.next)", "products", []string{"products", "list"}, "after", true, "", "", ""},
		{"coupons list (meta.page.has_more + links.next)", "products", []string{"coupons", "list"}, "after", true, "", "", ""},
		{"tags list (top-level has_more + links.next)", "tags", []string{"tags", "list"}, "after", true, "", "", ""},
		{"media files list (links.next only, no meta)", "media_links", []string{"media", "files", "list"}, "after", true, "", "has more", "next-page link"},
		{"media folders list (build_page_meta)", "build_page_meta", []string{"media", "folders", "list"}, "after", true, "", "", ""},
		{"community discussions list (meta.next_cursor)", "discussions", []string{"community", "discussions", "list", "--hub", testHub}, "after", true, "", "", ""},
		{"community discussions list (<iso>|<id> cursor)", "discussions", []string{"community", "discussions", "list", "--hub", testHub}, "after", true, discussionsCursor, "", ""},
		{"community discussions list (has_more, next_cursor null)", "discussions_null_cursor", []string{"community", "discussions", "list", "--hub", testHub}, "limit", false, "", "", ""},
		{"checkout payments list (meta.total only: a full page)", "admin_total", []string{"checkout", "payments", "list", "--hub", testHub, "--limit", "2"}, "after", true, "r2", "", ""},
		{"achievements offerings list (has_more, no cursor: last row id)", "page_only", []string{"achievements", "offerings", "list", "--hub", testHub}, "after", true, "r2", "", ""},
		{"activity top-engaged (links.next {href})", "activity", []string{"activity", "top-engaged", "--hub", testHub}, "after", true, "", "", ""},
		{"segments search (--page-after)", "discussions", []string{"segments", "search", "--conditions", `{"version":1,"groups":[]}`}, "page-after", true, "", "", ""},
		{"segments search at --page-size 150 (cap 200)", "discussions", []string{"segments", "search", "--conditions", `{"version":1,"groups":[]}`, "--page-size", "150"}, "page-after", true, "", "", "(or raise --page-size)"},
		{"segments members at --limit 150 (cap 200)", "discussions", []string{"segments", "members", "019f0000-0000-7000-8000-0000000000b1", "--limit", "150"}, "after", true, "", "", "(or raise --limit)"},
		{"segments search at --page-size 200 (its cap)", "discussions", []string{"segments", "search", "--conditions", `{"version":1,"groups":[]}`, "--page-size", "200"}, "page-after", true, "", "raise --page-size", ""},
		{"media search (top_n, --limit only)", "top_n", []string{"media", "search", "--query", "x"}, "limit", false, "", "", ""},
		{"media search at the page-size cap (--limit 100)", "top_n", []string{"media", "search", "--query", "x", "--limit", "100"}, "limit", false, "", "raise --limit", ""},
		{"tags list at the page-size cap (--limit 100)", "tags", []string{"tags", "list", "--limit", "100"}, "after", true, "", "raise --limit", ""},
	}
}

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
				if res.Stdout != ctl.Stdout {
					t.Errorf("stdout changed when the API reported more rows — the note must go to stderr only\n has_more: %q\n    last: %q", res.Stdout, ctl.Stdout)
				}
				if ctl.Stderr != "" {
					t.Errorf("no note is due on the last page; stderr=%q", ctl.Stderr)
				}
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
	if len(afterCmds) < 60 {
		t.Fatalf("found %d commands with --after; the walk is broken", len(afterCmds))
	}
	return afterCmds
}

func walkArgs(c *cobra.Command, extra ...string) []string {
	path := strings.Fields(c.CommandPath())[1:]
	args := append([]string{"--team", "019f0000-0000-7000-8000-0000000000f1", "--hub", testHub}, path...)
	args = append(args, positionalPlaceholders(c)...)
	args = append(args, requiredFlagPlaceholders(c)...)
	return append(args, extra...)
}

func walkEnv(url string) []string {
	return append(baseEnv(url), eventsContactTokenEnv+"=contact-token-walk")
}

func TestMoreRowsNote_EveryAfterCommand(t *testing.T) {
	const cursor = "cur-walk"
	for _, c := range afterCommands(t) {
		path := strings.Fields(c.CommandPath())[1:]
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

var realAfterShapes = map[string]string{
	"access-rules overrides list":        "tags",
	"access-rules rules list":            "tags",
	"achievements list":                  "products",
	"achievements offerings list":        "page_only",
	"activity top-engaged":               "activity",
	"api-keys list":                      "tags",
	"automations enrollments":            "tags",
	"automations list":                   "tags",
	"automations versions":               "tags",
	"checkout accounts list":             "unpaged_total",
	"checkout hub-prices list":           "unpaged_count",
	"checkout hub-products list":         "unpaged_count",
	"checkout orders list":               "admin_total",
	"checkout payments list":             "admin_total",
	"checkout subscriptions list":        "admin_total",
	"checkout webhooks list":             "admin_total",
	"community comments list":            "discussions",
	"community discussions list":         "discussions",
	"community moderation audit-log":     "discussions",
	"community moderation banned":        "discussions",
	"community moderation queue":         "discussions",
	"community moderation removed":       "discussions",
	"community spaces list":              "build_page_meta",
	"contact-attributes hub-config list": "unpaged_empty_meta",
	"contact-attributes list":            "tags",
	"contact-attributes options list":    "unpaged_empty_meta",
	"contacts list":                      "products",
	"content children":                   "products",
	"content list":                       "products",
	"coupons list":                       "products",
	"coupons products list":              "unpaged_count",
	"email drip-campaigns list":          "build_page_meta",
	"email enrollments list":             "build_page_meta",
	"email enrollments list-by-contact":  "build_page_meta",
	"email steps list":                   "build_page_meta",
	"email suppressions list":            "tags",
	"email templates list":               "build_page_meta",
	"events list":                        "tags",
	"events rsvps list":                  "tags",
	"external-login-providers list":      "unpaged_bare",
	"hub-memberships list":               "discussions",
	"hubs list":                          "products",
	"media attachments list":             "media_links",
	"media files list":                   "media_links",
	"media folders list":                 "build_page_meta",
	"media hub-media list":               "media_links",
	"media hub-playlists list":           "media_links",
	"media playlists items list":         "media_links",
	"media playlists list":               "media_links",
	"media remote-connections browse":    "media_links",
	"oauth-clients list":                 "tags",
	"pages list":                         "products",
	"pages sections list":                "unpaged_count",
	"products deliverables list":         "unpaged_count",
	"products list":                      "products",
	"products prices list":               "unpaged_count",
	"roles list":                         "tags",
	"roles permissions list":             "unpaged_bare",
	"segments list":                      "tags",
	"segments members":                   "discussions",
	"tags list":                          "tags",
	"teams list":                         "unpaged_total",
	"teams members list":                 "unpaged_total",
	"users list":                         "tags",
	"verified-domains list":              "unpaged_bare",
	"webhook-endpoints list":             "tags",
}

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
			cursor := "cur-real"
			if shape == "page_only" || shape == "admin_total" {
				cursor = "r2"
			}
			extra := []string{"-o", "json"}
			if shape == "admin_total" {
				extra = append(extra, "--limit", "2")
			}
			ps := newPagedStub(t, shape, cursor, true)
			args := walkArgs(c, extra...)
			res := runContract(t, walkEnv(ps.srv.URL), args...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d; args=%q stderr=%q", res.Code, args, res.Stderr)
			}
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

func noteLines(stderr string) []string {
	var out []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "note: ") {
			out = append(out, l)
		}
	}
	return out
}

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
		{"no cursor, --after: last row id", cmdWith("limit", "after"), withIDs(col(3, noCursor), "a1", "a2", "a3"),
			"note: the API returned 3 rows and has more; fetch the next page with --after a3 (or raise --limit)"},
		{"no cursor, --after, no rows", cmdWith("limit", "after"), col(0, noCursor),
			"note: the API returned 0 rows and has more; raise --limit to get more in one page"},
		{"cursor but no after flag", cmdWith("limit"), col(20, withCursor),
			"note: the API returned 20 rows and has more; raise --limit to get more in one page"},
		{"no paging flag at all", cmdWith(), col(20, withCursor),
			"note: the API returned 20 rows and has more; this command has no paging flag, and --raw shows the API's meta and links"},
		{"last page", cmdWith("limit", "after"), col(20, last), ""},
		{"shell-unsafe cursor is quoted", cmdWith("after"), col(20, `{"data":[],"meta":{"has_more":true,"next_cursor":"2026-09-24T11:00:00+00:00|abc"}}`),
			"note: the API returned 20 rows and has more; fetch the next page with --after '2026-09-24T11:00:00+00:00|abc'"},
		{"single quote inside a cursor", cmdWith("after"), col(20, `{"data":[],"meta":{"has_more":true,"next_cursor":"a'b c"}}`),
			`note: the API returned 20 rows and has more; fetch the next page with --after 'a'\''b c'`},
		{"links.next only (media lists)", cmdWith("limit", "after"), col(3, linkOnly),
			"note: the API returned 3 rows and a next-page link (the next page may be empty); fetch the next page with --after LINKCUR (or raise --limit)"},
		{"next_cursor null: no row id offered", cmdWith("limit", "after"), withIDs(col(3, nullCursor), "a1", "a2", "a3"),
			"note: the API returned 3 rows and has more; raise --limit to get more in one page"},
		{"--limit 99 can still be raised", set(cmdWith("limit", "after"), "limit", "99"), col(99, withCursor),
			"note: the API returned 99 rows and has more; fetch the next page with --after CUR (or raise --limit)"},
		{"--limit 100 is the cap", set(cmdWith("limit", "after"), "limit", "100"), col(100, withCursor),
			"note: the API returned 100 rows and has more; fetch the next page with --after CUR"},
		{"--limit 100 is the cap even for a short page", set(cmdWith("limit", "after"), "limit", "100"), col(40, withCursor),
			"note: the API returned 40 rows and has more; fetch the next page with --after CUR"},
		{"--page-size 150 is past the usual cap", set(cmdWith("page-size", "page-after"), "page-size", "150"), col(150, withCursor),
			"note: the API returned 150 rows and has more; fetch the next page with --page-after CUR"},
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
		{"unsignalled: full default page", unsignalled(cmdWith("limit", "after")), withLastID(col(20, totalOnly), "z20"),
			"note: the API returned 20 rows, a full page, and this list does not report whether more exist; fetch the next page with --after z20 (or raise --limit)"},
		{"unsignalled: short default page", unsignalled(cmdWith("limit", "after")), withLastID(col(19, totalOnly), "z19"), ""},
		{"unsignalled: full --limit page", set(unsignalled(cmdWith("limit", "after")), "limit", "5"), withLastID(col(5, totalOnly), "z5"),
			"note: the API returned 5 rows, a full page, and this list does not report whether more exist; fetch the next page with --after z5 (or raise --limit)"},
		{"unsignalled: short --limit page", set(unsignalled(cmdWith("limit", "after")), "limit", "5"), withLastID(col(4, totalOnly), "z4"), ""},
		{"unsignalled: empty page", unsignalled(cmdWith("limit", "after")), col(0, totalOnly), ""},
		{"unmarked meta.total list: no note", cmdWith("limit", "after"), withLastID(col(20, totalOnly), "z20"), ""},
		{"unsignalled mark, but the API says last page", unsignalled(cmdWith("limit", "after")), col(20, last), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := moreRowsNote(tc.cmd, tc.col)
			if got != tc.want {
				t.Errorf("moreRowsNote\n got: %q\nwant: %q", got, tc.want)
			}
			if p := tc.col.NextPage(); p.More && p.Cursor != "" && strings.Contains(got, "fetch the next page") {
				words := followNote(t, got)
				if len(words) != 2 || words[1] != p.Cursor {
					t.Errorf("through sh the suggestion gives argv %q; want the cursor %q intact", words, p.Cursor)
				}
			}
		})
	}
}

func positionalPlaceholders(c *cobra.Command) []string {
	var out []string
	for i, f := range strings.Fields(c.Use)[1:] {
		if strings.HasPrefix(f, "<") {
			out = append(out, fmt.Sprintf("019f0000-0000-7000-8000-0000000001%02d", i))
		}
	}
	return out
}

func requiredFlagPlaceholders(c *cobra.Command) []string {
	var out []string
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if _, req := f.Annotations[cobra.BashCompOneRequiredFlag]; req {
			out = append(out, "--"+f.Name, "019f0000-0000-7000-8000-0000000002ff")
		}
	})
	return out
}
