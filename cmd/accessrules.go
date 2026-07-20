package cmd

// accessrules.go implements the `mio access-rules` command group for managing
// access rules and access overrides nested under a hub. Every sub-command is
// hub-scoped: both {team_id} and {hub_id} must be resolved from context (or
// supplied via --team/--hub).
//
// Routes (see docs/internal/api-surface.md "access-rules"):
//
//	rules     create/list/retrieve/update/delete  /api/teams/{team_id}/hubs/{hub_id}/access-rules[/{id}]
//	overrides create/list/retrieve/update/delete  /api/teams/{team_id}/hubs/{hub_id}/access-overrides[/{id}]

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// access-rules rules <action>
	accessRulesRulesCmd.AddCommand(
		accessRulesRulesCreateCmd,
		accessRulesRulesListCmd,
		accessRulesRulesRetrieveCmd,
		accessRulesRulesUpdateCmd,
		accessRulesRulesDeleteCmd,
	)
	accessRulesCmd.AddCommand(accessRulesRulesCmd)

	// access-rules overrides <action>
	accessRulesOverridesCmd.AddCommand(
		accessRulesOverridesCreateCmd,
		accessRulesOverridesListCmd,
		accessRulesOverridesRetrieveCmd,
		accessRulesOverridesUpdateCmd,
		accessRulesOverridesDeleteCmd,
	)
	accessRulesCmd.AddCommand(accessRulesOverridesCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(accessRulesCmd)
}

// ---- access-rules group -----------------------------------------------------

var accessRulesCmd = &cobra.Command{
	Use:   "access-rules",
	Short: "Manage access rules and overrides for a hub.",
	Long: `Manage access rules and access overrides nested under a hub.

Access rules define the conditions (segments, entitlements, drip dates) that
gate content or sections within a hub. Access overrides grant or restrict
individual contacts regardless of rules.

Both sub-resources require --hub (or a configured hub in context).`,
}

// accessRulesContext is the shared boilerplate for access-rules sub-commands:
// builds the context, requires auth, and resolves both team id and hub id.
func accessRulesContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", "", err
	}
	if err := c.requireAuth(); err != nil {
		return nil, "", "", err
	}
	teamID, err := c.requireTeam()
	if err != nil {
		return nil, "", "", err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, "", "", err
	}
	return c, teamID, hubID, nil
}

// ---- access-rules rules sub-resource ----------------------------------------

var accessRulesRulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Manage access rules for a hub.",
	Long:  "Create, list, retrieve, update, and delete access rules for a hub.",
}

// rulesPath returns /api/teams/{team_id}/hubs/{hub_id}/access-rules[/{id}].
func rulesPath(teamID, hubID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/access-rules", teamID, hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

var accessRulesRulesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an access rule.",
	Long: `Create a new access rule for the active hub.

At least --target-type and --target-id must be provided. Use --conditions to
supply a JSON array of condition objects, each with condition_type,
condition_data, and position.`,
	Example: `  mio access-rules rules create --hub hub_abc --target-type section --target-id sec_xyz
  mio access-rules rules create --hub hub_abc \
    --target-type content_node --target-id cnt_123 \
    --logic-operator all \
    --conditions '[{"condition_type":"has_entitlement","condition_data":{"product_id":"prod_1"},"position":0}]'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "target-type")
		setStringFlag(cmd, attrs, "target-id")
		setStringFlag(cmd, attrs, "logic-operator")

		if err := setJSONArrayFlag(cmd, attrs, "conditions"); err != nil {
			return err
		}

		if _, ok := attrs["target_type"]; !ok {
			return errs.New(errs.ExitUsage, "nothing to create: --target-type and --target-id are required")
		}
		if _, ok := attrs["target_id"]; !ok {
			return errs.New(errs.ExitUsage, "nothing to create: --target-type and --target-id are required")
		}

		res, err := c.client.Create(c.ctx, rulesPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var accessRulesRulesListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List access rules for a hub.",
	Long:    "List all access rules defined for the active hub.",
	Example: `  mio access-rules rules list --hub hub_abc`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, rulesPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var accessRulesRulesRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an access rule by id.",
	Long:    "Fetch a single access rule by its id from the active hub.",
	Example: `  mio access-rules rules retrieve ar_abc123 --hub hub_abc`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, rulesPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var accessRulesRulesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an access rule by id.",
	Long: `Partially update an access rule. Only the flags you supply are changed.

Use --conditions to replace the full conditions list with a JSON array of
condition objects, each with condition_type, condition_data, and position.`,
	Example: `  mio access-rules rules update ar_abc123 --hub hub_abc --logic-operator all
  mio access-rules rules update ar_abc123 --hub hub_abc \
    --conditions '[{"condition_type":"in_segment","condition_data":{"segment_id":"seg_1"},"position":0}]'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "logic-operator")

		if err := setJSONArrayFlag(cmd, attrs, "conditions"); err != nil {
			return err
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, rulesPath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var accessRulesRulesDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete an access rule by id.",
	Long:    "Permanently delete an access rule from the active hub.",
	Example: `  mio access-rules rules delete ar_abc123 --hub hub_abc --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete access rule %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, rulesPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted access rule %s.\n", args[0])
		return nil
	},
}

// ---- access-rules overrides sub-resource ------------------------------------

var accessRulesOverridesCmd = &cobra.Command{
	Use:   "overrides",
	Short: "Manage access overrides for a hub.",
	Long:  "Create, list, retrieve, update, and delete per-contact access overrides for a hub.",
}

// overridesPath returns /api/teams/{team_id}/hubs/{hub_id}/access-overrides[/{id}].
func overridesPath(teamID, hubID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/access-overrides", teamID, hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

var accessRulesOverridesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an access override.",
	Long: `Create a new per-contact access override for the active hub.

--contact-id and --scope are required. Scope must be one of: full, basic, product.
When scope is "product", --product-id is also required.

--contact-id takes the GLOBAL contact id — the .attributes.contact_id field from
'mio contacts', NOT its .id (that is the team-contact id and this route will 404
with "not found or is inactive"). Extract it with:
  mio contacts retrieve <id> -o json --jq '.contact_id'`,
	Example: `  mio access-rules overrides create --hub hub_abc --contact-id con_123 --scope full
  mio access-rules overrides create --hub hub_abc \
    --contact-id con_123 --scope product --product-id prod_456 \
    --expires-at "2026-12-31T00:00:00Z" --reason "Trial access"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "contact-id")
		setStringFlag(cmd, attrs, "scope")
		setStringFlag(cmd, attrs, "product-id")
		setStringFlag(cmd, attrs, "expires-at")
		setStringFlag(cmd, attrs, "reason")

		if _, ok := attrs["contact_id"]; !ok {
			return errs.New(errs.ExitUsage, "nothing to create: --contact-id and --scope are required")
		}
		if _, ok := attrs["scope"]; !ok {
			return errs.New(errs.ExitUsage, "nothing to create: --contact-id and --scope are required")
		}

		res, err := c.client.Create(c.ctx, overridesPath(teamID, hubID, ""), attrs)
		if err != nil {
			return hintGlobalContactID(err)
		}
		return c.render(cmd, res)
	},
}

var accessRulesOverridesListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List access overrides for a hub.",
	Long:    "List all per-contact access overrides for the active hub.",
	Example: `  mio access-rules overrides list --hub hub_abc`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, overridesPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var accessRulesOverridesRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an access override by id.",
	Long:    "Fetch a single access override by its id from the active hub.",
	Example: `  mio access-rules overrides retrieve ao_abc123 --hub hub_abc`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, overridesPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var accessRulesOverridesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an access override by id.",
	Long: `Partially update an access override. Only the flags you supply are changed.

Scope must be one of: full, basic, product. When changing scope to "product",
supply --product-id as well.`,
	Example: `  mio access-rules overrides update ao_abc123 --hub hub_abc --scope basic
  mio access-rules overrides update ao_abc123 --hub hub_abc \
    --expires-at "2027-01-01T00:00:00Z" --reason "Extended trial"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "scope")
		setStringFlag(cmd, attrs, "product-id")
		setStringFlag(cmd, attrs, "expires-at")
		setStringFlag(cmd, attrs, "reason")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, overridesPath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var accessRulesOverridesDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete an access override by id.",
	Long:    "Permanently delete an access override from the active hub.",
	Example: `  mio access-rules overrides delete ao_abc123 --hub hub_abc --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := accessRulesContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete access override %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, overridesPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted access override %s.\n", args[0])
		return nil
	},
}

// ---- flag registration ------------------------------------------------------

func init() {
	// Rules create flags.
	accessRulesRulesCreateCmd.Flags().String("target-type", "", "Target type to gate (section or content_node).")
	accessRulesRulesCreateCmd.Flags().String("target-id", "", "Id of the target section or content node.")
	accessRulesRulesCreateCmd.Flags().String("logic-operator", "", "Condition logic operator: any (default) or all.")
	accessRulesRulesCreateCmd.Flags().String("conditions", "", "JSON array of condition objects [{condition_type, condition_data, position}].")

	// Rules update flags.
	accessRulesRulesUpdateCmd.Flags().String("logic-operator", "", "Condition logic operator: any or all.")
	accessRulesRulesUpdateCmd.Flags().String("conditions", "", "JSON array of condition objects [{condition_type, condition_data, position}] to replace the existing list.")

	// Rules pagination.
	addPaginationFlags(accessRulesRulesListCmd)

	// Overrides create flags.
	accessRulesOverridesCreateCmd.Flags().String("contact-id", "", "GLOBAL contact id (.attributes.contact_id from 'mio contacts', NOT its .id) to grant or restrict access for. Required.")
	accessRulesOverridesCreateCmd.Flags().String("scope", "", "Override scope: full, basic, or product.")
	accessRulesOverridesCreateCmd.Flags().String("product-id", "", "Id of the product (required when scope is \"product\").")
	accessRulesOverridesCreateCmd.Flags().String("expires-at", "", "Override expiry timestamp in RFC 3339 format (e.g. 2026-12-31T00:00:00Z).")
	accessRulesOverridesCreateCmd.Flags().String("reason", "", "Human-readable reason for this override.")

	// Overrides update flags.
	accessRulesOverridesUpdateCmd.Flags().String("scope", "", "Override scope: full, basic, or product.")
	accessRulesOverridesUpdateCmd.Flags().String("product-id", "", "Id of the product (required when scope is \"product\").")
	accessRulesOverridesUpdateCmd.Flags().String("expires-at", "", "Override expiry timestamp in RFC 3339 format (e.g. 2026-12-31T00:00:00Z).")
	accessRulesOverridesUpdateCmd.Flags().String("reason", "", "Human-readable reason for this override.")

	// Overrides pagination.
	addPaginationFlags(accessRulesOverridesListCmd)
}

// ---- helpers ----------------------------------------------------------------

// setJSONArrayFlag parses a JSON-array string flag into attrs[name] iff it was
// set by the user. The flag value must be a valid JSON array; an error is
// returned if it is set but cannot be decoded.
func setJSONArrayFlag(cmd *cobra.Command, attrs map[string]any, name string) error {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	raw, err := cmd.Flags().GetString(name)
	if err != nil || raw == "" {
		return nil
	}
	var parsed []any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return errs.New(errs.ExitUsage, "--%s must be a valid JSON array: %s", name, err)
	}
	attrs[attrKey(name)] = parsed
	return nil
}
