package cmd

// users.go implements the `mio users` command group.
//
// Routes (see docs/internal/api-surface.md "users"):
//
//	me       GET  /api/users/me
//	list     GET  /api/users
//	retrieve GET  /api/users/{id}
//	update   PATCH /api/users/{id}
//
// Users are not team-scoped or hub-scoped — paths are flat under /api/users.
// There is no create or delete in the users surface (accounts are provisioned
// via auth/register and cannot be deleted through this API).

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	usersCmd.AddCommand(
		usersMeCmd,
		usersListCmd,
		usersRetrieveCmd,
		usersUpdateCmd,
	)

	rootCmd.AddCommand(usersCmd)
}

// ---- users group ------------------------------------------------------------

var usersCmd = &cobra.Command{
	Use:   "users",
	Short: "Manage users.",
	Long:  "Retrieve and update users. Use 'mio users me' to inspect the authenticated user.",
}

// usersPath returns /api/users[/{id}].
func usersPath(id string) string {
	const base = "/api/users"
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ---- me ---------------------------------------------------------------------

var usersMeCmd = &cobra.Command{
	Use:   "me",
	Short: "Retrieve the authenticated user.",
	Long: `Retrieve the profile of the user identified by the active API key.

Equivalent to GET /api/users/me.`,
	Example: `  # Show the authenticated user
  mio users me

  # As JSON (pipe-friendly)
  mio users me --output json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, "/api/users/me")
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- list -------------------------------------------------------------------

var usersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List users.",
	Long: `List all users visible to the authenticated caller.

Equivalent to GET /api/users.`,
	Example: `  # List users (table on a TTY)
  mio users list

  # Paginate
  mio users list --limit 20 --after <cursor>`,
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

		col, err := c.client.List(c.ctx, usersPath(""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- retrieve ---------------------------------------------------------------

var usersRetrieveCmd = &cobra.Command{
	Use:   "retrieve <id>",
	Short: "Retrieve a user by id.",
	Long: `Retrieve a single user by their id.

Equivalent to GET /api/users/{id}.`,
	Example: `  mio users retrieve usr_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, usersPath(args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- update -----------------------------------------------------------------

var usersUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a user by id.",
	Long: `Update one or more fields of a user. Only the flags you set are sent
(partial update / PATCH semantics).

Equivalent to PATCH /api/users/{id}.`,
	Example: `  # Update display name and email
  mio users update usr_abc123 --first-name Alice --email alice@example.com

  # Update avatar URL only
  mio users update usr_abc123 --avatar-url https://example.com/avatar.png`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "first-name")
		setStringFlag(cmd, attrs, "last-name")
		setStringFlag(cmd, attrs, "email")
		setStringFlag(cmd, attrs, "avatar-url")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, usersPath(args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	// Pagination flags for list.
	addPaginationFlags(usersListCmd)

	// Attribute flags for update.
	usersUpdateCmd.Flags().String("first-name", "", "User's first name.")
	usersUpdateCmd.Flags().String("last-name", "", "User's last name.")
	usersUpdateCmd.Flags().String("email", "", "User's email address.")
	usersUpdateCmd.Flags().String("avatar-url", "", "URL to the user's avatar image.")
}
