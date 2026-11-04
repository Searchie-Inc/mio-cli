package docsgen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

// TestSharedShapeBlocks_RenderEveryPropertyAndEnumValue gives the generated
// shared-shape blocks an oracle that is NOT the generator: the catalog's own
// settingsSchema. TestSkillDocIsGeneratedFromCatalog only proves the skill equals
// what Render emits, so a Render that dropped every enum value (or a property)
// still passed it (review of MIO-3519's re-pin). Here, for each block that
// renders a shared:* shape, every property the catalog declares must be a row of
// the block and every enum value the catalog declares must be on that row.
func TestSharedShapeBlocks_RenderEveryPropertyAndEnumValue(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	blocks, err := Render(cat)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	schema, _ := cat.Raw()["settingsSchema"].(map[string]any)
	// block name -> the settingsSchema key it renders (the five blocks built by
	// renderSharedShape).
	for block, key := range map[string]string{
		"node-action":        "shared:action",
		"node-text-theme":    "shared:textTheme",
		"surface-visibility": "shared:visibility",
		"surface-background": "shared:background",
		"surface-gradient":   "shared:gradient",
	} {
		t.Run(block, func(t *testing.T) {
			entry, _ := schema[key].(map[string]any)
			props, _ := entry["properties"].(map[string]any)
			if len(props) == 0 {
				t.Fatalf("%s declares no properties in the catalog — the oracle is empty, update this test", key)
			}
			rows := map[string]string{}
			for _, line := range strings.Split(blocks[block], "\n") {
				if strings.HasPrefix(line, "| `") {
					name := strings.TrimPrefix(line, "| `")
					name = name[:strings.Index(name, "`")]
					rows[name] = line
				}
			}
			for name, raw := range props {
				row, ok := rows[name]
				if !ok {
					t.Errorf("block %s has no row for property %q of %s", block, name, key)
					continue
				}
				spec, _ := raw.(map[string]any)
				enum, _ := spec["enum"].([]any)
				for _, v := range enum {
					if !strings.Contains(row, fmt.Sprint(v)) {
						t.Errorf("block %s row %q omits enum value %v declared by %s", block, name, v, key)
					}
				}
			}
		})
	}
}
