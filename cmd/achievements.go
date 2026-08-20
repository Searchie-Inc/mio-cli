package cmd

// achievements.go implements the `mio achievements` command group — the
// team-scoped ADMIN surface of the achievements module (MIO-3054 Phase 1,
// CLI parity MIO-3412).
//
// Routes (mio-backend app/achievements/admin_router.py, prefix
// /api/teams/{team_id}):
//
//	create           POST    /achievements
//	list             GET     /achievements
//	retrieve         GET     /achievements/{id}
//	update           PATCH   /achievements/{id}
//	archive          DELETE  /achievements/{id}                                   (204)
//	offerings list   GET     /hubs/{hub_id}/achievements
//	offerings attach POST    /hubs/{hub_id}/achievements
//	offerings detach DELETE  /hubs/{hub_id}/achievements/{achievement_id}         (204)
//	grant            POST    /hubs/{hub_id}/members/{contact_id}/achievements     (201 new / 200 repeat)
//	revoke           DELETE  /hubs/{hub_id}/members/{contact_id}/achievements/{achievement_id}  (204; ?reason= query)
//	restore          POST    /hubs/{hub_id}/members/{contact_id}/achievements/{achievement_id}/restore
//
// AUTH: every route above requires the TEAM principal (require_team_owner on
// the backend) — a plain team API key works, so this group uses the standard
// newContext + requireAuth + requireTeam boilerplate. The MEMBER-facing reads
// (/api/hubs/{hub_id}/achievements etc., app/achievements/router.py) require a
// contact identity instead and are deliberately NOT covered here — same
// boundary as `mio events` (MIO-3173), see that file's package doc comment.
//
// FEATURE GATES: the module ships dark behind TWO backend gates that must both
// pass — the global ACHIEVEMENTS_ENABLED setting (app/config.py, default
// False) and the per-hub `hub.settings.achievements.enabled is True` flag.
// While either gate is closed every route above 404s with one generic message
// (deliberately indistinguishable from a missing hub), so `mio achievements *`
// exits 4 — that is the backend's answer, not a CLI defect. The per-hub flag
// is set with:
//
//	mio hubs update <hub> --settings-json '{"achievements":{"enabled":true}}'
//
// (`achievements` is an allowlisted settings key — see hubs_blob_keys.go; the
// merge is read-modify-write so sibling settings survive.)
//
// JSON:API write envelope types (app/achievements/schemas.py Literals):
// definitions self-derive "achievements" from the path; the hub-offering and
// earn writes need the typeOverrides entries {"hubs/achievements":
// "achievement_hubs"} and {"members/achievements": "achievement_earns"} in
// internal/client/client.go, plus "achievements" in knownCollections. The
// restore path (.../achievements/{id}/restore) resolves through the SAME
// members/achievements override because "restore" is deliberately not a known
// collection token (the transcript/revert precedent).
//
// revoke's optional reason travels as a QUERY parameter (?reason=…, backend
// alias for revoke_reason) on a body-less DELETE — client.DeleteWithQuery.
// restore's envelope is REQUIRED by the backend even when restore_reason is
// omitted, so the CLI always sends a non-nil (possibly empty) attributes map.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// achievements <action>
	achievementsCmd.AddCommand(
		achievementsCreateCmd,
		achievementsListCmd,
		achievementsRetrieveCmd,
		achievementsUpdateCmd,
		achievementsArchiveCmd,
		achievementsGrantCmd,
		achievementsRevokeCmd,
		achievementsRestoreCmd,
	)

	// achievements offerings <action>  (which achievements a hub offers)
	achievementsOfferingsCmd.AddCommand(
		achievementsOfferingsListCmd,
		achievementsOfferingsAttachCmd,
		achievementsOfferingsDetachCmd,
	)
	achievementsCmd.AddCommand(achievementsOfferingsCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(achievementsCmd)
}

// ---- achievements group -----------------------------------------------------

var achievementsCmd = &cobra.Command{
	Use:   "achievements",
	Short: "Manage achievement badges (definitions, hub offerings, manual awards).",
	Long: `Manage the team's achievement badge definitions, attach them to hubs as
offerings, and manually grant/revoke/restore member earns.

The achievements module ships dark: the backend 404s every route below until
BOTH its global ACHIEVEMENTS_ENABLED setting and the per-hub flag are on. Turn
the per-hub flag on with:

  mio hubs update <hub> --settings-json '{"achievements":{"enabled":true}}'

Definitions are team-scoped; offerings and awards are additionally hub-scoped
(--hub or a configured current_hub).`,
	Example: `  mio achievements create --title "First Post" --points 10
  mio achievements list
  mio achievements offerings attach ach_abc123 --hub hub_123
  mio achievements grant ach_abc123 --hub hub_123 --contact-id ct_456`,
}

// achievementsPath returns /api/teams/{team_id}/achievements[/{id}].
func achievementsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/achievements", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// achievementsOfferingsPath returns
// /api/teams/{team_id}/hubs/{hub_id}/achievements[/{achievement_id}].
func achievementsOfferingsPath(teamID, hubID, achievementID string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/achievements", teamID, hubID)
	if achievementID != "" {
		return base + "/" + achievementID
	}
	return base
}

// achievementsEarnPath returns
// /api/teams/{team_id}/hubs/{hub_id}/members/{contact_id}/achievements[/{achievement_id}].
func achievementsEarnPath(teamID, hubID, contactID, achievementID string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/members/%s/achievements", teamID, hubID, contactID)
	if achievementID != "" {
		return base + "/" + achievementID
	}
	return base
}

// achievementsContext is the shared boilerplate for team-scoped achievements
// sub-commands: context, auth, team.
func achievementsContext(cmd *cobra.Command) (*cmdContext, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", err
	}
	if err := c.requireAuth(); err != nil {
		return nil, "", err
	}
	teamID, err := c.requireTeam()
	if err != nil {
		return nil, "", err
	}
	return c, teamID, nil
}

// achievementsHubContext additionally resolves the hub scope (offerings and
// earn verbs).
func achievementsHubContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
	c, teamID, err := achievementsContext(cmd)
	if err != nil {
		return nil, "", "", err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, "", "", err
	}
	return c, teamID, hubID, nil
}

// requireContactID reads the required --contact-id flag for the earn verbs.
func requireContactID(cmd *cobra.Command) (string, error) {
	if !cmd.Flags().Changed("contact-id") {
		return "", errs.New(errs.ExitUsage, "missing required flag: --contact-id")
	}
	v, err := cmd.Flags().GetString("contact-id")
	if err != nil {
		return "", errs.Wrap(errs.ExitGeneric, err)
	}
	if strings.TrimSpace(v) == "" {
		return "", errs.New(errs.ExitUsage, "--contact-id must not be empty")
	}
	return v, nil
}

// achievementsAttrFlags is the set of create/update string flags, shared so
// both commands stay in lockstep. attrKey (see flags.go) translates each
// kebab-case flag name to its snake_case attribute key (e.g. --award-mode ->
// award_mode).
var achievementsAttrFlags = []string{"title", "description", "award-mode", "category"}

// setAchievementAttrs copies every changed create/update flag into attrs. Only
// flags the user actually set are copied (the set*Flag helpers no-op on an
// unset flag), so a PATCH stays a partial update and no field is ever sent as
// an explicit null. --appearance-json is forwarded WHOLESALE as the
// `appearance` object — the backend validates shape/icon/colors strictly
// (extra=forbid), and the CLI is a conduit, not a second validator.
func setAchievementAttrs(cmd *cobra.Command, attrs map[string]any) error {
	for _, f := range achievementsAttrFlags {
		setStringFlag(cmd, attrs, f)
	}
	setIntFlag(cmd, attrs, "points")
	setBoolFlag(cmd, attrs, "is-secret")
	setBoolFlag(cmd, attrs, "is-active")
	setBoolFlag(cmd, attrs, "email-notification-enabled")

	appearance, err := parseJSONObjectFlag(cmd, "appearance-json")
	if err != nil {
		return err
	}
	if appearance != nil {
		attrs["appearance"] = appearance
	}
	return nil
}

// ---- create -----------------------------------------------------------------

var achievementsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an achievement definition.",
	Long: `Create a new achievement badge definition for the active team.

--title is required; everything else defaults server-side (award_mode=manual,
points=0, is_secret=false, is_active=true, email_notification_enabled=true).

--appearance-json accepts the badge appearance object (inline JSON or @file),
validated strictly by the backend: keys shape, icon, emoji, text, image_src,
color, fill_color, content_color. The badge renders exactly ONE content slot,
picked in the order emoji > text > image_src > icon.`,
	Example: `  mio achievements create --title "First Post" --description "Posted for the first time" --points 10
  mio achievements create --title "Insider" --is-secret --category community \
    --appearance-json '{"shape":"hexagon","emoji":"🏆","color":"#5581f4"}'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := achievementsContext(cmd)
		if err != nil {
			return err
		}

		// --title is the only backend-required create attribute; validate
		// client-side so a missing-required body never reaches the API.
		if !cmd.Flags().Changed("title") {
			return errs.New(errs.ExitUsage, "missing required flag: --title")
		}

		attrs := map[string]any{}
		if err := setAchievementAttrs(cmd, attrs); err != nil {
			return err
		}

		res, err := c.client.Create(c.ctx, achievementsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- list -------------------------------------------------------------------

var achievementsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List achievement definitions.",
	Long: `List the team's achievement definitions (cursor-paginated).

--award-mode and --category filter server-side (filter[award_mode] /
filter[category]); values are forwarded verbatim.`,
	Example: `  mio achievements list
  mio achievements list --award-mode manual --category community --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := achievementsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)
		if cmd.Flags().Changed("award-mode") {
			v, _ := cmd.Flags().GetString("award-mode")
			query.Set("filter[award_mode]", v)
		}
		if cmd.Flags().Changed("category") {
			v, _ := cmd.Flags().GetString("category")
			query.Set("filter[category]", v)
		}

		col, err := c.client.List(c.ctx, achievementsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- retrieve ---------------------------------------------------------------

var achievementsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an achievement definition by id.",
	Long:    "Retrieve a single achievement definition. 404s if missing, archived, or cross-team.",
	Example: `  mio achievements retrieve ach_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := achievementsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, achievementsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- update -----------------------------------------------------------------

var achievementsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an achievement definition by id.",
	Long: `Partially update an achievement definition. Only the flags you supply are
changed (PATCH semantics) — an unset flag is never sent, so it can never be
null'd out by accident.

--appearance-json replaces the whole appearance object when supplied.`,
	Example: `  mio achievements update ach_abc123 --title "New Title" --points 25
  mio achievements update ach_abc123 --is-active=false`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := achievementsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		if err := setAchievementAttrs(cmd, attrs); err != nil {
			return err
		}
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, achievementsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- archive ----------------------------------------------------------------

var achievementsArchiveCmd = &cobra.Command{
	Use:   "archive <id>",
	Short: "Archive an achievement definition.",
	Long: `Archive (soft-delete) an achievement definition. The definition disappears
from lists and can no longer be granted; existing earns are unaffected.

Pass --yes to skip the confirmation prompt in non-interactive environments
(scripts, CI, AI agents).`,
	Example: `  mio achievements archive ach_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := achievementsContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Archive achievement %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, achievementsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Archived achievement %s.\n", args[0])
		return nil
	},
}

// ---- offerings sub-resource (which achievements a hub offers) ---------------

var achievementsOfferingsCmd = &cobra.Command{
	Use:   "offerings",
	Short: "Manage which achievements a hub offers.",
	Long: `Attach team achievement definitions to a hub (making them grantable and
visible there), list a hub's offerings, or detach one.

Hub-scoped: --hub is required (or a configured current_hub).`,
}

var achievementsOfferingsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List the achievements offered in a hub.",
	Long:    "List the hub's achievement offerings (cursor-paginated).",
	Example: `  mio achievements offerings list --hub hub_123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, achievementsOfferingsPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var achievementsOfferingsAttachCmd = &cobra.Command{
	Use:     "attach <achievement_id>",
	Short:   "Offer an achievement in a hub.",
	Long:    "Attach an achievement definition to the hub, making it grantable and visible there.",
	Example: `  mio achievements offerings attach ach_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{"achievement_id": args[0]}
		res, err := c.client.Create(c.ctx, achievementsOfferingsPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var achievementsOfferingsDetachCmd = &cobra.Command{
	Use:   "detach <achievement_id>",
	Short: "Stop offering an achievement in a hub.",
	Long: `Detach an achievement from the hub. It stops being grantable and visible
there; the definition itself and any existing earns are unaffected.

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio achievements offerings detach ach_abc123 --hub hub_123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Detach achievement %s from this hub?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, achievementsOfferingsPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Detached achievement %s.\n", args[0])
		return nil
	},
}

// ---- grant / revoke / restore (manual earns) --------------------------------

var achievementsGrantCmd = &cobra.Command{
	Use:   "grant <achievement_id>",
	Short: "Manually award an achievement to a hub member.",
	Long: `Manually award an achievement to an active hub member. Idempotent: granting
an already-earned achievement returns the existing earn unchanged (200).

The achievement must be offered in the hub (see 'mio achievements offerings')
and its award_mode must be manual. Granting over a previously REVOKED earn is
a 409 — use 'mio achievements restore' instead.

--reason is stored as the earn's award_reason (defaults to "manual").`,
	Example: `  mio achievements grant ach_abc123 --hub hub_123 --contact-id ct_456
  mio achievements grant ach_abc123 --hub hub_123 --contact-id ct_456 --reason "manual: community week winner"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}
		contactID, err := requireContactID(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{"achievement_id": args[0]}
		setMappedString(cmd, attrs, "reason", "award_reason")

		res, err := c.client.Create(c.ctx, achievementsEarnPath(teamID, hubID, contactID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var achievementsRevokeCmd = &cobra.Command{
	Use:   "revoke <achievement_id>",
	Short: "Revoke a member's earned achievement.",
	Long: `Soft-revoke a member's earned achievement. Idempotent — revoking an
already-revoked earn still succeeds. Reversible via 'mio achievements restore'.

--reason travels as the ?reason= query parameter (the DELETE has no body).

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio achievements revoke ach_abc123 --hub hub_123 --contact-id ct_456 --yes
  mio achievements revoke ach_abc123 --hub hub_123 --contact-id ct_456 --reason "granted in error" --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}
		contactID, err := requireContactID(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Revoke achievement %s for contact %s?", args[0], contactID)); err != nil {
			return err
		}

		query := url.Values{}
		if cmd.Flags().Changed("reason") {
			v, _ := cmd.Flags().GetString("reason")
			query.Set("reason", v)
		}

		if err := c.client.DeleteWithQuery(c.ctx, achievementsEarnPath(teamID, hubID, contactID, args[0]), query); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Revoked achievement %s for contact %s.\n", args[0], contactID)
		return nil
	},
}

var achievementsRestoreCmd = &cobra.Command{
	Use:   "restore <achievement_id>",
	Short: "Restore a previously revoked earn.",
	Long: `Restore a member's previously revoked achievement earn — the repair path for
an accidental revoke. Restoring an earn that is not revoked is a 409.

--reason is stored as the earn's restore_reason.`,
	Example: `  mio achievements restore ach_abc123 --hub hub_123 --contact-id ct_456
  mio achievements restore ach_abc123 --hub hub_123 --contact-id ct_456 --reason "revoked in error"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}
		contactID, err := requireContactID(cmd)
		if err != nil {
			return err
		}

		// The backend REQUIRES the JSON:API envelope on restore even when
		// restore_reason is omitted (AchievementRestoreEnvelope is a required
		// body param), so attrs must be non-nil — a nil body would send no
		// envelope at all and 422.
		attrs := map[string]any{}
		setMappedString(cmd, attrs, "reason", "restore_reason")

		path := achievementsEarnPath(teamID, hubID, contactID, args[0]) + "/restore"
		res, err := c.client.Action(c.ctx, "POST", path, attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Restored achievement %s for contact %s.\n", args[0], contactID)
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- flag registration -------------------------------------------------------

func init() {
	// Attribute flags for create/update.
	for _, cmd := range []*cobra.Command{achievementsCreateCmd, achievementsUpdateCmd} {
		cmd.Flags().String("title", "", "Badge title.")
		cmd.Flags().String("description", "", "Badge description.")
		cmd.Flags().String("award-mode", "", "Award mode (Phase 1 grants require: manual).")
		cmd.Flags().String("category", "", "Free-form category label.")
		cmd.Flags().Int("points", 0, "Points awarded with the badge (>= 0).")
		cmd.Flags().Bool("is-secret", false, "Hide the badge from members until earned.")
		cmd.Flags().Bool("is-active", true, "Whether the badge is active (grantable).")
		cmd.Flags().Bool("email-notification-enabled", true, "Email the member when the badge is earned.")
		cmd.Flags().String("appearance-json", "", "Badge appearance as a JSON object (inline or @file): shape, icon, emoji, text, image_src, color, fill_color, content_color.")
	}

	// Pagination + filters on the lists.
	addPaginationFlags(achievementsListCmd)
	achievementsListCmd.Flags().String("award-mode", "", "Filter by award mode (filter[award_mode]).")
	achievementsListCmd.Flags().String("category", "", "Filter by category (filter[category]).")
	addPaginationFlags(achievementsOfferingsListCmd)

	// Earn verbs: contact scope + reason.
	for _, cmd := range []*cobra.Command{achievementsGrantCmd, achievementsRevokeCmd, achievementsRestoreCmd} {
		cmd.Flags().String("contact-id", "", "GLOBAL contact id (.attributes.contact_id from 'mio contacts', NOT its .id) of the hub member. Required.")
		cmd.Flags().String("reason", "", "Audit reason recorded with the action.")
	}
}
