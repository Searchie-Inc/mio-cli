package cmd

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	contactAttributesCmd.AddCommand(
		contactAttributesCreateCmd,
		contactAttributesListCmd,
		contactAttributesRetrieveCmd,
		contactAttributesUpdateCmd,
		contactAttributesDeleteCmd,
	)

	contactAttributesOptionsCmd.AddCommand(
		contactAttributesOptionsCreateCmd,
		contactAttributesOptionsListCmd,
		contactAttributesOptionsUpdateCmd,
		contactAttributesOptionsDeleteCmd,
	)
	contactAttributesCmd.AddCommand(contactAttributesOptionsCmd)

	contactAttributesHubConfigCmd.AddCommand(
		contactAttributesHubConfigCreateCmd,
		contactAttributesHubConfigListCmd,
		contactAttributesHubConfigUpdateCmd,
		contactAttributesHubConfigDeleteCmd,
	)
	contactAttributesCmd.AddCommand(contactAttributesHubConfigCmd)

	contactAttributesValuesCmd.AddCommand(
		contactAttributesValuesGetCmd,
		contactAttributesValuesSetCmd,
	)
	contactAttributesCmd.AddCommand(contactAttributesValuesCmd)

	rootCmd.AddCommand(contactAttributesCmd)
}

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

func contactAttributesDefsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/contact-attributes", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

func contactAttributesOptionsPath(teamID, def, id string) string {
	base := fmt.Sprintf("/api/teams/%s/contact-attributes/%s/options", teamID, def)
	if id != "" {
		return base + "/" + id
	}
	return base
}

func contactAttributesHubConfigPath(teamID, hubID, def string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/contact-attributes", teamID, hubID)
	if def != "" {
		return base + "/" + def
	}
	return base
}

func contactAttributesValuesPath(teamID, contactID string) string {
	return fmt.Sprintf("/api/teams/%s/contacts/%s/attributes", teamID, contactID)
}

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

var attrDefFieldTypes = map[string]bool{
	"text":     true,
	"number":   true,
	"boolean":  true,
	"date":     true,
	"multiple": true,
	"single":   true,
}

type AttrDefInput struct {
	Name              *string
	Slug              *string
	FieldType         *string
	Description       *string
	IsContactEditable *bool
	Position          *int
}

func buildAttrDefCreateAttrs(d AttrDefInput) (map[string]any, error) {
	attrs := map[string]any{}
	if d.Name != nil {
		attrs["name"] = *d.Name
	}
	if d.Slug != nil {
		attrs["slug"] = *d.Slug
	}
	if d.FieldType != nil {
		if !attrDefFieldTypes[*d.FieldType] {
			return nil, errs.New(errs.ExitUsage, "invalid --field-type %q: must be text, number, boolean, date, multiple, or single", *d.FieldType)
		}
		attrs["type"] = *d.FieldType
	}
	if d.Description != nil {
		attrs["description"] = *d.Description
	}
	if d.IsContactEditable != nil {
		attrs["is_contact_editable"] = *d.IsContactEditable
	}
	if d.Position != nil {
		attrs["position"] = *d.Position
	}
	return attrs, nil
}

var contactAttributesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a contact attribute definition.",
	Long:  "Create a new contact attribute definition for the active team.",
	Example: `  # Create a text attribute
  mio contact-attributes create --name="Company" --slug="company" --field-type=text

  # Create a single-select attribute
  mio contact-attributes create --name="Plan" --slug="plan" --field-type=single

  # Create a multiple-select attribute
  mio contact-attributes create --name="Tier" --slug="tier" --field-type=multiple`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		attrs, err := buildAttrDefCreateAttrs(AttrDefInput{
			Name:              changedString(cmd, "name"),
			Slug:              changedString(cmd, "slug"),
			FieldType:         changedString(cmd, "field-type"),
			Description:       changedString(cmd, "description"),
			IsContactEditable: changedBool(cmd, "is-contact-editable"),
			Position:          changedInt(cmd, "position"),
		})
		if err != nil {
			return err
		}

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
	contactAttributesCreateCmd.Flags().String("name", "", "Attribute name. Required.")
	contactAttributesCreateCmd.Flags().String("slug", "", "Attribute slug (unique identifier within the team). Required.")
	contactAttributesCreateCmd.Flags().String("field-type", "", "Attribute field type: text, number, boolean, date, multiple, or single. Required.")
	markFlagsRequired(contactAttributesCreateCmd, "name", "slug", "field-type")
	contactAttributesCreateCmd.Flags().String("description", "", "Optional description or hint for this attribute.")
	contactAttributesCreateCmd.Flags().Bool("is-contact-editable", true, "Whether contacts can edit this attribute themselves.")
	contactAttributesCreateCmd.Flags().Int("position", 0, "Display order position (lower numbers appear first).")

	contactAttributesUpdateCmd.Flags().String("name", "", "Attribute name.")
	contactAttributesUpdateCmd.Flags().String("slug", "", "Attribute slug.")
	contactAttributesUpdateCmd.Flags().String("description", "", "Optional description or hint for this attribute.")
	contactAttributesUpdateCmd.Flags().Bool("is-contact-editable", true, "Whether contacts can edit this attribute themselves.")
	contactAttributesUpdateCmd.Flags().Int("position", 0, "Display order position (lower numbers appear first).")

	addPaginationFlags(contactAttributesListCmd)
}

var contactAttributesOptionsCmd = &cobra.Command{
	Use:   "options",
	Short: "Manage predefined options for a contact attribute definition.",
	Long:  "Create, list, update and delete option values for select or multi-select contact attribute definitions.",
}

var contactAttributesOptionsCreateCmd = &cobra.Command{
	Use:     "create <def_id>",
	Short:   "Create an option for a contact attribute definition.",
	Long:    "Add a new predefined option value to a select or multi-select contact attribute definition.",
	Example: `  mio contact-attributes options create attr_abc123 --label="Enterprise" --slug="enterprise"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "label")
		setStringFlag(cmd, attrs, "slug")
		setIntFlag(cmd, attrs, "position")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --label and --slug")
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
	Long:    "Update the label, slug, or position of a predefined option on a contact attribute definition.",
	Example: `  mio contact-attributes options update attr_abc123 opt_xyz --label="Enterprise Tier"`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := caContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "label")
		setStringFlag(cmd, attrs, "slug")
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
		cmd.Flags().String("slug", "", "Machine-readable slug for the option (the backend OptionCreate/Update field). Required on create.")
		cmd.Flags().Int("position", 0, "Display order position (lower numbers appear first).")
	}
	addPaginationFlags(contactAttributesOptionsListCmd)
}

var contactAttributesHubConfigCmd = &cobra.Command{
	Use:   "hub-config",
	Short: "Manage per-hub contact attribute configuration.",
	Long: `Configure which contact attributes are active (and in what order) for a specific hub.

Requires --hub to be set (or configured via 'mio config set current_hub <id>').`,
}

var contactAttributesHubConfigCreateCmd = &cobra.Command{
	Use:   "create <def_id>",
	Short: "Enable a contact attribute on a hub.",
	Long: `Enable a contact attribute definition on the active hub, optionally configuring
its display order and visibility.`,
	Example: `  mio contact-attributes hub-config create attr_abc123 --hub hub_xyz --position=1 --in-profile`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := caHubContext(cmd)
		if err != nil {
			return err
		}

		attrs := buildHubConfigAttrs(args[0], HubConfigInput{
			IsInProfile:    changedBool(cmd, "in-profile"),
			IsInOnboarding: changedBool(cmd, "in-onboarding"),
			IsRequired:     changedBool(cmd, "required"),
			IsReadOnly:     changedBool(cmd, "read-only"),
			IsSearchable:   changedBool(cmd, "searchable"),
		})
		setIntFlag(cmd, attrs, "position")

		res, err := c.client.Create(c.ctx, contactAttributesHubConfigPath(teamID, hubID, ""), attrs)
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
	Example: `  mio contact-attributes hub-config update attr_abc123 --hub hub_xyz --position=3 --required`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := caHubContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setIntFlag(cmd, attrs, "position")
		setHubConfigBoolFlags(cmd, attrs)

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
		cmd.Flags().Bool("in-profile", false, "Show this attribute on the contact's hub profile.")
		cmd.Flags().Bool("in-onboarding", false, "Collect this attribute during hub onboarding.")
		cmd.Flags().Bool("required", false, "Require a value for this attribute in the hub.")
		cmd.Flags().Bool("read-only", false, "Make this attribute read-only for contacts in the hub.")
		cmd.Flags().Bool("searchable", false, "Allow searching contacts by this attribute in the hub.")
	}
	addPaginationFlags(contactAttributesHubConfigListCmd)
}

func setHubConfigBoolFlags(cmd *cobra.Command, attrs map[string]any) {
	setMappedBool(cmd, attrs, "in-profile", "is_in_profile")
	setMappedBool(cmd, attrs, "in-onboarding", "is_in_onboarding")
	setMappedBool(cmd, attrs, "required", "is_required")
	setMappedBool(cmd, attrs, "read-only", "is_read_only")
	setMappedBool(cmd, attrs, "searchable", "is_searchable")
}

type HubConfigInput struct {
	IsInProfile    *bool
	IsInOnboarding *bool
	IsRequired     *bool
	IsReadOnly     *bool
	IsSearchable   *bool
}

func buildHubConfigAttrs(defID string, d HubConfigInput) map[string]any {
	attrs := map[string]any{"definition_id": defID}
	if d.IsInProfile != nil {
		attrs["is_in_profile"] = *d.IsInProfile
	}
	if d.IsInOnboarding != nil {
		attrs["is_in_onboarding"] = *d.IsInOnboarding
	}
	if d.IsRequired != nil {
		attrs["is_required"] = *d.IsRequired
	}
	if d.IsReadOnly != nil {
		attrs["is_read_only"] = *d.IsReadOnly
	}
	if d.IsSearchable != nil {
		attrs["is_searchable"] = *d.IsSearchable
	}
	return attrs
}

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

		col, err := c.client.List(c.ctx, contactAttributesValuesPath(teamID, args[0]), url.Values{})
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var contactAttributesValuesSetCmd = &cobra.Command{
	Use:   "set <contact_id>",
	Short: "Set attribute values on a contact.",
	Long: `Set one or more contact attribute values for a specific contact.
Pass each attribute as --attr <key>=<value>. Multiple --attr flags may be used.

For a single- or multiple-select attribute, <value> names one of the
attribute's options — by option id, by option slug, or by label
(case-insensitive). Setting a select attribute REPLACES its entire current
selection with the value(s) given in this command — it does not add to
whatever the contact already has. A multiple-select attribute may name more
than one option: repeat --attr for the same key, or pass a comma-separated
list — every --attr for one key in a single command merges into ONE
replacing set (it never accumulates across separate commands: running this
command again later replaces the selection again, from scratch). An option
whose slug or label contains a comma must be passed by id. A single-select
attribute accepts exactly one value.`,
	Example: `  mio contact-attributes values set tcid_abc123 --attr company=Acme --attr tier=enterprise
  mio contact-attributes values set tcid_abc123 --attr plan=Gold
  # Both --attr flags below name the SAME multiple-select attribute, so they merge into
  # one replacing set (interests = music, sports, travel) — not two incremental adds.
  mio contact-attributes values set tcid_abc123 --attr interests=music,sports --attr interests=travel`,
	Args: cobra.ExactArgs(1),
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

		type attrPair struct{ key, val string }
		pairs := make([]attrPair, 0, len(rawAttrs))
		for _, kv := range rawAttrs {
			key, val, ok := splitKV(kv)
			if !ok {
				return errs.New(errs.ExitUsage, "invalid --attr value %q: expected key=value", kv)
			}
			pairs = append(pairs, attrPair{key, val})
		}

		fieldTypes, err := caDefFieldTypesBySlug(c, teamID)
		if err != nil {
			return err
		}

		type caSelectGroup struct {
			def     caAttrDef
			values  []string
			seen    map[string]bool
			opIndex int
		}
		selectGroups := map[string]*caSelectGroup{}
		var selectOrder []string

		ops := make([]map[string]any, 0, len(pairs))
		for _, p := range pairs {
			def, known := fieldTypes[p.key]
			if !known {
				return errs.New(errs.ExitUsage,
					"unknown attribute slug %q: this team has no contact-attribute definition with that slug", p.key)
			}

			if def.FieldType != "multiple" && def.FieldType != "single" {
				attrs := map[string]any{"definition_slug": p.key}
				if err := setTypedAttrValue(attrs, p.key, def.FieldType, p.val); err != nil {
					return err
				}
				ops = append(ops, map[string]any{
					"type":       "set",
					"attributes": attrs,
				})
				continue
			}

			g, ok := selectGroups[p.key]
			if !ok {
				g = &caSelectGroup{def: def, seen: map[string]bool{}, opIndex: len(ops)}
				ops = append(ops, nil)
				selectGroups[p.key] = g
				selectOrder = append(selectOrder, p.key)
			}
			for _, tok := range strings.Split(p.val, ",") {
				if tok == "" {
					return errs.New(errs.ExitUsage,
						"invalid --attr value for attribute %q: option value cannot be empty", p.key)
				}
				if g.seen[tok] {
					continue
				}
				g.seen[tok] = true
				g.values = append(g.values, tok)
			}
		}

		for _, slug := range selectOrder {
			g := selectGroups[slug]
			if g.def.FieldType == "single" && len(g.values) > 1 {
				return errs.New(errs.ExitUsage,
					"attribute %q accepts exactly one option, not %d (%s)",
					slug, len(g.values), strings.Join(g.values, ", "))
			}
		}

		for _, slug := range selectOrder {
			g := selectGroups[slug]
			options, err := caOptionsByDef(c, teamID, g.def.ID)
			if err != nil {
				return err
			}

			resolvedIDs := make([]string, 0, len(g.values))
			seenIDs := map[string]bool{}
			for _, raw := range g.values {
				id, err := resolveCAOptionValue(slug, options, raw)
				if err != nil {
					return err
				}
				if seenIDs[id] {
					continue
				}
				seenIDs[id] = true
				resolvedIDs = append(resolvedIDs, id)
			}

			ops[g.opIndex] = map[string]any{
				"type": "set",
				"attributes": map[string]any{
					"definition_slug": slug,
					"option_ids":      resolvedIDs,
				},
			}
		}

		col, err := c.client.ActionCollectionRaw(c.ctx, http.MethodPatch,
			contactAttributesValuesPath(teamID, args[0]), map[string]any{"data": ops})
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

func init() {
	contactAttributesValuesSetCmd.Flags().StringArray("attr", nil, "Attribute key=value pair to set. May be repeated for multiple attributes. For a single/multiple-select attribute, value is an option id, slug, or label — comma-separated or repeated --attr to set more than one option on a multiple-select attribute.")
}

func splitKV(s string) (key, val string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

type caAttrDef struct {
	ID        string
	FieldType string
}

func caNextDefsPageAfter(col *client.Collection) string {
	if hasMore, present := col.Meta["has_more"].(bool); present && !hasMore {
		return ""
	}
	var doc struct {
		Links struct {
			Next string `json:"next"`
		} `json:"links"`
	}
	if err := json.Unmarshal(col.RawBody, &doc); err != nil || doc.Links.Next == "" {
		return ""
	}
	u, err := url.Parse(doc.Links.Next)
	if err != nil {
		return ""
	}
	return u.Query().Get("page[after]")
}

func caDefFieldTypesBySlug(c *cmdContext, teamID string) (map[string]caAttrDef, error) {
	out := map[string]caAttrDef{}
	seen := map[string]bool{}
	query := url.Values{}
	query.Set("page[size]", "100")
	const maxPages = 1000
	for page := 0; page < maxPages; page++ {
		col, err := c.client.List(c.ctx, contactAttributesDefsPath(teamID, ""), query)
		if err != nil {
			return nil, err
		}
		for _, r := range col.Data {
			slug, _ := r.Attributes["slug"].(string)
			ft, _ := r.Attributes["type"].(string)
			if slug != "" && ft != "" {
				out[slug] = caAttrDef{ID: r.ID, FieldType: ft}
			}
		}
		next := caNextDefsPageAfter(col)
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		query = url.Values{}
		query.Set("page[size]", "100")
		query.Set("page[after]", next)
	}
	return out, nil
}

type caAttrOption struct {
	ID    string
	Slug  string
	Label string
}

func caOptionsByDef(c *cmdContext, teamID, defID string) ([]caAttrOption, error) {
	col, err := c.client.List(c.ctx, contactAttributesOptionsPath(teamID, defID, ""), url.Values{})
	if err != nil {
		return nil, err
	}
	out := make([]caAttrOption, 0, len(col.Data))
	for _, r := range col.Data {
		slug, _ := r.Attributes["slug"].(string)
		label, _ := r.Attributes["label"].(string)
		out = append(out, caAttrOption{ID: r.ID, Slug: slug, Label: label})
	}
	return out, nil
}

func resolveCAOptionValue(defSlug string, options []caAttrOption, raw string) (string, error) {
	for _, o := range options {
		if o.ID == raw {
			return o.ID, nil
		}
	}
	for _, o := range options {
		if o.Slug == raw {
			return o.ID, nil
		}
	}
	var labelMatches []caAttrOption
	for _, o := range options {
		if strings.EqualFold(o.Label, raw) {
			labelMatches = append(labelMatches, o)
		}
	}
	switch len(labelMatches) {
	case 1:
		return labelMatches[0].ID, nil
	case 0:
		return "", errs.New(errs.ExitUsage,
			"unknown option %q for attribute %q: available options are %s",
			raw, defSlug, formatCAOptions(options))
	default:
		return "", errs.New(errs.ExitUsage,
			"option %q for attribute %q is ambiguous: %d options share that label — pass the option id or slug instead",
			raw, defSlug, len(labelMatches))
	}
}

func formatCAOptions(options []caAttrOption) string {
	parts := make([]string, 0, len(options))
	for _, o := range options {
		parts = append(parts, fmt.Sprintf("%s (%s)", o.Slug, o.Label))
	}
	return strings.Join(parts, ", ")
}

var caDateLayouts = []string{
	"2006-01-02",
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05Z",
}

func setTypedAttrValue(attrs map[string]any, slug, fieldType, val string) error {
	switch fieldType {
	case "number":
		n, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return errs.New(errs.ExitUsage, "invalid number value %q for attribute %q: %v", val, slug, err)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return errs.New(errs.ExitUsage, "invalid number value %q for attribute %q: must be a finite number", val, slug)
		}
		attrs["value_number"] = n
	case "boolean":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return errs.New(errs.ExitUsage, "invalid boolean value %q for attribute %q (use true/false): %v", val, slug, err)
		}
		attrs["value_boolean"] = b
	case "date":
		if !isValidCADate(val) {
			return errs.New(errs.ExitUsage,
				"invalid date value %q for attribute %q: use an ISO-8601 date like 2006-01-02 or 2006-01-02T15:04:05Z", val, slug)
		}
		attrs["value_date"] = val
	default:
		attrs["value_text"] = val
	}
	return nil
}

func isValidCADate(val string) bool {
	for _, layout := range caDateLayouts {
		if _, err := time.Parse(layout, val); err == nil {
			return true
		}
	}
	return false
}
