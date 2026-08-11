package catalog

// datasource_test.go — the playlist FILL CONTRACT (MIO-3065). Its oracle is the
// TREE the scaffold would PUT: a bound node must carry the created id at
// dataSource.id, because that is the value mio-backend's page compiler reads to
// derive (model_type, model_id). Asserting anything less — that the function was
// called, that a map was consulted — would pass over a tree that still ships the
// catalog's empty id.

import (
	"reflect"
	"testing"
)

// bandTree is a two-level tree shaped like the starter homepage's Getting
// Started band: the section node itself carries a playlist dataSource, and so
// does a descendant two levels down (the scroll rail). A one-level walker would
// bind the first and silently miss the second, which is why the nesting is here.
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

// dataSourceIDsForKey collects the dataSource.id of every playlist node in the
// tree, in walk order — read back off the TREE, not off the function's return
// value, so a function that counted bindings without writing them fails here.
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
	// Deduplicated: two nodes name the same key, and reporting it twice would
	// read as two separate problems.
	if !reflect.DeepEqual(unresolved, []string{"getting-started"}) {
		t.Errorf("unresolved = %v, want [getting-started] exactly once", unresolved)
	}
	if got := dataSourceIDsForKey(tree); !reflect.DeepEqual(got, []string{"", ""}) {
		t.Errorf("dataSource ids = %v, want both left as the catalog shipped them (empty)", got)
	}
}

// A dataSource with a real id and no key is NOT part of this contract: the
// author bound it directly. Rewriting it would silently repoint a hand-authored
// binding at a template playlist.
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
