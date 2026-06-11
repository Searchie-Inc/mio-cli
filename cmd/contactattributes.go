package cmd

// contactattributes.go implements the `mio contact-attributes` command tree.
//
// Sub-resource groups (all team-scoped unless noted):
//
//	defs:       create/list/retrieve/update/delete /api/teams/{team_id}/contact-attributes[/{id}]
//	options:    create/list/update/delete           /api/teams/{team_id}/contact-attributes/{def}/options[/{id}]
//	hub-config: create/list/update/delete           /api/teams/{team_id}/hubs/{hub_id}/contact-attributes[/{def}]
//	values:     get/set                             /api/teams/{team_id}/contacts/{tcid}/attributes
//
// Self-registered via init(); no other file is modified.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// defs actions
	contactAttributesCmd.AddCommand(
		contactAttributesCreateCmd,
		contactAttributesListCmd,
		contactAttributesRetrieveCmd,
		contactAttributesUpdateCmd,
		contactAttributesDeleteCmd,
	)

	// options sub-group
	contactAttributesOptionsCmd.AddCommand(
		contactAttributesOptionsCreateCmd,
		contactAttributesOptionsListCmd,
		contactAttributesOptionsUpdateCmd,
		contactAttributesOptionsDeleteCmd,
	)
	contactAttributesCmd.AddCommand(contactAttributesOptionsCmd)

	// hub-config sub-group
	contactAttributesHubConfigCmd.AddCommand(
		contactAttributesHubConfigCreateCmd,
		contactAttributesHubConfigListCmd,
		contactAttributesHubConfigUpdateCmd,
		contactAttributesHubConfigDeleteCmd,
	)
	contactAttributesCmd.AddCommand(contactAttributesHubConfigCmd)

	// values sub-group
	contactAttributesValuesCmd.AddCommand(
		contactAttributesValuesGetCmd,
		contactAttributesValuesSetCmd,
	)
	contactAttributesCmd.AddCommand(contactAttributesValuesCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(contactAttributesCmd)
}

// ---- root group -------------------------------------------------------------

var contactAttributesCmd = &cobra.Command{
	Use:   "contact-attributes",
	Short: "Manage contact attribute definitions, options, hub config, and values.",
	Long: `Manage contact attribute definitions for the active team.

Sub-commands are grouped into four areas:
  defs        — attribute definition CRUD
  options     — predefined option values for select/multi-select attributes
  hub-config  — per-hub attribute configuration (ordering, visibility)
  values      — read and write attribute values on individual contacts`,
}

// ---- path helpers -----------------------------------------------------------

// contactAttributesDefsPath builds /api/teams/{team_id}/contact-attributes[/{id}].
func contactAttributesDefsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/contact-attributes", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// contactAttributesOptionsPath builds /api/teams/{team_id}/contact-attributes/{def}/options[/{id}].
func contactAttributesOptionsPath(teamID, def, id string) string {
	base := fmt.Sprintf("/api/teams/%s/contact-attributes/%s/options", teamID, def)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// contactAttributesHubConfigPath builds /api/teams/{team_id}/hubs/{hub_id}/contact-attributes[/{def}].
func contactAttributesHubConfigPath(teamID, hubID, def string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/contact-attributes", teamID, hubID)
	if def != "" {
		return base + "/" + def
	}
	return base
}

// contactAttributesValuesPath builds /api/teams/{team_id}/contacts/{tcid}/attributes.
func contactAttributesValuesPath(teamID, contactID string) string {
	return fmt.Sprintf("/api/teams/%s/contacts/%s/attributes", teamID, contactID)
}

// ---- shared context helpers -------------------------------------------------

// caContext is the shared boilerplate for contact-attribute defs sub-commands:
// build the context, require auth, resolve team id.
func caContext(cmd *cobra.Command) (*cmdContext, string, error) {
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

// caHubContext is the shared boilerplate for hub-scoped sub-commands:
// build the context, require auth, resolve team id and hub id.
func caHubContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
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

// ============================================================================
// defs: create/list/retrieve/update/delete
// ============================================================================

var contactAttributesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a contact attribute definition.",
	Long:  "Create a new contact attribute definition for the active team.",
	Example: `  # Create a text attribute
  mio contact-attributes create --name="Company" --slug="company" --field-type=text

  # Create a multiple-select attribute
  mio contact-attributes create --name="Tier" --slug="tier" --field-type=multiple`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		var missing []string
		if !cmd.Flags().Changed("name") {
			missing = append(missing, "--name")
		}
		if !cmd.Flags().Changed("slug") {
			missing = append(missing, "--slug")
		}
		if !cmd.Flags().Changed("field-type") {
			missing = append(missing, "--field-type")
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flags: %s", strings.Join(missing, ", "))
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "slug")
		// --field-type maps to backend field "type" (domain attribute type, not resource type)
		setMappedString(cmd, attrs, "field-type", "type")
		setStringFlag(cmd, attrs, "description")
		setBoolFlag(cmd, attrs, "is-contact-editable")
		setIntFlag(cmd, attrs, "position")

		res, err := c.client.Create(c.ctx, contactAttributesDefsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var contactAttributesListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List contact attribute definitions.",
	Long:    "List all contact attribute definitions for the active team.",
	Example: `  mio contact-attributes list`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, contactAttributesDefsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var contactAttributesRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a contact attribute definition by id.",
	Long:    "Retrieve a single contact attribute definition by its id.",
	Example: `  mio contact-attributes retrieve attr_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, contactAttributesDefsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var contactAttributesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a contact attribute definition by id.",
	Long:  "Update one or more fields on a contact attribute definition. Only the flags you set are changed. Note: field type is immutable and cannot be updated.",
	Example: `  mio contact-attributes update attr_abc123 --name="Company Name"
  mio contact-attributes update attr_abc123 --slug="company-name" --description="Employer name"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setBoolFlag(cmd, attrs, "is-contact-editable")
		setIntFlag(cmd, attrs, "position")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, contactAttributesDefsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var contactAttributesDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a contact attribute definition by id.",
	Long:    "Permanently delete a contact attribute definition and all its values. This action cannot be undone.",
	Example: `  mio contact-attributes delete attr_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete contact attribute %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, contactAttributesDefsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted contact attribute %s.\n", args[0])
		return nil
	},
}

func init() {
	// create-only flags (field type is required on create; immutable on update)
	contactAttributesCreateCmd.Flags().String("name", "", "Attribute name. Required.")
	contactAttributesCreateCmd.Flags().String("slug", "", "Attribute slug (unique identifier within the team). Required.")
	contactAttributesCreateCmd.Flags().String("field-type", "", "Attribute field type: text, number, boolean, date, or multiple. Required.")
	contactAttributesCreateCmd.Flags().String("description", "", "Optional description or hint for this attribute.")
	contactAttributesCreateCmd.Flags().Bool("is-contact-editable", true, "Whether contacts can edit this attribute themselves.")
	contactAttributesCreateCmd.Flags().Int("position", 0, "Display order position (lower numbers appear first).")

	// update flags (field type is immutable — not exposed here)
	contactAttributesUpdateCmd.Flags().String("name", "", "Attribute name.")
	contactAttributesUpdateCmd.Flags().String("slug", "", "Attribute slug.")
	contactAttributesUpdateCmd.Flags().String("description", "", "Optional description or hint for this attribute.")
	contactAttributesUpdateCmd.Flags().Bool("is-contact-editable", true, "Whether contacts can edit this attribute themselves.")
	contactAttributesUpdateCmd.Flags().Int("position", 0, "Display order position (lower numbers appear first).")

	addPaginationFlags(contactAttributesListCmd)
}

// ============================================================================
// options: create/list/update/delete under a definition
// ============================================================================

var contactAttributesOptionsCmd = &cobra.Command{
	Use:   "options",
	Short: "Manage predefined options for a contact attribute definition.",
	Long:  "Create, list, update and delete option values for select or multi-select contact attribute definitions.",
}

var contactAttributesOptionsCreateCmd = &cobra.Command{
	Use:     "create <def_id>",
	Short:   "Create an option for a contact attribute definition.",
	Long:    "Add a new predefined option value to a select or multi-select contact attribute definition.",
	Example: `  mio contact-attributes options create attr_abc123 --label="Enterprise" --value="enterprise"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "label")
		setStringFlag(cmd, attrs, "value")
		setIntFlag(cmd, attrs, "position")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --label and --value")
		}

		res, err := c.client.Create(c.ctx, contactAttributesOptionsPath(teamID, args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var contactAttributesOptionsListCmd = &cobra.Command{
	Use:     "list <def_id>",
	Short:   "List options for a contact attribute definition.",
	Long:    "List all predefined option values for a given contact attribute definition.",
	Example: `  mio contact-attributes options list attr_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, contactAttributesOptionsPath(teamID, args[0], ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var contactAttributesOptionsUpdateCmd = &cobra.Command{
	Use:     "update <def_id> <option_id>",
	Short:   "Update an option on a contact attribute definition.",
	Long:    "Update the label, value, or position of a predefined option on a contact attribute definition.",
	Example: `  mio contact-attributes options update attr_abc123 opt_xyz --label="Enterprise Tier"`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "label")
		setStringFlag(cmd, attrs, "value")
		setIntFlag(cmd, attrs, "position")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, contactAttributesOptionsPath(teamID, args[0], args[1]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var contactAttributesOptionsDeleteCmd = &cobra.Command{
	Use:     "delete <def_id> <option_id>",
	Short:   "Delete an option from a contact attribute definition.",
	Long:    "Permanently delete a predefined option from a contact attribute definition.",
	Example: `  mio contact-attributes options delete attr_abc123 opt_xyz --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete option %s from attribute %s?", args[1], args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, contactAttributesOptionsPath(teamID, args[0], args[1])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted option %s from attribute %s.\n", args[1], args[0])
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{contactAttributesOptionsCreateCmd, contactAttributesOptionsUpdateCmd} {
		cmd.Flags().String("label", "", "Human-readable label for the option.")
		cmd.Flags().String("value", "", "Machine-readable value stored when this option is selected.")
		cmd.Flags().Int("position", 0, "Display order position (lower numbers appear first).")
	}
	addPaginationFlags(contactAttributesOptionsListCmd)
}

// ============================================================================
// hub-config: create/list/update/delete per-hub attribute configuration
// ============================================================================

var contactAttributesHubConfigCmd = &cobra.Command{
	Use:   "hub-config",
	Short: "Manage per-hub contact attribute configuration.",
	Long: `Configure which contact attributes are active (and in what order) for a specific hub.

Requires --hub to be set (or configured via 'mio config set hub <id>').`,
}

var contactAttributesHubConfigCreateCmd = &cobra.Command{
	Use:   "create <def_id>",
	Short: "Enable a contact attribute on a hub.",
	Long: `Enable a contact attribute definition on the active hub, optionally configuring
its display order and visibility.`,
	Example: `  mio contact-attributes hub-config create attr_abc123 --hub hub_xyz --position=1 --visible=true`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := caHubContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setIntFlag(cmd, attrs, "position")
		setBoolFlag(cmd, attrs, "visible")

		res, err := c.client.Create(c.ctx, contactAttributesHubConfigPath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var contactAttributesHubConfigListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List contact attribute hub-config entries for a hub.",
	Long:    "List all contact attribute configuration entries for the active hub.",
	Example: `  mio contact-attributes hub-config list --hub hub_xyz`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := caHubContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, contactAttributesHubConfigPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var contactAttributesHubConfigUpdateCmd = &cobra.Command{
	Use:     "update <def_id>",
	Short:   "Update a contact attribute's hub configuration.",
	Long:    "Update the position or visibility of a contact attribute on the active hub.",
	Example: `  mio contact-attributes hub-config update attr_abc123 --hub hub_xyz --position=3`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := caHubContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setIntFlag(cmd, attrs, "position")
		setBoolFlag(cmd, attrs, "visible")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, contactAttributesHubConfigPath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var contactAttributesHubConfigDeleteCmd = &cobra.Command{
	Use:     "delete <def_id>",
	Short:   "Remove a contact attribute from a hub's configuration.",
	Long:    "Remove (disable) a contact attribute definition from the active hub. The definition and its values are not deleted.",
	Example: `  mio contact-attributes hub-config delete attr_abc123 --hub hub_xyz --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := caHubContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Remove attribute %s from hub %s config?", args[0], hubID)); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, contactAttributesHubConfigPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed attribute %s from hub %s config.\n", args[0], hubID)
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{contactAttributesHubConfigCreateCmd, contactAttributesHubConfigUpdateCmd} {
		cmd.Flags().Int("position", 0, "Display order position within the hub (lower numbers appear first).")
		cmd.Flags().Bool("visible", false, "Whether the attribute is visible to contacts in this hub.")
	}
	addPaginationFlags(contactAttributesHubConfigListCmd)
}

// ============================================================================
// values: get/set attribute values on a contact
// ============================================================================

var contactAttributesValuesCmd = &cobra.Command{
	Use:   "values",
	Short: "Read and write attribute values on a contact.",
	Long:  "Get or set contact attribute values for a specific contact record.",
}

var contactAttributesValuesGetCmd = &cobra.Command{
	Use:     "get <contact_id>",
	Short:   "Get all attribute values for a contact.",
	Long:    "Retrieve all contact attribute key/value pairs for a specific contact.",
	Example: `  mio contact-attributes values get tcid_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, contactAttributesValuesPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var contactAttributesValuesSetCmd = &cobra.Command{
	Use:   "set <contact_id>",
	Short: "Set attribute values on a contact.",
	Long: `Set one or more contact attribute values for a specific contact.
Pass each attribute as --attr <key>=<value>. Multiple --attr flags may be used.`,
	Example: `  mio contact-attributes values set tcid_abc123 --attr company=Acme --attr tier=enterprise`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		rawAttrs, err := cmd.Flags().GetStringArray("attr")
		if err != nil {
			return errs.Wrap(errs.ExitUsage, err)
		}
		if len(rawAttrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to set: provide at least one --attr key=value pair")
		}

		attrs := map[string]any{}
		for _, kv := range rawAttrs {
			key, val, ok := splitKV(kv)
			if !ok {
				return errs.New(errs.ExitUsage, "invalid --attr value %q: expected key=value", kv)
			}
			attrs[key] = val
		}

		res, err := c.client.Update(c.ctx, contactAttributesValuesPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	contactAttributesValuesSetCmd.Flags().StringArray("attr", nil, "Attribute key=value pair to set. May be repeated for multiple attributes.")
}

// splitKV splits a "key=value" string. Returns ok=false if no '=' is found.
func splitKV(s string) (key, val string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}
