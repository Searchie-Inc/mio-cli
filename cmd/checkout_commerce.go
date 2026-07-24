package cmd

// checkout_commerce.go — hub-scoped commerce DISPLAY rows (MIO-2268).
//
// A hub advertises products through two display resources managed here:
//
//	hub-products  hub_product_display rows — what a hub offers.
//	              list   GET    /api/teams/{team}/hubs/{hub}/products
//	              attach POST   /api/teams/{team}/hubs/{hub}/products
//	              update PATCH  /api/teams/{team}/hubs/{hub}/products/{display_id}
//	              detach DELETE /api/teams/{team}/hubs/{hub}/products/{display_id}
//
//	hub-prices    hub_price_display rows — auto-created when a product is
//	              attached; only visibility/position are editable.
//	              list   GET    /api/teams/{team}/hubs/{hub}/prices
//	              update PATCH  /api/teams/{team}/hubs/{hub}/prices/{display_id}
//
// The write bodies derive their JSON:API type from the request path:
//	hubs/products → hub_product_displays, hubs/prices → hub_price_displays
// (both typeOverrides already exist in internal/client/client.go).
//
// All routes require a team-owner JWT and a hub context (--hub). They register
// on the shared checkoutCmd defined in checkout.go, reusing checkoutHubContext.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// checkout hub-products <action>
	checkoutHubProductsCmd.AddCommand(
		checkoutHubProductsListCmd,
		checkoutHubProductsAttachCmd,
		checkoutHubProductsUpdateCmd,
		checkoutHubProductsDetachCmd,
	)
	checkoutCmd.AddCommand(checkoutHubProductsCmd)

	// checkout hub-prices <action>
	checkoutHubPricesCmd.AddCommand(
		checkoutHubPricesListCmd,
		checkoutHubPricesUpdateCmd,
	)
	checkoutCmd.AddCommand(checkoutHubPricesCmd)

	// pagination
	addPaginationFlags(checkoutHubProductsListCmd)
	addPaginationFlags(checkoutHubPricesListCmd)

	// hub-products attach flags (product_id is a positional arg).
	checkoutHubProductsAttachCmd.Flags().Int("position", 0, "Display position within the hub (>= 0).")
	checkoutHubProductsAttachCmd.Flags().Bool("visible", true, "Whether the product is visible on the hub's checkout surface.")
	checkoutHubProductsAttachCmd.Flags().Bool("free-tier", false, "Mark this product as the hub's free tier.")

	// hub-products update flags (all optional; at least one required).
	checkoutHubProductsUpdateCmd.Flags().Int("position", 0, "New display position within the hub (>= 0).")
	checkoutHubProductsUpdateCmd.Flags().Bool("visible", true, "Whether the product is visible on the hub's checkout surface.")
	checkoutHubProductsUpdateCmd.Flags().Bool("free-tier", false, "Mark this product as the hub's free tier.")

	// hub-prices update flags (all optional; at least one required).
	checkoutHubPricesUpdateCmd.Flags().Int("position", 0, "New display position for the price (>= 0).")
	checkoutHubPricesUpdateCmd.Flags().Bool("visible", true, "Whether the price is visible on the hub's checkout surface.")
}

// ---- path helpers -------------------------------------------------------------

// hubProductsPath returns /api/teams/{team}/hubs/{hub}/products[/{display_id}].
func hubProductsPath(teamID, hubID, displayID string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/products", teamID, hubID)
	if displayID != "" {
		return base + "/" + displayID
	}
	return base
}

// hubPricesPath returns /api/teams/{team}/hubs/{hub}/prices[/{display_id}].
func hubPricesPath(teamID, hubID, displayID string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/prices", teamID, hubID)
	if displayID != "" {
		return base + "/" + displayID
	}
	return base
}

// ---- hub-products group -------------------------------------------------------

var checkoutHubProductsCmd = &cobra.Command{
	Use:   "hub-products",
	Short: "Manage the products a hub offers (hub_product_display rows).",
	Long: `List, attach, update, and detach the product DISPLAY rows for a hub.

A hub_product_display row makes a product purchasable on a hub's checkout
surface. Attaching a product also seeds a hub_price_display row for every active
price on the product (manage those with 'mio checkout hub-prices').

All commands require a hub context: pass --hub <id> or run 'mio config set current_hub <id>'.`,
}

var checkoutHubProductsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List the products offered by a hub.",
	Long:    "List all hub_product_display rows for the active hub, ordered by position. Requires --hub.",
	Example: `  mio checkout hub-products list --hub hub_abc123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, hubProductsPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var checkoutHubProductsAttachCmd = &cobra.Command{
	Use:   "attach <product_id>",
	Short: "Attach a product to a hub.",
	Long: `Attach a product to the active hub, creating a hub_product_display row plus a
default hub_price_display row for every active price on the product.

Requires --hub. The positional argument is the PRODUCT id (not a display id).`,
	Example: `  mio checkout hub-products attach prod_abc123 --hub hub_abc123
  mio checkout hub-products attach prod_abc123 --hub hub_abc123 --position 1 --free-tier`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate before resolving context so a usage error fires no request.
		productID := stringArg(args[0])
		if productID == "" {
			return errs.New(errs.ExitUsage, "product id must not be empty")
		}
		if err := validateNonNegativePosition(cmd); err != nil {
			return err
		}

		attrs := map[string]any{"product_id": productID}
		setIntFlag(cmd, attrs, "position")
		setBoolFlag(cmd, attrs, "visible")
		setMappedBool(cmd, attrs, "free-tier", "is_free_tier")

		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, hubProductsPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutHubProductsUpdateCmd = &cobra.Command{
	Use:   "update <display_id>",
	Short: "Update a hub product display row.",
	Long: `Toggle visibility, reposition, or set the free-tier flag on a hub_product_display
row. Only the flags you supply are changed (PATCH semantics).

Requires --hub. The positional argument is the DISPLAY id (from
'mio checkout hub-products list'), not the product id.`,
	Example: `  mio checkout hub-products update hpd_abc123 --hub hub_abc123 --visible=false
  mio checkout hub-products update hpd_abc123 --hub hub_abc123 --position 3`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		displayID := stringArg(args[0])
		if displayID == "" {
			return errs.New(errs.ExitUsage, "display id must not be empty")
		}
		if err := validateNonNegativePosition(cmd); err != nil {
			return err
		}

		attrs := map[string]any{}
		setIntFlag(cmd, attrs, "position")
		setBoolFlag(cmd, attrs, "visible")
		setMappedBool(cmd, attrs, "free-tier", "is_free_tier")
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one of --position, --visible, --free-tier")
		}

		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Update(c.ctx, hubProductsPath(teamID, hubID, displayID), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutHubProductsDetachCmd = &cobra.Command{
	Use:   "detach <display_id>",
	Short: "Detach a product from a hub.",
	Long: `Detach a product from the active hub, hard-deleting its hub_product_display and
the associated hub_price_display rows. The product itself is not deleted.

Requires --hub. The positional argument is the DISPLAY id (from
'mio checkout hub-products list'). Pass --yes to skip the confirmation prompt in
non-interactive environments.`,
	Example: `  mio checkout hub-products detach hpd_abc123 --hub hub_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		displayID := stringArg(args[0])
		if displayID == "" {
			return errs.New(errs.ExitUsage, "display id must not be empty")
		}

		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Detach product display %s from the hub?", displayID)); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, hubProductsPath(teamID, hubID, displayID)); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Detached product display %s from the hub.\n", displayID)
		return nil
	},
}

// ---- hub-prices group ---------------------------------------------------------

var checkoutHubPricesCmd = &cobra.Command{
	Use:   "hub-prices",
	Short: "Manage a hub's price display rows (hub_price_display rows).",
	Long: `List and update the price DISPLAY rows for a hub.

hub_price_display rows are created automatically when a product is attached to a
hub ('mio checkout hub-products attach'); they cannot be created or deleted
directly. Only visibility and position are editable.

All commands require a hub context: pass --hub <id> or run 'mio config set current_hub <id>'.`,
}

var checkoutHubPricesListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List a hub's price display rows.",
	Long:    "List all hub_price_display rows for the active hub, ordered by position. Requires --hub.",
	Example: `  mio checkout hub-prices list --hub hub_abc123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, hubPricesPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var checkoutHubPricesUpdateCmd = &cobra.Command{
	Use:   "update <display_id>",
	Short: "Update a hub price display row.",
	Long: `Toggle visibility or reposition a single hub_price_display row. Only the flags
you supply are changed (PATCH semantics).

Requires --hub. The positional argument is the price DISPLAY id (from
'mio checkout hub-prices list').`,
	Example: `  mio checkout hub-prices update hprd_abc123 --hub hub_abc123 --visible=false
  mio checkout hub-prices update hprd_abc123 --hub hub_abc123 --position 2`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		displayID := stringArg(args[0])
		if displayID == "" {
			return errs.New(errs.ExitUsage, "display id must not be empty")
		}
		if err := validateNonNegativePosition(cmd); err != nil {
			return err
		}

		attrs := map[string]any{}
		setIntFlag(cmd, attrs, "position")
		setBoolFlag(cmd, attrs, "visible")
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one of --position, --visible")
		}

		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Update(c.ctx, hubPricesPath(teamID, hubID, displayID), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- shared helpers -----------------------------------------------------------

// validateNonNegativePosition rejects a negative --position before any request
// (backend constraint: position >= 0). No-op when --position is unset.
func validateNonNegativePosition(cmd *cobra.Command) error {
	if !cmd.Flags().Changed("position") {
		return nil
	}
	pos, err := cmd.Flags().GetInt("position")
	if err != nil {
		return errs.New(errs.ExitUsage, "--position: %s", err)
	}
	if pos < 0 {
		return errs.New(errs.ExitUsage, "invalid --position %d: must be >= 0", pos)
	}
	return nil
}
