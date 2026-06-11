package cmd

// roles.go implements the `mio roles` command group.
//
// Routes (see docs/internal/api-surface.md "roles"):
//
//	roles:       CRUD /api/roles[/{id}]
//	permissions: list GET /api/permissions

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// roles <action>
	rolesCmd.AddCommand(
		rolesCreateCmd,
		rolesListCmd,
		rolesRetrieveCmd,
		rolesUpdateCmd,
		rolesDeleteCmd,
	)

	// roles permissions <action>
	rolesPermissionsCmd.AddCommand(rolesPermissionsListCmd)
	rolesCmd.AddCommand(rolesPermissionsCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(rolesCmd)
}

// ---- roles group ------------------------------------------------------------

var rolesCmd = &cobra.Command{
	Use:   "roles",
	Short: "Manage roles.",
	Long:  "Create, list, retrieve, update and delete roles.",
}

// rolesPath returns /api/roles[/{id}].
func rolesPath(id string) string {
	if id != "" {
		return "/api/roles/" + id
	}
	return "/api/roles"
}

var rolesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a role.",
	Long:  "Create a new role with the given name and slug.",
	Example: `  # Create a basic role
  mio roles create --name "Content Editor" --slug content-editor

  # Create a team-scoped role
  mio roles create --name "Moderator" --slug moderator`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		var missing []string
		if !cmd.Flags().Changed("name") {
			missing = append(missing, "--name")
		}
		if !cmd.Flags().Changed("slug") {
			missing = append(missing, "--slug")
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flags: %s", strings.Join(missing, ", "))
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "slug")

		// Flat body: the backend RoleCreate schema is a plain pydantic model,
		// not a JSON:API envelope.
		res, err := c.client.CreateWith(c.ctx, client.StyleFlat, rolesPath(""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var rolesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List roles.",
	Long:  "List all roles available in the system.",
	Example: `  mio roles list
  mio roles list --limit 20`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, rolesPath(""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var rolesRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a role by id.",
	Long:    "Fetch a single role by its identifier.",
	Example: `  mio roles retrieve role_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, rolesPath(args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var rolesUpdateCmd = &cobra.Command{
	Use:     "update <id>",
	Short:   "Update a role by id.",
	Long:    "Partially update a role. Only flags you provide are changed (PATCH semantics).",
	Example: `  mio roles update role_abc123 --name "Senior Editor"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		// Flat body: the backend RoleUpdate schema is a plain pydantic model,
		// not a JSON:API envelope.
		res, err := c.client.UpdateWith(c.ctx, client.StyleFlat, rolesPath(args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var rolesDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a role by id.",
	Long:  "Permanently delete a role. This action is irreversible.",
	Example: `  mio roles delete role_abc123
  mio roles delete role_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete role %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, rolesPath(args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted role %s.\n", args[0])
		return nil
	},
}

func init() {
	// create: name + slug are both required; slug is immutable after creation.
	rolesCreateCmd.Flags().String("name", "", "Role name. Required.")
	rolesCreateCmd.Flags().String("slug", "", "Role slug (unique identifier). Required.")

	// update: only name is mutable per the backend RoleUpdate schema.
	rolesUpdateCmd.Flags().String("name", "", "Role name.")

	addPaginationFlags(rolesListCmd)
}

// ---- roles permissions sub-resource ----------------------------------------

var rolesPermissionsCmd = &cobra.Command{
	Use:   "permissions",
	Short: "Browse available permissions.",
	Long:  "List all permissions that can be assigned to roles.",
}

var rolesPermissionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all permissions.",
	Long:  "Fetch the full catalog of permissions available in the system.",
	Example: `  mio roles permissions list
  mio roles permissions list --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, "/api/permissions", query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

func init() {
	addPaginationFlags(rolesPermissionsListCmd)
}
