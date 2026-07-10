package cmd

// coupons_products.go — the `mio coupons products` sub-group (MIO-2268).
//
// Scopes a coupon to specific products (an M:N coupon_products join). An empty
// scope means the coupon applies to EVERY product in the team; attaching one or
// more products restricts it to those.
//
// Routes (app/products/router.py, prefix /api/teams/{team_id}):
//
//	list   GET    /coupons/{coupon_id}/products
//	attach POST   /coupons/{coupon_id}/products
//	detach DELETE /coupons/{coupon_id}/products/{product_id}
//
// The attach body derives JSON:API type "coupon_products" from the
// coupons/products path tail (typeOverride already in internal/client) and
// carries a single attribute: product_id.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	couponsProductsCmd.AddCommand(
		couponsProductsListCmd,
		couponsProductsAttachCmd,
		couponsProductsDetachCmd,
	)
	couponsCmd.AddCommand(couponsProductsCmd)

	addPaginationFlags(couponsProductsListCmd)
}

// couponProductsPath returns /api/teams/{team}/coupons/{coupon}/products[/{product_id}].
func couponProductsPath(teamID, couponID, productID string) string {
	base := fmt.Sprintf("/api/teams/%s/coupons/%s/products", teamID, couponID)
	if productID != "" {
		return base + "/" + productID
	}
	return base
}

var couponsProductsCmd = &cobra.Command{
	Use:   "products",
	Short: "Manage a coupon's product scope.",
	Long: `List, attach, and detach the products a coupon is scoped to.

A coupon with NO products attached applies to every product in the team.
Attaching one or more products restricts the coupon to just those products.`,
}

var couponsProductsListCmd = &cobra.Command{
	Use:     "list <coupon_id>",
	Short:   "List the products a coupon is scoped to.",
	Long:    "List the product scope of a coupon. An empty list means the coupon applies to every product in the team.",
	Example: `  mio coupons products list cpn_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		couponID := stringArg(args[0])
		if couponID == "" {
			return errs.New(errs.ExitUsage, "coupon id must not be empty")
		}

		c, teamID, err := couponsProductsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, couponProductsPath(teamID, couponID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var couponsProductsAttachCmd = &cobra.Command{
	Use:   "attach <coupon_id> <product_id>",
	Short: "Attach a product to a coupon's scope.",
	Long: `Add a product to a coupon's scope, restricting the coupon to that product (and
any others already attached).`,
	Example: `  mio coupons products attach cpn_abc123 prod_xyz789`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		couponID := stringArg(args[0])
		productID := stringArg(args[1])
		if couponID == "" || productID == "" {
			return errs.New(errs.ExitUsage, "coupon id and product id must not be empty")
		}

		c, teamID, err := couponsProductsContext(cmd)
		if err != nil {
			return err
		}
		// POST .../coupons/{coupon}/products with
		// {data:{type:coupon_products,attributes:{product_id}}}.
		res, err := c.client.Create(c.ctx, couponProductsPath(teamID, couponID, ""), map[string]any{"product_id": productID})
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var couponsProductsDetachCmd = &cobra.Command{
	Use:   "detach <coupon_id> <product_id>",
	Short: "Detach a product from a coupon's scope.",
	Long: `Remove a product from a coupon's scope.

Detaching the last product widens the coupon back to every product in the team.
Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio coupons products detach cpn_abc123 prod_xyz789 --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		couponID := stringArg(args[0])
		productID := stringArg(args[1])
		if couponID == "" || productID == "" {
			return errs.New(errs.ExitUsage, "coupon id and product id must not be empty")
		}

		c, teamID, err := couponsProductsContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Detach product %s from coupon %s?", productID, couponID)); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, couponProductsPath(teamID, couponID, productID)); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Detached product %s from coupon %s.\n", productID, couponID)
		return nil
	},
}

// couponsProductsContext resolves context + team id for coupon-product commands.
func couponsProductsContext(cmd *cobra.Command) (*cmdContext, string, error) {
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
