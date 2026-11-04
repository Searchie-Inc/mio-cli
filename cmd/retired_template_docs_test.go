package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The `community` hub template was retired in favour of `starter` (MIO-3053);
// MIO-3519 found `mio hubs scaffold --help` and the docs still teaching it, so
// the documented command failed against every backend. Oracle: the text an
// agent actually reads (the command's own Example and the shipped docs), not a
// second list. This is a tripwire for that exact regression; the real oracle
// (the live catalog) is out of reach offline.
var retiredTemplateRe = regexp.MustCompile(`--template[ =]community\b`)

func TestDocs_DoNotTeachRetiredCommunityTemplate(t *testing.T) {
	surfaces := map[string]string{
		"hubs scaffold Example":    hubsScaffoldCmd.Example,
		"hubs scaffold --template": hubsScaffoldCmd.Flags().Lookup("template").Usage,
	}
	for _, f := range []string{"../README.md", "../llms.txt", "../AGENTS.md", "skills/content/mio-skill.md"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		surfaces[f] = string(b)
	}
	for name, text := range surfaces {
		if retiredTemplateRe.MatchString(text) || (strings.HasPrefix(name, "hubs scaffold --template") && strings.Contains(text, "community")) {
			t.Errorf("%s still names the retired `community` hub template; use `starter`", name)
		}
	}
}
