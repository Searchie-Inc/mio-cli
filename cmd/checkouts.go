package cmd

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var checkoutAfterActions = []string{"into_hub", "hub_page", "external_url", "thank_you_email"}

func init() {
	checkoutsCmd.AddCommand(checkoutsListCmd, checkoutsRetrieveCmd, checkoutsCreateCmd, checkoutsUpdateCmd)
	rootCmd.AddCommand(checkoutsCmd)

	addPaginationFlags(checkoutsListCmd)
	markUnsignalledPaging(checkoutsListCmd, 20)

	addCheckoutWriteFlags(checkoutsCreateCmd)
	checkoutsCreateCmd.Flags().String("slug", "", "URL slug, fixed at creation. (required)")
	checkoutsCreateCmd.Flags().String("status", "open", "Checkout status: open or closed.")
	checkoutsCreateCmd.Flags().Bool("default", false, "Make this the hub's default checkout.")

	addCheckoutWriteFlags(checkoutsUpdateCmd)
	checkoutsUpdateCmd.Flags().String("status", "", "Checkout status: open or closed.")
	checkoutsUpdateCmd.Flags().Bool("default", false, "Make this the hub's default checkout.")
}

func addCheckoutWriteFlags(c *cobra.Command) {
	c.Flags().String("name", "", "Internal name. (required on create)")
	c.Flags().String("title", "", "Title shown to buyers. (required on create)")
	c.Flags().String("description", "", "Description shown to buyers.")
	c.Flags().StringSlice("price-id", nil, "Price id offered on this checkout. Repeatable or comma-separated. (required on create; replaces the set on update)")
	c.Flags().String("closed-message", "", "Message buyers see while the checkout is closed.")
	c.Flags().Bool("allow-promo-codes", false, "Let buyers enter promotion codes.")
	c.Flags().String("after-action", "", "What happens after purchase: into_hub, hub_page, external_url or thank_you_email.")
	c.Flags().String("after-page-id", "", "Page id to show after purchase. Used with --after-action hub_page.")
	c.Flags().String("after-url", "", "URL to send the buyer to after purchase. Used with --after-action external_url.")
}

func checkoutsPath(teamID, hubID, id string) string {
	base := checkoutHubBase(teamID, hubID) + "/checkouts"
	if id != "" {
		return base + "/" + id
	}
	return base
}

func validateCheckoutEnums(cmd *cobra.Command) error {
	if cmd.Flags().Changed("status") {
		v, _ := cmd.Flags().GetString("status")
		if v != "open" && v != "closed" {
			return errs.New(errs.ExitUsage, "--status must be open or closed, got %q", v)
		}
	}
	if cmd.Flags().Changed("after-action") {
		v, _ := cmd.Flags().GetString("after-action")
		for _, ok := range checkoutAfterActions {
			if v == ok {
				return nil
			}
		}
		return errs.New(errs.ExitUsage, "--after-action must be one of into_hub, hub_page, external_url, thank_you_email, got %q", v)
	}
	return nil
}

func checkoutWriteAttrs(cmd *cobra.Command) map[string]any {
	attrs := map[string]any{}
	setStringFlag(cmd, attrs, "name")
	setStringFlag(cmd, attrs, "title")
	setNullableMappedString(cmd, attrs, "description", "description")
	setStringFlag(cmd, attrs, "slug")
	setStringFlag(cmd, attrs, "status")
	setNullableMappedString(cmd, attrs, "closed-message", "closed_message")
	setMappedBool(cmd, attrs, "default", "is_default")
	setMappedBool(cmd, attrs, "allow-promo-codes", "allow_promotion_codes")
	setMappedString(cmd, attrs, "after-action", "after_action")
	setNullableMappedString(cmd, attrs, "after-page-id", "after_page_id")
	setNullableMappedString(cmd, attrs, "after-url", "after_url")
	if cmd.Flags().Changed("price-id") {
		ids, _ := cmd.Flags().GetStringSlice("price-id")
		attrs["price_ids"] = ids
	}
	return attrs
}

var checkoutsCmd = &cobra.Command{
	Use:   "checkouts",
	Short: "Manage a hub's checkouts (the buyer pages that sell its plans).",
	Long: `List, retrieve, create and update the checkouts of the active hub.

A checkout offers one or more prices to buyers. The response includes the buyer
'url' once the checkout is open and its hub is switched on; otherwise it is null.

All commands require a hub context: pass --hub <id> or run 'mio config set current_hub <id>'.`,
}

var checkoutsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List the hub's checkouts.",
	Example: `  mio checkouts list --hub hub_abc123 --jq 'map({id, name, status, url})'`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}
		query := url.Values{}
		addPageFlags(cmd, query)
		col, err := c.client.List(c.ctx, checkoutsPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var checkoutsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <checkout_id>",
	Short:   "Retrieve one checkout.",
	Example: `  mio checkouts retrieve chk_abc123 --hub hub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := stringArg(args[0])
		if id == "" {
			return errs.New(errs.ExitUsage, "checkout id must not be empty")
		}
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, checkoutsPath(teamID, hubID, id))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a checkout on the hub.",
	Example: `  mio checkouts create --hub hub_abc123 --name "Spring sale" --slug spring-sale \
    --title "Join the hub" --price-id price_1,price_2 --allow-promo-codes
  mio checkouts create --hub hub_abc123 --name Main --slug main --title Join \
    --price-id price_1 --after-action external_url --after-url https://example.com/welcome`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		for _, f := range []string{"name", "slug", "title"} {
			if flagValue(cmd, f) == "" {
				return errs.New(errs.ExitUsage, "--%s is required", f)
			}
		}
		if ids, _ := cmd.Flags().GetStringSlice("price-id"); len(ids) == 0 {
			return errs.New(errs.ExitUsage, "--price-id is required (at least one)")
		}
		if err := validateCheckoutEnums(cmd); err != nil {
			return err
		}
		attrs := checkoutWriteAttrs(cmd)
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, checkoutsPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutsUpdateCmd = &cobra.Command{
	Use:   "update <checkout_id>",
	Short: "Update a checkout.",
	Long: `Change a checkout. Only the flags you supply change (PATCH semantics).
The slug is fixed at creation. --price-id replaces the whole set of prices.
Pass an empty --description, --closed-message, --after-page-id or --after-url to clear it.`,
	Example: `  mio checkouts update chk_abc123 --hub hub_abc123 --status closed --closed-message "Back soon"
  mio checkouts update chk_abc123 --hub hub_abc123 --default`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := stringArg(args[0])
		if id == "" {
			return errs.New(errs.ExitUsage, "checkout id must not be empty")
		}
		if err := validateCheckoutEnums(cmd); err != nil {
			return err
		}
		attrs := checkoutWriteAttrs(cmd)
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Update(c.ctx, checkoutsPath(teamID, hubID, id), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}
