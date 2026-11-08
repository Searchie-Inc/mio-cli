package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type hubDisplayListCase struct {
	name string
	args []string
	data string
}

var hubDisplayListCases = []hubDisplayListCase{
	{
		name: "hub-prices",
		args: []string{"checkout", "hub-prices", "list"},
		data: `[
			{"type":"hub_price_displays","id":"01a0cdb8-ee62-7822-94a1-3a5bee8af332","attributes":{
				"created_at":"2026-09-23T10:00:00Z","hub_id":"01a0cd95-78df-7ff2-9ef3-3f2588c95a50",
				"position":0,"price_id":"01a0cdb8-e5a5-7000-8000-000000000001",
				"updated_at":"2026-09-23T10:00:00Z","visible":true}},
			{"type":"hub_price_displays","id":"01a0cdb8-ee62-7822-94a1-3a5bee8af333","attributes":{
				"created_at":"2026-09-23T10:00:01Z","hub_id":"01a0cd95-78df-7ff2-9ef3-3f2588c95a50",
				"position":1,"price_id":"01a0cdb8-e5a5-7000-8000-000000000002",
				"updated_at":"2026-09-23T10:00:01Z","visible":false}}
		]`,
	},
	{
		name: "hub-products",
		args: []string{"checkout", "hub-products", "list"},
		data: `[
			{"type":"hub_product_displays","id":"01a0cdb8-d04a-70b3-85c0-5e57251b99a2","attributes":{
				"created_at":"2026-09-23T10:00:00Z","hub_id":"01a0cd95-78df-7ff2-9ef3-3f2588c95a50",
				"is_free_tier":false,"position":0,"product_id":"01a0cdb8-b571-7d21-9cc4-a8fc66a0930c",
				"updated_at":"2026-09-23T10:00:00Z","visible":true}},
			{"type":"hub_product_displays","id":"01a0cdb8-d04a-70b3-85c0-5e57251b99a3","attributes":{
				"created_at":"2026-09-23T10:00:01Z","hub_id":"01a0cd95-78df-7ff2-9ef3-3f2588c95a50",
				"is_free_tier":true,"position":1,"product_id":"01a0cdb8-b571-7d21-9cc4-a8fc66a0930d",
				"updated_at":"2026-09-23T10:00:01Z","visible":false}}
		]`,
	},
}

type wireRow struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
}

func (tc hubDisplayListCase) rows(t *testing.T) []wireRow {
	t.Helper()
	var rows []wireRow
	if err := json.Unmarshal([]byte(tc.data), &rows); err != nil {
		t.Fatalf("%s fixture is not valid JSON: %v", tc.name, err)
	}
	if len(rows) != 2 || rows[0].Attributes["visible"] != true || rows[1].Attributes["visible"] != false {
		t.Fatalf("%s fixture must carry visible=true then visible=false to discriminate a faked value", tc.name)
	}
	return rows
}

func (tc hubDisplayListCase) run(t *testing.T, srvURL string, extra ...string) string {
	t.Helper()
	args := []string{"--team", "t_team1", "--hub", "hub_123"}
	args = append(args, tc.args...)
	args = append(args, extra...)
	res := runContract(t, baseEnv(srvURL), args...)
	if res.Code != errs.ExitOK {
		t.Fatalf("%s list %v: exit code = %d, want %d (ExitOK); stderr=%q", tc.name, extra, res.Code, errs.ExitOK, res.Stderr)
	}
	return res.Stdout
}

func cellText(v any) string {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func requireRenderedRow(t *testing.T, where string, i int, wire wireRow, got map[string]string) {
	t.Helper()
	for k, v := range wire.Attributes {
		g, ok := got[k]
		if !ok {
			t.Errorf("%s row %d: API attribute %q is missing from the rendered row (rendered keys %v) — "+
				"list must render every attribute under its own name (MIO-4156)", where, i, k, sortedKeysOf(got))
			continue
		}
		if want := cellText(v); g != want {
			t.Errorf("%s row %d: attribute %q rendered as %q, want %q (the API's value)", where, i, k, g, want)
		}
	}
	for k := range got {
		if _, fromWire := wire.Attributes[k]; !fromWire && k != "id" && k != "type" {
			t.Errorf("%s row %d: rendered key %q is not an API attribute — the renderer invented or renamed it", where, i, k)
		}
	}
	if got["id"] != wire.ID {
		t.Errorf("%s row %d: id rendered as %q, want %q", where, i, got["id"], wire.ID)
	}
}

func sortedKeysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestCheckoutHubDisplayList_RendersVisibleUnderItsOwnName(t *testing.T) {
	for _, tc := range hubDisplayListCases {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.rows(t)
			srv, _, _, _ := captureCommerceRequest(t, http.StatusOK, `{"data":`+tc.data+`,"meta":{"count":2}}`)

			t.Run("json", func(t *testing.T) {
				var got []map[string]any
				out := tc.run(t, srv.URL, "-o", "json")
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatalf("json output is not a JSON array: %v; stdout=%q", err, out)
				}
				if len(got) != len(wire) {
					t.Fatalf("rendered %d rows, want %d; stdout=%q", len(got), len(wire), out)
				}
				for i, row := range got {
					text := make(map[string]string, len(row))
					for k, v := range row {
						text[k] = cellText(v)
					}
					if v, ok := row["visible"]; ok && reflect.TypeOf(v) != reflect.TypeOf(true) {
						t.Errorf("json row %d: visible is %T, want a JSON bool", i, v)
					}
					requireRenderedRow(t, "json", i, wire[i], text)
				}
			})

			t.Run("table", func(t *testing.T) {
				lines := nonEmptyLines(tc.run(t, srv.URL, "-o", "table"))
				if len(lines) != 1+len(wire) {
					t.Fatalf("table has %d lines, want header + %d rows:\n%s", len(lines), len(wire), strings.Join(lines, "\n"))
				}
				header := strings.Fields(lines[0])
				for i, line := range lines[1:] {
					cells := strings.Fields(line)
					if len(cells) != len(header) {
						t.Fatalf("table row %d has %d cells for %d columns:\n%s", i, len(cells), len(header), strings.Join(lines, "\n"))
					}
					text := make(map[string]string, len(header))
					for j, col := range header {
						text[strings.ToLower(col)] = cells[j]
					}
					requireRenderedRow(t, "table", i, wire[i], text)
				}
			})

			t.Run("plain", func(t *testing.T) {
				records := strings.Split(strings.TrimSpace(tc.run(t, srv.URL, "-o", "plain")), "\n\n")
				if len(records) != len(wire) {
					t.Fatalf("plain output has %d records, want %d: %q", len(records), len(wire), records)
				}
				for i, rec := range records {
					text := map[string]string{}
					for _, line := range nonEmptyLines(rec) {
						k, v, ok := strings.Cut(line, "=")
						if !ok {
							t.Fatalf("plain record %d line %q is not key=value", i, line)
						}
						text[k] = v
					}
					requireRenderedRow(t, "plain", i, wire[i], text)
				}
			})

			t.Run("jq", func(t *testing.T) {
				var got []any
				out := tc.run(t, srv.URL, "--jq", "[.[] | .visible]")
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatalf("--jq output is not a JSON array: %v; stdout=%q", err, out)
				}
				want := []any{wire[0].Attributes["visible"], wire[1].Attributes["visible"]}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("--jq '[.[] | .visible]' = %v, want %v — `visible` must be selectable by its API name", got, want)
				}
			})
		})
	}
}

func updateWireAttributes(t *testing.T, cmd *cobra.Command, path []string) []string {
	t.Helper()
	srv, _, _, gotBody := captureCommerceRequest(t, http.StatusOK, commerceResourceBody)

	args := append([]string{"--hub", "hub_123"}, path...)
	cmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "help" {
			return
		}
		switch f.Value.Type() {
		case "bool":
			args = append(args, "--"+f.Name+"=false")
		case "int":
			args = append(args, "--"+f.Name+"=1")
		default:
			t.Fatalf("%s: flag --%s has type %q; teach updateWireAttributes a valid value for it", cmd.CommandPath(), f.Name, f.Value.Type())
		}
	})

	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("%v: exit code = %d, want %d (ExitOK); stderr=%q", args, res.Code, errs.ExitOK, res.Stderr)
	}
	_, attrs := decodeDataTypeAttrs(t, *gotBody)
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		t.Fatalf("%v sent no attributes; the guard has nothing to check", args)
	}
	return keys
}

func TestCheckoutHubDisplayHelp_NamesEachEditableWireAttribute(t *testing.T) {
	cases := []struct {
		name  string
		cmd   *cobra.Command
		path  []string
		pages [][]string
	}{
		{
			name:  "hub-prices",
			cmd:   checkoutHubPricesUpdateCmd,
			path:  []string{"checkout", "hub-prices", "update", "hprd_1"},
			pages: [][]string{{"checkout", "hub-prices"}, {"checkout", "hub-prices", "update"}},
		},
		{
			name:  "hub-products",
			cmd:   checkoutHubProductsUpdateCmd,
			path:  []string{"checkout", "hub-products", "update", "hpd_1"},
			pages: [][]string{{"checkout", "hub-products", "update"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys := updateWireAttributes(t, tc.cmd, tc.path)
			for _, page := range tc.pages {
				out := helpOutput(t, page...)
				for _, k := range keys {
					if !strings.Contains(out, "`"+k+"`") {
						t.Errorf("`mio %s --help` does not name the editable attribute `%s` (update sends %v); "+
							"name each one exactly as the API spells it:\n%s", strings.Join(page, " "), k, keys, out)
					}
				}
			}
		})
	}

	for _, page := range [][]string{
		{"checkout", "hub-prices"},
		{"checkout", "hub-prices", "list"},
		{"checkout", "hub-prices", "update"},
		{"checkout", "hub-products"},
		{"checkout", "hub-products", "list"},
		{"checkout", "hub-products", "attach"},
		{"checkout", "hub-products", "update"},
		{"checkout", "hub-products", "detach"},
	} {
		if out := helpOutput(t, page...); strings.Contains(strings.ToLower(out), "visibility") {
			t.Errorf("`mio %s --help` says \"visibility\"; the attribute and the flag are both `visible` (MIO-4156):\n%s",
				strings.Join(page, " "), out)
		}
	}
}

var jqExample = regexp.MustCompile(`--jq '([^']+)'`)

func helpJQExamples(t *testing.T, page, help string) []string {
	t.Helper()
	var progs []string
	for _, line := range strings.Split(help, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "--jq") || strings.HasPrefix(line, "--jq string ") {
			continue
		}
		found := jqExample.FindAllStringSubmatch(line, -1)
		if n := strings.Count(line, "--jq"); n != len(found) {
			t.Errorf("`%s` line %q passes --jq in a form this guard cannot run; write the program single-quoted: --jq '<program>'", page, line)
		}
		for _, m := range found {
			progs = append(progs, m[1])
		}
	}
	return progs
}

func jqFieldReads(t *testing.T, program string) []string {
	t.Helper()
	q, err := gojq.Parse(program)
	if err != nil {
		t.Fatalf("--jq '%s' does not parse: %v", program, err)
	}
	literal := func(s *gojq.String) (string, bool) {
		if s == nil || s.Queries != nil {
			return "", false
		}
		return s.Str, true
	}
	seen := map[string]bool{}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
			return
		case reflect.Slice:
			for i := range v.Len() {
				walk(v.Index(i))
			}
			return
		case reflect.Struct:
		default:
			return
		}
		switch n := v.Addr().Interface().(type) {
		case *gojq.Index:
			if n.Name != "" {
				seen[n.Name] = true
			}
			if k, ok := literal(n.Str); ok {
				seen[k] = true
			}
			if s := n.Start; s != nil && !n.IsSlice && s.Left == nil && s.Term != nil &&
				s.Term.Type == gojq.TermTypeString && len(s.Term.SuffixList) == 0 {
				if k, ok := literal(s.Term.Str); ok {
					seen[k] = true
				}
			}
		case *gojq.ObjectKeyVal:
			if n.Val == nil {
				if n.Key != "" && !strings.HasPrefix(n.Key, "$") {
					seen[n.Key] = true
				}
				if k, ok := literal(n.KeyString); ok {
					seen[k] = true
				}
			}
		case *gojq.PatternObject:
			if k := strings.TrimPrefix(n.Key, "$"); k != "" {
				seen[k] = true
			}
			if k, ok := literal(n.KeyString); ok {
				seen[k] = true
			}
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				walk(v.Field(i))
			}
		}
	}
	walk(reflect.ValueOf(q))
	reads := make([]string, 0, len(seen))
	for k := range seen {
		reads = append(reads, k)
	}
	sort.Strings(reads)
	return reads
}

func TestJQFieldReads_SeesEachLiteralRead(t *testing.T) {
	program := `. as {p: $x, $q} | map({a, "b", out: .c, $x}) | .[0].d | ."e" | .["f"] | select(.g) | "\(.h)"`
	want := []string{"a", "b", "c", "d", "e", "f", "g", "h", "p", "q"}
	if got := jqFieldReads(t, program); !reflect.DeepEqual(got, want) {
		t.Errorf("jqFieldReads(%s) = %v, want %v", program, got, want)
	}
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	case string:
		return "a string"
	case bool:
		return "a bool"
	default:
		return "a number"
	}
}

func containsNull(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case []any:
		for _, e := range x {
			if containsNull(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range x {
			if containsNull(e) {
				return true
			}
		}
	}
	return false
}

func yieldsEachRow(out any, wire []wireRow, editable []string) string {
	arr, ok := out.([]any)
	if !ok || len(arr) != len(wire) {
		return fmt.Sprintf("yields %s, not one object per row (%d rows)", jsonKind(out), len(wire))
	}
	for i, e := range arr {
		obj, ok := e.(map[string]any)
		if !ok {
			return fmt.Sprintf("row %d yields %s, not an object", i, jsonKind(e))
		}
		for _, k := range editable {
			if v, ok := obj[k]; !ok || !reflect.DeepEqual(v, wire[i].Attributes[k]) {
				return fmt.Sprintf("row %d: editable attribute %q = %v (present=%v), want %v", i, k, v, ok, wire[i].Attributes[k])
			}
		}
	}
	return ""
}

func TestCheckoutHubDisplayListExample_SelectsEachEditableAttribute(t *testing.T) {
	cases := []struct {
		list       hubDisplayListCase
		updateCmd  *cobra.Command
		updatePath []string
	}{
		{hubDisplayListCases[0], checkoutHubPricesUpdateCmd, []string{"checkout", "hub-prices", "update", "hprd_1"}},
		{hubDisplayListCases[1], checkoutHubProductsUpdateCmd, []string{"checkout", "hub-products", "update", "hpd_1"}},
	}
	for _, tc := range cases {
		t.Run(tc.list.name, func(t *testing.T) {
			keys := updateWireAttributes(t, tc.updateCmd, tc.updatePath)
			page := "mio " + strings.Join(tc.list.args, " ") + " --help"
			examples := helpJQExamples(t, page, helpOutput(t, tc.list.args...))
			if len(examples) == 0 {
				t.Fatalf("`%s` has no --jq example; show how to select %v", page, keys)
			}

			wire := tc.list.rows(t)
			rowKeys := map[string]bool{"id": true, "type": true}
			for _, r := range wire {
				for k := range r.Attributes {
					rowKeys[k] = true
				}
			}
			known := sortedKeysOfBool(rowKeys)

			counts := []int{len(wire), 1, 0}
			srvURL := make([]string, len(counts))
			for i, n := range counts {
				data, err := json.Marshal(wire[:n])
				if err != nil {
					t.Fatal(err)
				}
				srv, _, _, _ := captureCommerceRequest(t, http.StatusOK,
					fmt.Sprintf(`{"data":%s,"meta":{"count":%d}}`, data, n))
				srvURL[i] = srv.URL
			}

			var shortfalls []string
			for _, prog := range examples {
				ex := fmt.Sprintf("`%s` example --jq '%s'", page, prog)

				for _, k := range jqFieldReads(t, prog) {
					if !rowKeys[k] {
						t.Errorf("%s reads %q, a key no row has (rows have %v); gojq answers null for it, silently (MIO-4156)", ex, k, known)
					}
				}

				outs := make([]any, len(counts))
				for i := range counts {
					raw := tc.list.run(t, srvURL[i], "--jq", prog)
					if err := json.Unmarshal([]byte(raw), &outs[i]); err != nil {
						t.Fatalf("%s against %d rows did not print JSON: %v; stdout=%q", ex, counts[i], err, raw)
					}
				}

				for i := 1; i < len(counts); i++ {
					if jsonKind(outs[i]) != jsonKind(outs[0]) {
						t.Errorf("%s yields %s for %d rows, %s for %d row and %s for %d rows; its shape must not depend "+
							"on how many rows the hub has (wrap a per-row program in map(...))",
							ex, jsonKind(outs[0]), counts[0], jsonKind(outs[1]), counts[1], jsonKind(outs[2]), counts[2])
						break
					}
				}

				if containsNull(outs[0]) {
					t.Errorf("%s yields a null against rows that hold none: it reads something the rows do not have; output=%v", ex, outs[0])
				}
				objs := []any{outs[0]}
				if arr, ok := outs[0].([]any); ok {
					objs = arr
				}
				foreign := map[string]bool{}
				for _, o := range objs {
					obj, _ := o.(map[string]any)
					for k := range obj {
						if !rowKeys[k] {
							foreign[k] = true
						}
					}
				}
				if len(foreign) > 0 {
					t.Errorf("%s yields key(s) %v, which no row has (rows have %v); show each attribute under its own name",
						ex, sortedKeysOfBool(foreign), known)
				}

				if why := yieldsEachRow(outs[0], wire, keys); why != "" {
					shortfalls = append(shortfalls, fmt.Sprintf("--jq '%s': %s", prog, why))
				}
			}
			if len(shortfalls) == len(examples) {
				t.Errorf("no --jq example on `%s` yields one object per row carrying every editable attribute %v:\n  %s",
					page, keys, strings.Join(shortfalls, "\n  "))
			}
		})
	}
}

func sortedKeysOfBool(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
