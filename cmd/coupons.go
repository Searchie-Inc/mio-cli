package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	couponsCmd.AddCommand(
		couponsCreateCmd,
		couponsListCmd,
		couponsRetrieveCmd,
		couponsUpdateCmd,
		couponsDeleteCmd,
	)

	rootCmd.AddCommand(couponsCmd)
}

var couponsCmd = &cobra.Command{
	Use:   "coupons",
	Short: "Manage discount coupons.",
	Long:  "Create, list, retrieve, update and delete discount coupons for the active team (admin only).",
}

func couponsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/coupons", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

var couponsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a coupon.",
	Long:  "Create a new discount coupon for the active team. Code is automatically normalized to uppercase.",
	Example: `  # Create a 20% off coupon
  mio coupons create --code SAVE20 --discount-type percent --discount-value 20

  # Create a $10 off coupon (currency required for amount type)
  mio coupons create --code TEN --discount-type amount --discount-value 10 --currency usd

  # Create a coupon with a redemption limit and expiry
  mio coupons create --code LAUNCH --discount-type percent --discount-value 15 \
    --max-redemptions 100 --expires-at "2026-12-31T23:59:59Z"`,
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

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "code")
		setMappedString(cmd, attrs, "discount-type", "discount_type")
		setIntFlag(cmd, attrs, "discount-value")
		setStringFlag(cmd, attrs, "currency")
		setIntFlag(cmd, attrs, "max-redemptions")
		setBoolFlag(cmd, attrs, "first-time-only")
		setStringFlag(cmd, attrs, "expires-at")
		setBoolFlag(cmd, attrs, "is-active")

		if dt, ok := attrs["discount_type"].(string); ok {
			if dt != "percent" && dt != "amount" {
				return errs.New(errs.ExitUsage, "--discount-type must be \"percent\" or \"amount\", got %q", dt)
			}
		}

		if raw, ok := attrs["currency"].(string); ok && raw != "" {
			normalized := strings.ToLower(raw)
			validCurrencies := map[string]bool{"usd": true, "cad": true, "gbp": true, "eur": true, "aud": true}
			if !validCurrencies[normalized] {
				return errs.New(errs.ExitUsage, "--currency must be one of usd, cad, gbp, eur, aud, got %q", raw)
			}
			attrs["currency"] = normalized
		}

		res, err := c.client.Create(c.ctx, couponsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var couponsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List coupons.",
	Long:  "List all discount coupons for the active team.",
	Example: `  mio coupons list
  mio coupons list --limit 50`,
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

		col, err := c.client.List(c.ctx, couponsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var couponsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a coupon by id.",
	Long:    "Retrieve a single discount coupon by its id.",
	Example: `  mio coupons retrieve cpn_abc123`,
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

		res, err := c.client.Retrieve(c.ctx, couponsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var couponsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a coupon by id.",
	Long: `Update one or more mutable fields on a coupon. Only the flags you supply are changed.
Note: code, discount_type, discount_value, and currency are immutable once a coupon is created.`,
	Example: `  # Deactivate a coupon
  mio coupons update cpn_abc123 --is-active=false

  # Set a redemption cap
  mio coupons update cpn_abc123 --max-redemptions 500`,
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
		setIntFlag(cmd, attrs, "max-redemptions")
		setBoolFlag(cmd, attrs, "first-time-only")
		setStringFlag(cmd, attrs, "expires-at")
		setBoolFlag(cmd, attrs, "is-active")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, couponsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var couponsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a coupon by id.",
	Long:  "Soft-delete a discount coupon. Pass --yes to skip the confirmation prompt in non-interactive environments.",
	Example: `  mio coupons delete cpn_abc123
  mio coupons delete cpn_abc123 --yes`,
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

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete coupon %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, couponsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted coupon %s.\n", args[0])
		return nil
	},
}

func init() {
	couponsCreateCmd.Flags().String("code", "", "Coupon code (alphanumeric + hyphens, normalized to uppercase). Required.")
	couponsCreateCmd.Flags().String("discount-type", "", "Discount type: percent or amount. Required.")
	couponsCreateCmd.Flags().Int("discount-value", 0, "Discount value: percentage (1-100) or fixed amount in smallest currency unit. Required.")
	couponsCreateCmd.Flags().String("currency", "", "ISO 4217 currency code in lowercase (required when discount-type=amount, e.g. usd). Accepted: usd, cad, gbp, eur, aud.")
	couponsCreateCmd.Flags().Int("max-redemptions", 0, "Maximum number of times the coupon can be redeemed (must be ≥ 1). Omit for unlimited.")
	couponsCreateCmd.Flags().Bool("first-time-only", false, "Restrict to first-time buyers only.")
	couponsCreateCmd.Flags().String("expires-at", "", "Expiry timestamp in RFC 3339 format, e.g. 2026-12-31T23:59:59Z.")
	couponsCreateCmd.Flags().Bool("is-active", true, "Whether the coupon is active (default: true).")
	markFlagsRequired(couponsCreateCmd, "code", "discount-type", "discount-value")

	couponsUpdateCmd.Flags().Int("max-redemptions", 0, "Maximum number of redemptions (must be ≥ 1). Omit to leave unchanged.")
	couponsUpdateCmd.Flags().Bool("first-time-only", false, "Restrict to first-time buyers only.")
	couponsUpdateCmd.Flags().String("expires-at", "", "Expiry timestamp in RFC 3339 format, e.g. 2026-12-31T23:59:59Z.")
	couponsUpdateCmd.Flags().Bool("is-active", true, "Whether the coupon is active.")

	addPaginationFlags(couponsListCmd)
}
