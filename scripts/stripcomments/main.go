package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

var (
	directive   = regexp.MustCompile(`^//(go:[a-z]+|line |export |\s?\+build\s)`)
	nolint      = regexp.MustCompile(`^//\s?(nolint:[A-Za-z0-9_-]+(?:,[A-Za-z0-9_-]+)*)(?:\s|$)`)
	vendorLine  = regexp.MustCompile(`^//\s?vendor: \S.* https?://[^\s#]\S*$`)
	licenseWord = regexp.MustCompile(`Copyright|SPDX-License-Identifier`)
	salvage     = regexp.MustCompile(`(?i)https?://|\b(workaround|work-around|work around|hack|because)\b|\b(stripe|github|goreleaser|homebrew|keychain|wincred|kwallet|secret service|d-bus|dbus|windows|macos|darwin|cobra|pflag|jira|mjml|cloudflare|apple|aws|s3|vault|npm|node|powershell|curl)\b`)
	posType     = reflect.TypeOf(token.NoPos)
)

type finding struct {
	Path string
	Line int
	Text string
}

func isGenerated(path string, src []byte) bool {
	_, f, err := parse(path, src, parser.ParseComments|parser.PackageClauseOnly)
	return err == nil && ast.IsGenerated(f)
}

func parse(path string, src []byte, mode parser.Mode) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, mode)
	return fset, f, err
}

func licenseGroup(f *ast.File) *ast.CommentGroup {
	if len(f.Comments) == 0 {
		return nil
	}
	g := f.Comments[0]
	if g.End() < f.Package && licenseWord.MatchString(g.Text()) {
		return g
	}
	return nil
}

func keepText(c *ast.Comment) (string, bool) {
	t := c.Text
	if !strings.HasPrefix(t, "//") {
		return "", false
	}
	if directive.MatchString(t) {
		return t, true
	}
	if m := nolint.FindStringSubmatch(t); m != nil {
		return "//" + m[1], true
	}
	if vendorLine.MatchString(t) {
		return t, true
	}
	return "", false
}

type span struct {
	start, end int
	repl       string
}

func analyze(path string, src []byte) (kept, dropped []finding, spans []span, err error) {
	fset, f, err := parse(path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, nil, err
	}
	lic := licenseGroup(f)
	for _, g := range f.Comments {
		if g == lic {
			continue
		}
		for _, c := range g.List {
			pos := fset.Position(c.Pos())
			start, end := pos.Offset, fset.Position(c.End()).Offset
			if text, ok := keepText(c); ok {
				kept = append(kept, finding{path, pos.Line, text})
				if text != c.Text {
					dropped = append(dropped, finding{path, pos.Line, "(trailing text after nolint rule)"})
					spans = append(spans, span{start, end, text})
				}
				continue
			}
			dropped = append(dropped, finding{path, pos.Line, c.Text})
			spans = append(spans, span{start, end, ""})
		}
	}
	return kept, dropped, spans, nil
}

func cut(src []byte, spans []span) []byte {
	out := append([]byte(nil), src...)
	for i := len(spans) - 1; i >= 0; i-- {
		s, e, repl := spans[i].start, spans[i].end, spans[i].repl
		if repl != "" {
			out = append(out[:s], append([]byte(repl), out[e:]...)...)
			continue
		}
		ls := bytes.LastIndexByte(out[:s], '\n') + 1
		le := len(out)
		if j := bytes.IndexByte(out[e:], '\n'); j >= 0 {
			le = e + j + 1
		}
		if len(bytes.TrimSpace(out[ls:s])) == 0 && len(bytes.TrimSpace(out[e:le])) == 0 {
			out = append(out[:ls], out[le:]...)
			continue
		}
		sep := " "
		if bytes.IndexByte(src[spans[i].start:spans[i].end], '\n') >= 0 {
			sep = "\n"
		}
		out = append(out[:s], append([]byte(sep), out[e:]...)...)
	}
	return out
}

func strip(path string, src []byte) ([]byte, bool, error) {
	if isGenerated(path, src) {
		return src, false, nil
	}
	_, dropped, spans, err := analyze(path, src)
	if err != nil {
		return nil, false, err
	}
	if len(dropped) == 0 {
		return src, false, nil
	}
	if bytes.Contains(src, []byte("import \"C\"")) {
		return nil, false, fmt.Errorf("%s: cgo preamble comments are code; strip by hand", path)
	}
	out, err := format.Source(cut(src, spans))
	if err != nil {
		return nil, false, err
	}
	before, err := astDigest(path, src)
	if err != nil {
		return nil, false, err
	}
	after, err := astDigest(path, out)
	if err != nil {
		return nil, false, err
	}
	if before != after {
		return nil, false, fmt.Errorf("%s: stripping would change the syntax tree; strip by hand", path)
	}
	return out, !bytes.Equal(out, src), nil
}

func violations(path string, src []byte) ([]finding, error) {
	if isGenerated(path, src) {
		return nil, nil
	}
	_, dropped, _, err := analyze(path, src)
	if err != nil {
		return nil, err
	}
	return dropped, nil
}

func goFiles(roots []string) ([]string, error) {
	var out []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				n := d.Name()
				if p != root && (n == ".git" || n == "vendor" || n == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".go") {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func astDigest(path string, src []byte) (string, error) {
	fset, f, err := parse(path, src, parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	filter := func(name string, v reflect.Value) bool {
		return v.Type() != posType && ast.NotNilFilter(name, v)
	}
	if err := ast.Fprint(&buf, fset, f, filter); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func gitShow(base, path string) ([]byte, bool) {
	out, err := exec.Command("git", "show", base+":"+filepath.ToSlash(path)).Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

func proof(files []string, base string) (int, error) {
	bad := 0
	if err := exec.Command("git", "rev-parse", "--verify", "--quiet", base+"^{commit}").Run(); err != nil {
		return 0, fmt.Errorf("proof base %q is not a commit", base)
	}
	for _, p := range files {
		after, err := os.ReadFile(p)
		if err != nil {
			return bad, err
		}
		before, ok := gitShow(base, p)
		if !ok {
			fmt.Printf("NEW   %s (not in %s)\n", p, base)
			continue
		}
		if isGenerated(p, after) {
			fmt.Printf("GEN   %s untouched=%v\n", p, bytes.Equal(before, after))
			if !bytes.Equal(before, after) {
				bad++
			}
			continue
		}
		b, err := astDigest(p, before)
		if err != nil {
			return bad, fmt.Errorf("%s at %s: %w", p, base, err)
		}
		a, err := astDigest(p, after)
		if err != nil {
			return bad, fmt.Errorf("%s: %w", p, err)
		}
		status := "SAME"
		if a != b {
			status = "DIFF"
			bad++
		}
		fmt.Printf("%s  %s  before=%s after=%s\n", status, p, b, a)
	}
	return bad, nil
}

func run() int {
	check := flag.Bool("check", false, "report prohibited comments and exit 1 if any; write nothing")
	list := flag.Bool("salvage", false, "list comments that would be removed and cite a URL, a vendor or platform, or say workaround/hack/because")
	proofBase := flag.String("proof", "", "git ref: print before/after comment-free syntax-tree digests per file and exit 1 on any difference")
	flag.Parse()
	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}
	files, err := goFiles(roots)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *proofBase != "" {
		bad, err := proof(files, *proofBase)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if bad > 0 {
			return 1
		}
		return 0
	}
	total := 0
	for _, p := range files {
		src, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		switch {
		case *check || *list:
			v, err := violations(p, src)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
				return 2
			}
			for _, x := range v {
				if *list && !salvage.MatchString(x.Text) {
					continue
				}
				total++
				fmt.Printf("%s:%d: %s\n", x.Path, x.Line, x.Text)
			}
		default:
			out, changed, err := strip(p, src)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
				return 2
			}
			if changed {
				total++
				if err := os.WriteFile(p, out, 0o644); err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 2
				}
				fmt.Println("stripped", p)
			}
		}
	}
	if *check && total > 0 {
		fmt.Fprintf(os.Stderr, "%d prohibited comment(s); run: go run ./scripts/stripcomments\n", total)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run())
}
