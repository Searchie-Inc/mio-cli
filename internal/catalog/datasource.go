// datasource.go — the hubTemplate playlist FILL CONTRACT (MIO-3065).
//
// A page template may bind a section to a playlist the SAME hub template
// creates. The catalog cannot carry the playlist id (it does not exist until
// the scaffold creates it), so the node ships as
//
//	"dataSource": {"type": "playlist", "id": "", "key": "<playlists[].key>"}
//
// and the consumer resolves `key` against the playlists it just created,
// writing the created id into `id`. That convention is DECLARED by the catalog
// (mio-page-catalog #33, pinned cross-field in tests/hubtemplates.test.ts) —
// this file is its consumer half.
//
// Why the id matters, precisely: mio-backend's page compiler
// (app/pages/converter.py, _resolve_model_type_and_id) maps a container
// template — `compact` among them — onto model_type="container" with
// model_id = dataSource.id whenever that key is PRESENT. An empty string is
// present, so an unfilled node compiles to ("container", "") — a section
// bound to nothing, which is why the band renders empty rather than falling
// back to anything sensible.
//
// `key` is deliberately NOT stripped after the fill. It is inert to the
// renderer (dataSource is additionalProperties:true) and keeping it is what
// lets a later reader — a re-run, an audit, a human — see WHICH template
// playlist a bound section came from; the id alone cannot say that.

package catalog

// forEachPlaylistDataSource walks node in place, depth-first over `children`,
// invoking fn for every node carrying a playlist dataSource with a non-empty
// `key`. Same traversal shape as InterpolateTreeValues: `children` is the only
// container an instantiated tree has, and no other node field is inspected.
//
// A dataSource with no `key` is skipped rather than reported: a hand-authored
// tree may bind a REAL id directly (that is what `id` is for), and only the
// key-carrying form participates in this contract.
func forEachPlaylistDataSource(node Node, fn func(ds map[string]any, key string)) {
	if ds, ok := node["dataSource"].(map[string]any); ok {
		if t, _ := ds["type"].(string); t == "playlist" {
			if key, _ := ds["key"].(string); key != "" {
				fn(ds, key)
			}
		}
	}
	if children, ok := node["children"].([]any); ok {
		for _, c := range children {
			if child, ok := c.(map[string]any); ok {
				forEachPlaylistDataSource(child, fn)
			}
		}
	}
}

// PlaylistDataSourceKeys returns every distinct playlist-dataSource `key`
// declared in a node tree, in first-seen (depth-first) order. Read-only.
//
// Used by HubTemplate.Validate to check, write-free, that every key a page
// template declares names a playlist the SAME hub template creates.
func PlaylistDataSourceKeys(node Node) []string {
	var keys []string
	seen := map[string]bool{}
	forEachPlaylistDataSource(node, func(_ map[string]any, key string) {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	})
	return keys
}

// BindPlaylistDataSources writes the created playlist ids into an instantiated
// tree IN PLACE: for every playlist dataSource carrying a `key`, `id` becomes
// idsByKey[key]. It returns how many nodes it bound and the keys it could not
// resolve (first-seen order, deduplicated).
//
// An unresolved key leaves the node exactly as the catalog shipped it — id ""
// — rather than deleting the dataSource or the node: the tree is a catalog
// artifact this code is filling in, not authoring, and a half-removed binding
// is harder to diagnose than an unfilled one. The caller reports the
// unresolved keys; this function never errors, because "no playlist for this
// key" is a runtime condition (the playlists step skipped on a hub that
// already had playlists), not a malformed tree — the malformed-tree case is
// caught write-free by Validate.
func BindPlaylistDataSources(node Node, idsByKey map[string]string) (bound int, unresolved []string) {
	seenUnresolved := map[string]bool{}
	forEachPlaylistDataSource(node, func(ds map[string]any, key string) {
		id := idsByKey[key]
		if id == "" {
			if !seenUnresolved[key] {
				seenUnresolved[key] = true
				unresolved = append(unresolved, key)
			}
			return
		}
		ds["id"] = id
		bound++
	})
	return bound, unresolved
}
