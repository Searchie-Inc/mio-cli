package catalog

import (
	"reflect"
	"testing"
)

func bandTree() Node {
	return Node{
		"kind": "stack",
		"children": []any{
			map[string]any{"kind": "headline", "value": "Welcome"},
			map[string]any{
				"kind":       "section",
				"dataSource": map[string]any{"type": "playlist", "id": "", "key": "getting-started"},
				"children": []any{
					map[string]any{
						"kind": "row",
						"children": []any{
							map[string]any{"kind": "text", "value": "x"},
							map[string]any{
								"kind":       "scroll",
								"dataSource": map[string]any{"type": "playlist", "id": "", "key": "getting-started"},
							},
						},
					},
				},
			},
		},
	}
}

func dataSourceIDsForKey(n Node) []string {
	var ids []string
	forEachPlaylistDataSource(n, func(ds map[string]any, _ string) {
		id, _ := ds["id"].(string)
		ids = append(ids, id)
	})
	return ids
}

func TestBindPlaylistDataSources_WritesTheCreatedIDAtEveryDepth(t *testing.T) {
	tree := bandTree()
	bound, unresolved := BindPlaylistDataSources(tree, map[string]string{"getting-started": "pl_made"})
	if bound != 2 {
		t.Errorf("bound = %d, want 2 (the section node AND the nested rail)", bound)
	}
	if len(unresolved) != 0 {
		t.Errorf("unresolved = %v, want none", unresolved)
	}
	if got := dataSourceIDsForKey(tree); !reflect.DeepEqual(got, []string{"pl_made", "pl_made"}) {
		t.Errorf("dataSource ids in the tree = %v, want [pl_made pl_made] — the id must be WRITTEN INTO the tree that gets PUT", got)
	}
}

func TestBindPlaylistDataSources_UnresolvedKeyIsReportedAndLeavesTheTreeAlone(t *testing.T) {
	tree := bandTree()
	bound, unresolved := BindPlaylistDataSources(tree, map[string]string{"other": "pl_other"})
	if bound != 0 {
		t.Errorf("bound = %d, want 0 — no playlist was created for this key", bound)
	}
	if !reflect.DeepEqual(unresolved, []string{"getting-started"}) {
		t.Errorf("unresolved = %v, want [getting-started] exactly once", unresolved)
	}
	if got := dataSourceIDsForKey(tree); !reflect.DeepEqual(got, []string{"", ""}) {
		t.Errorf("dataSource ids = %v, want both left as the catalog shipped them (empty)", got)
	}
}

func TestBindPlaylistDataSources_LeavesKeylessAndNonPlaylistBindingsAlone(t *testing.T) {
	tree := Node{
		"kind": "stack",
		"children": []any{
			map[string]any{"kind": "a", "dataSource": map[string]any{"type": "playlist", "id": "pl_authored"}},
			map[string]any{"kind": "b", "dataSource": map[string]any{"type": "content", "id": "", "key": "getting-started"}},
		},
	}
	bound, unresolved := BindPlaylistDataSources(tree, map[string]string{"getting-started": "pl_made"})
	if bound != 0 || len(unresolved) != 0 {
		t.Errorf("bound=%d unresolved=%v, want 0/none", bound, unresolved)
	}
	kids, _ := tree["children"].([]any)
	authored, _ := kids[0].(map[string]any)["dataSource"].(map[string]any)
	if authored["id"] != "pl_authored" {
		t.Errorf("a keyless dataSource id = %v, want pl_authored (untouched)", authored["id"])
	}
	content, _ := kids[1].(map[string]any)["dataSource"].(map[string]any)
	if content["id"] != "" {
		t.Errorf("a NON-playlist dataSource must not be bound, id = %v", content["id"])
	}
}

func TestPlaylistDataSourceKeys_DeduplicatesInWalkOrder(t *testing.T) {
	tree := bandTree()
	kids, _ := tree["children"].([]any)
	section, _ := kids[1].(map[string]any)
	section["dataSource"] = map[string]any{"type": "playlist", "id": "", "key": "featured"}
	if got := PlaylistDataSourceKeys(tree); !reflect.DeepEqual(got, []string{"featured", "getting-started"}) {
		t.Errorf("keys = %v, want [featured getting-started] (depth-first, deduplicated)", got)
	}
	if got := PlaylistDataSourceKeys(Node{"kind": "stack"}); got != nil {
		t.Errorf("a tree with no playlist dataSource must yield no keys, got %v", got)
	}
}
