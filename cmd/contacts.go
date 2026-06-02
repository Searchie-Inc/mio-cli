package cmd

// contacts.go — the `mio contacts` command tree.
//
// Routes (see docs/internal/api-surface.md "contacts"):
//
//	contacts: CRUD /api/teams/{team_id}/contacts[/{id}]
//	restore:  POST /api/teams/{team_id}/contacts/{id}/restore

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// contacts <action>
	contactsCmd.AddCommand(
		contactsListCmd,
		contactsCreateCmd,
		contactsRetrieveCmd,
		contactsUpdateCmd,
		contactsDeleteCmd,
		contactsRestoreCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(contactsCmd)
}

// ---- contacts group ----------------------------------------------------------

var contactsCmd = &cobra.Command{
	Use:   "contacts",
	Short: "Manage contacts.",
	Long:  "Create, list, retrieve, update, delete and restore contacts for the active team.",
}

// contactsPath returns /api/teams/{team_id}/contacts[/{id}].
func contactsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/contacts", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ---- contacts list -----------------------------------------------------------

var contactsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List contacts.",
	Long:  "List all contacts for the active team. Supports pagination and optional filters.",
	Example: `  # List contacts with table output
  mio contacts list

  # Page through results
  mio contacts list --limit 50 --after <cursor>

  # Filter by email
  mio contacts list --filter-email user@example.com`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		if cmd.Flags().Changed("filter-email") {
			if v, ferr := cmd.Flags().GetString("filter-email"); ferr == nil && v != "" {
				query.Set("filter[email]", v)
			}
		}
		if cmd.Flags().Changed("filter-status") {
			if v, ferr := cmd.Flags().GetString("filter-status"); ferr == nil && v != "" {
				query.Set("filter[status]", v)
			}
		}

		col, err := c.client.List(c.ctx, contactsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- contacts create ---------------------------------------------------------

var contactsCreateCmd = &cobra.Command{
	Use:     "create",
	Short:   "Create a contact.",
	Long:    "Create a new contact in the active team.",
	Example: `  mio contacts create --email user@example.com --first-name Alice --last-name Smith`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "email")
		setStringFlag(cmd, attrs, "first_name")
		setStringFlag(cmd, attrs, "last_name")
		setStringFlag(cmd, attrs, "phone")
		setStringFlag(cmd, attrs, "status")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --email")
		}

		res, err := c.client.Create(c.ctx, contactsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- contacts retrieve -------------------------------------------------------

var contactsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a contact by id.",
	Long:    "Retrieve a single contact record by its id.",
	Example: `  mio contacts retrieve ctt_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, contactsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- contacts update ---------------------------------------------------------

var contactsUpdateCmd = &cobra.Command{
	Use:     "update <id>",
	Short:   "Update a contact by id.",
	Long:    "Partially update a contact. Only the flags you set are sent (PATCH semantics).",
	Example: `  mio contacts update ctt_abc123 --first-name Bob --status active`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "email")
		setStringFlag(cmd, attrs, "first_name")
		setStringFlag(cmd, attrs, "last_name")
		setStringFlag(cmd, attrs, "phone")
		setStringFlag(cmd, attrs, "status")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, contactsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- contacts delete ---------------------------------------------------------

var contactsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a contact by id.",
	Long: `Soft-delete a contact. The contact can be restored afterwards with
'mio contacts restore'. Pass --yes to skip the confirmation prompt in
non-interactive shells.`,
	Example: `  mio contacts delete ctt_abc123
  mio contacts delete ctt_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete contact %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, contactsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted contact %s.\n", args[0])
		return nil
	},
}

// ---- contacts restore --------------------------------------------------------

var contactsRestoreCmd = &cobra.Command{
	Use:     "restore <id>",
	Short:   "Restore a deleted contact.",
	Long:    "Restore a previously deleted contact, making it active again.",
	Example: `  mio contacts restore ctt_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Restore contact %s?", args[0])); err != nil {
			return err
		}

		path := fmt.Sprintf("%s/restore", contactsPath(teamID, args[0]))
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Restored contact %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	// Attribute flags for create/update.
	for _, cmd := range []*cobra.Command{contactsCreateCmd, contactsUpdateCmd} {
		cmd.Flags().String("email", "", "Contact email address.")
		cmd.Flags().String("first_name", "", "Contact first name.")
		cmd.Flags().String("last_name", "", "Contact last name.")
		cmd.Flags().String("phone", "", "Contact phone number.")
		cmd.Flags().String("status", "", "Contact status (e.g. active, unsubscribed).")
	}

	// Pagination + filter flags for list.
	addPaginationFlags(contactsListCmd)
	contactsListCmd.Flags().String("filter-email", "", "Filter contacts by exact email address.")
	contactsListCmd.Flags().String("filter-status", "", "Filter contacts by status.")
}
