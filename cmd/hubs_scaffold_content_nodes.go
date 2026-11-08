package cmd

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type scaffoldContentNode struct {
	legacyHash, outcome, nodeID string
}

func (sc *scaffoldContext) recordContentNodes(nodes []scaffoldContentNode) {
	sc.contentNodes = nodes
	sc.contentNodesKnown = true
}

func contentReconcilePath(teamID, hubID string) string {
	return contentBasePath(teamID, hubID, "") + "/reconcile"
}

func stepContentNodes(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Playlists) == 0 {
		return sc.step("content-nodes", "no playlists in template", func() error {
			sc.recordContentNodes(nil)
			return nil
		})
	}
	keys := make([]string, len(t.Playlists))
	for i, p := range t.Playlists {
		keys[i] = p.Key
	}
	detail := fmt.Sprintf("POST %s — create content items (a container per playlist, a lesson per item) for playlist(s) [%s], naming the ids the playlists step created or recovered (playlist_ids, template order); a backend without the route is noted, not failed",
		contentReconcilePath(sc.teamID, sc.hubIDOrPlaceholder()), strings.Join(keys, ", "))
	return sc.step("content-nodes", detail, func() error {
		ids, missing, notInHub := scaffoldReconcileIDs(sc, t)
		if len(missing) > 0 {
			sc.notef("content-nodes: no playlist id for key(s) %s — the playlists step skipped (this hub already has playlists) and they cannot be matched to a hub playlist without guessing, so their content items were NOT created. Find the ids of the playlists scoped to this hub with %s and run %s",
				strings.Join(missing, ", "), hubScopedPlaylistsCommand(sc), reconcileCommand(sc, []string{"<id>"}))
		}
		if len(notInHub) > 0 {
			sc.notef("content-nodes: playlist(s) %s are published into this hub but not scoped to this hub (their own hub_id is not %s), so their content items were NOT created: reconcile accepts only the hub's own playlists and would reject the whole request (422 playlist_not_in_hub). A page binding still uses them.",
				strings.Join(notInHub, ", "), sc.hubID)
		}
		if len(ids) == 0 {
			return nil
		}

		res, err := sc.cl.ActionWithType(sc.ctx, http.MethodPost, contentReconcilePath(sc.teamID, sc.hubID),
			contentReconcileType, map[string]any{"playlist_ids": ids})
		if err != nil {
			if contentReconcileRouteAbsent(err) {
				sc.notef("content-nodes: this backend has no content reconcile route (POST …/content/reconcile answered %d) — the hub's playlists got NO content items, so their files have no progress tracking, My List or comments. On a backend that has the route (mio-backend MIO-3258), run %s",
					errs.HTTPStatusOf(err), reconcileCommand(sc, ids))
				return nil
			}
			sep := ". "
			if strings.HasSuffix(err.Error(), ".") {
				sep = " "
			}
			return errs.Wrap(errs.CodeOf(err), fmt.Errorf(
				"content reconcile: %w%sThe playlists exist: create their content items with %s — a resume skips the playlists step, so it can reconcile only the ids a page binding lets it recover",
				err, sep, reconcileCommand(sc, ids)))
		}

		nodes, ok := reconcileResultNodes(res)
		if !ok {
			sc.notef("content-nodes: the reconcile succeeded but its response carried no results list — check the hub's content items with `mio content list --hub %s`", sc.hubID)
			return nil
		}
		sc.recordContentNodes(nodes)
		sc.notef("content-nodes: %d playlist(s) reconciled into %d content item(s) (%s)",
			len(ids), len(nodes), describeContentOutcomes(nodes))
		return nil
	})
}

func scaffoldReconcileIDs(sc *scaffoldContext, t *catalog.HubTemplate) (ids, missing, notInHub []string) {
	for _, p := range t.Playlists {
		id := sc.playlistIDsByKey[p.Key]
		switch {
		case id == "":
			missing = append(missing, p.Key)
		case sc.playlistsNotInHub[id]:
			notInHub = append(notInHub, fmt.Sprintf("%s (%s)", p.Key, id))
		default:
			ids = append(ids, id)
		}
	}
	return ids, missing, notInHub
}

func hubScopedPlaylistsCommand(sc *scaffoldContext) string {
	parts := []string{"mio media playlists list"}
	if sc.teamID != "" {
		parts = append(parts, "--team "+sc.teamID)
	}
	parts = append(parts, "--limit 100 -o json", fmt.Sprintf(`--jq '.[] | select(.hub_id == "%s") | {id, title}'`, sc.hubID))
	return "`" + strings.Join(parts, " ") + "`"
}

func reconcileCommand(sc *scaffoldContext, ids []string) string {
	parts := []string{"mio content reconcile", "--hub " + sc.hubID}
	if sc.teamID != "" {
		parts = append(parts, "--team "+sc.teamID)
	}
	for _, id := range ids {
		parts = append(parts, "--playlist-id "+id)
	}
	return "`" + strings.Join(parts, " ") + "`"
}

func contentReconcileRouteAbsent(err error) bool {
	switch errs.HTTPStatusOf(err) {
	case http.StatusMethodNotAllowed:
		return true
	case http.StatusNotFound:
		return !client.HasAPIErrorCode(err, "hub_not_found")
	}
	return false
}

func reconcileResultNodes(res *client.Resource) (nodes []scaffoldContentNode, ok bool) {
	if res == nil {
		return nil, false
	}
	raw, ok := res.Attributes["results"].([]any)
	if !ok {
		return nil, false
	}
	nodes = make([]scaffoldContentNode, 0, len(raw))
	for _, r := range raw {
		m, isObj := r.(map[string]any)
		if !isObj {
			continue
		}
		n := scaffoldContentNode{}
		n.legacyHash, _ = m["legacy_hash"].(string)
		n.outcome, _ = m["outcome"].(string)
		n.nodeID, _ = m["node_id"].(string)
		nodes = append(nodes, n)
	}
	return nodes, true
}

func describeContentOutcomes(nodes []scaffoldContentNode) string {
	if len(nodes) == 0 {
		return "no items to create"
	}
	counts := map[string]int{}
	for _, n := range nodes {
		counts[n.outcome]++
	}
	outcomes := make([]string, 0, len(counts))
	for o := range counts {
		outcomes = append(outcomes, o)
	}
	sort.Strings(outcomes)
	parts := make([]string, len(outcomes))
	for i, o := range outcomes {
		parts[i] = fmt.Sprintf("%s %d", o, counts[o])
	}
	return strings.Join(parts, ", ")
}

func recordHubOpContentNodes(sc *scaffoldContext, res client.HubFromTemplateResult, idsTrusted bool) {
	var nodes []scaffoldContentNode
	ids := res.CreatedIDs["content_nodes"]
	next, createdPlaylists := 0, 0
	for _, row := range res.Summary {
		if row.Resource == "content_nodes" {
			return
		}
		if strings.HasPrefix(row.Resource, "playlist:") && row.Action == "created" {
			createdPlaylists++
			continue
		}
		hash, ok := strings.CutPrefix(row.Resource, "content_node:")
		if !ok {
			continue
		}
		n := scaffoldContentNode{legacyHash: hash, outcome: row.Action}
		switch row.Action {
		case "created":
			if idsTrusted && next < len(ids) {
				n.nodeID = ids[next]
			}
			next++
		case "skipped":
			n.outcome = row.Reason
		}
		nodes = append(nodes, n)
	}
	if len(nodes) == 0 && createdPlaylists > 0 {
		cmd := reconcileCommand(sc, []string{"<id>"})
		if ids, missing, notInHub := scaffoldReconcileIDs(sc, &sc.hubTmpl); len(missing) == 0 && len(notInHub) == 0 && len(ids) > 0 {
			cmd = reconcileCommand(sc, ids)
		}
		sc.notef("content-nodes: the backend op created %d playlist(s) but reported no content items for them (it predates mio-backend MIO-3258) — create them with %s",
			createdPlaylists, cmd)
		return
	}
	sc.recordContentNodes(nodes)
}

func contentNodesResult(sc *scaffoldContext) any {
	if !sc.contentNodesKnown {
		return nil
	}
	out := make([]any, 0, len(sc.contentNodes))
	for _, n := range sc.contentNodes {
		out = append(out, map[string]any{
			"legacy_hash": n.legacyHash,
			"outcome":     n.outcome,
			"node_id":     nilIfEmpty(n.nodeID),
		})
	}
	return out
}
