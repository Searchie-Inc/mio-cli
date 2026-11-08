package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	achievementsCmd.AddCommand(
		achievementsCreateCmd,
		achievementsListCmd,
		achievementsRetrieveCmd,
		achievementsUpdateCmd,
		achievementsArchiveCmd,
		achievementsGrantCmd,
		achievementsRevokeCmd,
		achievementsRestoreCmd,
		achievementsOverrideCmd,
	)

	achievementsOfferingsCmd.AddCommand(
		achievementsOfferingsListCmd,
		achievementsOfferingsAttachCmd,
		achievementsOfferingsDetachCmd,
	)
	achievementsCmd.AddCommand(achievementsOfferingsCmd)

	achievementsRuleCmd.AddCommand(
		achievementsRuleSetCmd,
		achievementsRuleDeleteCmd,
	)
	achievementsCmd.AddCommand(achievementsRuleCmd)

	rootCmd.AddCommand(achievementsCmd)
}

var achievementsCmd = &cobra.Command{
	Use:   "achievements",
	Short: "Manage achievement badges (definitions, hub offerings, manual awards).",
	Long: `Manage the team's achievement badge definitions, attach them to hubs as
offerings, and manually grant/revoke/restore member earns.

The achievements module ships dark, behind a SPLIT gate: the team-scoped
definition commands (create/list/retrieve/update/archive) need only the
backend's global ACHIEVEMENTS_ENABLED setting; the hub-scoped offerings and
earn commands additionally need the per-hub flag. Any closed gate answers a
generic 404 (exit 4) by design. Turn the per-hub flag on with:

  mio hubs update <hub> --settings-json '{"achievements":{"enabled":true}}'

Definitions are team-scoped; offerings and awards are additionally hub-scoped
(--hub or a configured current_hub).`,
	Example: `  mio achievements create --title "First Post" --points 10
  mio achievements list
  mio achievements offerings attach ach_abc123 --hub hub_123
  mio achievements grant ach_abc123 --hub hub_123 --contact-id ct_456`,
}

func achievementsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/achievements", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

func achievementsOfferingsPath(teamID, hubID, achievementID string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/achievements", teamID, hubID)
	if achievementID != "" {
		return base + "/" + achievementID
	}
	return base
}

func achievementsRulePath(teamID, hubID, achievementID string) string {
	return achievementsOfferingsPath(teamID, hubID, achievementID) + "/rule"
}

func achievementsEarnPath(teamID, hubID, contactID, achievementID string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/members/%s/achievements", teamID, hubID, contactID)
	if achievementID != "" {
		return base + "/" + achievementID
	}
	return base
}

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

const achievementsEarn404Hint = "this 404 is ambiguous by design; it can mean " +
	"(a) the achievements feature is off — the global ACHIEVEMENTS_ENABLED " +
	"setting and the hub's settings.achievements.enabled flag must BOTH be on " +
	"for earn routes — or the hub is not in this team, or (b) the achievement " +
	"does not exist in this team, is inactive, or (on grant) is not offered " +
	"in this hub. A wrong contact id never answers 404 here: grant answers " +
	"422 (not an active member), revoke answers 204 (idempotent no-op), " +
	"restore answers 409 (no earn exists)"

const achievementsContactCapture = "these routes need the GLOBAL contact id: capture it with " +
	"`mio contacts retrieve <team-contact-id> -o plain --jq .contact_id` " +
	"(the flattened contact_id field, NOT the row's .id)"

const achievementsGrant422Hint = "if this 422 says the contact is not an active member and you " +
	"believe they are, check the id namespace first — passing the team-contact " +
	"row's .id produces exactly that symptom; " + achievementsContactCapture

const achievementsRestore409Hint = "if this 409 says no earn exists and you believe one does, " +
	"check the contact-id namespace first — passing the team-contact row's .id " +
	"produces exactly that symptom (" + achievementsContactCapture + "). " +
	"If the earn exists but is not currently revoked, there is nothing to restore"

func hintAchievementsEarnErr(verb string, err error) error {
	if err == nil {
		return err
	}
	var hint string
	switch errs.HTTPStatusOf(err) {
	case 404:
		hint = achievementsEarn404Hint
	case 422:
		if verb == "grant" {
			hint = achievementsGrant422Hint
		}
	case 409:
		if verb == "restore" {
			hint = achievementsRestore409Hint
		}
	}
	if hint == "" {
		return err
	}
	return errs.NewHTTP(errs.HTTPStatusOf(err), "%w\nhint: %s", err, hint)
}

func requireContactID(cmd *cobra.Command) (string, error) {
	v, err := cmd.Flags().GetString("contact-id")
	if err != nil {
		return "", errs.Wrap(errs.ExitGeneric, err)
	}
	if strings.TrimSpace(v) == "" {
		return "", errs.New(errs.ExitUsage, "--contact-id must not be empty")
	}
	return v, nil
}

func requireOverrideReason(cmd *cobra.Command) (string, error) {
	v, err := cmd.Flags().GetString("reason")
	if err != nil {
		return "", errs.Wrap(errs.ExitGeneric, err)
	}
	if strings.TrimSpace(v) == "" {
		return "", errs.New(errs.ExitUsage, "--reason must not be empty or whitespace-only")
	}
	return v, nil
}

func applyGrantAwardReason(cmd *cobra.Command, attrs map[string]any) error {
	if !cmd.Flags().Changed("reason") {
		return nil
	}
	v, err := cmd.Flags().GetString("reason")
	if err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	if v != "manual" {
		return errs.New(errs.ExitUsage, "--reason must be \"manual\" — the backend only accepts award_reason=\"manual\" (MIO-3488) and records provenance itself")
	}
	attrs["award_reason"] = v
	return nil
}

var achievementsScalarFlags = []struct {
	name string
	set  func(cmd *cobra.Command, attrs map[string]any, name string)
}{
	{"title", setStringFlag},
	{"description", setStringFlag},
	{"award-mode", setStringFlag},
	{"category", setStringFlag},
	{"rule-type", setStringFlag},
	{"rule-criteria", setStringFlag},
	{"points", setIntFlag},
	{"rule-threshold", setIntFlag},
	{"rule-window-days", setIntFlag},
	{"is-secret", setBoolFlag},
	{"is-active", setBoolFlag},
	{"email-notification-enabled", setBoolFlag},
}

var achievementsClearableExtraFlags = []string{"rule-content-node-ids"}

func achievementsClearAttrKey(name string) (string, bool) {
	for _, f := range achievementsScalarFlags {
		if f.name == name {
			return attrKey(name), true
		}
	}
	for _, n := range achievementsClearableExtraFlags {
		if n == name {
			return attrKey(name), true
		}
	}
	return "", false
}

func achievementsParseClearFlag(cmd *cobra.Command) ([]string, error) {
	if !cmd.Flags().Changed("clear") {
		return nil, nil
	}
	raw, err := cmd.Flags().GetStringArray("clear")
	if err != nil {
		return nil, errs.Wrap(errs.ExitUsage, err)
	}

	var wireKeys []string
	for _, entry := range raw {
		for _, part := range strings.Split(entry, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				return nil, errs.New(errs.ExitUsage,
					"--clear %q has a blank entry (stray comma?); every comma-separated entry must be a field name", entry)
			}
			wireKey, ok := achievementsClearAttrKey(name)
			if !ok {
				return nil, errs.New(errs.ExitUsage,
					"--clear %q: unknown field; run 'mio achievements update --help' for the fields --clear accepts", name)
			}
			if cmd.Flags().Changed(name) {
				return nil, errs.New(errs.ExitUsage,
					"--clear %s conflicts with --%s: cannot set and clear the same field in one command", name, name)
			}
			wireKeys = append(wireKeys, wireKey)
		}
	}
	if len(wireKeys) == 0 {
		return nil, errs.New(errs.ExitUsage,
			"--clear is empty: pass at least one field name (e.g. --clear rule-type)")
	}
	return wireKeys, nil
}

func setAchievementAttrs(cmd *cobra.Command, attrs map[string]any) error {
	for _, f := range achievementsScalarFlags {
		f.set(cmd, attrs, f.name)
	}

	if cmd.Flags().Changed("rule-content-node-ids") {
		ids, ferr := cmd.Flags().GetStringSlice("rule-content-node-ids")
		if ferr != nil {
			return errs.Wrap(errs.ExitGeneric, ferr)
		}
		cleaned := make([]string, 0, len(ids))
		for _, raw := range ids {
			id := strings.TrimSpace(raw)
			if id == "" {
				return errs.New(errs.ExitUsage,
					"--rule-content-node-ids contains an empty value; remove it or supply a real content node id")
			}
			cleaned = append(cleaned, id)
		}
		attrs["rule_content_node_ids"] = cleaned
	}

	appearance, err := parseJSONObjectFlag(cmd, "appearance-json")
	if err != nil {
		return err
	}
	if appearance != nil {
		attrs["appearance"] = appearance
	}

	clearKeys, err := achievementsParseClearFlag(cmd)
	if err != nil {
		return err
	}
	for _, k := range clearKeys {
		attrs[k] = nil
	}
	return nil
}

var achievementsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an achievement definition.",
	Long: `Create a new achievement badge definition for the active team.

--title is required; everything else defaults server-side (award_mode=manual,
points=0, is_secret=false, is_active=true, email_notification_enabled=true).

--appearance-json accepts the badge appearance object (inline JSON or @file),
validated strictly by the backend: keys shape, icon, emoji, text, image_src,
color, fill_color, content_color. The badge renders exactly ONE content slot,
picked in the order emoji > text > image_src > icon.

--award-mode=rule additionally needs --rule-type/--rule-criteria plus either
--rule-threshold or --rule-content-node-ids (see each flag's help — they are
mutually exclusive). Setting the pieces here is necessary but NOT sufficient:
the badge still does not award anyone until 'mio achievements rule set
--confirm' is run per hub offering — see 'mio achievements rule --help'.`,
	Example: `  mio achievements create --title "First Post" --description "Posted for the first time" --points 10
  mio achievements create --title "Insider" --is-secret --category community \
    --appearance-json '{"shape":"hexagon","emoji":"🏆","color":"#5581f4"}'
  mio achievements create --title "Fast Learner" --award-mode rule \
    --rule-type milestone --rule-criteria completed-content \
    --rule-content-node-ids node_1,node_2`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := achievementsContext(cmd)
		if err != nil {
			return err
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

var achievementsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an achievement definition by id.",
	Long: `Partially update an achievement definition. Only the flags you supply are
changed (PATCH semantics) — an unset flag is never sent, so it can never be
null'd out by accident.

--appearance-json replaces the whole appearance object when supplied.

The --rule-* flags (see 'mio achievements create --help') work the same way
here: only the ones you supply change, so setting --rule-threshold on an
existing rule leaves --rule-type/--rule-criteria as they were.

Flipping --award-mode from "rule" back to "manual" ADDITIONALLY requires
clearing every rule piece the badge still carries, in the SAME command — the
backend rejects a manual badge that still carries any rule piece
(rule_pieces_not_allowed). Use the repeatable --clear <field> flag to null a
field instead of setting it:

  mio achievements update ach_abc123 --award-mode manual \
    --clear rule-type --clear rule-criteria --clear rule-threshold

--clear is a general mechanism, not a rule-piece special case — it works for
any nullable attribute this command knows about (see --clear's own help for
the full list), repeatable and comma-separated. Setting and clearing the
same field in one command (e.g. --rule-type milestone --clear rule-type) is
a usage error.`,
	Example: `  mio achievements update ach_abc123 --title "New Title" --points 25
  mio achievements update ach_abc123 --is-active=false
  mio achievements update ach_abc123 --rule-threshold 5
  mio achievements update ach_abc123 --award-mode manual --clear rule-type --clear rule-criteria --clear rule-threshold`,
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
			return hintAchievementsUpdateErr(err)
		}
		return c.render(cmd, res)
	},
}

const achievementsUpdateRulePiecesHint = "a badge can't move away from award_mode \"rule\" while it " +
	"still carries any rule piece. Clear every rule piece it currently has in the SAME command with " +
	"the repeatable --clear flag, e.g.: --award-mode manual --clear rule-type --clear rule-criteria " +
	"--clear rule-threshold (also --clear rule-window-days / --clear rule-content-node-ids if the " +
	"badge set those too). Note: this error names /data/attributes/rule_type as its source regardless " +
	"of which piece is actually left over — that pointer is not reliable here, so clear every rule " +
	"piece the badge has, not just the one it names"

func hintAchievementsUpdateErr(err error) error {
	if err == nil || !client.HasAPIErrorCode(err, "rule_pieces_not_allowed") {
		return err
	}
	return errs.NewHTTP(errs.HTTPStatusOf(err), "%w\nhint: %s", err, achievementsUpdateRulePiecesHint)
}

var achievementsArchiveCmd = &cobra.Command{
	Use:   "archive <id>",
	Short: "Archive an achievement definition.",
	Long: `Archive (soft-delete) an achievement definition. The definition disappears
from lists and can no longer be granted; existing earns are unaffected.

--yes is REQUIRED in a non-interactive shell (scripts, CI, AI agents): without it
the command exits 5 before it changes anything.`,
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

--yes is REQUIRED in a non-interactive shell: without it the command exits 5 before it changes anything.`,
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

var achievementsRuleCmd = &cobra.Command{
	Use:   "rule",
	Short: "Compile, persist, and unbind an achievement's award rule.",
	Long: `Compile an achievement's rule pieces into a hub-scoped segment ("set"), or
unbind a previously compiled rule ("delete").

An achievement does not award anyone — no matter how it looks in
'achievements retrieve' or 'achievements offerings list' — until its rule has
been compiled AND persisted here. Creating the definition and attaching it to
a hub ('achievements offerings attach') are both necessary but NOT
sufficient; this is the missing, required step. A badge that skips it looks
fully configured and is permanently un-earnable, with nothing else
surfacing that state.

Hub-scoped: --hub is required (or a configured current_hub) on every verb —
there is no team-scoped variant of this route, it 404s.`,
}

var achievementsRuleSetCmd = &cobra.Command{
	Use:   "set <achievement_id>",
	Short: "Compile an achievement's rule, previewing by default.",
	Long: `Compile the achievement's rule pieces (rule_type/rule_criteria/rule_threshold/
rule_window_days/rule_content_node_ids — set via 'achievements create' or
'achievements update') into one segment for THIS hub's offering.

THE DEFAULT IS A PREVIEW. Without --confirm, nothing is persisted: no rule
row, no segment. The response carries only a qualifying-member count, which
this command prints in plain words. Nothing about the achievement or the hub
changes until you pass --confirm — that is the step that actually makes the
badge awardable. This default is deliberate, not a shortcut: it is the safe
choice, and it teaches the two-step (preview, then confirm) every time it
runs.

HUB-SCOPED: --hub is required (or a configured current_hub). There is no
team-scoped variant of this route — a request built without a hub in the
path 404s, which reads exactly like a missing achievement (MIO-3662: this is
how the gap this command closes was found — a hand-rolled request hit the
team-scoped shape and never worked).

--notify-on-backfill (only meaningful together with --confirm) opts the
pre-existing qualifying members — the "backfill" cohort — into award
notifications. Default false: they earn the badge silently.`,
	Example: `  mio achievements rule set ach_abc123 --hub hub_123
  mio achievements rule set ach_abc123 --hub hub_123 --confirm
  mio achievements rule set ach_abc123 --hub hub_123 --confirm --notify-on-backfill`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}

		confirm, ferr := cmd.Flags().GetBool("confirm")
		if ferr != nil {
			return errs.Wrap(errs.ExitGeneric, ferr)
		}
		notifyOnBackfill, ferr := cmd.Flags().GetBool("notify-on-backfill")
		if ferr != nil {
			return errs.Wrap(errs.ExitGeneric, ferr)
		}

		attrs := map[string]any{
			"confirm":            confirm,
			"notify_on_backfill": notifyOnBackfill,
		}

		path := achievementsRulePath(teamID, hubID, args[0])

		if !confirm {
			raw, rerr := c.client.ActionRaw(c.ctx, client.StyleEnvelope, "PUT", path, attrs)
			if rerr != nil {
				return rerr
			}
			return printAchievementRulePreview(cmd, args[0], hubID, raw)
		}

		res, aerr := c.client.Action(c.ctx, "PUT", path, attrs)
		if aerr != nil {
			return aerr
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Rule persisted for achievement %s in hub %s.\n", args[0], hubID)
			return nil
		}
		return c.render(cmd, res)
	},
}

func printAchievementRulePreview(cmd *cobra.Command, achievementID, hubID string, raw map[string]any) error {
	meta, _ := raw["meta"].(map[string]any)
	count, isLowerBound := achievementRulePreviewCount(meta)

	countPhrase := fmt.Sprintf("%d", count)
	if isLowerBound {
		countPhrase = fmt.Sprintf("at least %d", count)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Preview only — NOTHING WAS SAVED. This rule would currently award achievement %s to %s member(s) in hub %s.\n"+
			"Re-run with --confirm to persist the rule (and its segment) — that is the step that actually makes the badge awardable.\n",
		achievementID, countPhrase, hubID)
	return nil
}

func achievementRulePreviewCount(meta map[string]any) (count int, isLowerBound bool) {
	if v, ok := meta["preview_count"].(float64); ok {
		count = int(v)
	}
	if v, ok := meta["preview_count_is_lower_bound"].(bool); ok {
		isLowerBound = v
	}
	return count, isLowerBound
}

var achievementsRuleDeleteCmd = &cobra.Command{
	Use:   "delete <achievement_id>",
	Short: "Unbind an achievement's rule.",
	Long: `Unbind the achievement's rule for this hub offering. Removes the rule row
and soft-deletes its compiled segment; existing earns are unaffected — members
who already have the badge keep it (spec §5.5).

This STOPS the badge from awarding anyone new in this hub until
'achievements rule set --confirm' is run again.

Hub-scoped: --hub is required (or a configured current_hub).

--yes is REQUIRED in a non-interactive shell: without it the command exits 5 before it changes anything.`,
	Example: `  mio achievements rule delete ach_abc123 --hub hub_123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf(
			"Unbind the rule for achievement %s in this hub? It stops awarding anyone new until the rule is set again.", args[0],
		)); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, achievementsRulePath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Unbound the rule for achievement %s in hub %s.\n", args[0], hubID)
		return nil
	},
}

var achievementsGrantCmd = &cobra.Command{
	Use:   "grant <achievement_id>",
	Short: "Manually award an achievement to a hub member.",
	Long: `Manually award an achievement to an active hub member. Idempotent: granting
an already-earned achievement returns the existing earn unchanged (200).

The achievement must be offered in the hub (see 'mio achievements offerings')
and its award_mode must be manual. Granting over a previously REVOKED earn is
a 409 — use 'mio achievements restore' instead.

--reason accepts only the literal value "manual" (MIO-3488): the backend
request schema types award_reason as Literal["manual"] and 422s
("Input should be manual") on anything else, then records provenance itself
regardless of what was sent. Any other --reason value is rejected here before
a request is sent.`,
	Example: `  mio achievements grant ach_abc123 --hub hub_123 --contact-id ct_456
  mio achievements grant ach_abc123 --hub hub_123 --contact-id ct_456 --reason manual`,
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
		if err := applyGrantAwardReason(cmd, attrs); err != nil {
			return err
		}

		res, err := c.client.Create(c.ctx, achievementsEarnPath(teamID, hubID, contactID, ""), attrs)
		if err != nil {
			return hintAchievementsEarnErr("grant", err)
		}
		return c.render(cmd, res)
	},
}

var achievementsRevokeCmd = &cobra.Command{
	Use:   "revoke <achievement_id>",
	Short: "Revoke a member's earned achievement.",
	Long: `Soft-revoke a member's earned achievement. Idempotent — revoking an
already-revoked or NONEXISTENT earn also answers 204, so a success here does
not confirm an earn existed (a wrong contact id is not an error on this verb).
Reversible via 'mio achievements restore'.

--reason travels as the ?reason= query parameter (the DELETE has no body).

--yes is REQUIRED in a non-interactive shell: without it the command exits 5 before it changes anything.`,
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
			return hintAchievementsEarnErr("revoke", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"Revoke accepted for achievement %s, contact %s. Note: the API is idempotent — "+
				"204 does not confirm a live earn existed (a nonexistent earn, an already-revoked "+
				"earn, or a wrong contact id all answer the same).\n", args[0], contactID)
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

		attrs := map[string]any{}
		setMappedString(cmd, attrs, "reason", "restore_reason")

		path := achievementsEarnPath(teamID, hubID, contactID, args[0]) + "/restore"
		res, err := c.client.Action(c.ctx, "POST", path, attrs)
		if err != nil {
			return hintAchievementsEarnErr("restore", err)
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Restored achievement %s for contact %s.\n", args[0], contactID)
			return nil
		}
		return c.render(cmd, res)
	},
}

var achievementsOverrideCmd = &cobra.Command{
	Use:   "override <achievement_id>",
	Short: "Audited override-grant of a RULE-based achievement.",
	Long: `Award a rule-based achievement to a member outside its normal signal — the
audited repair path for a member who should have qualified but the segment
never contained them, or whose live signal was missed.

THIS IS FOR "rule" BADGES, NOT "manual" ONES — the opposite direction from
'mio achievements grant', which is manual-only. override requires
award_mode="rule" and 422s (achievement_award_mode_invalid) on a manual
badge; grant requires award_mode="manual" and 422s the same way on a rule
badge. Sending the wrong verb for the badge's mode is the trap here.

REQUIRES A BOUND RULE: the achievement must have had 'mio achievements rule
set --confirm' run for this hub offering already. Without one, this 404s
(achievement_rule_not_found) — that 404 means the rule was never
compiled/persisted, NOT that the badge or offering is missing.

A PREVIOUSLY-REVOKED earn is a 409 (achievement_already_revoked) — this does
NOT reactivate a revoked earn; use 'mio achievements restore' for that.

--reason is REQUIRED (unlike grant/revoke/restore's optional --reason) and
audited as the earn's grant_reason — an empty or whitespace-only value is
rejected before any request is sent.`,
	Example: `  mio achievements override ach_abc123 --hub hub_123 --contact-id ct_456 --reason "segment missed them before the rule was bound"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := achievementsHubContext(cmd)
		if err != nil {
			return err
		}
		contactID, err := requireContactID(cmd)
		if err != nil {
			return err
		}
		reason, err := requireOverrideReason(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{"reason": reason}

		path := achievementsEarnPath(teamID, hubID, contactID, args[0]) + "/override"
		res, err := c.client.Action(c.ctx, "POST", path, attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	for _, cmd := range []*cobra.Command{achievementsCreateCmd, achievementsUpdateCmd} {
		cmd.Flags().String("title", "", "Badge title.")
		cmd.Flags().String("description", "", "Badge description.")
		cmd.Flags().String("award-mode", "", "How the badge is awarded: \"manual\" (grant/revoke/restore only — see 'mio achievements grant') or \"rule\" (auto-awarded once its rule is compiled). A \"rule\" badge additionally needs the --rule-* flags below AND 'mio achievements rule set --confirm' per hub offering before it can award anyone — see 'mio achievements rule --help'.")
		cmd.Flags().String("category", "", "Free-form category label.")
		cmd.Flags().Int("points", 0, "Points awarded with the badge (>= 0).")
		cmd.Flags().Bool("is-secret", false, "Hide the badge from members until earned.")
		cmd.Flags().Bool("is-active", true, "Whether the badge is active (grantable).")
		cmd.Flags().Bool("email-notification-enabled", true, "Email the member when the badge is earned.")
		cmd.Flags().String("appearance-json", "", "Badge appearance as a JSON object (inline or @file): shape, icon, emoji, text, image_src, color, fill_color, content_color.")

		cmd.Flags().String("rule-type", "", "Rule category, required for --award-mode=rule. Currently accepted by the backend: milestone (other recognized-but-not-yet-available values 422 with rule_type_not_available: challenge, streak, community). The backend is authoritative and carries no OpenAPI enum for this field — this list is informational, not enforced by the CLI, and can change.")
		cmd.Flags().String("rule-criteria", "", "What the rule measures, scoped by --rule-type. Currently accepted for rule-type=milestone: completed-content, time-since-joining, number-of-searches, unique-logins. Informational only, same caveat as --rule-type — not enforced by the CLI.")
		cmd.Flags().Int("rule-threshold", 0, "How many times/units of --rule-criteria must be met (>= 1). Required for a milestone rule UNLESS --rule-content-node-ids is set — the two are MUTUALLY EXCLUSIVE (a content-scoped rule always means \"complete every picked item\"); sending both 422s server-side (rule_threshold_conflicts_with_content_ids), the CLI does not block it client-side.")
		cmd.Flags().Int("rule-window-days", 0, "Rolling window in days — only accepted for --rule-type=challenge, which is not yet available (see --rule-type); setting this on any rule type accepted today 422s server-side (rule_window_days_not_allowed). Reserved for when challenge ships.")
		cmd.Flags().StringSlice("rule-content-node-ids", nil, "Content node ids that must ALL be completed. Only valid with --rule-criteria=completed-content, and MUTUALLY EXCLUSIVE with --rule-threshold (see its help) — the backend 422s the combination, the CLI does not block it client-side. Repeatable or comma-separated; 1-50 items, no duplicates (enforced server-side).")
	}
	markFlagsRequired(achievementsCreateCmd, "title")

	achievementsUpdateCmd.Flags().StringArray("clear", nil, "Null out a field instead of setting it, e.g. --clear rule-type. Repeatable and comma-separated. Valid targets: title, description, award-mode, category, rule-type, rule-criteria, points, rule-threshold, rule-window-days, is-secret, is-active, email-notification-enabled, rule-content-node-ids. Setting a field with --<field> and clearing it with --clear in the same command is a usage error. This only validates that the name is one the CLI knows how to null — whether the backend actually accepts null for a given field is its own call (it 422s the ones it won't accept, e.g. rule_pieces_not_allowed until every rule piece is cleared).")

	addPaginationFlags(achievementsListCmd)
	achievementsListCmd.Flags().String("award-mode", "", "Filter by award mode (filter[award_mode]).")
	achievementsListCmd.Flags().String("category", "", "Filter by category (filter[category]).")
	addPaginationFlags(achievementsOfferingsListCmd)

	achievementsRuleSetCmd.Flags().Bool("confirm", false, "Persist the compiled rule and its segment. Without this flag the call PREVIEWS ONLY — nothing is saved (default).")
	achievementsRuleSetCmd.Flags().Bool("notify-on-backfill", false, "Send award notifications to the pre-existing qualifying (\"backfill\") cohort when persisting. Default false: they earn the badge silently.")

	for _, cmd := range []*cobra.Command{achievementsGrantCmd, achievementsRevokeCmd, achievementsRestoreCmd, achievementsOverrideCmd} {
		cmd.Flags().String("contact-id", "", "GLOBAL contact id of the hub member — capture with `mio contacts retrieve <team-contact-id> -o plain --jq .contact_id` (the flattened contact_id field, NOT the row's .id). Required.")
		markFlagsRequired(cmd, "contact-id")
	}
	achievementsGrantCmd.Flags().String("reason", "", "Must be exactly \"manual\" (MIO-3488) — the backend rejects any other value with a 422 and records provenance itself.")
	achievementsRevokeCmd.Flags().String("reason", "", "Audit reason recorded as the earn's revoke_reason (sent as the ?reason= query parameter).")
	achievementsRestoreCmd.Flags().String("reason", "", "Audit reason recorded as the earn's restore_reason.")
	achievementsOverrideCmd.Flags().String("reason", "", "REQUIRED. Audit reason recorded as the earn's grant_reason. Unlike grant/revoke/restore's optional --reason, this one is mandatory — an empty or whitespace-only value is rejected before any request is sent (the backend's own AuditReason validator strips whitespace before its required check too, so a whitespace-only value would 422 there as well).")
	markFlagsRequired(achievementsOverrideCmd, "reason")
}
