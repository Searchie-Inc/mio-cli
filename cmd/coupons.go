package cmd

// coupons.go implements the `mio coupons` command group.
//
// Coupons are team-scoped discount codes managed under the products domain.
// All routes require a team admin JWT — this is an admin-only surface.
//
// Routes (see app/products/router.py "admin_router", prefix /api/teams/{team_id}):
//
//	create     POST   /api/teams/{team_id}/coupons/
//	list       GET    /api/teams/{team_id}/coupons/
//	retrieve   GET    /api/teams/{team_id}/coupons/{coupon_id}
//	update     PATCH  /api/teams/{team_id}/coupons/{coupon_id}
//	delete     DELETE /api/teams/{team_id}/coupons/{coupon_id}
//
// Create required fields (from CouponCreateAttributes, extra="forbid"):
//   - code          (string, alphanumeric + hyphens, normalized to uppercase)
//   - discount_type (string: "percentage" or "amount")
//   - discount_value (integer > 0)
//   - currency       (required when discount_type = "amount")
//
// Update mutable fields (from CouponUpdateAttributes, extra="forbid"):
//   - max_redemptions, first_time_only, expires_at, is_active, metadata
// Note: code, discount_type, discount_value, currency are IMMUTABLE post-create.
//
// MIO-846 — scaffolded from the Jake V3 QA audit (2026-06-09).

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// coupons <action>
	couponsCmd.AddCommand(
		couponsCreateCmd,
		couponsListCmd,
		couponsRetrieveCmd,
		couponsUpdateCmd,
		couponsDeleteCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(couponsCmd)
}

// ---- coupons group -------------------------------------------------------------

var couponsCmd = &cobra.Command{
	Use:   "coupons",
	Short: "Manage discount coupons.",
	Long:  "Create, list, retrieve, update and delete discount coupons for the active team (admin only).",
}

// couponsPath returns /api/teams/{team_id}/coupons[/{id}].
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

		var missing []string
		if !cmd.Flags().Changed("code") {
			missing = append(missing, "--code")
		}
		if !cmd.Flags().Changed("discount-type") {
			missing = append(missing, "--discount-type")
		}
		if !cmd.Flags().Changed("discount-value") {
			missing = append(missing, "--discount-value")
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flags: %s", strings.Join(missing, ", "))
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

		// Validate discount_type enum client-side (backend: DISCOUNT_TYPES = {"percent","amount"}).
		if dt, ok := attrs["discount_type"].(string); ok {
			if dt != "percent" && dt != "amount" {
				return errs.New(errs.ExitUsage, "--discount-type must be \"percent\" or \"amount\", got %q", dt)
			}
		}

		// Normalize currency to lowercase and validate against backend SUPPORTED_CURRENCIES.
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
	// create: code, discount_type, discount_value are required (immutable post-create).
	// currency is required when discount_type=amount (validated by backend).
	couponsCreateCmd.Flags().String("code", "", "Coupon code (alphanumeric + hyphens, normalized to uppercase). Required.")
	couponsCreateCmd.Flags().String("discount-type", "", "Discount type: percent or amount. Required.")
	couponsCreateCmd.Flags().Int("discount-value", 0, "Discount value: percentage (1-100) or fixed amount in smallest currency unit. Required.")
	couponsCreateCmd.Flags().String("currency", "", "ISO 4217 currency code in lowercase (required when discount-type=amount, e.g. usd). Accepted: usd, cad, gbp, eur, aud.")
	couponsCreateCmd.Flags().Int("max-redemptions", 0, "Maximum number of times the coupon can be redeemed (must be ≥ 1). Omit for unlimited.")
	couponsCreateCmd.Flags().Bool("first-time-only", false, "Restrict to first-time buyers only.")
	couponsCreateCmd.Flags().String("expires-at", "", "Expiry timestamp in RFC 3339 format, e.g. 2026-12-31T23:59:59Z.")
	couponsCreateCmd.Flags().Bool("is-active", true, "Whether the coupon is active (default: true).")

	// update: only mutable fields; code/discount_type/discount_value/currency absent.
	couponsUpdateCmd.Flags().Int("max-redemptions", 0, "Maximum number of redemptions (must be ≥ 1). Omit to leave unchanged.")
	couponsUpdateCmd.Flags().Bool("first-time-only", false, "Restrict to first-time buyers only.")
	couponsUpdateCmd.Flags().String("expires-at", "", "Expiry timestamp in RFC 3339 format, e.g. 2026-12-31T23:59:59Z.")
	couponsUpdateCmd.Flags().Bool("is-active", true, "Whether the coupon is active.")

	addPaginationFlags(couponsListCmd)
}
