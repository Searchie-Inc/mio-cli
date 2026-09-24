package docexamples

import (
	"reflect"
	"strings"
	"testing"
)

// selView is a compact, comparable view of one extracted selector.
type selView struct {
	Line  int
	Path  string
	Steps string // "k" per field, "[]" per index step, joined by "·"
}

func viewSelectors(sels []Selector) []selView {
	out := make([]selView, 0, len(sels))
	for _, s := range sels {
		parts := make([]string, 0, len(s.Steps))
		for _, st := range s.Steps {
			if st.Index {
				parts = append(parts, "[]")
			} else {
				parts = append(parts, st.Key)
			}
		}
		out = append(out, selView{s.Line, s.Path, strings.Join(parts, "·")})
	}
	return out
}

// TestSelectors_Shapes pins what counts as a selector. Each case is a shape
// that appears on a doc surface or help page: the MIO-3413 prose form, a code
// span, a --jq program, a capture, raw-envelope paths, and the prose that must
// NOT read as a path (abbreviations, file names, a dotted body path with no
// leading dot).
func TestSelectors_Shapes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []selView
	}{
		{
			name: "MIO-3413 prose",
			text: "the .attributes.contact_id field from 'mio contacts', NOT its .id (that is",
			want: []selView{{1, ".attributes.contact_id", "attributes·contact_id"}, {1, ".id", "id"}},
		},
		{
			name: "code span and a sentence-ending dot",
			text: "flattened `.contact_id` when flattened. Then .id.",
			want: []selView{{1, ".contact_id", "contact_id"}, {1, ".id", "id"}},
		},
		{
			name: "--jq program with iteration and a pipe",
			text: `mio contacts list --jq '.[].contact_id | select(.status == "x")'`,
			want: []selView{{1, ".[].contact_id", "[]·contact_id"}, {1, ".status", "status"}},
		},
		{
			name: "raw envelope paths",
			text: "under --raw it is .data.attributes.contact_id, or .data[0].attributes.x",
			want: []selView{
				{1, ".data.attributes.contact_id", "data·attributes·contact_id"},
				{1, ".data[0].attributes.x", "data·[]·attributes·x"},
			},
		},
		{
			name: "quoted keys",
			text: `.attributes["contact_id"] and ."attributes".x`,
			want: []selView{
				{1, `.attributes["contact_id"]`, "attributes·contact_id"},
				{1, `."attributes".x`, "attributes·x"},
			},
		},
		{
			name: "capture on the second line",
			text: "Capture it with:\n  CID=$(mio contacts retrieve <id> -o plain --jq .contact_id)",
			want: []selView{{2, ".contact_id", "contact_id"}},
		},
		{
			name: "not paths",
			text: "e.g. config.toml, ./mio, data.attributes.media_id, a.b.c, 1.5, ...",
			want: []selView{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := viewSelectors(Selectors("f.md", 1, tc.text))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Selectors(%q)\n got %+v\nwant %+v", tc.text, got, tc.want)
			}
		})
	}
}

// TestSelectors_LineNumbersAndReads: line numbers are offset by firstLine, and
// Reads sees a field at any step but never an index step.
func TestSelectors_LineNumbersAndReads(t *testing.T) {
	sels := Selectors("f.go", 40, "first\nsecond .attributes.contact_id\nthird .[0]")
	if len(sels) != 2 || sels[0].Line != 41 || sels[1].Line != 42 {
		t.Fatalf("got %+v, want selectors on lines 41 and 42", viewSelectors(sels))
	}
	if !sels[0].Reads("attributes") || !sels[0].Reads("contact_id") || sels[0].Reads("id") {
		t.Errorf("Reads on %q is wrong", sels[0].Path)
	}
	if sels[1].Reads("") {
		t.Errorf("an index step must not count as a read of the empty key")
	}
	if sels[0].LineText != "second .attributes.contact_id" {
		t.Errorf("LineText = %q", sels[0].LineText)
	}
}
