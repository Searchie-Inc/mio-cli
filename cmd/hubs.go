package cmd

// hubs.go — `mio hubs` command group.
//
// Routes (see docs/internal/api-surface.md "hubs"):
//
//	create          POST   /api/teams/{team_id}/hubs
//	list            GET    /api/teams/{team_id}/hubs
//	retrieve        GET    /api/teams/{team_id}/hubs/{id}
//	update          PATCH  /api/teams/{team_id}/hubs/{id}
//	delete          DELETE /api/teams/{team_id}/hubs/{id}
//	policies update PATCH  /api/teams/{team_id}/hubs/{hub_id}/policies
//
// All routes are team-scoped. Hub id comes from a positional argument (not the
// --hub context flag) so operators can manage any hub, not just the active one.

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	hubsCmd.AddCommand(
		hubsCreateCmd,
		hubsListCmd,
		hubsRetrieveCmd,
		hubsUpdateCmd,
		hubsDeleteCmd,
	)

	// hubs policies <action>  (nested sub-resource)
	hubsPoliciesCmd.AddCommand(hubsPoliciesUpdateCmd)
	hubsCmd.AddCommand(hubsPoliciesCmd)

	rootCmd.AddCommand(hubsCmd)
}

// ---- hubs group -------------------------------------------------------------

var hubsCmd = &cobra.Command{
	Use:   "hubs",
	Short: "Manage hubs.",
	Long:  "Create, list, retrieve, update and delete hubs for the active team.",
	Example: `  mio hubs list
  mio hubs create --name "My Community" --slug my-community
  mio hubs retrieve hub_abc123
  mio hubs update hub_abc123 --name "Renamed Community"
  mio hubs delete hub_abc123`,
}

// hubsPath returns /api/teams/{team_id}/hubs[/{id}].
func hubsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// hubsContext is shared boilerplate: build context, require auth, resolve team.
func hubsContext(cmd *cobra.Command) (*cmdContext, string, error) {
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

// ---- create -----------------------------------------------------------------

var hubsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a hub.",
	Long:  "Create a new hub for the active team.",
	Example: `  mio hubs create --name "My Community" --slug my-community
  mio hubs create --name "Support Hub" --slug support --description "Help articles"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setMappedString(cmd, attrs, "name", "title")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setMappedNestedString(cmd, attrs, "logo-url", "branding", "logo_url")
		setMappedBoolInverted(cmd, attrs, "published", "is_private")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.Create(c.ctx, hubsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- list -------------------------------------------------------------------

var hubsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List hubs.",
	Long:    "List all hubs for the active team.",
	Example: `  mio hubs list`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, hubsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- retrieve ---------------------------------------------------------------

var hubsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a hub by id.",
	Long:    "Retrieve a single hub by its id.",
	Example: `  mio hubs retrieve hub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, hubsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- update -----------------------------------------------------------------

var hubsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a hub by id.",
	Long:  "Update one or more fields on a hub. Only the flags you provide are changed (partial update).",
	Example: `  mio hubs update hub_abc123 --name "New Name"
  mio hubs update hub_abc123 --published=true`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		// --logo-url is not supported on update: the backend assigns branding
		// wholesale (setattr, not merge), so patching any branding field would
		// silently clobber all sibling keys (primary_color, background_color, etc.).
		// Fail fast so the caller knows the logo was NOT updated. (MIO-901)
		if cmd.Flags().Changed("logo-url") {
			return errs.New(errs.ExitUsage,
				"--logo-url is not supported on `hubs update` yet: updating it would overwrite other branding fields. Set the logo when creating the hub. (tracked: MIO-901)")
		}

		attrs := map[string]any{}
		setMappedString(cmd, attrs, "name", "title")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setMappedBoolInverted(cmd, attrs, "published", "is_private")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, hubsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- delete -----------------------------------------------------------------

var hubsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a hub by id.",
	Long:  "Permanently delete a hub. This action cannot be undone. Pass --yes to skip the confirmation prompt.",
	Example: `  mio hubs delete hub_abc123
  mio hubs delete hub_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete hub %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, hubsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted hub %s.\n", args[0])
		return nil
	},
}

// ---- flag registration ------------------------------------------------------

func init() {
	// Shared writable attribute flags for create and update.
	for _, cmd := range []*cobra.Command{hubsCreateCmd, hubsUpdateCmd} {
		cmd.Flags().String("name", "", "Hub display name.")
		cmd.Flags().String("slug", "", "Hub URL slug (unique within the team).")
		cmd.Flags().String("description", "", "Short description of the hub.")
		cmd.Flags().String("logo-url", "", "URL of the hub's logo image.")
		cmd.Flags().Bool("published", false, "Whether the hub is publicly published.")
	}

	addPaginationFlags(hubsListCmd)
}

// ---- hubs policies sub-resource ---------------------------------------------

var hubsPoliciesCmd = &cobra.Command{
	Use:   "policies",
	Short: "Manage hub legal policies.",
	Long:  "Create or update legal policies (Terms of Service, Privacy Policy) for a hub.",
}

// hubsPoliciesPath returns /api/teams/{team_id}/hubs/{hub_id}/policies.
func hubsPoliciesPath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/policies", teamID, hubID)
}

// validPolicyTypes is the set of accepted --policy-type values. Validation is
// done client-side so a typo exits with ExitUsage rather than a 422 round-trip.
var validPolicyTypes = map[string]bool{
	"tos":            true,
	"privacy_policy": true,
}

var hubsPoliciesUpdateCmd = &cobra.Command{
	Use:   "update <hub_id>",
	Short: "Update (or create) a hub legal policy.",
	Long: `Create or replace a hub legal policy (Terms of Service or Privacy Policy).

The hub identifier is a positional argument (not the --hub context flag) so you
can target any hub regardless of the active context.

Policy content may be supplied inline or read from a file by prefixing the path
with '@':  --content @policy.md

Exactly one of --content or --reset-content must be provided:
  --content        Supply the policy body (inline string or @file).
  --reset-content  Revert the policy to the backend default (sends content: null).`,
	Example: `  mio hubs policies update hub_abc123 --policy-type tos --content "# Terms of Service\n…"
  mio hubs policies update hub_abc123 --policy-type tos --content @tos.md --require-acceptance
  mio hubs policies update hub_abc123 --policy-type privacy_policy --content @privacy.md
  mio hubs policies update hub_abc123 --policy-type tos --reset-content`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		policyType, ferr := cmd.Flags().GetString("policy-type")
		if ferr != nil {
			return errs.New(errs.ExitUsage, "--policy-type: %s", ferr.Error())
		}
		if !validPolicyTypes[policyType] {
			return errs.New(errs.ExitUsage,
				"--policy-type %q is not valid: must be one of tos, privacy_policy", policyType)
		}

		contentChanged := cmd.Flags().Changed("content")
		resetChanged := cmd.Flags().Changed("reset-content")

		// Exactly one of --content / --reset-content must be provided.
		if contentChanged && resetChanged {
			return errs.New(errs.ExitUsage, "--content and --reset-content are mutually exclusive: provide exactly one")
		}
		if !contentChanged && !resetChanged {
			return errs.New(errs.ExitUsage, "provide exactly one of --content or --reset-content")
		}

		attrs := map[string]any{
			"policy_type": policyType,
		}

		if resetChanged {
			// Explicitly send content: null to revert to the backend default.
			// A Go map value of nil marshals to JSON null.
			attrs["content"] = nil
		} else {
			rawContent, ferr := cmd.Flags().GetString("content")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--content: %s", ferr.Error())
			}

			// Support the @path convention: a value starting with '@' is treated as a
			// file path whose contents become the policy text.
			content := rawContent
			if after, ok := strings.CutPrefix(rawContent, "@"); ok {
				b, rerr := os.ReadFile(after)
				if rerr != nil {
					return errs.New(errs.ExitGeneric, "read %s: %s", after, rerr.Error())
				}
				content = string(b)
			}
			attrs["content"] = content
		}

		// --require-acceptance is only included when the flag was explicitly set.
		// Sending it with policy_type=privacy_policy is a backend 422; we let the
		// backend enforce that constraint rather than pre-blocking here.
		setBoolFlag(cmd, attrs, "require-acceptance")

		hubID := args[0]
		res, err := c.client.Update(c.ctx, hubsPoliciesPath(teamID, hubID), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	hubsPoliciesUpdateCmd.Flags().String("policy-type", "", "Policy type: tos or privacy_policy. Required.")
	_ = hubsPoliciesUpdateCmd.MarkFlagRequired("policy-type")

	hubsPoliciesUpdateCmd.Flags().String("content", "", "Policy body in Markdown. Prefix with '@' to read from a file (e.g. @tos.md). Mutually exclusive with --reset-content.")
	hubsPoliciesUpdateCmd.Flags().Bool("reset-content", false, "Revert the policy to the backend default (sends content: null). Mutually exclusive with --content.")

	hubsPoliciesUpdateCmd.Flags().Bool("require-acceptance", false, "Require hub members to accept the policy before accessing content (TOS only).")
}
