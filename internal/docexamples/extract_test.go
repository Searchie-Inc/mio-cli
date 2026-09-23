package docexamples

import (
	"reflect"
	"strings"
	"testing"
)

// got is a compact, comparable view of one extracted invocation.
type got struct {
	Line, EndLine int
	Args          string
	Elided        bool
}

func view(invs []Invocation) []got {
	out := make([]got, 0, len(invs))
	for _, i := range invs {
		out = append(out, got{i.Line, i.EndLine, strings.Join(i.Args, "·"), i.Elided})
	}
	return out
}

// TestFromScript_Shapes pins every doc shape the guard in cmd/doc_examples_test.go
// relies on. Each case is a shape that appears in a shipped doc surface today (or
// in the task that commissioned this parser); a regression here would make an
// example silently unguarded, which the Uncovered check exists to prevent.
func TestFromScript_Shapes(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   []got
	}{
		{
			name:   "plain invocation with quoted values",
			script: `mio products create --name "Pro Plan" --type membership --description 'Full access'`,
			want:   []got{{1, 1, `products·create·--name·Pro Plan·--type·membership·--description·Full access`, false}},
		},
		{
			name:   "console prompt",
			script: "$ mio contacts list --output json\n[{\"id\":\"c_1\"}]",
			want:   []got{{1, 1, "contacts·list·--output·json", false}},
		},
		{
			name:   "console transcript output is not parsed",
			script: "$ mio version\nmio 0.2.0\n$ mio hubs create --name A \\\n  --slug a\n{\"id\": \"hub_1\"}",
			want: []got{
				{1, 1, "version", false},
				{3, 4, "hubs·create·--name·A·--slug·a", false},
			},
		},
		{
			name:   "command substitution into a variable",
			script: `HUB_ID=$(mio hubs list -o plain --jq '.[0].id')`,
			want:   []got{{1, 1, "hubs·list·-o·plain·--jq·.[0].id", false}},
		},
		{
			name:   "substitution inside double quotes inside an invocation",
			script: `mio content update c_1 --published-at "$(date -u +%Y)" --title "$(mio hubs list -o plain --jq .[0].name)"`,
			want: []got{
				{1, 1, "content·update·c_1·--published-at·$(…)·--title·$(…)", false},
				{1, 1, "hubs·list·-o·plain·--jq·.[0].name", false},
			},
		},
		{
			name:   "backtick substitution",
			script: "ID=`mio hubs list -o plain --jq .[0].id`",
			want:   []got{{1, 1, "hubs·list·-o·plain·--jq·.[0].id", false}},
		},
		{
			name:   "backslash continuation across lines",
			script: "mio products prices create <product-id> --amount 4900 --currency usd \\\n  --type recurring --interval month",
			want:   []got{{1, 2, "products·prices·create·<product-id>·--amount·4900·--currency·usd·--type·recurring·--interval·month", false}},
		},
		{
			name:   "continuation inside a substitution",
			script: "HUB_ID=$(mio hubs scaffold --template community \\\n  --name Acme -o plain --jq .hub_id)",
			want:   []got{{1, 2, "hubs·scaffold·--template·community·--name·Acme·-o·plain·--jq·.hub_id", false}},
		},
		{
			name:   "pipes, and-lists and redirections",
			script: "mio contacts retrieve <id> 2>err.json || jq -r '.errors[0].status' err.json\nmio pages tree get <p> --jq '{root: .tree}' > tree.json && mio pages tree set <p> --file tree.json 2>&1 | tee log",
			want: []got{
				{1, 1, "contacts·retrieve·<id>", false},
				{2, 2, "pages·tree·get·<p>·--jq·{root: .tree}", false},
				{2, 2, "pages·tree·set·<p>·--file·tree.json", false},
			},
		},
		{
			name:   "placeholders are words, not redirections",
			script: "mio hubs update <hub-id> --unset <path> --target <claude|codex>",
			want:   []got{{1, 1, "hubs·update·<hub-id>·--unset·<path>·--target·<claude|codex>", false}},
		},
		{
			name:   "comments are not arguments, and a commented-out invocation is not extracted",
			script: "mio version   # → mio 0.2.0\n# once the hub exists: mio config set current_hub <hub-uuid>",
			want:   []got{{1, 1, "version", false}},
		},
		{
			name:   "environment prefix and shell keywords",
			script: "MIO_EMAIL=a@b.c MIO_PASSWORD='x y' mio register\nfor id in a b; do mio contacts delete \"$id\" --yes; done",
			want: []got{
				{1, 1, "register", false},
				{2, 2, "contacts·delete·$id·--yes", false},
			},
		},
		{
			name:   "optional group dropped, quoted brackets kept",
			script: "mio pages catalog scaffold --template <id> [--variant <v>]\nmio media files cards set f_1 --cards '[{\"at\":1}]'",
			want: []got{
				{1, 1, "pages·catalog·scaffold·--template·<id>", false},
				{2, 2, `media·files·cards·set·f_1·--cards·[{"at":1}]`, false},
			},
		},
		{
			name:   "ellipsis marks an elided invocation",
			script: "PAGE_ID=$(mio pages create … -o plain --jq .id)",
			want:   []got{{1, 1, "pages·create·-o·plain·--jq·.id", true}},
		},
		{
			name:   "heredoc bodies are not code",
			script: "cat > tree.json <<'EOF'\nmio this is not a command\nEOF\nmio pages tree set p_1 --file tree.json",
			want:   []got{{4, 4, "pages·tree·set·p_1·--file·tree.json", false}},
		},
		{
			name:   "arithmetic expansion is not a substitution",
			script: `mio pages publish "$PAGE_ID" --if-match "$((V + 1))"`,
			want:   []got{{1, 1, "pages·publish·$PAGE_ID·--if-match·$((V + 1))", false}},
		},
		{
			name:   "global flags before the subcommand; ./mio is mio; go build -o mio is not",
			script: "go build -o mio .\n./mio --api-base https://api.example.com contacts list",
			want:   []got{{2, 2, "--api-base·https://api.example.com·contacts·list", false}},
		},
		{
			name:   "xargs",
			script: "mio contacts list -o plain --jq '.[].id' | xargs -n1 mio contacts delete --yes",
			want: []got{
				{1, 1, "contacts·list·-o·plain·--jq·.[].id", false},
				{1, 1, "contacts·delete·--yes", false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := FromScript("x.md", 1, tc.script)
			if g := view(res.Invocations); !reflect.DeepEqual(g, tc.want) {
				t.Errorf("invocations\n got %+v\nwant %+v", g, tc.want)
			}
			if len(res.Uncovered) != 0 {
				t.Errorf("unexpected uncovered mentions: %+v", res.Uncovered)
			}
		})
	}
}

// TestFromScript_UncoveredMentionIsReported is the parser's own tripwire: a line
// that visibly invokes mio but that the parser does not extract must be
// reported, never silently dropped. The shape used here — mio as the argument
// of an unknown wrapper — is one the parser deliberately does not follow.
func TestFromScript_UncoveredMentionIsReported(t *testing.T) {
	res := FromScript("x.md", 10, "true\nwatch -n5 mio contacts list\n")
	if len(res.Invocations) != 0 {
		t.Fatalf("expected no invocation, got %+v", res.Invocations)
	}
	if len(res.Uncovered) != 1 || res.Uncovered[0].Line != 11 {
		t.Fatalf("want exactly one uncovered mention at line 11, got %+v", res.Uncovered)
	}
}

// TestFromScript_UncoveredMentionBesideAnExtractedOne: coverage is per `mio`
// word, not per line. An extracted invocation must not vouch for a second,
// unfollowed one on the same line (codex review, MIO-4154 round 1).
func TestFromScript_UncoveredMentionBesideAnExtractedOne(t *testing.T) {
	res := FromScript("x.md", 1, "mio version; watch -n5 mio contacts list")
	if len(res.Invocations) != 1 || strings.Join(res.Invocations[0].Args, " ") != "version" {
		t.Fatalf("want only `mio version` extracted, got %+v", res.Invocations)
	}
	if len(res.Uncovered) != 1 || res.Uncovered[0].Line != 1 {
		t.Fatalf("want the `watch … mio contacts list` mention reported at line 1, got %+v", res.Uncovered)
	}
}

// TestFromScript_TranscriptOutputMentionIsReported: one `$ ` prompt makes a
// block a console transcript, and its unprompted lines are output, never parsed.
// A `mio <word>` on such a line must still be REPORTED: otherwise a single
// prompt line blinds the guard to every unprompted command in the same block,
// and line 1 below, the exact MIO-4154 defect, would pass unseen (blind review).
// A version banner (`mio 0.2.0`) names no command word and is not a mention.
func TestFromScript_TranscriptOutputMentionIsReported(t *testing.T) {
	res := FromScript("x.md", 1, strings.Join([]string{
		`mio products create --name "Pro Plan"`, // 1: unprompted — reported
		"$ mio version",                         // 2: prompt — extracted
		"mio 0.2.0",                             // 3: output, no command word
		"Publish it with: mio hubs update hub_1 --published", // 4: output naming mio — reported
	}, "\n"))
	if g, want := view(res.Invocations), []got{{2, 2, "version", false}}; !reflect.DeepEqual(g, want) {
		t.Errorf("invocations\n got %+v\nwant %+v", g, want)
	}
	var lines []int
	for _, m := range res.Uncovered {
		lines = append(lines, m.Line)
		if !m.Output {
			t.Errorf("line %d: Output = false, want true (it is an unprompted line of a transcript)", m.Line)
		}
	}
	if want := []int{1, 4}; !reflect.DeepEqual(lines, want) {
		t.Errorf("uncovered lines = %v, want %v: an unprompted `mio <word>` in a transcript must be reported, not blanked", lines, want)
	}
}

// TestFromMarkdown_BlockquoteFences: a fence inside a blockquote (`> ```sh`,
// README's go-install note) is code like any other, so its invocations must be
// extracted and its mentions covered (blind review, MIO-4154). The fence ends
// where its container ends, two ways: a top-level fence straight after a quoted
// one opens a new block instead of being swallowed (lines 12-16), and prose
// straight after one is prose, not code (lines 17-19). Each half fails on its
// own mutation; the second exists because the first alone passed with the
// container-end check deleted (the stray backticks parsed as a substitution).
func TestFromMarkdown_BlockquoteFences(t *testing.T) {
	doc := strings.Join([]string{
		"> **Note:** rename it:", // 1
		"> ```sh",                // 2
		`> mio products create --name "Pro Plan"`, // 3
		">```",                  // 4: close, no space after >
		"> > ```bash",           // 5: nested quote
		"> > mio contacts list", // 6
		"> > ```",               // 7
		"  > - item:",           // 8: indented quote holding a list
		">   ```sh",             // 9: list-indented fence in a quote
		">   V=$(mio pages tree get <p> --jq .tree)", // 10
		">   ```",                      // 11
		"> ```sh",                      // 12: ended by its container ending
		"> mio version",                // 13
		"```sh",                        // 14: a NEW top-level fence
		"mio hubs list",                // 15
		"```",                          // 16
		"> ```sh",                      // 17: also ended by its container ending,
		"> mio contacts retrieve <id>", // 18
		"prose: mio pages list",        // 19: here by prose, which is not read
		"",                             // 20
		"more prose",                   // 21
	}, "\n")
	res := FromMarkdown("doc.md", doc)
	want := []got{
		{3, 3, "products·create·--name·Pro Plan", false},
		{6, 6, "contacts·list", false},
		{10, 10, "pages·tree·get·<p>·--jq·.tree", false},
		{13, 13, "version", false},
		{15, 15, "hubs·list", false},
		{18, 18, "contacts·retrieve·<id>", false},
	}
	if g := view(res.Invocations); !reflect.DeepEqual(g, want) {
		t.Errorf("invocations\n got %+v\nwant %+v", g, want)
	}
	if len(res.Uncovered) != 0 {
		t.Errorf("unexpected uncovered: %+v", res.Uncovered)
	}
}

// TestFromMarkdown_FencesAndLineNumbers checks that only fenced code is read,
// that line numbers are DOCUMENT lines (what a failure message names), and that
// indented fences (inside list items) and ~~~ fences are handled.
func TestFromMarkdown_FencesAndLineNumbers(t *testing.T) {
	doc := strings.Join([]string{
		"# Title",                              // 1
		"Prose mentions `mio hubs list` here.", // 2
		"```sh",                                // 3
		"mio contacts list",                    // 4
		"```",                                  // 5
		"- item:",                              // 6
		"  ```bash",                            // 7
		"  V=$(mio pages tree get <p> \\",      // 8
		"    --jq .draft_version)",             // 9
		"  ```",                                // 10
		"~~~",                                  // 11
		"mio version",                          // 12
		"~~~",                                  // 13
		"```json",                              // 14
		`{"note": "not a command"}`,            // 15
		"```",                                  // 16
	}, "\n")
	res := FromMarkdown("doc.md", doc)
	want := []got{
		{4, 4, "contacts·list", false},
		{8, 9, "pages·tree·get·<p>·--jq·.draft_version", false},
		{12, 12, "version", false},
	}
	if g := view(res.Invocations); !reflect.DeepEqual(g, want) {
		t.Errorf("invocations\n got %+v\nwant %+v", g, want)
	}
	for _, inv := range res.Invocations {
		if inv.File != "doc.md" {
			t.Errorf("File = %q, want doc.md", inv.File)
		}
	}
	if len(res.Uncovered) != 0 {
		t.Errorf("unexpected uncovered: %+v", res.Uncovered)
	}
}
