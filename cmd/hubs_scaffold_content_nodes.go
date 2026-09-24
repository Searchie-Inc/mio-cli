package cmd

// hubs_scaffold_content_nodes.go — the content-nodes step of the CLIENT-SIDE
// scaffold pipeline (MIO-4167), and the matching read of the whole-hub op's own
// content step, so `-o json` reports content items the same way on both paths.
//
// WHY THE STEP EXISTS. Media playlists and content items are two surfaces: a
// file that lives only in a playlist has no content item, so progress and
// completion tracking, "My List", comments and single-file page bindings are
// all missing for it. The backend's whole-hub op materialises content items for
// the playlists it creates (mio-backend app/hub_scaffold/service.py
// _step_content_nodes, MIO-3258 T3). The client-side pipeline never did, so
// every hub it built shipped without them until someone ran
// `mio content reconcile --playlist-id …` by hand — and the bare form of that
// command cannot help, because it reads HubTemplateApplication provenance that
// only the server-side op records. The MIO-3258 plan (§6) prescribed exactly
// this call for a CLI that runs its own step sequence.
//
// WHAT IT SENDS. POST …/hubs/{hub}/content/reconcile with EXPLICIT playlist_ids:
// the ids this run holds, in template order — the ones stepPlaylists created,
// or, on a resume whose playlists step skipped, the ones it recovered
// unambiguously. Never a bodyless POST (it would ask for provenance this path
// never records and 422 no_playlist_provenance) and never a title guess: the
// starter template ships two playlists titled "Add another playlist", so a title
// cannot say which row is which. A key with no id is NAMED on stderr instead.
//
// WHERE IT RUNS. Between playlists and pages, the position the op gives it:
// after the playlists and their items are published (a lesson is created
// published only when its file AND its playlist are) and before any page, so a
// page bound to a playlist sees a fully materialised hub.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// scaffoldContentNode is one content item the reconcile (client path) or the
// op's content step (op path) reported. outcome is the backend's D7 vocabulary
// verbatim (created / adopted / adopted_not_member_visible / skipped_*); nodeID
// is "" when this run did not learn it (the op reports ids for CREATED rows
// only).
type scaffoldContentNode struct {
	legacyHash, outcome, nodeID string
}

// recordContentNodes stores what the backend reported and marks the run as one
// that DID reconcile. A run that never calls it reports content_nodes as null:
// "did not reconcile" is a different fact from "reconciled nothing".
func (sc *scaffoldContext) recordContentNodes(nodes []scaffoldContentNode) {
	sc.contentNodes = nodes
	sc.contentNodesKnown = true
}

// contentReconcilePath is the hub content reconcile route (MIO-3258 T4) — the
// same path `mio content reconcile` posts to.
func contentReconcilePath(teamID, hubID string) string {
	return contentBasePath(teamID, hubID, "") + "/reconcile"
}

// stepContentNodes materialises content items for the playlists this run holds
// ids for. See the file comment for the why; the rules here are:
//
//   - no playlists in the template: nothing to do, and the result is [] (a
//     certain "nothing"), not null;
//   - a key with no id (a resume whose playlists step skipped and could not
//     recover it unambiguously): named on stderr with the command that fixes
//     it, never guessed;
//   - a backend without the route (405 from the /content/{node_id} route the
//     path falls onto, or a bare 404): a stderr note, not a failure — the rest
//     of the hub is still worth building;
//   - anything else fails the step like any other step's failure, and the error
//     carries the exact reconcile command, ids included, because a resume skips
//     the playlists step and can only recover the ids a page binding needs.
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
		ids, missing := scaffoldReconcileIDs(sc, t)
		if len(missing) > 0 {
			sc.notef("content-nodes: no playlist id for key(s) %s — the playlists step skipped (this hub already has playlists) and they cannot be matched to a hub playlist without guessing, so their content items were NOT created. Find their ids with `mio media hub-playlists list --hub %s` and run %s",
				strings.Join(missing, ", "), sc.hubID, reconcileCommand(sc, []string{"<id>"}))
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
			return errs.Wrap(errs.CodeOf(err), fmt.Errorf(
				"content reconcile: %w. The playlists exist: create their content items with %s — a resume skips the playlists step, so it can reconcile only the ids a page binding lets it recover",
				err, reconcileCommand(sc, ids)))
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

// scaffoldReconcileIDs returns, in TEMPLATE order, the playlist ids this run
// holds — created by stepPlaylists, or recovered by it — and the keys it holds
// none for. It reads sc.playlistIDsByKey and nothing else: the playlists step
// owns creating and recovering ids, and this step only reconciles what it has.
func scaffoldReconcileIDs(sc *scaffoldContext, t *catalog.HubTemplate) (ids, missing []string) {
	for _, p := range t.Playlists {
		if id := sc.playlistIDsByKey[p.Key]; id != "" {
			ids = append(ids, id)
		} else {
			missing = append(missing, p.Key)
		}
	}
	return ids, missing
}

// reconcileCommand is the exact `mio content reconcile` invocation for ids on
// this hub — what an operator runs when the step could not.
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

// contentReconcileRouteAbsent reports whether a reconcile failure means the
// backend has no reconcile route at all. On a backend that predates it the path
// matches GET|PATCH|DELETE /content/{node_id}, so the POST is a 405; a backend
// with no content router answers a bare 404. The route's OWN 404 carries
// code hub_not_found, and that one is a real failure: the route exists.
func contentReconcileRouteAbsent(err error) bool {
	switch errs.HTTPStatusOf(err) {
	case http.StatusMethodNotAllowed:
		return true
	case http.StatusNotFound:
		return !client.HasAPIErrorCode(err, "hub_not_found")
	}
	return false
}

// reconcileResultNodes reads the reconcile response
// (HubContentReconcileResultAttributes.results, one entry per NodeSpec in the
// backend's own order). ok is false when there is no results list to read.
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

// describeContentOutcomes renders outcome counts for the operator note, sorted
// by outcome so the line is deterministic.
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

// recordHubOpContentNodes reads the whole-hub op's content step off its summary,
// so the op path reports content_nodes with the same entry shape the client
// path does. The op's rows are `content_node:<legacy_hash>`: "created" appends
// an id to created_resource_ids.content_nodes, "skipped" carries the D7 outcome
// as its reason and no id. idsTrusted is recordHubOpResult's count check for
// the content_nodes kind — when the counts disagree every id is dropped rather
// than paired by guesswork, exactly as for every other kind.
//
// Two ways the op did NOT reconcile, reported as null rather than []:
//   - its content step was skipped wholesale (resource "content_nodes"; the
//     generic skip note already says why);
//   - it created playlists yet reported no content rows at all — every created
//     playlist yields at least a container row, so this is a backend op that
//     predates MIO-3258 T3.
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
		if ids, missing := scaffoldReconcileIDs(sc, &sc.hubTmpl); len(missing) == 0 && len(ids) > 0 {
			cmd = reconcileCommand(sc, ids)
		}
		sc.notef("content-nodes: the backend op created %d playlist(s) but reported no content items for them (it predates mio-backend MIO-3258) — create them with %s",
			createdPlaylists, cmd)
		return
	}
	sc.recordContentNodes(nodes)
}

// contentNodesResult renders the machine-readable content_nodes value: null
// when the run did not reconcile, else one {legacy_hash, outcome, node_id}
// entry per content item in the order the backend reported them ([] when there
// was nothing to reconcile). node_id is null when this run did not learn it.
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
