package docexamples

import (
	"regexp"
	"strings"
)

type Selector struct {
	File     string
	Line     int
	Path     string
	Steps    []Step
	LineText string
}

type Step struct {
	Key   string
	Index bool
}

func (s Selector) Reads(key string) bool {
	for _, st := range s.Steps {
		if !st.Index && st.Key == key {
			return true
		}
	}
	return false
}

var selectorRE = regexp.MustCompile(
	`(^|[^A-Za-z0-9_\])}.])` +
		`(\.(?:[A-Za-z_][A-Za-z0-9_]*|"[^"\n]*"|\[(?:[0-9]*|"[^"\n]*")\])` +
		`(?:\.?\[(?:[0-9]*|"[^"\n]*")\]|\.[A-Za-z_][A-Za-z0-9_]*|\."[^"\n]*")*)`)

var stepRE = regexp.MustCompile(`\.?\[([0-9]*|"[^"\n]*")\]|\.([A-Za-z_][A-Za-z0-9_]*)|\."([^"\n]*)"`)

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
