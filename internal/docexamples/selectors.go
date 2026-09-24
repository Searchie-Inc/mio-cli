package docexamples

import (
	"regexp"
	"strings"
)

// Selector is one jq-style path (`.contact_id`, `.data.attributes.contact_id`,
// `.[0].id`) written anywhere in a text: prose, a code span, a help string or
// a --jq program. Documentation that names a field as `.x` is telling an agent
// what to select, so every such path is read, not only the ones inside a --jq
// argument (MIO-3413: help prose named `.attributes.contact_id` as the field
// to select, and it yields null in every output mode).
type Selector struct {
	File     string
	Line     int    // 1-based line of the path's first character
	Path     string // the path exactly as written
	Steps    []Step // the path, one step per field or index
	LineText string // the whole line the path is on, for context
}

// Step is one step of a Selector: a field read (Key) or an index/iteration
// (`[]`, `[0]`), which leaves Key empty.
type Step struct {
	Key   string
	Index bool
}

// Reads reports whether the selector reads key as a field at any step.
func (s Selector) Reads(key string) bool {
	for _, st := range s.Steps {
		if !st.Index && st.Key == key {
			return true
		}
	}
	return false
}

// selectorRE matches a path that starts at a `.` which is not itself the end of
// a longer expression: the byte before it (group 1) must not be a word
// character, a closing bracket or a dot, so `e.g.`, `config.toml`,
// `data.attributes.x` (no leading dot) and the `.x` inside `.a.x` never start
// a path of their own. The first step is `.name`, `."name"` or `.[…]`; later
// steps are `.name`, `."name"`, `[…]` or `.[…]`.
var selectorRE = regexp.MustCompile(
	`(^|[^A-Za-z0-9_\])}.])` +
		`(\.(?:[A-Za-z_][A-Za-z0-9_]*|"[^"\n]*"|\[(?:[0-9]*|"[^"\n]*")\])` +
		`(?:\.?\[(?:[0-9]*|"[^"\n]*")\]|\.[A-Za-z_][A-Za-z0-9_]*|\."[^"\n]*")*)`)

// stepRE splits a matched path into its steps.
var stepRE = regexp.MustCompile(`\.?\[([0-9]*|"[^"\n]*")\]|\.([A-Za-z_][A-Za-z0-9_]*)|\."([^"\n]*)"`)

// Selectors returns every jq-style path in text, whose first line is line
// firstLine of file. It reads the whole text — prose included — because a
// field named in prose is a selector an agent will type.
func Selectors(file string, firstLine int, text string) []Selector {
	var out []Selector
	for i, line := range strings.Split(text, "\n") {
		for _, m := range selectorRE.FindAllStringSubmatchIndex(line, -1) {
			path := line[m[4]:m[5]]
			out = append(out, Selector{
				File:     file,
				Line:     firstLine + i,
				Path:     path,
				Steps:    steps(path),
				LineText: line,
			})
		}
	}
	return out
}

func steps(path string) []Step {
	var out []Step
	for _, m := range stepRE.FindAllStringSubmatch(path, -1) {
		switch {
		case m[2] != "":
			out = append(out, Step{Key: m[2]})
		case strings.HasPrefix(m[0], `."`):
			out = append(out, Step{Key: m[3]})
		case strings.HasPrefix(m[1], `"`):
			out = append(out, Step{Key: strings.Trim(m[1], `"`)})
		default:
			out = append(out, Step{Index: true})
		}
	}
	return out
}
