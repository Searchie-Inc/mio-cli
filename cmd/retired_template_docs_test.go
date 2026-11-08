package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var retiredTemplateRe = regexp.MustCompile(`--template[ =]["']?community\b|the (shipped |live )?` + "`community`" + ` (one|template)`)

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
