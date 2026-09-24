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
	"math/big"
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
		results, err := applyJQ(data, opts.JQ)
		if err != nil {
			return err
		}
		// A filter that yields no values prints nothing in plain mode, so a
		// `while read` loop runs zero times rather than once on an empty line
		// (MIO-4174). json and table keep their historical output (null / an
		// empty line) byte for byte.
		if len(results) == 0 && opts.Format == FormatPlain {
			return nil
		}
		data = collapseJQ(results)
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

// rawify produces the original JSON:API response envelope for --raw output.
//
// When the client retained the original response bytes (Resource.RawBody /
// Collection.RawBody), those are decoded verbatim so the full envelope —
// including top-level links, included, and meta that the flattened views do
// not model — round-trips unchanged. When no raw bytes are available (e.g. a
// resource synthesized in-process, or an older code path), it falls back to
// re-encoding the modelled value.
func rawify(v any) any {
	if raw := rawBytesOf(v); len(raw) > 0 {
		var out any
		if err := json.Unmarshal(raw, &out); err == nil {
			return out
		}
	}
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

// rawBytesOf returns the retained original envelope bytes for a Resource or
// Collection, or nil if none were retained.
func rawBytesOf(v any) []byte {
	switch t := v.(type) {
	case *client.Resource:
		if t != nil {
			return t.RawBody
		}
	case *client.Collection:
		if t != nil {
			return t.RawBody
		}
	}
	return nil
}

func toAnySlice(rows []map[string]any) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out
}

// applyJQ runs a gojq program over data and returns every value it emits, in
// order. The count matters to the caller (zero values render differently in
// plain mode), so collapsing to one value is left to collapseJQ.
func applyJQ(data any, program string) ([]any, error) {
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
	return results, nil
}

// collapseJQ turns jq's output values into the one value the formatters take:
// nil for none, the value itself for one, and a slice for two or more. A stream
// of 2+ therefore reaches the formatter exactly like a filter that returned one
// array, which is why renderPlain treats both the same way.
func collapseJQ(results []any) any {
	switch len(results) {
	case 0:
		return nil
	case 1:
		return results[0]
	default:
		return results
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

// renderPlain prints key=value lines. For a list of objects, records are
// separated by a blank line. Scalars are printed as-is, and a list made only of
// scalars prints each value bare, followed by a newline — so `-o plain --jq
// '.[].id'` gives the same shape for one id as for twenty (MIO-4174). A string
// is printed verbatim, so one that holds a newline spans lines. A list that mixes scalars
// with objects or arrays keeps the record format, where a scalar reads
// value=X.
func renderPlain(w io.Writer, data any) error {
	switch t := data.(type) {
	case []any:
		if allScalars(t) {
			for _, it := range t {
				fmt.Fprintln(w, scalar(it))
			}
			return nil
		}
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

// allScalars reports whether every item is a JSON scalar (string, number,
// boolean or null). The number types are the ones that reach the renderer:
// float64 from encoding/json, and int, float64 and *big.Int from gojq (which
// emits *big.Int for an integer too large for int). An object, an array or any
// other Go type makes the list a record list. An empty list counts as
// all-scalar, and prints nothing either way.
func allScalars(items []any) bool {
	for _, it := range items {
		switch it.(type) {
		case nil, string, bool, float64, int, *big.Int:
		default:
			return false
		}
	}
	return true
}

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
