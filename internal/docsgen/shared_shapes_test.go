package docsgen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

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
