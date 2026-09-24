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
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
)

// moreRowsNote returns the note for a list page the API says is not the last,
// or "" when none is due. The flag it names is the one this command actually
// registers: --after (the shared pagination flags) or --page-after (segments
// search) for a cursor, else --limit / --page-size when there is no cursor to
// offer (media search is top-N and takes no page[after]: more rows come only
// from a larger page).
func moreRowsNote(cmd *cobra.Command, col *client.Collection) string {
	more, cursor := col.NextPage()
	if !more {
		return ""
	}
	n := len(col.Data)
	noun := "rows"
	if n == 1 {
		noun = "row"
	}
	head := fmt.Sprintf("note: the API returned %d %s and has more", n, noun)
	after := registeredFlag(cmd, "after", "page-after")
	limit := registeredFlag(cmd, "limit", "page-size")
	// A list that reports has_more with no cursor at all (achievements
	// offerings, mio-backend origin/main admin_router.list_hub_offerings) still
	// takes page[after], and the backend's pagination contract defines it as
	// the id of the last row of the previous page (infrastructure/pagination.py
	// get_pagination). Every list that pages any other way sends its cursor.
	if cursor == "" && after != "" && n > 0 {
		cursor = col.Data[n-1].ID
	}
	switch {
	case cursor != "" && after != "":
		s := fmt.Sprintf("%s; fetch the next page with --%s %s", head, after, shellQuote(cursor))
		if limit != "" {
			s += fmt.Sprintf(" (or raise --%s)", limit)
		}
		return s
	case limit != "":
		return fmt.Sprintf("%s; raise --%s to get more in one page", head, limit)
	default:
		return head + "; this command has no paging flag, and --raw shows the API's meta and links"
	}
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
