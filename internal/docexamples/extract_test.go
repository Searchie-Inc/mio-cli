package docexamples

import (
	"reflect"
	"strings"
	"testing"
)

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

func TestFromScript_UncoveredMentionIsReported(t *testing.T) {
	res := FromScript("x.md", 10, "true\nwatch -n5 mio contacts list\n")
	if len(res.Invocations) != 0 {
		t.Fatalf("expected no invocation, got %+v", res.Invocations)
	}
	if len(res.Uncovered) != 1 || res.Uncovered[0].Line != 11 {
		t.Fatalf("want exactly one uncovered mention at line 11, got %+v", res.Uncovered)
	}
}

func TestFromScript_UncoveredMentionBesideAnExtractedOne(t *testing.T) {
	res := FromScript("x.md", 1, "mio version; watch -n5 mio contacts list")
	if len(res.Invocations) != 1 || strings.Join(res.Invocations[0].Args, " ") != "version" {
		t.Fatalf("want only `mio version` extracted, got %+v", res.Invocations)
	}
	if len(res.Uncovered) != 1 || res.Uncovered[0].Line != 1 {
		t.Fatalf("want the `watch … mio contacts list` mention reported at line 1, got %+v", res.Uncovered)
	}
}

func TestFromScript_TranscriptOutputMentionIsReported(t *testing.T) {
	res := FromScript("x.md", 1, strings.Join([]string{
		`mio products create --name "Pro Plan"`,
		"$ mio version",
		"mio 0.2.0",
		"Publish it with: mio hubs update hub_1 --published",
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

func TestFromMarkdown_BlockquoteFences(t *testing.T) {
	doc := strings.Join([]string{
		"> **Note:** rename it:",
		"> ```sh",
		`> mio products create --name "Pro Plan"`,
		">```",
		"> > ```bash",
		"> > mio contacts list",
		"> > ```",
		"  > - item:",
		">   ```sh",
		">   V=$(mio pages tree get <p> --jq .tree)",
		">   ```",
		"> ```sh",
		"> mio version",
		"```sh",
		"mio hubs list",
		"```",
		"> ```sh",
		"> mio contacts retrieve <id>",
		"prose: mio pages list",
		"",
		"more prose",
		"> ```sh",
		`> mio pages tree get <p> --jq '{root: .tree}' \`,
		"> > tree.json",
		"> ```",
	}, "\n")
	res := FromMarkdown("doc.md", doc)
	want := []got{
		{3, 3, "products·create·--name·Pro Plan", false},
		{6, 6, "contacts·list", false},
		{10, 10, "pages·tree·get·<p>·--jq·.tree", false},
		{13, 13, "version", false},
		{15, 15, "hubs·list", false},
		{18, 18, "contacts·retrieve·<id>", false},
		{23, 23, "pages·tree·get·<p>·--jq·{root: .tree}", false},
	}
	if g := view(res.Invocations); !reflect.DeepEqual(g, want) {
		t.Errorf("invocations\n got %+v\nwant %+v", g, want)
	}
	if len(res.Uncovered) != 0 {
		t.Errorf("unexpected uncovered: %+v", res.Uncovered)
	}
}

func TestFromMarkdown_FencesAndLineNumbers(t *testing.T) {
	doc := strings.Join([]string{
		"# Title",
		"Prose mentions `mio hubs list` here.",
		"```sh",
		"mio contacts list",
		"```",
		"- item:",
		"  ```bash",
		"  V=$(mio pages tree get <p> \\",
		"    --jq .draft_version)",
		"  ```",
		"~~~",
		"mio version",
		"~~~",
		"```json",
		`{"note": "not a command"}`,
		"```",
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
