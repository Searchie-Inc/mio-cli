package cmd

// products.go is THE REFERENCE RESOURCE for the mio CLI. Every other resource
// command file is a copy of this structure adapted to its own routes.
//
// Conventions a copying agent MUST follow:
//
//   - Self-registration only: this file attaches its whole command tree in
//     init() via rootCmd.AddCommand. It never edits a shared registry file, so
//     resources can be added in parallel without merge conflicts.
//   - One cobra.Command per action (create/list/retrieve/update/delete) plus a
//     sub-resource group (prices) showing the nested pattern.
//   - Paths are built from the resolved team id (and hub id where applicable),
//     never hardcoded.
//   - create/update read --flags into an attributes map; the client wraps them
//     in the JSON:API envelope. Only flags the user actually set are sent, so a
//     PATCH stays a partial update.
//   - delete is destructive → it goes through confirmDestructive (honours --yes,
//     exits 5 off a TTY without --yes).
//   - Every command resolves context via newContext and requires auth.
//
// Routes (see docs/internal/api-surface.md "products"):
//
//	products: CRUD /api/teams/{team_id}/products[/{id}]
//	prices:   CRUD /api/teams/{team_id}/products/{id}/prices[/{pid}]

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// products <action>
	productsCmd.AddCommand(
		productsCreateCmd,
		productsListCmd,
		productsRetrieveCmd,
		productsUpdateCmd,
		productsDeleteCmd,
	)

	// products prices <action>  (nested sub-resource)
	productsPricesCmd.AddCommand(
		productsPricesCreateCmd,
		productsPricesListCmd,
		productsPricesRetrieveCmd,
		productsPricesUpdateCmd,
		productsPricesDeleteCmd,
	)
	productsCmd.AddCommand(productsPricesCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(productsCmd)
}

// ---- products group ---------------------------------------------------------

var productsCmd = &cobra.Command{
	Use:   "products",
	Short: "Manage products.",
	Long:  "Create, list, retrieve, update and delete products for the active team.",
}

// productsPath returns /api/teams/{team_id}/products[/{id}].
func productsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/products", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

var productsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a product.",
	Args:  cobra.NoArgs,
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
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "status")
		setBoolFlag(cmd, attrs, "published")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.Create(c.ctx, productsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var productsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List products.",
	Args:  cobra.NoArgs,
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

		col, err := c.client.List(c.ctx, productsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var productsRetrieveCmd = &cobra.Command{
	Use:   "retrieve <id>",
	Short: "Retrieve a product by id.",
	Args:  cobra.ExactArgs(1),
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

		res, err := c.client.Retrieve(c.ctx, productsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var productsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a product by id.",
	Args:  cobra.ExactArgs(1),
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
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "status")
		setBoolFlag(cmd, attrs, "published")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, productsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var productsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a product by id.",
	Args:  cobra.ExactArgs(1),
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

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete product %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, productsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted product %s.\n", args[0])
		return nil
	},
}

func init() {
	// Attribute flags for create/update.
	for _, cmd := range []*cobra.Command{productsCreateCmd, productsUpdateCmd} {
		cmd.Flags().String("name", "", "Product name.")
		cmd.Flags().String("description", "", "Product description.")
		cmd.Flags().String("status", "", "Product status.")
		cmd.Flags().Bool("published", false, "Whether the product is published.")
	}
	addPaginationFlags(productsListCmd)
}

// ---- products prices sub-resource ------------------------------------------

var productsPricesCmd = &cobra.Command{
	Use:   "prices",
	Short: "Manage a product's prices.",
	Long:  "Create, list, retrieve, update and delete prices nested under a product.",
}

// pricesPath returns /api/teams/{team_id}/products/{product_id}/prices[/{price_id}].
func pricesPath(teamID, productID, priceID string) string {
	base := fmt.Sprintf("/api/teams/%s/products/%s/prices", teamID, productID)
	if priceID != "" {
		return base + "/" + priceID
	}
	return base
}

var productsPricesCreateCmd = &cobra.Command{
	Use:   "create <product_id>",
	Short: "Create a price on a product.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := pricesContext(cmd)
		if err != nil {
			return err
		}
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "currency")
		setIntFlag(cmd, attrs, "amount")
		setStringFlag(cmd, attrs, "interval")
		setStringFlag(cmd, attrs, "nickname")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --amount and --currency")
		}
		res, err := c.client.Create(c.ctx, pricesPath(teamID, args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var productsPricesListCmd = &cobra.Command{
	Use:   "list <product_id>",
	Short: "List a product's prices.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := pricesContext(cmd)
		if err != nil {
			return err
		}
		query := url.Values{}
		addPageFlags(cmd, query)
		col, err := c.client.List(c.ctx, pricesPath(teamID, args[0], ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var productsPricesRetrieveCmd = &cobra.Command{
	Use:   "retrieve <product_id> <price_id>",
	Short: "Retrieve a price by id.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := pricesContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, pricesPath(teamID, args[0], args[1]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var productsPricesUpdateCmd = &cobra.Command{
	Use:   "update <product_id> <price_id>",
	Short: "Update a price by id.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := pricesContext(cmd)
		if err != nil {
			return err
		}
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "currency")
		setIntFlag(cmd, attrs, "amount")
		setStringFlag(cmd, attrs, "interval")
		setStringFlag(cmd, attrs, "nickname")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}
		res, err := c.client.Update(c.ctx, pricesPath(teamID, args[0], args[1]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var productsPricesDeleteCmd = &cobra.Command{
	Use:   "delete <product_id> <price_id>",
	Short: "Delete a price by id.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := pricesContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Delete price %s?", args[1])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, pricesPath(teamID, args[0], args[1])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted price %s.\n", args[1])
		return nil
	},
}

// pricesContext is the shared boilerplate for the price subcommands: build the
// context, require auth, and resolve the team id.
func pricesContext(cmd *cobra.Command) (*cmdContext, string, error) {
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

func init() {
	for _, cmd := range []*cobra.Command{productsPricesCreateCmd, productsPricesUpdateCmd} {
		cmd.Flags().String("currency", "", "ISO currency code, e.g. usd.")
		cmd.Flags().Int("amount", 0, "Price amount in the currency's minor unit (e.g. cents).")
		cmd.Flags().String("interval", "", "Billing interval (e.g. month, year) for recurring prices.")
		cmd.Flags().String("nickname", "", "Human-readable price nickname.")
	}
	addPaginationFlags(productsPricesListCmd)
}
