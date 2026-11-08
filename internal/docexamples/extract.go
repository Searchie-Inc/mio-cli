package docexamples

import (
	"regexp"
	"sort"
	"strings"
)

type Invocation struct {
	File    string
	Line    int
	EndLine int
	Args    []string
	Elided  bool

	pos int
}

func (i Invocation) Text() string {
	parts := make([]string, 0, len(i.Args)+1)
	parts = append(parts, "mio")
	for _, a := range i.Args {
		if a == "" || strings.ContainsAny(a, " \t\"'") {
			a = "'" + a + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

type Mention struct {
	File   string
	Line   int
	Text   string
	Output bool
}

type Result struct {
	Invocations []Invocation
	Uncovered   []Mention
}

var mentionRE = regexp.MustCompile("(?:^|[\\s;&|(`])((?:\\./)?mio)[ \\t]+[a-z-]")

func FromMarkdown(file, content string) Result {
	lines := strings.Split(content, "\n")
	var res Result
	for i := 0; i < len(lines); i++ {
		opener, depth := stripQuote(lines[i], -1)
		indent, marker, ok := fenceOpen(opener)
		if !ok {
			continue
		}
		start := i + 1
		end := start
		closed := false
		for end < len(lines) {
			l, d := stripQuote(lines[end], depth)
			if d < depth {
				break
			}
			if fenceClose(l, marker) {
				closed = true
				break
			}
			end++
		}
		body := make([]string, 0, end-start)
		for _, l := range lines[start:end] {
			l, _ = stripQuote(l, depth)
			body = append(body, stripIndent(l, indent))
		}
		sub := FromScript(file, start+1, strings.Join(body, "\n"))
		res.Invocations = append(res.Invocations, sub.Invocations...)
		res.Uncovered = append(res.Uncovered, sub.Uncovered...)
		i = end
		if !closed {
			i = end - 1
		}
	}
	return res
}

func stripQuote(line string, max int) (string, int) {
	n := 0
	for max < 0 || n < max {
		t := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(t, ">") {
			break
		}
		line = strings.TrimPrefix(t[1:], " ")
		n++
	}
	return line, n
}

func FromScript(file string, firstLine int, script string) Result {
	code, output := consoleCode(strings.Split(script, "\n"))
	script = strings.Join(code, "\n")
	p := &parser{src: script, file: file, firstLine: firstLine, output: output}
	p.lineStarts = []int{0}
	for i := 0; i < len(script); i++ {
		if script[i] == '\n' {
			p.lineStarts = append(p.lineStarts, i+1)
		}
	}
	p.parseList(0)
	sort.SliceStable(p.invocations, func(a, b int) bool { return p.invocations[a].pos < p.invocations[b].pos })
	return Result{Invocations: p.invocations, Uncovered: p.uncovered()}
}

func fenceOpen(line string) (indent int, marker string, ok bool) {
	t := strings.TrimLeft(line, " \t")
	indent = len(line) - len(t)
	for _, ch := range []string{"`", "~"} {
		n := 0
		for n < len(t) && string(t[n]) == ch {
			n++
		}
		if n >= 3 {
			if ch == "`" && strings.Contains(t[n:], "`") {
				return 0, "", false
			}
			return indent, strings.Repeat(ch, n), true
		}
	}
	return 0, "", false
}

func fenceClose(line, marker string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, marker) {
		return false
	}
	return strings.Trim(t, marker[:1]) == ""
}

func stripIndent(line string, indent int) string {
	i := 0
	for i < indent && i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return line[i:]
}

func consoleCode(body []string) (code, output []string) {
	isConsole := false
	for _, l := range body {
		if strings.HasPrefix(strings.TrimLeft(l, " \t"), "$ ") {
			isConsole = true
			break
		}
	}
	if !isConsole {
		return body, nil
	}
	code = make([]string, len(body))
	output = make([]string, len(body))
	cont := false
	for i, l := range body {
		t := strings.TrimLeft(l, " \t")
		switch {
		case strings.HasPrefix(t, "$ "):
			code[i] = t[2:]
		case cont:
			code[i] = l
		default:
			output[i] = l
		}
		cont = code[i] != "" && strings.HasSuffix(strings.TrimRight(code[i], " \t"), "\\")
	}
	return code, output
}

type word struct {
	text   string
	quoted bool
	pos    int
	end    int
}

type heredoc struct {
	delim string
	dash  bool
}

type parser struct {
	src        string
	pos        int
	file       string
	firstLine  int
	lineStarts []int
	output     []string

	pending     []heredoc
	nonCode     [][2]int
	invocations []Invocation
}

func (p *parser) peek(off int) byte {
	if p.pos+off < len(p.src) {
		return p.src[p.pos+off]
	}
	return 0
}

func (p *parser) lineOf(pos int) int {
	i := sort.Search(len(p.lineStarts), func(i int) bool { return p.lineStarts[i] > pos }) - 1
	return p.firstLine + i
}

func (p *parser) parseList(stop byte) {
	var cur []word
	flush := func() {
		if len(cur) > 0 {
			p.emit(cur)
		}
		cur = nil
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case stop != 0 && c == stop:
			flush()
			return
		case c == ' ' || c == '\t' || c == '\r':
			p.pos++
		case c == '\\' && p.peek(1) == '\n':
			p.pos += 2
		case c == '\n':
			flush()
			p.pos++
			p.skipHeredocBodies()
		case c == '#':
			start := p.pos
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
			p.nonCode = append(p.nonCode, [2]int{start, p.pos})
		case c == ';' || c == '&' || c == '|':
			flush()
			p.pos++
		case c == '(':
			flush()
			p.pos++
			p.parseList(')')
			if p.pos < len(p.src) {
				p.pos++
			}
		case c == ')':
			flush()
			p.pos++
		case (c == '<' || c == '>') && !p.atPlaceholder():
			p.redirect()
		default:
			w := p.readWord(stop)
			if isAllDigits(w.text) && !w.quoted && (p.peek(0) == '<' || p.peek(0) == '>') {
				p.redirect()
				continue
			}
			if w.end > w.pos {
				cur = append(cur, w)
			} else {
				p.pos++
			}
		}
	}
	flush()
}

var placeholderRE = regexp.MustCompile(`^<[A-Za-z0-9_][A-Za-z0-9_.:/|-]*>`)

func (p *parser) atPlaceholder() bool {
	if p.src[p.pos] != '<' {
		return false
	}
	rest := p.src[p.pos:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return placeholderRE.MatchString(rest)
}

func (p *parser) readWord(stop byte) word {
	w := word{pos: p.pos}
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if stop != 0 && c == stop {
			break
		}
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' ||
			c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			w.end = p.pos
			w.text = b.String()
			return w
		case c == '<' && p.atPlaceholder():
			m := placeholderRE.FindString(p.src[p.pos:])
			b.WriteString(m)
			p.pos += len(m)
		case c == '<' || c == '>':
			w.end = p.pos
			w.text = b.String()
			return w
		case c == '\\' && p.peek(1) == '\n':
			p.pos += 2
		case c == '\\':
			if p.pos+1 < len(p.src) {
				b.WriteByte(p.src[p.pos+1])
			}
			p.pos += 2
		case c == '\'':
			w.quoted = true
			end := strings.IndexByte(p.src[p.pos+1:], '\'')
			if end < 0 {
				b.WriteString(p.src[p.pos+1:])
				p.pos = len(p.src)
			} else {
				b.WriteString(p.src[p.pos+1 : p.pos+1+end])
				p.pos += end + 2
			}
		case c == '"':
			w.quoted = true
			p.pos++
			p.readDouble(&b)
		case c == '$' && p.peek(1) == '(' && p.peek(2) == '(':
			b.WriteString(p.skipArithmetic())
		case c == '$' && p.peek(1) == '(':
			p.pos += 2
			p.parseList(')')
			if p.pos < len(p.src) {
				p.pos++
			}
			b.WriteString("$(…)")
		case c == '`':
			p.pos++
			p.parseList('`')
			if p.pos < len(p.src) {
				p.pos++
			}
			b.WriteString("$(…)")
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	w.end = p.pos
	w.text = b.String()
	return w
}

func (p *parser) readDouble(b *strings.Builder) {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '"':
			p.pos++
			return
		case c == '\\' && p.pos+1 < len(p.src):
			n := p.src[p.pos+1]
			switch n {
			case '\n':
			case '"', '\\', '$', '`':
				b.WriteByte(n)
			default:
				b.WriteByte(c)
				b.WriteByte(n)
			}
			p.pos += 2
		case c == '$' && p.peek(1) == '(' && p.peek(2) == '(':
			b.WriteString(p.skipArithmetic())
		case c == '$' && p.peek(1) == '(':
			p.pos += 2
			p.parseList(')')
			if p.pos < len(p.src) {
				p.pos++
			}
			b.WriteString("$(…)")
		case c == '`':
			p.pos++
			p.parseList('`')
			if p.pos < len(p.src) {
				p.pos++
			}
			b.WriteString("$(…)")
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
}

func (p *parser) skipArithmetic() string {
	start := p.pos
	p.pos += 3
	depth := 2
	for p.pos < len(p.src) && depth > 0 {
		switch p.src[p.pos] {
		case '(':
			depth++
		case ')':
			depth--
		}
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) redirect() {
	rest := p.src[p.pos:]
	switch {
	case strings.HasPrefix(rest, "<<<"):
		p.pos += 3
	case strings.HasPrefix(rest, "<<-"), strings.HasPrefix(rest, "<<"):
		dash := strings.HasPrefix(rest, "<<-")
		if dash {
			p.pos += 3
		} else {
			p.pos += 2
		}
		p.skipBlanks()
		d := p.readWord(0)
		p.pending = append(p.pending, heredoc{delim: d.text, dash: dash})
		return
	default:
		for p.pos < len(p.src) && strings.IndexByte("<>&|", p.src[p.pos]) >= 0 {
			p.pos++
		}
	}
	p.skipBlanks()
	p.readWord(0)
}

func (p *parser) skipBlanks() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) skipHeredocBodies() {
	for _, h := range p.pending {
		start := p.pos
		for p.pos < len(p.src) {
			end := strings.IndexByte(p.src[p.pos:], '\n')
			line := p.src[p.pos:]
			next := len(p.src)
			if end >= 0 {
				line = p.src[p.pos : p.pos+end]
				next = p.pos + end + 1
			}
			if h.dash {
				line = strings.TrimLeft(line, "\t")
			}
			p.pos = next
			if line == h.delim {
				break
			}
		}
		p.nonCode = append(p.nonCode, [2]int{start, p.pos})
	}
	p.pending = nil
}

var assignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"do": true, "done": true, "while": true, "until": true,
	"!": true, "{": true, "}": true,
	"time": true, "exec": true, "command": true, "env": true, "sudo": true, "nohup": true,
}

func isMio(w word) bool {
	return w.text == "mio" || (!w.quoted && strings.HasSuffix(w.text, "/mio"))
}

func (p *parser) emit(words []word) {
	i := 0
	for i < len(words) && (assignRE.MatchString(words[i].text) || (!words[i].quoted && shellKeywords[words[i].text])) {
		i++
	}
	if i < len(words) && words[i].text == "xargs" {
		for j := i + 1; j < len(words); j++ {
			if isMio(words[j]) {
				i = j
				break
			}
		}
	}
	if i >= len(words) || !isMio(words[i]) {
		return
	}
	inv := Invocation{
		File:    p.file,
		Line:    p.lineOf(words[i].pos),
		EndLine: p.lineOf(words[len(words)-1].end - 1),
		pos:     words[i].pos,
	}
	inGroup := false
	for _, w := range words[i+1:] {
		if !w.quoted && (w.text == "…" || w.text == "...") {
			inv.Elided = true
			continue
		}
		if !w.quoted && strings.HasPrefix(w.text, "[") {
			inGroup = true
		}
		if inGroup {
			if !w.quoted && strings.HasSuffix(w.text, "]") {
				inGroup = false
			}
			continue
		}
		inv.Args = append(inv.Args, w.text)
	}
	p.invocations = append(p.invocations, inv)
}

func (p *parser) uncovered() []Mention {
	heads := map[int]bool{}
	for _, inv := range p.invocations {
		heads[inv.pos] = true
	}
	var out []Mention
	for idx, start := range p.lineStarts {
		if idx < len(p.output) && mentionRE.MatchString(p.output[idx]) {
			out = append(out, Mention{File: p.file, Line: p.firstLine + idx, Text: strings.TrimSpace(p.output[idx]), Output: true})
			continue
		}
		end := len(p.src)
		if idx+1 < len(p.lineStarts) {
			end = p.lineStarts[idx+1] - 1
		}
		for _, m := range mentionRE.FindAllStringSubmatchIndex(p.src[start:end], -1) {
			at := start + m[2]
			if heads[at] || p.inNonCode(at) {
				continue
			}
			out = append(out, Mention{File: p.file, Line: p.firstLine + idx, Text: strings.TrimSpace(p.src[start:end])})
			break
		}
	}
	return out
}

func (p *parser) inNonCode(i int) bool {
	for _, r := range p.nonCode {
		if i >= r[0] && i < r[1] {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
