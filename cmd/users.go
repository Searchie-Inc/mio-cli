package cmd

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
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

var usersCmd = &cobra.Command{
	Use:   "users",
	Short: "Manage users.",
	Long:  "Retrieve and update users. Use 'mio users me' to inspect the authenticated user.",
}

func usersPath(id string) string {
	const base = "/api/users"
	if id != "" {
		return base + "/" + id
	}
	return base
}

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

var usersUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a user by id.",
	Long: `Update one or more fields of a user. Only the flags you set are sent
(partial update / PATCH semantics).

Equivalent to PATCH /api/users/{id}.`,
	Example: `  # Update display name
  mio users update usr_abc123 --first-name Alice --last-name Smith`,
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

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.UpdateWith(c.ctx, client.StyleFlat, usersPath(args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	addPaginationFlags(usersListCmd)

	usersUpdateCmd.Flags().String("first-name", "", "User's first name.")
	usersUpdateCmd.Flags().String("last-name", "", "User's last name.")
}
