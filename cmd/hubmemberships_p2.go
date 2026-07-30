package cmd

// hubmemberships_p2.go — admin membership authoring (MIO-2261 `add`, MIO-2263
// `set-role`). The create-membership endpoint (POST .../members) ships in
// mio-backend PR #487; set-role (PATCH .../members/{contact_id}/role) already
// exists.

import (
	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// hubMemberRoles is the elevated-role enum the backend accepts (None = a plain
// active member — omit --role).
var hubMemberRoles = map[string]bool{"admin": true, "moderator": true}

// hubMembersPath returns /api/admin/teams/{team}/hubs/{hub}/members[/{contact_id}].
func hubMembersPath(teamID, hubID, contactID string) string {
	base := "/api/admin/teams/" + teamID + "/hubs/" + hubID + "/members"
	if contactID != "" {
		return base + "/" + contactID
	}
	return base
}

var hubMembershipsAddCmd = &cobra.Command{
	Use:   "add <contact_id>",
	Short: "Add a contact as an active hub member.",
	Long: `Add (or re-activate) a contact as an ACTIVE member of the hub, optionally
granting an admin or moderator role. Emits MemberAdded, so the contact's
community profile is created as a side effect.

<contact_id> is the GLOBAL contact id — the .attributes.contact_id field from
'mio contacts', NOT its .id (that is the team-contact id and this verb will 404
on it).`,
	Example: `  # contact_id is the GLOBAL id: read it from 'mio contacts', not the .id
  mio hub-memberships add "$(mio contacts retrieve ctt_abc -o plain --jq '.contact_id')" --hub hub_abc123
  mio hub-memberships add contact_xyz --hub hub_abc123 --role moderator`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		attrs := map[string]any{"contact_id": args[0]}
		if cmd.Flags().Changed("role") {
			role, err := cmd.Flags().GetString("role")
			if err != nil {
				return errs.New(errs.ExitUsage, "--role: %s", err)
			}
			if !hubMemberRoles[role] {
				return errs.New(errs.ExitUsage, "invalid --role %q: must be admin or moderator (omit for a plain member)", role)
			}
			attrs["role"] = role
		}

		c, teamID, hubID, err := hubMembershipsContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, hubMembersPath(teamID, hubID, ""), attrs)
		if err != nil {
			return hintGlobalContactID(err)
		}
		return c.render(cmd, res)
	},
}

var hubMembershipsSetRoleCmd = &cobra.Command{
	Use:   "set-role <contact_id>",
	Short: "Set a hub member's role.",
	Long: `Set an active member's role to admin or moderator, or demote an elevated member
back to a plain member with --role member. The member must already be active
(add them with 'hub-memberships add' first).

<contact_id> is the GLOBAL contact id — the .attributes.contact_id field from
'mio contacts', NOT its .id (the team-contact id).`,
	Example: `  mio hub-memberships set-role contact_xyz --hub hub_abc123 --role moderator
  mio hub-memberships set-role contact_xyz --hub hub_abc123 --role member    # demote to plain member`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("role") {
			return errs.New(errs.ExitUsage, "--role is required: admin, moderator, or member (demote)")
		}
		role, err := cmd.Flags().GetString("role")
		if err != nil {
			return errs.New(errs.ExitUsage, "--role: %s", err)
		}
		// admin/moderator elevate; member demotes to a plain member — the backend
		// clears the role when it receives an explicit null (HubMembershipRoleUpdate
		// role is Literal["admin","moderator"] | None, where None = regular member).
		var roleValue any
		switch role {
		case "admin", "moderator":
			roleValue = role
		case "member":
			roleValue = nil
		default:
			return errs.New(errs.ExitUsage, "invalid --role %q: must be admin, moderator, or member (demote to plain member)", role)
		}

		c, teamID, hubID, err := hubMembershipsContext(cmd)
		if err != nil {
			return err
		}
		// PATCH .../members/{contact_id}/role with {data:{type:hub_memberships,attributes:{role}}};
		// role is null to demote to a plain member.
		res, err := c.client.ActionWith(c.ctx, client.StyleEnvelope, "PATCH",
			hubMembersPath(teamID, hubID, args[0])+"/role", map[string]any{"role": roleValue})
		if err != nil {
			return hintGlobalContactID(err)
		}
		return c.render(cmd, res)
	},
}

func init() {
	hubMembershipsCmd.AddCommand(hubMembershipsAddCmd, hubMembershipsSetRoleCmd)

	hubMembershipsAddCmd.Flags().String("role", "", "Optional elevated role: admin or moderator (omit for a plain member).")
	hubMembershipsSetRoleCmd.Flags().String("role", "", "New role: admin, moderator, or member (demote to plain member). Required.")
}
