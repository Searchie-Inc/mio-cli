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
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

// attrDefFieldTypes is the set of accepted --field-type values (the backend
// AttributeType enum: text/number/boolean/date/multiple/single — mio-backend
// app/contact_attributes/schemas.py's ATTRIBUTE_TYPES/AttributeType, mirrored
// server-side by the models.py:60 CHECK constraint). Validated client-side so
// a typo exits ExitUsage rather than a 422 round-trip — NEW client-side
// validation added with the pure-builder extraction (MIO-2543); "single"
// added MIO-4124 (it was missing even though the backend enum, and this
// command's own read/set paths, have always supported it).
var attrDefFieldTypes = map[string]bool{
	"text":     true,
	"number":   true,
	"boolean":  true,
	"date":     true,
	"multiple": true,
	"single":   true,
}

// AttrDefInput carries the resolved attribute-definition create attributes,
// decoupled from *cobra.Command so both `contact-attributes create` and the
// scaffold (MIO-2543) can build the same POST body. Each pointer is nil when the
// flag was unset. The required-flag ergonomics (name/slug/field-type must be set)
// stay with the command; this builder validates the field-type VALUE.
type AttrDefInput struct {
	Name              *string
	Slug              *string
	FieldType         *string // → type (validated enum)
	Description       *string
	IsContactEditable *bool // → is_contact_editable
	Position          *int
}

// buildAttrDefCreateAttrs assembles the attribute-definition create body from d.
// --field-type maps to the backend field "type" (the domain attribute type, not
// the JSON:API resource type) and is validated against attrDefFieldTypes. It is a
// pure builder (takes data, not flags) so the scaffold gets the same validation.
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
	// create-only flags (field type is required on create; immutable on update)
	contactAttributesCreateCmd.Flags().String("name", "", "Attribute name. Required.")
	contactAttributesCreateCmd.Flags().String("slug", "", "Attribute slug (unique identifier within the team). Required.")
	contactAttributesCreateCmd.Flags().String("field-type", "", "Attribute field type: text, number, boolean, date, multiple, or single. Required.")
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

// ============================================================================
// hub-config: create/list/update/delete per-hub attribute configuration
// ============================================================================

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

		// The definition id travels in the body, not the URL: create POSTs to
		// the COLLECTION path .../contact-attributes. The /{definition_id}
		// path only supports PATCH/DELETE, so a POST there 405s. buildHubConfigAttrs
		// owns the definition_id-in-body pattern (MIO-2502) + the bool flags.
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

// setHubConfigBoolFlags maps the hub-config boolean flags onto their backend
// attribute keys. Only flags the caller changed are sent, matching the
// partial-update / upsert semantics of the backend schema. Used by the hub-config
// UPDATE command (the def id is in the URL there, so no definition_id in the body).
func setHubConfigBoolFlags(cmd *cobra.Command, attrs map[string]any) {
	setMappedBool(cmd, attrs, "in-profile", "is_in_profile")
	setMappedBool(cmd, attrs, "in-onboarding", "is_in_onboarding")
	setMappedBool(cmd, attrs, "required", "is_required")
	setMappedBool(cmd, attrs, "read-only", "is_read_only")
	setMappedBool(cmd, attrs, "searchable", "is_searchable")
}

// HubConfigInput carries the hub-config boolean flags, decoupled from
// *cobra.Command. Each pointer is nil when the flag was unset (partial-update /
// upsert semantics preserved).
type HubConfigInput struct {
	IsInProfile    *bool // → is_in_profile
	IsInOnboarding *bool // → is_in_onboarding
	IsRequired     *bool // → is_required
	IsReadOnly     *bool // → is_read_only
	IsSearchable   *bool // → is_searchable
}

// buildHubConfigAttrs assembles the hub-config CREATE body: the definition id in
// the body (MIO-2502 — create POSTs to the collection path, so the def id cannot
// travel in the URL) plus the bool flags the caller set. It is a pure builder so
// the scaffold (MIO-2543) can enable an attribute on a hub without a
// *cobra.Command. Position is added by the caller (setIntFlag), matching the
// command's previous inline assembly.
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

		// GET .../attributes returns a ContactValueListResponse (a LIST at
		// /data); decode it as a collection, not a single resource.
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

		// Split every pair up front so a malformed --attr exits ExitUsage
		// before any request fires (no-request-on-usage-error contract).
		type attrPair struct{ key, val string }
		pairs := make([]attrPair, 0, len(rawAttrs))
		for _, kv := range rawAttrs {
			key, val, ok := splitKV(kv)
			if !ok {
				return errs.New(errs.ExitUsage, "invalid --attr value %q: expected key=value", kv)
			}
			pairs = append(pairs, attrPair{key, val})
		}

		// Look up each attribute's field type (and id) once (MIO-2553/MIO-4124):
		// a scalar value must travel in the typed field the def declares
		// (value_number/value_boolean/value_date/value_text), and a select-type
		// (single/multiple) def needs its id to fetch its options for
		// option-value resolution. A slug the team has no definition for is a
		// usage error (exit 2, no write) rather than a guess the backend would
		// reject anyway.
		fieldTypes, err := caDefFieldTypesBySlug(c, teamID)
		if err != nil {
			return err
		}

		// caSelectGroup accumulates the raw (unresolved) option-value tokens for
		// ONE select-type (single/multiple) attribute slug, across however many
		// --attr occurrences and comma-separated segments reference it
		// (MIO-4124): repeated --attr flags and comma-separated values both
		// merge into the SAME group, in first-occurrence order, with exact
		// (literal, not resolved-id) duplicate tokens dropped. opIndex is the
		// position reserved in ops for this slug's single merged op, so the
		// final wire order still follows the order slugs first appeared in.
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
				// Scalar attribute: unchanged from pre-MIO-4124 behaviour — one
				// "set" op per --attr pair, in order.
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
				ops = append(ops, nil) // reserved slot, filled once resolved below
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

		// Client-side validation happens entirely here, BEFORE any write
		// request. Single-select cardinality is checked first — and without
		// fetching that definition's options — so a call with an obviously
		// wrong number of values never round-trips to fetch options it won't
		// need: mirrors the backend's own "accepts exactly one option, not
		// multiple" rule.
		for _, slug := range selectOrder {
			g := selectGroups[slug]
			if g.def.FieldType == "single" && len(g.values) > 1 {
				return errs.New(errs.ExitUsage,
					"attribute %q accepts exactly one option, not %d (%s)",
					slug, len(g.values), strings.Join(g.values, ", "))
			}
		}

		// Resolve each select group's option values against its definition's
		// options. selectOrder holds each distinct SLUG once, however many
		// --attr occurrences (or comma-separated tokens) referenced it, and a
		// slug maps to exactly one definition (fieldTypes is keyed by slug, and
		// slugs are unique per team) — so this loop already calls
		// caOptionsByDef at most once per definition, with no extra cache
		// needed.
		for _, slug := range selectOrder {
			g := selectGroups[slug]
			options, err := caOptionsByDef(c, teamID, g.def.ID)
			if err != nil {
				return err
			}

			// Two different raw tokens (e.g. a slug and a label, or a slug and
			// an id) can resolve to the SAME option — "tier=pro --attr
			// tier=Pro --attr tier=opt_pro" all name opt_pro. Raw-token dedup
			// above only catches literal duplicates; dedupe by resolved id too
			// so the wire list never repeats one option, preserving the order
			// each id was first resolved in.
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

			// The resolved ids are always sent as option_ids — never
			// option_slugs, never a value_* key — so what goes on the wire is
			// exactly what was resolved, regardless of whether the raw --attr
			// value named an id, a slug, or a label.
			ops[g.opIndex] = map[string]any{
				"type": "set",
				"attributes": map[string]any{
					"definition_slug": slug,
					"option_ids":      resolvedIDs,
				},
			}
		}

		// The backend binds a BulkValuePatchEnvelope whose `data` is a LIST of
		// value operations (extra="forbid"); a JSON:API object at /data 400s
		// with "Input should be a valid list (/data)". Send the raw {data:[...]}
		// list and decode the ContactValueListResponse it returns.
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

// splitKV splits a "key=value" string. Returns ok=false if no '=' is found.
func splitKV(s string) (key, val string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// caAttrDef carries one contact-attribute definition's id and field type, as
// resolved by slug in caDefFieldTypesBySlug's returned map. `values set` needs
// both: the field type routes a scalar value into its typed field (MIO-2553),
// and the id is needed to fetch a select-type (single/multiple) definition's
// options for option-value resolution (MIO-4124).
type caAttrDef struct {
	ID        string
	FieldType string // the backend AttributeType: text/number/boolean/date/multiple/single
}

// caDefFieldTypesBySlug lists the team's contact-attribute definitions and
// returns a slug→caAttrDef map (id + the backend AttributeType: text/number/
// boolean/date/multiple/single). `values set` uses it to route each --attr
// value into the correct typed field, to resolve a select-type definition's
// options (MIO-4124), and to reject unknown slugs before any write (MIO-2553).
// It follows the backend's pagination cursor to exhaustion (meta.page.next_cursor
// gated by has_more — the same convention as the scaffold's nextPageCursor) so
// every definition resolves even for teams with more than one page of
// attributes; without that, a slug on a later page would look unknown. The
// seen-cursor set + maxPages bound are a stall guard against a buggy server
// returning a stable/looping cursor.
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
		next := nextPageCursor(col)
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

// caAttrOption mirrors the fields of one option resource relevant to
// option-value resolution: id, slug, and label (`values set`, MIO-4124).
type caAttrOption struct {
	ID    string
	Slug  string
	Label string
}

// caOptionsByDef lists ALL options for the given select-type (single/multiple)
// definition with a SINGLE GET. GET .../contact-attributes/{definition_id}/
// options (mio-backend app/contact_attributes/router.py's list_options) takes
// no page params and returns every row in one response (meta={}) — verified
// against mio-backend origin/main 2026-09-22, `git grep next_cursor` under
// app/contact_attributes/ has zero hits. Unlike caDefFieldTypesBySlug's
// definitions list, this endpoint is NOT paginated, so no cursor loop is
// needed here.
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

// resolveCAOptionValue resolves one raw --attr value against a select-type
// definition's option list, in the order the backend contract implies
// (MIO-4124): exact option id, then exact option slug, then a
// case-insensitive exact label match. A label match tying across more than
// one option is reported as ambiguous rather than guessed; no match at all
// names the definition and lists its options as "slug (label)" so the caller
// can correct the value without a second round trip.
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

// formatCAOptions renders a definition's options as "slug (label)" pairs,
// comma-separated, for an unknown-option-value error message.
func formatCAOptions(options []caAttrOption) string {
	parts := make([]string, 0, len(options))
	for _, o := range options {
		parts = append(parts, fmt.Sprintf("%s (%s)", o.Slug, o.Label))
	}
	return strings.Join(parts, ", ")
}

// caDateLayouts are the ISO-8601 shapes a date attribute value may take. A value
// is validated client-side against these before the write so an obviously bad
// date exits ExitUsage instead of round-tripping to a backend error; the original
// string is passed through for the backend to parse canonically.
var caDateLayouts = []string{
	"2006-01-02",           // date only (the common case)
	time.RFC3339,           // 2006-01-02T15:04:05Z07:00
	time.RFC3339Nano,       // with sub-second precision
	"2006-01-02T15:04:05",  // naive datetime (no zone)
	"2006-01-02T15:04:05Z", // explicit UTC
}

// setTypedAttrValue writes val into attrs under the typed value field the
// attribute's field type requires, mirroring the backend SetOperationAttributes
// schema (MIO-2553):
//
//	number  → value_number  (parsed with strconv; finite JSON number)
//	boolean → value_boolean (parsed with strconv; JSON bool)
//	date    → value_date    (validated as ISO-8601, then the string is passed through)
//	text    → value_text
//
// number/boolean/date parse failures exit ExitUsage (no round-trip). Non-finite
// numbers (NaN/±Inf, which strconv.ParseFloat accepts but JSON cannot encode) are
// rejected the same way rather than failing later as a generic marshal error.
// Select-type (multiple/single) attributes are NOT routed through here: `values
// set`'s RunE resolves their option value(s) to option_ids before building the
// op (MIO-4124), so fieldType here is always a scalar backend type. The caller
// has already rejected slugs the team has no definition for.
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
		// "text" and any type the backend adds later that we do not specially
		// route → value_text.
		attrs["value_text"] = val
	}
	return nil
}

// isValidCADate reports whether val parses as one of the accepted ISO-8601 date
// shapes (caDateLayouts).
func isValidCADate(val string) bool {
	for _, layout := range caDateLayouts {
		if _, err := time.Parse(layout, val); err == nil {
			return true
		}
	}
	return false
}
