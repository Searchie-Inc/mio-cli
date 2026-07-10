package cmd

// community_spaces.go — shared flag→attribute mapping for `mio community spaces
// create/update` (aligned to the backend Space schema) and the `spaces reorder`
// command (MIO-2260).

import (
	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var (
	spaceAccessLevels       = map[string]bool{"public": true, "restricted": true}
	spacePostingPermissions = map[string]bool{"any_member": true, "admins_only": true, "segment": true}
)

// setSpaceWriteAttrs copies the shared create/update space flags into attrs
// using the backend Space attribute keys. --access-level and --posting-permission
// are validated against their enums; --position must be >= 0. The old --is-private
// flag is gone — access is governed by access_level (public|restricted).
func setSpaceWriteAttrs(cmd *cobra.Command, attrs map[string]any) error {
	setStringFlag(cmd, attrs, "name")
	setStringFlag(cmd, attrs, "slug")
	setStringFlag(cmd, attrs, "description")
	setStringFlag(cmd, attrs, "icon")
	setStringFlag(cmd, attrs, "color")
	setMappedString(cmd, attrs, "segment-id", "segment_id")
	setMappedBool(cmd, attrs, "is-default", "is_default")
	setMappedBool(cmd, attrs, "is-pinned", "is_pinned")

	if cmd.Flags().Changed("access-level") {
		v, err := cmd.Flags().GetString("access-level")
		if err != nil {
			return errs.New(errs.ExitUsage, "--access-level: %s", err)
		}
		if !spaceAccessLevels[v] {
			return errs.New(errs.ExitUsage, "invalid --access-level %q: must be public or restricted", v)
		}
		attrs["access_level"] = v
	}
	if cmd.Flags().Changed("posting-permission") {
		v, err := cmd.Flags().GetString("posting-permission")
		if err != nil {
			return errs.New(errs.ExitUsage, "--posting-permission: %s", err)
		}
		if !spacePostingPermissions[v] {
			return errs.New(errs.ExitUsage, "invalid --posting-permission %q: must be any_member, admins_only, or segment", v)
		}
		attrs["posting_permission"] = v
	}
	if cmd.Flags().Changed("position") {
		pos, err := cmd.Flags().GetInt("position")
		if err != nil {
			return errs.New(errs.ExitUsage, "--position: %s", err)
		}
		if pos < 0 {
			return errs.New(errs.ExitUsage, "invalid --position %d: must be >= 0", pos)
		}
		attrs["position"] = pos
	}
	return nil
}

var communitySpacesReorderCmd = &cobra.Command{
	Use:     "reorder",
	Short:   "Reorder spaces in a hub.",
	Long:    "Set the display order of spaces by providing --order as a comma-separated list of space ids (first = position 0).",
	Example: `  mio community spaces reorder --hub hub_abc123 --order sp_1,sp_2,sp_3`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		order, err := cmd.Flags().GetString("order")
		if err != nil {
			return errs.New(errs.ExitUsage, "--order: %s", err)
		}
		ids := splitCSV(order)
		if len(ids) == 0 {
			return errs.New(errs.ExitUsage, "nothing to reorder: set --order with a comma-separated list of space ids")
		}
		data := make([]map[string]any, len(ids))
		for i, id := range ids {
			data[i] = map[string]any{"type": "spaces", "id": id}
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		// POST .../spaces/reorder with a bare {data:[{type,id}]} list; the backend
		// takes position from the list order (ReorderEnvelope). Send raw.
		col, err := c.client.ActionCollectionRaw(c.ctx, "POST",
			spacesPath(teamID, hubID, "")+"/reorder", map[string]any{"data": data})
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}
