package cmd

// checkout_hub_display_visible_test.go — MIO-4156.
//
// QA reported that `mio checkout hub-prices list` flattened the API's `visible`
// attribute under the wrong key. It does not: the report's own filter asked for
// `.visibility`, a key no hub display row has, and gojq answers null for any
// missing key. What WAS wrong is the help, which called the attribute
// "visibility" while the flag and the wire both say `visible`.
//
// Three guards follow, each with an oracle the implementation cannot fake:
//
//   - the list render guard feeds the real command the exact attributes the API
//     returns and reads the RENDERED stdout (json, table, plain, --jq) — not the
//     Flatten function — so a rename, drop or rewrite anywhere between the HTTP
//     body and stdout fails by attribute name;
//   - the help guard derives the editable attributes from the WIRE (the body the
//     update command actually sends with every one of its flags set) and requires
//     the help to name each one verbatim, so neither a reworded help nor a new
//     flag documented under some other word can stay green;
//   - the list example guard RUNS each list command's documented --jq example
//     against the API's shape and requires every one of those editable
//     attributes back with its value, so the example cannot vanish or drift
//     to a key the rows do not have.

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// hubDisplayListCase is one list command plus the collection its route returns.
// data is the JSON:API `data` array verbatim; the expected rendered values are
// decoded from the same bytes, so the fixture is the only source of truth.
type hubDisplayListCase struct {
	name string
	args []string
	data string
}

// hubDisplayListCases: row 0 of hub-prices is the MIO-4156 report's `--raw`
// attributes (`visible: true`); every case carries a second row with
// `visible: false`, so a renderer that fakes the value (a constant, a default)
// cannot satisfy both rows.
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

// wireRow is one resource as the API sent it.
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

// cellText is how the table and plain formatters print a JSON scalar.
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

// requireRenderedRow checks one rendered row against the row the API sent:
// every wire attribute must be present under its OWN name with its own value,
// and nothing but id/type may be added. want/got are compared as text so the
// same check serves json (via cellText), table and plain output.
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

// TestCheckoutHubDisplayList_RendersVisibleUnderItsOwnName is the MIO-4156
// premise check turned into a guard: the attribute the API calls `visible`
// reaches stdout as `visible`, with its value, in every output mode.
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
					// JSON keeps types: a string "true" must not pass for the bool.
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

			// The report's own filter shape, asking for the key the API sends.
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

// updateWireAttributes runs `<path> <display_id>` with EVERY local flag of cmd
// set and returns the attribute keys the request body carried — the editable
// attributes as the API sees them, not as anyone describes them.
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

// TestCheckoutHubDisplayHelp_NamesEachEditableWireAttribute: the help that says
// what is editable must name each editable attribute exactly as the API spells
// it (backticked), and no hub display help page may use "visibility" — the word
// that sent the MIO-4156 reporter to a key that does not exist.
func TestCheckoutHubDisplayHelp_NamesEachEditableWireAttribute(t *testing.T) {
	cases := []struct {
		name  string
		cmd   *cobra.Command
		path  []string
		pages [][]string // help pages that describe what update can change
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

// jqExample pulls the --jq program out of a rendered help page's examples.
var jqExample = regexp.MustCompile(`--jq '([^']+)'`)

// TestCheckoutHubDisplayListExample_SelectsEachEditableAttribute: each list
// command's help carries a --jq example, and running THAT example against the
// API's shape must yield every editable attribute (taken from the update wire,
// as above) with the value the API sent. An example that is deleted, or that
// selects a key rows do not have (`.visibility` answers null), fails here.
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
			m := jqExample.FindStringSubmatch(helpOutput(t, tc.list.args...))
			if m == nil {
				t.Fatalf("`%s` has no --jq example; show how to select %v", page, keys)
			}

			wire := tc.list.rows(t)
			srv, _, _, _ := captureCommerceRequest(t, http.StatusOK, `{"data":`+tc.list.data+`,"meta":{"count":2}}`)
			var got []map[string]any
			out := tc.list.run(t, srv.URL, "--jq", m[1])
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("`%s` example --jq '%s' did not yield one object per row: %v; stdout=%q", page, m[1], err, out)
			}
			if len(got) != len(wire) {
				t.Fatalf("`%s` example --jq '%s' yielded %d objects for %d rows", page, m[1], len(got), len(wire))
			}
			for i, obj := range got {
				for _, k := range keys {
					if v, ok := obj[k]; !ok || !reflect.DeepEqual(v, wire[i].Attributes[k]) {
						t.Errorf("`%s` example --jq '%s' row %d: editable attribute %q = %v (present=%v), want %v",
							page, m[1], i, k, v, ok, wire[i].Attributes[k])
					}
				}
			}
		})
	}
}
