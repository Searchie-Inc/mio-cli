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
//	rule set         PUT     /hubs/{hub_id}/achievements/{achievement_id}/rule    (200 preview meta-only / 200 recompile / 201 new bind)
//	rule delete      DELETE  /hubs/{hub_id}/achievements/{achievement_id}/rule    (204)
//	grant            POST    /hubs/{hub_id}/members/{contact_id}/achievements     (201 new / 200 repeat)
//	revoke           DELETE  /hubs/{hub_id}/members/{contact_id}/achievements/{achievement_id}  (204; ?reason= query)
//	restore          POST    /hubs/{hub_id}/members/{contact_id}/achievements/{achievement_id}/restore
//	override         POST    /hubs/{hub_id}/members/{contact_id}/achievements/{achievement_id}/override  (201 new / 200 repeat)
//
// RULE (MIO-3372/MIO-3662): a badge does not award anyone until its rule is
// compiled AND persisted via "rule set --confirm" — creating the definition
// (award_mode "rule") and attaching it to a hub via "offerings attach" are
// both necessary but NOT sufficient. There is deliberately no team-scoped
// variant of PUT/DELETE .../rule; it is hub-scoped only and 404s otherwise.
// "rule set" without --confirm PREVIEWS ONLY (nothing persisted, response is
// meta-only: meta.preview_count / meta.preview_count_is_lower_bound) —
// --confirm is what actually persists the rule and its compiled segment.
// PUT/DELETE .../rule additionally require the backend's OWN
// ACHIEVEMENTS_RULES_ENABLED kill switch (assert_achievement_rules_enabled,
// app/achievements/feature_flag.py) on top of the two SPLIT gates below — a
// third, independent flag collapsing into the SAME generic 404.
//
// OVERRIDE (MIO-3376 2a-7/MIO-3662): the deterministic, synchronous way to
// prove a "rule" badge actually awards — the automatic engine (SegmentJoined
// fast path / backfill worker / sweeper) is background-only and has no HTTP
// route to trigger, so QA/support has no other CLI-reachable way to force a
// specific award. override is the mirror image of grant: grant requires
// award_mode="manual" (422s achievement_award_mode_invalid on "rule"),
// override requires award_mode="rule" (422s the same code on "manual") AND a
// rule already bound via "rule set --confirm" (404 achievement_rule_not_found
// otherwise). --reason is REQUIRED here (AuditReason, min_length=1 after
// whitespace-strip) — unlike grant/revoke/restore's optional --reason.
//
// AUTH: every route above requires the TEAM principal (require_team_owner on
// the backend) — a plain team API key works, so this group uses the standard
// newContext + requireAuth + requireTeam boilerplate. The MEMBER-facing reads
// (/api/hubs/{hub_id}/achievements etc., app/achievements/router.py) require a
// contact identity instead and are deliberately NOT covered here — same
// boundary as `mio events` (MIO-3173), see that file's package doc comment.
//
// FEATURE GATES — the gate model is SPLIT (app/achievements/feature_flag.py):
// the team-scoped DEFINITION routes (create/list/retrieve/update/archive) are
// gated on the global ACHIEVEMENTS_ENABLED setting ONLY — they have no hub in
// the path and never read the per-hub flag. The hub-scoped OFFERING and EARN
// routes additionally require the per-hub
// `hub.settings.achievements.enabled is True` flag. A closed gate 404s with
// one generic message (deliberately indistinguishable from a missing hub), so
// `mio achievements *` exits 4 — that is the backend's answer, not a CLI
// defect. The per-hub flag is set with:
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
// collection token (the transcript/revert precedent), and override
// (.../achievements/{id}/override) resolves the SAME way for the SAME
// reason — verified, not assumed, by a dedicated TestResourceTypeFromPath
// case, since a missing knownCollections entry misresolves SILENTLY (proven
// by the "rule" token below, which does need one). PUT/DELETE .../rule needs
// its OWN entry, {"achievements/rule": "achievement_rules"} plus "rule" in
// knownCollections — without "rule" as a token the tail silently misresolves
// through the hubs/achievements override instead (see
// internal/client/client_test.go TestResourceTypeFromPath).
//
// revoke's optional reason travels as a QUERY parameter (?reason=…, backend
// alias for revoke_reason) on a body-less DELETE — client.DeleteWithQuery.
// restore's envelope is REQUIRED by the backend even when restore_reason is
// omitted, so the CLI always sends a non-nil (possibly empty) attributes map.
//
// rule set's confirm=false response is META-ONLY (no `data` member — see
// AchievementRulePreviewResponse), so it is decoded with client.ActionRaw,
// not the ordinary resource decoder (same class as automations
// fire-event/test, MIO-2503/MIO-2554: the resource decoder errors "response
// had no `data` member" on every successful preview). confirm=true responses
// DO carry `data` (a full achievement_rules resource, 201 new bind / 200
// recompile) and go through the ordinary client.Action + c.render path.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
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
		achievementsOverrideCmd,
	)

	// achievements offerings <action>  (which achievements a hub offers)
	achievementsOfferingsCmd.AddCommand(
		achievementsOfferingsListCmd,
		achievementsOfferingsAttachCmd,
		achievementsOfferingsDetachCmd,
	)
	achievementsCmd.AddCommand(achievementsOfferingsCmd)

	// achievements rule <action>  (compile/bind/preview and unbind a hub's rule)
	achievementsRuleCmd.AddCommand(
		achievementsRuleSetCmd,
		achievementsRuleDeleteCmd,
	)
	achievementsCmd.AddCommand(achievementsRuleCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(achievementsCmd)
}

// ---- achievements group -----------------------------------------------------

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

// achievementsRulePath returns
// /api/teams/{team_id}/hubs/{hub_id}/achievements/{achievement_id}/rule —
// ALWAYS hub-scoped (there is no team-scoped variant; it 404s). Built on top
// of achievementsOfferingsPath so the hub scoping is correct by construction,
// not by convention (MIO-3662: the gap this command closes was found because
// a hand-rolled request hit the team-scoped shape instead).
func achievementsRulePath(teamID, hubID, achievementID string) string {
	return achievementsOfferingsPath(teamID, hubID, achievementID) + "/rule"
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

// Earn-verb error hints — STATUS-KEYED and verb-aware, because the backend's
// answer shapes differ per verb and a wrong contact id NEVER produces a 404
// (verified against app/achievements/{admin_router,service}.py, and flagged
// by the blind review on PR #109 — the earlier 404-only contact-id hint had
// zero recall):
//
//	wrong contact id on grant   → 422 achievement_membership_required
//	wrong contact id on revoke  → 204 (idempotent no-op — success!)
//	wrong contact id on restore → 409 achievement_not_revoked ("No earn exists")
//
// The 404 hint therefore names only the causes a 404 CAN have (gates,
// achievement, hub containment) and explicitly says the contact id is not
// among them; the namespace guidance rides the grant 422 and restore 409
// instead, and revoke's success message carries its own idempotency
// disclosure. Hints key off errs.HTTPStatusOf — the transport status — never
// off backend message strings.

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

// hintAchievementsEarnErr appends the status-appropriate hint above to an
// earn-verb error, preserving the transport status (and therefore the exit
// code). verb is "grant", "revoke" or "restore". Any other error (or nil)
// passes through untouched.
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
	return errs.NewHTTP(errs.HTTPStatusOf(err), "%s\nhint: %s", err.Error(), hint)
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

// requireOverrideReason reads the required --reason flag for 'override'.
// Unlike grant/revoke/restore's optional --reason, override's is REQUIRED
// and audited (AchievementOverrideAttributes.reason: AuditReason) — the
// backend 422s a missing, empty, OR whitespace-only value (AuditReason strips
// whitespace before its min_length=1 check runs). Validating the blank case
// client-side is worth it here specifically because the server's rule is
// unambiguous: there is no reading of "   " that the backend would ever
// accept, so rejecting it locally saves a guaranteed round trip.
func requireOverrideReason(cmd *cobra.Command) (string, error) {
	if !cmd.Flags().Changed("reason") {
		return "", errs.New(errs.ExitUsage, "missing required flag: --reason")
	}
	v, err := cmd.Flags().GetString("reason")
	if err != nil {
		return "", errs.Wrap(errs.ExitGeneric, err)
	}
	if strings.TrimSpace(v) == "" {
		return "", errs.New(errs.ExitUsage, "--reason must not be empty or whitespace-only")
	}
	return v, nil
}

// achievementsAttrFlags is the set of create/update string flags, shared so
// both commands stay in lockstep. attrKey (see flags.go) translates each
// kebab-case flag name to its snake_case attribute key (e.g. --award-mode ->
// award_mode).
//
// rule-type/rule-criteria (MIO-3372/MIO-3662): the backend's write schemas
// type these as plain strings, NOT an OpenAPI enum (schemas.py: "Plain
// strings/ints, no OpenAPI enum ... mirrors category's STATED CONTRACT
// precedent"). The CLI does not hard-validate them client-side either — the
// currently-accepted values are named in the flag help as information, not
// as a client-enforced closed list; app/achievements/rules.py
// (validate_rule_pieces) is the actual authority and 422s precisely.
var achievementsAttrFlags = []string{"title", "description", "award-mode", "category", "rule-type", "rule-criteria"}

// setAchievementAttrs copies every changed create/update flag into attrs. Only
// flags the user actually set are copied (the set*Flag helpers no-op on an
// unset flag), so a PATCH stays a partial update and no field is ever sent as
// an explicit null. --appearance-json is forwarded WHOLESALE as the
// `appearance` object — the backend validates shape/icon/colors strictly
// (extra=forbid), and the CLI is a conduit, not a second validator.
//
// --rule-content-node-ids is the one rule-piece flag that isn't a plain
// setIntFlag/setStringFlag copy: it needs the slice read explicitly so a
// blank entry can be rejected before it silently ships a SHORTER list than
// the caller named (same shape as content.go's --playlist-id, MIO-3074). The
// mutual exclusivity between --rule-content-node-ids and --rule-threshold,
// and the count/duplicate checks, are deliberately NOT re-validated here —
// app/achievements/rules.py::validate_rule_pieces is the single source of
// truth for that combination and already 422s each violation precisely
// (rule_threshold_conflicts_with_content_ids, rule_content_node_ids_invalid);
// duplicating it here would just drift.
func setAchievementAttrs(cmd *cobra.Command, attrs map[string]any) error {
	for _, f := range achievementsAttrFlags {
		setStringFlag(cmd, attrs, f)
	}
	setIntFlag(cmd, attrs, "points")
	setIntFlag(cmd, attrs, "rule-threshold")
	setIntFlag(cmd, attrs, "rule-window-days")
	setBoolFlag(cmd, attrs, "is-secret")
	setBoolFlag(cmd, attrs, "is-active")
	setBoolFlag(cmd, attrs, "email-notification-enabled")

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
		// A real []string, NOT a joined string — it must serialize as a JSON
		// array on the wire (the backend reads rule_content_node_ids as
		// list[str]; a comma-joined string would be one bad id, not a list).
		attrs["rule_content_node_ids"] = cleaned
	}

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

--appearance-json replaces the whole appearance object when supplied.

The --rule-* flags (see 'mio achievements create --help') work the same way
here: only the ones you supply change, so setting --rule-threshold on an
existing rule leaves --rule-type/--rule-criteria as they were. Flipping
--award-mode from "rule" back to "manual" does NOT clear a previously-set
rule piece in the same command — the CLI has no null-clearing flag for these
fields yet, and the backend rejects a manual badge that still carries any
rule piece (rule_pieces_not_allowed) until they are cleared some other way.`,
	Example: `  mio achievements update ach_abc123 --title "New Title" --points 25
  mio achievements update ach_abc123 --is-active=false
  mio achievements update ach_abc123 --rule-threshold 5`,
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

// ---- rule sub-resource (compile / bind / preview / unbind, MIO-3372/MIO-3662) ----

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

		// confirm/notify_on_backfill are ALWAYS sent explicitly, unlike the
		// Changed()-gated partial-update flags elsewhere in this file: this PUT
		// is a control call, not a partial patch, and the whole point of the
		// safe default is that it must be unmissable on the wire — not merely
		// relied on as a server-side default the CLI happens to agree with.
		attrs := map[string]any{
			"confirm":            confirm,
			"notify_on_backfill": notifyOnBackfill,
		}

		path := achievementsRulePath(teamID, hubID, args[0])

		if !confirm {
			// confirm=false is a META-ONLY response (no `data` member) — see the
			// package doc comment for why this goes through ActionRaw rather
			// than the ordinary resource decode.
			raw, rerr := c.client.ActionRaw(c.ctx, client.StyleEnvelope, "PUT", path, attrs)
			if rerr != nil {
				return rerr
			}
			return printAchievementRulePreview(cmd, args[0], hubID, raw)
		}

		// confirm=true: 201 on a new bind, 200 on a re-PUT recompile — both
		// carry a full achievement_rules resource, so the ordinary resource
		// decode applies regardless of which status came back.
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

// printAchievementRulePreview renders a confirm=false PUT .../rule response
// in plain words rather than dumping the raw meta object. The operator's
// actual question is "how many members would this award right now?", and the
// answer must say, unmissably, that nothing was persisted and how to change
// that — the whole reason this command exists is that a preview silently
// read as a completed bind once already (MIO-3662).
func printAchievementRulePreview(cmd *cobra.Command, achievementID, hubID string, raw map[string]any) error {
	meta, _ := raw["meta"].(map[string]any)
	count, isLowerBound := achievementRulePreviewCount(meta)

	countPhrase := fmt.Sprintf("%d", count)
	if isLowerBound {
		// preview_count_is_lower_bound=true means the true count is AT LEAST
		// this many (the estimate is capped) — printing it as exact would
		// misrepresent an estimate as a precise figure.
		countPhrase = fmt.Sprintf("at least %d", count)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Preview only — NOTHING WAS SAVED. This rule would currently award achievement %s to %s member(s) in hub %s.\n"+
			"Re-run with --confirm to persist the rule (and its segment) — that is the step that actually makes the badge awardable.\n",
		achievementID, countPhrase, hubID)
	return nil
}

// achievementRulePreviewCount extracts meta.preview_count and
// meta.preview_count_is_lower_bound from a confirm=false PUT .../rule
// response (AchievementRulePreviewMeta). JSON numbers decode as float64 in a
// map[string]any, hence the explicit conversion. A missing/malformed field
// defaults to zero/false rather than erroring the whole preview out — the
// count is informational, not something a partial decode should hard-fail.
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

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
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

// ---- grant / revoke / restore (manual earns) --------------------------------

var achievementsGrantCmd = &cobra.Command{
	Use:   "grant <achievement_id>",
	Short: "Manually award an achievement to a hub member.",
	Long: `Manually award an achievement to an active hub member. Idempotent: granting
an already-earned achievement returns the existing earn unchanged (200).

The achievement must be offered in the hub (see 'mio achievements offerings')
and its award_mode must be manual. Granting over a previously REVOKED earn is
a 409 — use 'mio achievements restore' instead.

--reason is forwarded as award_reason, which the request schema accepts — but
the Phase 1 backend records award_reason="manual" regardless (verified live:
grant_manual hardcodes it; revoke/restore reasons ARE persisted).`,
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
			return hintAchievementsEarnErr("revoke", err)
		}
		// 204 is idempotent server-side: a nonexistent earn — including a
		// contact id in the wrong namespace — answers the same 204 as a real
		// revoke, so this message must not claim an earn was revoked (blind
		// review, PR #109).
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

		// The backend REQUIRES the JSON:API envelope on restore even when
		// restore_reason is omitted (AchievementRestoreEnvelope is a required
		// body param), so attrs must be non-nil — a nil body would send no
		// envelope at all and 422.
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

		// Deliberately NOT routed through hintAchievementsEarnErr: that hint
		// set is scoped to grant/revoke/restore (see its own doc comment) and
		// its 404 text does not mention achievement_rule_not_found — reusing
		// it here would attach a hint that is wrong for override's most
		// interesting 404 case. The Long help above covers all three of
		// override's distinguishing failure modes (wrong award_mode, no
		// bound rule, already-revoked) instead.
		path := achievementsEarnPath(teamID, hubID, contactID, args[0]) + "/override"
		res, err := c.client.Action(c.ctx, "POST", path, attrs)
		if err != nil {
			return err
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
		// Rule mode has been live in production for every hub since MIO-3372 —
		// this help used to say "Phase 1 grants require: manual", which is now
		// stale and actively misleading to someone trying to build a rule
		// badge (MIO-3662).
		cmd.Flags().String("award-mode", "", "How the badge is awarded: \"manual\" (grant/revoke/restore only — see 'mio achievements grant') or \"rule\" (auto-awarded once its rule is compiled). A \"rule\" badge additionally needs the --rule-* flags below AND 'mio achievements rule set --confirm' per hub offering before it can award anyone — see 'mio achievements rule --help'.")
		cmd.Flags().String("category", "", "Free-form category label.")
		cmd.Flags().Int("points", 0, "Points awarded with the badge (>= 0).")
		cmd.Flags().Bool("is-secret", false, "Hide the badge from members until earned.")
		cmd.Flags().Bool("is-active", true, "Whether the badge is active (grantable).")
		cmd.Flags().Bool("email-notification-enabled", true, "Email the member when the badge is earned.")
		cmd.Flags().String("appearance-json", "", "Badge appearance as a JSON object (inline or @file): shape, icon, emoji, text, image_src, color, fill_color, content_color.")

		// Rule pieces (MIO-3372/MIO-3662): only meaningful when
		// --award-mode=rule (a manual badge must carry none of them — the
		// backend 422s rule_pieces_not_allowed otherwise; the CLI does not
		// duplicate that check, the 422 already names it precisely).
		cmd.Flags().String("rule-type", "", "Rule category, required for --award-mode=rule. Currently accepted by the backend: milestone (other recognized-but-not-yet-available values 422 with rule_type_not_available: challenge, streak, community). The backend is authoritative and carries no OpenAPI enum for this field — this list is informational, not enforced by the CLI, and can change.")
		cmd.Flags().String("rule-criteria", "", "What the rule measures, scoped by --rule-type. Currently accepted for rule-type=milestone: completed-content, time-since-joining, number-of-searches, unique-logins. Informational only, same caveat as --rule-type — not enforced by the CLI.")
		cmd.Flags().Int("rule-threshold", 0, "How many times/units of --rule-criteria must be met (>= 1). Required for a milestone rule UNLESS --rule-content-node-ids is set — the two are MUTUALLY EXCLUSIVE (a content-scoped rule always means \"complete every picked item\"); sending both 422s server-side (rule_threshold_conflicts_with_content_ids), the CLI does not block it client-side.")
		cmd.Flags().Int("rule-window-days", 0, "Rolling window in days — only accepted for --rule-type=challenge, which is not yet available (see --rule-type); setting this on any rule type accepted today 422s server-side (rule_window_days_not_allowed). Reserved for when challenge ships.")
		cmd.Flags().StringSlice("rule-content-node-ids", nil, "Content node ids that must ALL be completed. Only valid with --rule-criteria=completed-content, and MUTUALLY EXCLUSIVE with --rule-threshold (see its help) — the backend 422s the combination, the CLI does not block it client-side. Repeatable or comma-separated; 1-50 items, no duplicates (enforced server-side).")
	}

	// Pagination + filters on the lists.
	addPaginationFlags(achievementsListCmd)
	achievementsListCmd.Flags().String("award-mode", "", "Filter by award mode (filter[award_mode]).")
	achievementsListCmd.Flags().String("category", "", "Filter by category (filter[category]).")
	addPaginationFlags(achievementsOfferingsListCmd)

	// Rule set: confirm (persist vs preview) + notify-on-backfill.
	achievementsRuleSetCmd.Flags().Bool("confirm", false, "Persist the compiled rule and its segment. Without this flag the call PREVIEWS ONLY — nothing is saved (default).")
	achievementsRuleSetCmd.Flags().Bool("notify-on-backfill", false, "Send award notifications to the pre-existing qualifying (\"backfill\") cohort when persisting. Default false: they earn the badge silently.")

	// Earn verbs: contact scope + a per-verb reason (the four backends treat
	// it differently, so the help must not share one claim — Jay-r review,
	// PR #109).
	for _, cmd := range []*cobra.Command{achievementsGrantCmd, achievementsRevokeCmd, achievementsRestoreCmd, achievementsOverrideCmd} {
		cmd.Flags().String("contact-id", "", "GLOBAL contact id of the hub member — capture with `mio contacts retrieve <team-contact-id> -o plain --jq .contact_id` (the flattened contact_id field, NOT the row's .id). Required.")
	}
	achievementsGrantCmd.Flags().String("reason", "", "Forwarded as award_reason. The Phase 1 backend accepts but does NOT persist it (award_reason is always recorded as \"manual\").")
	achievementsRevokeCmd.Flags().String("reason", "", "Audit reason recorded as the earn's revoke_reason (sent as the ?reason= query parameter).")
	achievementsRestoreCmd.Flags().String("reason", "", "Audit reason recorded as the earn's restore_reason.")
	achievementsOverrideCmd.Flags().String("reason", "", "REQUIRED. Audit reason recorded as the earn's grant_reason. Unlike grant/revoke/restore's optional --reason, this one is mandatory — an empty or whitespace-only value is rejected before any request is sent (the backend's own AuditReason validator strips whitespace before its required check too, so a whitespace-only value would 422 there as well).")
}
