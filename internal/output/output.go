// Package output renders command results in one of three formats — json, table,
// or plain — and applies an optional gojq post-filter. JSON is the agent-facing
// default off a TTY; table is the human default on a TTY.
//
// The renderer accepts the client's *Resource and *Collection types as well as
// arbitrary maps/slices, so commands can hand it whatever they have. It always
// flattens JSON:API resources (id+type+attributes merged) unless Raw is set.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/itchyny/gojq"

	"github.com/Searchie-Inc/mio-cli/internal/client"
)

// Format is the rendering mode.
type Format string

const (
	FormatJSON  Format = "json"
	FormatTable Format = "table"
	FormatPlain Format = "plain"
)

// ParseFormat validates a --output value.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatJSON, FormatTable, FormatPlain:
		return Format(s), nil
	default:
		return "", fmt.Errorf("invalid output format %q (want json|table|plain)", s)
	}
}

// Options controls a single Render call.
type Options struct {
	Format Format
	// JQ, when non-empty, is a gojq program applied to the (flattened-or-raw)
	// data before formatting. Mirrors `gh --jq`.
	JQ string
	// Raw emits the original JSON:API envelope instead of the flattened view.
	// Only affects *Resource / *Collection inputs.
	Raw bool
}

// Render writes v to w in the requested format. v is normally a *client.Resource
// or *client.Collection but may be any JSON-encodable value (e.g. a plain map
// from an auth route).
func Render(w io.Writer, v any, opts Options) error {
	data := normalize(v, opts.Raw)

	if opts.JQ != "" {
		filtered, err := applyJQ(data, opts.JQ)
		if err != nil {
			return err
		}
		data = filtered
	}

	switch opts.Format {
	case FormatJSON, "":
		return renderJSON(w, data)
	case FormatTable:
		return renderTable(w, data)
	case FormatPlain:
		return renderPlain(w, data)
	default:
		return fmt.Errorf("unknown output format %q", opts.Format)
	}
}

// normalize converts client resource types into plain Go values (maps/slices)
// for uniform downstream handling. With raw=true the original JSON:API shape is
// preserved by round-tripping through JSON.
func normalize(v any, raw bool) any {
	switch t := v.(type) {
	case *client.Resource:
		if t == nil {
			return map[string]any{}
		}
		if raw {
			return rawify(t)
		}
		return t.Flatten()
	case client.Resource:
		if raw {
			return rawify(&t)
		}
		return t.Flatten()
	case *client.Collection:
		if t == nil {
			return []any{}
		}
		if raw {
			return rawify(t)
		}
		return toAnySlice(t.Flatten())
	case client.Collection:
		if raw {
			return rawify(&t)
		}
		return toAnySlice(t.Flatten())
	default:
		return v
	}
}

// rawify round-trips a value through JSON so the original envelope keys
// (data/type/attributes/meta) are preserved as generic maps.
func rawify(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

func toAnySlice(rows []map[string]any) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out
}

// applyJQ runs a gojq program over data and returns the (possibly multiple)
// outputs. A single output is returned bare; multiple outputs become a slice.
func applyJQ(data any, program string) (any, error) {
	query, err := gojq.Parse(program)
	if err != nil {
		return nil, fmt.Errorf("invalid --jq expression: %w", err)
	}
	iter := query.Run(data)
	var results []any
	for {
		val, ok := iter.Next()
		if !ok {
			break
		}
		if jqErr, isErr := val.(error); isErr {
			return nil, fmt.Errorf("--jq: %w", jqErr)
		}
		results = append(results, val)
	}
	switch len(results) {
	case 0:
		return nil, nil
	case 1:
		return results[0], nil
	default:
		return results, nil
	}
}

func renderJSON(w io.Writer, data any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// renderTable prints aligned columns. A slice of maps becomes a multi-row
// table; a single map becomes a two-column key/value table.
func renderTable(w io.Writer, data any) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	defer tw.Flush()

	switch t := data.(type) {
	case []any:
		rows := asMapRows(t)
		if len(rows) == 0 {
			fmt.Fprintln(tw, "(no results)")
			return nil
		}
		cols := columnOrder(rows)
		fmt.Fprintln(tw, strings.Join(upper(cols), "\t"))
		for _, row := range rows {
			cells := make([]string, len(cols))
			for i, c := range cols {
				cells[i] = scalar(row[c])
			}
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
	case map[string]any:
		fmt.Fprintln(tw, "FIELD\tVALUE")
		for _, k := range sortedKeys(t) {
			fmt.Fprintf(tw, "%s\t%s\n", k, scalar(t[k]))
		}
	default:
		fmt.Fprintln(tw, scalar(data))
	}
	return nil
}

// renderPlain prints key=value lines. For a list, records are separated by a
// blank line. Scalars are printed as-is.
func renderPlain(w io.Writer, data any) error {
	switch t := data.(type) {
	case []any:
		rows := asMapRows(t)
		for i, row := range rows {
			if i > 0 {
				fmt.Fprintln(w)
			}
			for _, k := range sortedKeys(row) {
				fmt.Fprintf(w, "%s=%s\n", k, scalar(row[k]))
			}
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			fmt.Fprintf(w, "%s=%s\n", k, scalar(t[k]))
		}
	default:
		fmt.Fprintln(w, scalar(data))
	}
	return nil
}

// ---- helpers ----------------------------------------------------------------

func asMapRows(items []any) []map[string]any {
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			rows = append(rows, m)
		} else {
			rows = append(rows, map[string]any{"value": it})
		}
	}
	return rows
}

// columnOrder picks a stable, friendly column order: id and type first (when
// present), then the remaining keys alphabetically, unioned across all rows.
func columnOrder(rows []map[string]any) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			seen[k] = true
		}
	}
	var rest []string
	for k := range seen {
		if k != "id" && k != "type" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)

	var cols []string
	if seen["id"] {
		cols = append(cols, "id")
	}
	if seen["type"] {
		cols = append(cols, "type")
	}
	return append(cols, rest...)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func upper(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToUpper(s)
	}
	return out
}

// scalar renders a single cell value. Nested objects/arrays are compact-JSON
// encoded so a table/plain cell never spans lines.
func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		// JSON numbers decode as float64; print integers without a decimal.
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
