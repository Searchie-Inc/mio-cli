package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
)

const usualPageSizeCap = 100

const pageSizeCapAnnotation = "mio/page-size-cap"

func markPageSizeCap(cmd *cobra.Command, rows int) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[pageSizeCapAnnotation] = strconv.Itoa(rows)
}

func pageSizeCap(cmd *cobra.Command) int {
	if n, err := strconv.Atoi(cmd.Annotations[pageSizeCapAnnotation]); err == nil && n > 0 {
		return n
	}
	return usualPageSizeCap
}

const unsignalledPagingAnnotation = "mio/unsignalled-paging-default-size"

func markUnsignalledPaging(cmd *cobra.Command, defaultPageSize int) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[unsignalledPagingAnnotation] = strconv.Itoa(defaultPageSize)
}

func moreRowsNote(cmd *cobra.Command, col *client.Collection) string {
	p := col.NextPage()
	n := len(col.Data)
	after := registeredFlag(cmd, "after", "page-after")
	limit := registeredFlag(cmd, "limit", "page-size")
	requested := requestedPageSize(cmd, limit)

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
		head = fmt.Sprintf("note: the API returned %d %s and a next-page link (the next page may be empty)", n, noun)
	}
	cursor := p.Cursor
	if cursor == "" && !p.APICursors && after != "" && n > 0 {
		cursor = col.Data[n-1].ID
	}
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

func shellSafeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	default:
		return strings.ContainsRune("_-.:/=+,@%", r)
	}
}

func registeredFlag(cmd *cobra.Command, names ...string) string {
	for _, name := range names {
		if cmd.Flags().Lookup(name) != nil {
			return name
		}
	}
	return ""
}
