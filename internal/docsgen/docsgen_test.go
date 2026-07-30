package docsgen

import (
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

// TestRenderProducesEveryDeclaredBlock guards the contract BlockNames states: the
// drift test iterates BlockNames, so a block declared there but never rendered
// would silently guard nothing.
func TestRenderProducesEveryDeclaredBlock(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	blocks, err := Render(cat)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != len(BlockNames) {
		t.Errorf("Render returned %d blocks, BlockNames declares %d", len(blocks), len(BlockNames))
	}
	for _, name := range BlockNames {
		body, ok := blocks[name]
		if !ok {
			t.Errorf("Render omitted declared block %q", name)
			continue
		}
		if strings.TrimSpace(body) == "" {
			t.Errorf("block %q rendered empty — empty documentation is worse than none", name)
		}
		if !strings.HasSuffix(body, "\n") {
			t.Errorf("block %q does not end in a newline; the closing marker would not start its own line", name)
		}
	}
}

// TestApplyRejectsMarkerMistakes pins the failure modes that would otherwise let a
// generated section quietly stop being generated: a renamed marker, a deleted one,
// and a duplicated one.
func TestApplyRejectsMarkerMistakes(t *testing.T) {
	blocks := map[string]string{}
	for _, name := range BlockNames {
		blocks[name] = "body\n"
	}

	var full strings.Builder
	for _, name := range BlockNames {
		full.WriteString("<!-- catalog-gen:" + name + " -->\nstale\n<!-- /catalog-gen -->\n\n")
	}
	good := full.String()

	if _, err := Apply(good, blocks); err != nil {
		t.Fatalf("Apply on a well-formed doc: %v", err)
	}

	t.Run("unknown marker", func(t *testing.T) {
		doc := good + "<!-- catalog-gen:not-a-block -->\nx\n<!-- /catalog-gen -->\n"
		_, err := Apply(doc, blocks)
		if err == nil || !strings.Contains(err.Error(), "unknown catalog-gen block") {
			t.Fatalf("want an unknown-block error, got %v", err)
		}
	})

	t.Run("missing marker", func(t *testing.T) {
		doc := strings.Replace(good,
			"<!-- catalog-gen:"+BlockNames[0]+" -->\nstale\n<!-- /catalog-gen -->\n", "", 1)
		_, err := Apply(doc, blocks)
		if err == nil || !strings.Contains(err.Error(), "has no <!-- catalog-gen:"+BlockNames[0]) {
			t.Fatalf("want a missing-block error naming %q, got %v", BlockNames[0], err)
		}
	})

	t.Run("duplicate marker", func(t *testing.T) {
		dup := "<!-- catalog-gen:" + BlockNames[0] + " -->\nstale\n<!-- /catalog-gen -->\n"
		_, err := Apply(good+dup, blocks)
		if err == nil || !strings.Contains(err.Error(), "appears more than once") {
			t.Fatalf("want a duplicate-block error, got %v", err)
		}
	})
}

// TestApplyPreservesProseByteForByte is the property that makes generation safe to
// run on a doc that is mostly hand-written: everything outside the markers is
// untouched.
func TestApplyPreservesProseByteForByte(t *testing.T) {
	blocks := map[string]string{}
	for _, name := range BlockNames {
		blocks[name] = "GENERATED-" + name + "\n"
	}
	var b strings.Builder
	b.WriteString("# Title\n\nHand-written intro with `backticks` and a | pipe.\n\n")
	for _, name := range BlockNames {
		b.WriteString("prose before " + name + "\n\n<!-- catalog-gen:" + name + " -->\nold\n<!-- /catalog-gen -->\n\nprose after " + name + "\n\n")
	}
	out, err := Apply(b.String(), blocks)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, name := range BlockNames {
		if !strings.Contains(out, "prose before "+name) || !strings.Contains(out, "prose after "+name) {
			t.Errorf("Apply dropped prose around block %q", name)
		}
		if !strings.Contains(out, "GENERATED-"+name) {
			t.Errorf("Apply did not substitute block %q", name)
		}
	}
	if strings.Contains(out, "\nold\n") {
		t.Error("Apply left a stale block body behind")
	}
	if !strings.Contains(out, "Hand-written intro with `backticks` and a | pipe.") {
		t.Error("Apply mangled the hand-written intro")
	}
}

// TestRenderRejectsAnEmptyVocabulary is the vacuous-pass guard at its source: a
// catalog that stops declaring nodeKinds or settingsSchema must fail generation
// rather than emit empty documentation that then byte-matches an empty doc.
func TestRenderRejectsAnEmptyVocabulary(t *testing.T) {
	for _, key := range []string{"nodeKinds", "settingsSchema"} {
		t.Run(key, func(t *testing.T) {
			cat, err := catalog.Load()
			if err != nil {
				t.Fatalf("catalog.Load: %v", err)
			}
			cat.Raw()[key] = map[string]any{}
			_, err = Render(cat)
			if err == nil || !strings.Contains(err.Error(), "EMPTY") {
				t.Fatalf("Render with an empty %s: want an EMPTY error, got %v", key, err)
			}
		})
	}
}
