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
	"strings"

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
	Long: `Create a new product for the active team.

--name and --type are required. Allowed values for --type:
  course, membership, bundle, digital_download, booking`,
	Example: `  mio products create --name "Intro to Go" --type course
  mio products create --name "Pro Membership" --type membership --description "Full access plan"`,
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

		// Both --name and --type are required by the backend
		// ProductCreateAttributes schema; validate client-side so a
		// partial-required body never reaches the API.
		var missing []string
		if !cmd.Flags().Changed("name") {
			missing = append(missing, "--name")
		}
		if !cmd.Flags().Changed("type") {
			missing = append(missing, "--type")
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flag(s): %s", strings.Join(missing, ", "))
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "type")
		setStringFlag(cmd, attrs, "description")
		setBoolFlag(cmd, attrs, "is-active")

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
	Long: `Partially update a product. Only the flags you supply are changed (PATCH semantics).

Allowed values for --type (when provided): course, membership, bundle, digital_download, booking`,
	Example: `  mio products update prod_abc123 --name "Advanced Go"
  mio products update prod_abc123 --type membership --is-active`,
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

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "type")
		setStringFlag(cmd, attrs, "description")
		setBoolFlag(cmd, attrs, "is-active")

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
	// NOTE: --status and --published were removed (MIO-941) — the backend
	// ProductCreateAttributes/ProductUpdateAttributes schemas do not include those
	// fields; sending them caused a 422 "extra inputs not permitted".
	// --type maps to attributes.type (required on create; allowed values:
	// course, membership, bundle, digital_download, booking).
	// --is-active maps to attributes.is_active.
	for _, cmd := range []*cobra.Command{productsCreateCmd, productsUpdateCmd} {
		cmd.Flags().String("name", "", "Product name.")
		cmd.Flags().String("type", "", "Product type: course, membership, bundle, digital_download, or booking. Required on create.")
		cmd.Flags().String("description", "", "Product description.")
		cmd.Flags().Bool("is-active", false, "Whether the product is active.")
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
	Long: `Create a price attached to the given product.

--amount, --currency, and --type are required. For recurring prices, --interval
and --interval-count are also required.

Allowed values for --currency: usd, cad, gbp, eur, aud
Allowed values for --type:     one_time, recurring
Allowed values for --interval: month, year, week, day (required when --type=recurring)`,
	Example: `  mio products prices create prod_abc123 --amount 4999 --currency usd --type one_time
  mio products prices create prod_abc123 --amount 999 --currency usd --type recurring --interval month --interval-count 1`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := pricesContext(cmd)
		if err != nil {
			return err
		}

		// --amount, --currency, and --type are required by PriceCreateAttributes.
		var missing []string
		if !cmd.Flags().Changed("amount") {
			missing = append(missing, "--amount")
		}
		if !cmd.Flags().Changed("currency") {
			missing = append(missing, "--currency")
		}
		if !cmd.Flags().Changed("type") {
			missing = append(missing, "--type")
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flag(s): %s", strings.Join(missing, ", "))
		}

		attrs := map[string]any{}
		setIntFlag(cmd, attrs, "amount")
		setStringFlag(cmd, attrs, "currency")
		setStringFlag(cmd, attrs, "type")
		setStringFlag(cmd, attrs, "interval")
		setIntFlag(cmd, attrs, "interval-count")
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "description")
		setBoolFlag(cmd, attrs, "is-active")

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
	Long: `Partially update a price. Only the flags you supply are changed (PATCH semantics).

Billing fields (amount, currency, type, interval, interval_count) are IMMUTABLE
after creation. To change them, create a new price and deactivate the old one.

Mutable fields: --name, --description, --is-active.`,
	Example: `  mio products prices update prod_abc123 price_xyz --name "Monthly Plan"
  mio products prices update prod_abc123 price_xyz --is-active=false`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := pricesContext(cmd)
		if err != nil {
			return err
		}
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "description")
		setBoolFlag(cmd, attrs, "is-active")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one mutable field flag (--name, --description, --is-active)")
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
	// Create flags: amount, currency, type are required; interval/interval-count
	// are required when type=recurring; name/description/is-active are optional.
	productsPricesCreateCmd.Flags().Int("amount", 0, "Price amount in the currency's minor unit (e.g. cents). Required.")
	productsPricesCreateCmd.Flags().String("currency", "", "ISO currency code: usd, cad, gbp, eur, or aud. Required.")
	productsPricesCreateCmd.Flags().String("type", "", "Price type: one_time or recurring. Required.")
	productsPricesCreateCmd.Flags().String("interval", "", "Billing interval: month, year, week, or day. Required when --type=recurring.")
	productsPricesCreateCmd.Flags().Int("interval-count", 0, "Number of intervals between billings (≥1). Required when --type=recurring.")
	productsPricesCreateCmd.Flags().String("name", "", "Human-readable price label (max 100 chars).")
	productsPricesCreateCmd.Flags().String("description", "", "Price description (max 500 chars).")
	productsPricesCreateCmd.Flags().Bool("is-active", true, "Whether the price is active.")

	// Update flags: only mutable fields (billing fields are immutable after creation).
	productsPricesUpdateCmd.Flags().String("name", "", "Human-readable price label (max 100 chars).")
	productsPricesUpdateCmd.Flags().String("description", "", "Price description (max 500 chars).")
	productsPricesUpdateCmd.Flags().Bool("is-active", false, "Whether the price is active.")

	addPaginationFlags(productsPricesListCmd)
}
