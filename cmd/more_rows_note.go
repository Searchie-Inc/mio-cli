package cmd

// more_rows_note.go — MIO-4174: say on stderr when a list has more rows than
// the page the API returned.
//
// Every list command returns one page (the API's default size, usually 20),
// and the flattened output — a bare JSON array — carries no pagination signal:
// has_more and the next cursor survive only under --raw. So a team's 21st
// product looked absent. The signal cannot go on stdout without turning the
// documented bare array into an object and breaking every `--jq '.[0].id'`,
// so it goes to stderr, as one line, naming the flag that fetches the rest.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
)

// usualPageSizeCap is the most rows one page can hold on mio-backend's
// paginated lists: infrastructure/pagination.py MAX_PAGE_SIZE = 100, and the
// page[size] bound of every list handler but the two segments routes, which
// markPageSizeCap raises. Past the cap the API rejects the request (422)
// instead of returning more, so the note offers a larger page only below it.
const usualPageSizeCap = 100

// pageSizeCapAnnotation carries a command's own page-size cap when it is not
// usualPageSizeCap. See markPageSizeCap.
const pageSizeCapAnnotation = "mio/page-size-cap"

// markPageSizeCap records that cmd's route accepts pages of up to rows rows.
func markPageSizeCap(cmd *cobra.Command, rows int) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[pageSizeCapAnnotation] = strconv.Itoa(rows)
}

// pageSizeCap returns cmd's page-size cap: its markPageSizeCap value, else
// usualPageSizeCap.
func pageSizeCap(cmd *cobra.Command) int {
	if n, err := strconv.Atoi(cmd.Annotations[pageSizeCapAnnotation]); err == nil && n > 0 {
		return n
	}
	return usualPageSizeCap
}

// unsignalledPagingAnnotation marks a list command whose route pages — it
// honours page[size] and takes page[after] = the last row's id — but whose
// envelope says nothing about further pages. Its value is the route's default
// page[size]. See markUnsignalledPaging.
const unsignalledPagingAnnotation = "mio/unsignalled-paging-default-size"

// markUnsignalledPaging declares that cmd's route pages without reporting
// has_more, a cursor or a next link, and serves defaultPageSize rows when no
// --limit is given. For such a command a FULL page is the only hint that more
// rows may exist, so the note says so and offers the last row's id. Only mark
// a route that pages: on a route that ignores page[after], the offered cursor
// would fetch the same page again.
func markUnsignalledPaging(cmd *cobra.Command, defaultPageSize int) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[unsignalledPagingAnnotation] = strconv.Itoa(defaultPageSize)
}

// moreRowsNote returns the note for a list page the API points past (has_more,
// or a next link), or for a full page on a route marked with
// markUnsignalledPaging, or "" when none is due. The flag it names is the one this command actually
// registers: --after (the shared pagination flags) or --page-after (segments
// search) for a cursor, else --limit / --page-size when there is no cursor to
// offer (media search is top-N and takes no page[after]: more rows come only
// from a larger page, up to the API's cap).
func moreRowsNote(cmd *cobra.Command, col *client.Collection) string {
	p := col.NextPage()
	n := len(col.Data)
	after := registeredFlag(cmd, "after", "page-after")
	limit := registeredFlag(cmd, "limit", "page-size")
	requested := requestedPageSize(cmd, limit)

	// A route marked with markUnsignalledPaging reports nothing either way, so
	// a full page is the only hint (the checkout admin hub lists).
	full := false
	if !p.Signalled {
		if def, ok := unsignalledDefaultPageSize(cmd); ok && after != "" && n > 0 {
			size := def
			if requested > 0 {
				size = requested
			}
			full = n >= size
		}
	}
	if !p.More && !full {
		return ""
	}
	noun := "rows"
	if n == 1 {
		noun = "row"
	}
	head := fmt.Sprintf("note: the API returned %d %s and has more", n, noun)
	switch {
	case full:
		head = fmt.Sprintf("note: the API returned %d %s, a full page, and this list does not report whether more exist", n, noun)
	case p.LinkOnly:
		// No has_more, only a next link, which media files and attachments
		// send on any full page: say what the API sent, not more than that.
		head = fmt.Sprintf("note: the API returned %d %s and a next-page link (the next page may be empty)", n, noun)
	}
	// A list with no place for its own cursor still takes page[after], and
	// the backend's pagination contract defines it as the id of the last row
	// of the previous page (infrastructure/pagination.py get_pagination):
	// achievements offerings (admin_router.list_hub_achievements) reports
	// has_more with no cursor, and the checkout admin hub lists page by
	// `id > page[after]`. A list that DOES have a place for its cursor and
	// leaves it null (discussions, when the last row's last_activity_at is
	// NULL) gets no derived one: its own parser reads a bare id as no cursor
	// and serves the first page again.
	cursor := p.Cursor
	if cursor == "" && !p.APICursors && after != "" && n > 0 {
		cursor = col.Data[n-1].ID
	}
	// A larger page helps only below the API's cap.
	pageCap := pageSizeCap(cmd)
	canRaise := limit != "" && max(requested, n) < pageCap
	switch {
	case cursor != "" && after != "":
		s := fmt.Sprintf("%s; fetch the next page with --%s %s", head, after, shellQuote(cursor))
		if canRaise {
			s += fmt.Sprintf(" (or raise --%s)", limit)
		}
		return s
	case canRaise:
		return fmt.Sprintf("%s; raise --%s to get more in one page", head, limit)
	case limit != "":
		return fmt.Sprintf("%s; the API sent no cursor for the next page and --%s is already at its cap (%d), so this command cannot fetch the rest", head, limit, pageCap)
	default:
		return head + "; this command has no paging flag, and --raw shows the API's meta and links"
	}
}

// requestedPageSize returns the page size the user asked for with the named
// flag, or 0 when they did not set it.
func requestedPageSize(cmd *cobra.Command, flag string) int {
	if flag == "" || !cmd.Flags().Changed(flag) {
		return 0
	}
	v, err := cmd.Flags().GetInt(flag)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// unsignalledDefaultPageSize reads markUnsignalledPaging's annotation.
func unsignalledDefaultPageSize(cmd *cobra.Command) (int, bool) {
	v, ok := cmd.Annotations[unsignalledPagingAnnotation]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// shellQuote returns s as one POSIX shell word, so the note's suggestion can be
// pasted as-is. Most cursors are base64url or UUIDs and print bare, but some
// lists hand out "<isoformat>|<id>" (discussions, comments, messages on
// mio-backend origin/main), where an unquoted `|` would pipe the command into
// the cursor's second half. Anything outside a conservative safe set is
// single-quoted; an embedded single quote closes the quoting, is written
// backslash-escaped, and reopens it.
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !shellSafeRune(r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSafeRune reports whether r needs no quoting anywhere in a POSIX shell
// argument word.
func shellSafeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	default:
		return strings.ContainsRune("_-.:/=+,@%", r)
	}
}

// registeredFlag returns the first of names that cmd registers, or "".
func registeredFlag(cmd *cobra.Command, names ...string) string {
	for _, name := range names {
		if cmd.Flags().Lookup(name) != nil {
			return name
		}
	}
	return ""
}
