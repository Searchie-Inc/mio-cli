package cmd

import (
	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var (
	spaceAccessLevels       = map[string]bool{"public": true, "restricted": true}
	spacePostingPermissions = map[string]bool{"any_member": true, "admins_only": true, "admins_and_moderators": true, "segment": true}
)

type SpaceInput struct {
	Name              *string
	Slug              *string
	Description       *string
	Icon              *string
	Color             *string
	SegmentID         *string
	IsDefault         *bool
	IsPinned          *bool
	AccessLevel       *string
	PostingPermission *string
	Position          *int
}

func buildSpaceAttrs(s SpaceInput) (map[string]any, error) {
	attrs := map[string]any{}
	if s.Name != nil {
		attrs["name"] = *s.Name
	}
	if s.Slug != nil {
		attrs["slug"] = *s.Slug
	}
	if s.Description != nil {
		attrs["description"] = *s.Description
	}
	if s.Icon != nil {
		attrs["icon"] = *s.Icon
	}
	if s.Color != nil {
		attrs["color"] = *s.Color
	}
	if s.SegmentID != nil {
		attrs["segment_id"] = *s.SegmentID
	}
	if s.IsDefault != nil {
		attrs["is_default"] = *s.IsDefault
	}
	if s.IsPinned != nil {
		attrs["is_pinned"] = *s.IsPinned
	}
	if s.AccessLevel != nil {
		if !spaceAccessLevels[*s.AccessLevel] {
			return nil, errs.New(errs.ExitUsage, "invalid --access-level %q: must be public or restricted", *s.AccessLevel)
		}
		attrs["access_level"] = *s.AccessLevel
	}
	if s.PostingPermission != nil {
		if !spacePostingPermissions[*s.PostingPermission] {
			return nil, errs.New(errs.ExitUsage, "invalid --posting-permission %q: must be any_member, admins_only, admins_and_moderators, or segment", *s.PostingPermission)
		}
		attrs["posting_permission"] = *s.PostingPermission
	}
	if s.Position != nil {
		if *s.Position < 0 {
			return nil, errs.New(errs.ExitUsage, "invalid --position %d: must be >= 0", *s.Position)
		}
		attrs["position"] = *s.Position
	}
	return attrs, nil
}

func setSpaceWriteAttrs(cmd *cobra.Command, attrs map[string]any) error {
	built, err := buildSpaceAttrs(SpaceInput{
		Name:              changedString(cmd, "name"),
		Slug:              changedString(cmd, "slug"),
		Description:       changedString(cmd, "description"),
		Icon:              changedString(cmd, "icon"),
		Color:             changedString(cmd, "color"),
		SegmentID:         changedString(cmd, "segment-id"),
		IsDefault:         changedBool(cmd, "is-default"),
		IsPinned:          changedBool(cmd, "is-pinned"),
		AccessLevel:       changedString(cmd, "access-level"),
		PostingPermission: changedString(cmd, "posting-permission"),
		Position:          changedInt(cmd, "position"),
	})
	if err != nil {
		return err
	}
	for k, v := range built {
		attrs[k] = v
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
		col, err := c.client.ActionCollectionRaw(c.ctx, "POST",
			spacesPath(teamID, hubID, "")+"/reorder", map[string]any{"data": data})
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}
