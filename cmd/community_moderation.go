package cmd

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var (
	queueSortValues        = map[string]bool{"last_reported_at": true, "-last_reported_at": true, "report_count": true, "-report_count": true}
	reportableTypeValues   = map[string]bool{"discussion": true, "comment": true, "member": true, "message": true}
	bannedSortValues       = map[string]bool{"banned_at": true, "-banned_at": true}
	removedSortValues      = map[string]bool{"removed_at": true, "-removed_at": true}
	moderationContentTypes = map[string]bool{"discussion": true, "comment": true}
	resolutionValues       = map[string]bool{"dismissed": true, "deleted": true, "warned": true, "banned": true, "soft_banned": true}
	banReasonCodeValues    = map[string]bool{"terms_violation": true, "spamming": true, "compromised_account": true}
	commentTargetTypes     = map[string]bool{"discussion": true, "content_node": true, "section": true}
)

func setEnumQuery(cmd *cobra.Command, query url.Values, flagName, key string, allowed map[string]bool, allowedDesc string) error {
	if !cmd.Flags().Changed(flagName) {
		return nil
	}
	v, err := cmd.Flags().GetString(flagName)
	if err != nil {
		return errs.New(errs.ExitUsage, "--%s: %s", flagName, err)
	}
	if v != "" && !allowed[v] {
		return errs.New(errs.ExitUsage, "invalid --%s %q: must be %s", flagName, v, allowedDesc)
	}
	if v != "" {
		query.Set(key, v)
	}
	return nil
}

func setNonNegativeInt(cmd *cobra.Command, attrs map[string]any, flagName string) error {
	if !cmd.Flags().Changed(flagName) {
		return nil
	}
	v, err := cmd.Flags().GetInt(flagName)
	if err != nil {
		return errs.New(errs.ExitUsage, "--%s: %s", flagName, err)
	}
	if v < 0 {
		return errs.New(errs.ExitUsage, "invalid --%s %d: must be >= 0", flagName, v)
	}
	attrs[attrKey(flagName)] = v
	return nil
}

func setStringQuery(cmd *cobra.Command, query url.Values, flagName, key string) {
	if !cmd.Flags().Changed(flagName) {
		return
	}
	if v, err := cmd.Flags().GetString(flagName); err == nil && v != "" {
		query.Set(key, v)
	}
}

func rawDataEnvelope(typ, id string, attrs map[string]any) map[string]any {
	data := map[string]any{"type": typ, "attributes": attrs}
	if id != "" {
		data["id"] = id
	}
	return map[string]any{"data": data}
}

var communityReportReasonsCmd = &cobra.Command{
	Use:   "report-reasons",
	Short: "Manage community report reasons.",
	Long:  "List, create, update and delete the report reasons offered to members when they flag content. Requires team-admin privileges.",
}

func reportReasonsPath(teamID, hubID, id string) string {
	base := communityAdminBase(teamID, hubID) + "/report-reasons"
	if id != "" {
		return base + "/" + id
	}
	return base
}

var communityReportReasonsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List report reasons for a hub.",
	Long:    "List report reasons visible to the hub (platform defaults plus hub-scoped reasons, including inactive ones).",
	Example: `  mio community report-reasons list --hub hub_abc123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		col, err := c.client.List(c.ctx, reportReasonsPath(teamID, hubID, ""), url.Values{})
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var communityReportReasonsCreateCmd = &cobra.Command{
	Use:     "create",
	Short:   "Create a hub-scoped report reason.",
	Long:    "Create a new report reason for the hub. --label is required (1-80 chars); --position controls display order.",
	Example: `  mio community report-reasons create --hub hub_abc123 --label "Spam" --position 0`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !cmd.Flags().Changed("label") {
			return errs.New(errs.ExitUsage, "--label is required to create a report reason")
		}
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "label")
		if err := setNonNegativeInt(cmd, attrs, "position"); err != nil {
			return err
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, reportReasonsPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communityReportReasonsUpdateCmd = &cobra.Command{
	Use:     "update <reason_id>",
	Short:   "Update a hub-scoped report reason.",
	Long:    "Partially update a hub-scoped report reason. Only the flags you provide are changed. Platform-default reasons cannot be modified.",
	Example: `  mio community report-reasons update rr_abc --hub hub_abc123 --label "Harassment" --is-active=false`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "label")
		if err := setNonNegativeInt(cmd, attrs, "position"); err != nil {
			return err
		}
		setBoolFlag(cmd, attrs, "is-active")
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one of --label, --position, --is-active")
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		body := rawDataEnvelope("report_reasons", args[0], attrs)
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "PATCH", reportReasonsPath(teamID, hubID, args[0]), body)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communityReportReasonsDeleteCmd = &cobra.Command{
	Use:     "delete <reason_id>",
	Short:   "Delete a hub-scoped report reason.",
	Long:    "Soft-delete (deactivate) a hub-scoped report reason. Platform-default reasons cannot be deleted. Pass --yes to skip the confirmation prompt.",
	Example: `  mio community report-reasons delete rr_abc --hub hub_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Delete report reason %s?", args[0])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, reportReasonsPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted report reason %s.\n", args[0])
		return nil
	},
}

var communityCommentsCmd = &cobra.Command{
	Use:   "comments",
	Short: "Moderate community comments.",
	Long:  "List (including tombstoned) and admin-delete comments within a hub. Requires team-admin privileges.",
}

func commentsAdminPath(teamID, hubID, id string) string {
	base := communityAdminBase(teamID, hubID) + "/comments"
	if id != "" {
		return base + "/" + id
	}
	return base
}

var communityCommentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List comments on a target within a hub.",
	Long: `List comments (including tombstoned/deleted ones) for a comment target.

Both --target-type and --target-id are required to return results: the backend
returns an empty list when either is omitted (it avoids a full-hub scan).`,
	Example: `  mio community comments list --hub hub_abc123 --target-type discussion --target-id disc_xyz`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		query := url.Values{}
		if err := setEnumQuery(cmd, query, "target-type", "filter[target_type]", commentTargetTypes,
			"discussion, content_node, or section"); err != nil {
			return err
		}
		setStringQuery(cmd, query, "target-id", "filter[target_id]")

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, commentsAdminPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var communityCommentsDeleteCmd = &cobra.Command{
	Use:     "delete <comment_id>",
	Short:   "Admin-delete a comment.",
	Long:    "Delete a comment by id, bypassing the author check. Pass --yes to skip the confirmation prompt.",
	Example: `  mio community comments delete cmt_abc --hub hub_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Delete comment %s?", args[0])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, commentsAdminPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted comment %s.\n", args[0])
		return nil
	},
}

var communityModerationCmd = &cobra.Command{
	Use:   "moderation",
	Short: "Community moderation console.",
	Long:  "Inspect the moderation queue, counts, audit log, banned members, removed content, individual reports, and take content/report actions. Requires team-admin privileges.",
}

func moderationPath(teamID, hubID, suffix string) string {
	return communityAdminBase(teamID, hubID) + "/moderation/" + suffix
}

var communityModerationQueueCmd = &cobra.Command{
	Use:   "queue",
	Short: "List the duplicate-collapsed moderation queue.",
	Long:  "List pending reports, collapsed to one entry per reported target. Filter by status and reportable type; sort and paginate.",
	Example: `  mio community moderation queue --hub hub_abc123
  mio community moderation queue --hub hub_abc123 --reportable-type comment --sort -report_count`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		query := url.Values{}
		setStringQuery(cmd, query, "status", "filter[status]")
		if err := setEnumQuery(cmd, query, "reportable-type", "filter[reportable_type]", reportableTypeValues,
			"discussion, comment, member, or message"); err != nil {
			return err
		}
		if err := setEnumQuery(cmd, query, "sort", "sort", queueSortValues,
			"last_reported_at, -last_reported_at, report_count, or -report_count"); err != nil {
			return err
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, moderationPath(teamID, hubID, "queue"), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var communityModerationCountsCmd = &cobra.Command{
	Use:     "counts",
	Short:   "Show queue / removed / banned totals.",
	Long:    "Return the full-pagination totals for the queue, removed-content, and banned-member lists.",
	Example: `  mio community moderation counts --hub hub_abc123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, moderationPath(teamID, hubID, "counts"))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communityModerationAuditLogCmd = &cobra.Command{
	Use:   "audit-log",
	Short: "List the moderation audit log.",
	Long:  "List moderation actions taken in the hub, newest first. Filter by action type or admin actor; paginate.",
	Example: `  mio community moderation audit-log --hub hub_abc123
  mio community moderation audit-log --hub hub_abc123 --action-type ban_member`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		query := url.Values{}
		setStringQuery(cmd, query, "action-type", "filter[action_type]")
		setStringQuery(cmd, query, "admin-user-id", "filter[admin_user_id]")

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, moderationPath(teamID, hubID, "audit-log"), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var communityModerationBannedCmd = &cobra.Command{
	Use:     "banned",
	Short:   "List banned members.",
	Long:    "List members banned from the hub. Sort by banned_at direction; paginate.",
	Example: `  mio community moderation banned --hub hub_abc123 --sort banned_at`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		query := url.Values{}
		if err := setEnumQuery(cmd, query, "sort", "sort", bannedSortValues,
			"banned_at or -banned_at"); err != nil {
			return err
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, moderationPath(teamID, hubID, "banned-members"), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var communityModerationRemovedCmd = &cobra.Command{
	Use:   "removed",
	Short: "List removed content.",
	Long:  "List discussions/comments that have been removed (soft-hidden) in the hub. Filter by content type; sort and paginate.",
	Example: `  mio community moderation removed --hub hub_abc123
  mio community moderation removed --hub hub_abc123 --content-type comment`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		query := url.Values{}
		if err := setEnumQuery(cmd, query, "content-type", "filter[content_type]", moderationContentTypes,
			"discussion or comment"); err != nil {
			return err
		}
		if err := setEnumQuery(cmd, query, "sort", "sort", removedSortValues,
			"removed_at or -removed_at"); err != nil {
			return err
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, moderationPath(teamID, hubID, "removed"), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var communityModerationContentCmd = &cobra.Command{
	Use:   "content",
	Short: "Inspect and remove/restore reported content.",
	Long:  "View the full body of a discussion or comment (live or removed), soft-remove it, or restore it.",
}

func moderationContentPath(teamID, hubID, contentType, contentID, action string) string {
	p := moderationPath(teamID, hubID, "content/"+contentType+"/"+contentID)
	if action != "" {
		p += "/" + action
	}
	return p
}

func validateContentType(contentType string) error {
	if !moderationContentTypes[contentType] {
		return errs.New(errs.ExitUsage, "invalid content type %q: must be discussion or comment", contentType)
	}
	return nil
}

var communityModerationContentViewCmd = &cobra.Command{
	Use:     "view <content_type> <content_id>",
	Short:   "View a discussion or comment (live or removed).",
	Long:    "Fetch the full body and metadata of a discussion or comment, whether or not it has been removed. content_type is discussion or comment.",
	Example: `  mio community moderation content view comment cmt_abc --hub hub_abc123`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateContentType(args[0]); err != nil {
			return err
		}
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, moderationContentPath(teamID, hubID, args[0], args[1], ""))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communityModerationContentRemoveCmd = &cobra.Command{
	Use:     "remove <content_type> <content_id>",
	Short:   "Remove (soft-hide) a discussion or comment.",
	Long:    "Soft-hide a discussion or comment so members can no longer see it. content_type is discussion or comment. Pass --yes to skip the confirmation prompt.",
	Example: `  mio community moderation content remove comment cmt_abc --hub hub_abc123 --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateContentType(args[0]); err != nil {
			return err
		}
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Remove %s %s?", args[0], args[1])); err != nil {
			return err
		}
		res, err := c.client.Action(c.ctx, "POST", moderationContentPath(teamID, hubID, args[0], args[1], "remove"), nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s %s.\n", args[0], args[1])
			return nil
		}
		return c.render(cmd, res)
	},
}

var communityModerationContentRestoreCmd = &cobra.Command{
	Use:     "restore <content_type> <content_id>",
	Short:   "Restore a removed discussion or comment.",
	Long:    "Un-hide a previously removed discussion or comment. content_type is discussion or comment.",
	Example: `  mio community moderation content restore comment cmt_abc --hub hub_abc123`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateContentType(args[0]); err != nil {
			return err
		}
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Action(c.ctx, "POST", moderationContentPath(teamID, hubID, args[0], args[1], "restore"), nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Restored %s %s.\n", args[0], args[1])
			return nil
		}
		return c.render(cmd, res)
	},
}

var communityModerationReportsCmd = &cobra.Command{
	Use:   "reports",
	Short: "Inspect and resolve moderation reports.",
	Long:  "Fetch a single moderation report or resolve a pending one.",
}

var communityModerationReportsGetCmd = &cobra.Command{
	Use:     "get <report_id>",
	Short:   "Fetch a moderation report.",
	Long:    "Fetch a single moderation report by id (admin view).",
	Example: `  mio community moderation reports get rep_abc --hub hub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, moderationPath(teamID, hubID, "reports/"+args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communityModerationReportsResolveCmd = &cobra.Command{
	Use:   "resolve <report_id>",
	Short: "Resolve a pending moderation report.",
	Long: `Resolve a pending moderation report. --resolution is required and must be one
of: dismissed, deleted, warned, banned, soft_banned. For soft_banned you may
pass --soft-ban-until (ISO 8601); --notes records an admin note.`,
	Example: `  mio community moderation reports resolve rep_abc --hub hub_abc123 --resolution dismissed --yes
  mio community moderation reports resolve rep_abc --hub hub_abc123 --resolution soft_banned --soft-ban-until 2026-08-01T00:00:00Z --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("resolution") {
			return errs.New(errs.ExitUsage, "--resolution is required: dismissed, deleted, warned, banned, or soft_banned")
		}
		resolution, err := cmd.Flags().GetString("resolution")
		if err != nil {
			return errs.New(errs.ExitUsage, "--resolution: %s", err)
		}
		if !resolutionValues[resolution] {
			return errs.New(errs.ExitUsage, "invalid --resolution %q: must be dismissed, deleted, warned, banned, or soft_banned", resolution)
		}
		attrs := map[string]any{"resolution": resolution}
		setStringFlag(cmd, attrs, "notes")
		setMappedString(cmd, attrs, "soft-ban-until", "soft_ban_until")

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Resolve report %s as %q?", args[0], resolution)); err != nil {
			return err
		}
		body := rawDataEnvelope("moderation_reports", "", attrs)
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "PATCH", moderationPath(teamID, hubID, "reports/"+args[0]), body)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communityMembersSoftBanCmd = &cobra.Command{
	Use:   "soft-ban <contact_id>",
	Short: "Soft-ban (temporarily suspend) a hub member.",
	Long: `Issue a standalone soft (temporary) ban on a hub member. Pass --until (ISO
8601) to set the expiry; the backend defaults to now + 7 days when omitted.
--reason is a canonical ban-reason code; --notes records an admin note.

<contact_id> is the GLOBAL contact id, NOT the .id from 'mio contacts'. Capture
it with:
  CID=$(mio contacts retrieve <team-contact-id> -o plain --jq .contact_id)`,
	Example: `  mio community members soft-ban contact_xyz --hub hub_abc123 --yes
  mio community members soft-ban contact_xyz --hub hub_abc123 --reason spamming --until 2026-08-01T00:00:00Z --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "notes")
		setMappedString(cmd, attrs, "until", "soft_ban_until")
		if cmd.Flags().Changed("reason") {
			reason, err := cmd.Flags().GetString("reason")
			if err != nil {
				return errs.New(errs.ExitUsage, "--reason: %s", err)
			}
			if reason != "" && !banReasonCodeValues[reason] {
				return errs.New(errs.ExitUsage, "invalid --reason %q: must be terms_violation, spamming, or compromised_account", reason)
			}
			if reason != "" {
				attrs["reason"] = reason
			}
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Soft-ban member %s?", args[0])); err != nil {
			return err
		}
		path := memberActionPath(teamID, hubID, args[0], "soft_ban")
		body := rawDataEnvelope("moderation_actions", "", attrs)
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, body)
		if err != nil {
			return hintGlobalContactID(err)
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Soft-banned member %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	communityReportReasonsCmd.AddCommand(
		communityReportReasonsListCmd,
		communityReportReasonsCreateCmd,
		communityReportReasonsUpdateCmd,
		communityReportReasonsDeleteCmd,
	)
	communityCmd.AddCommand(communityReportReasonsCmd)

	communityReportReasonsCreateCmd.Flags().String("label", "", "Reason label shown to members (1-80 chars). Required.")
	communityReportReasonsCreateCmd.Flags().Int("position", 0, "Zero-based display position.")
	communityReportReasonsUpdateCmd.Flags().String("label", "", "New reason label (1-80 chars).")
	communityReportReasonsUpdateCmd.Flags().Int("position", 0, "New zero-based display position.")
	communityReportReasonsUpdateCmd.Flags().Bool("is-active", true, "Whether the reason is active.")

	communityCommentsCmd.AddCommand(communityCommentsListCmd, communityCommentsDeleteCmd)
	communityCmd.AddCommand(communityCommentsCmd)

	communityCommentsListCmd.Flags().String("target-type", "", "Comment target type: discussion, content_node, or section.")
	communityCommentsListCmd.Flags().String("target-id", "", "Comment target id (required together with --target-type to return results).")
	addPaginationFlags(communityCommentsListCmd)

	communityModerationContentCmd.AddCommand(
		communityModerationContentViewCmd,
		communityModerationContentRemoveCmd,
		communityModerationContentRestoreCmd,
	)
	communityModerationReportsCmd.AddCommand(
		communityModerationReportsGetCmd,
		communityModerationReportsResolveCmd,
	)
	communityModerationCmd.AddCommand(
		communityModerationQueueCmd,
		communityModerationCountsCmd,
		communityModerationAuditLogCmd,
		communityModerationBannedCmd,
		communityModerationRemovedCmd,
		communityModerationContentCmd,
		communityModerationReportsCmd,
	)
	communityCmd.AddCommand(communityModerationCmd)

	communityModerationQueueCmd.Flags().String("status", "", "Filter by report status (default pending).")
	communityModerationQueueCmd.Flags().String("reportable-type", "", "Filter by reportable type: discussion, comment, member, or message.")
	communityModerationQueueCmd.Flags().String("sort", "", "Sort order: last_reported_at, -last_reported_at, report_count, or -report_count.")
	addPaginationFlags(communityModerationQueueCmd)

	communityModerationAuditLogCmd.Flags().String("action-type", "", "Filter by action type (e.g. ban_member, warn_member).")
	communityModerationAuditLogCmd.Flags().String("admin-user-id", "", "Filter by the admin actor id.")
	addPaginationFlags(communityModerationAuditLogCmd)

	communityModerationBannedCmd.Flags().String("sort", "", "Sort order: banned_at or -banned_at.")
	addPaginationFlags(communityModerationBannedCmd)

	communityModerationRemovedCmd.Flags().String("content-type", "", "Filter by content type: discussion or comment.")
	communityModerationRemovedCmd.Flags().String("sort", "", "Sort order: removed_at or -removed_at.")
	addPaginationFlags(communityModerationRemovedCmd)

	communityModerationReportsResolveCmd.Flags().String("resolution", "", "Resolution: dismissed, deleted, warned, banned, or soft_banned. Required.")
	communityModerationReportsResolveCmd.Flags().String("notes", "", "Optional admin note for the resolution.")
	communityModerationReportsResolveCmd.Flags().String("soft-ban-until", "", "For soft_banned: ISO 8601 expiry timestamp.")

	communityMembersCmd.AddCommand(communityMembersSoftBanCmd)
	communityMembersSoftBanCmd.Flags().String("notes", "", "Optional admin note for the soft ban.")
	communityMembersSoftBanCmd.Flags().String("reason", "", "Ban reason code: terms_violation, spamming, or compromised_account.")
	communityMembersSoftBanCmd.Flags().String("until", "", "ISO 8601 expiry timestamp (defaults to now + 7 days).")
}
