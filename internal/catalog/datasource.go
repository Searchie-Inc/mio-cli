package catalog

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
