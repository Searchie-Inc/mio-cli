package cmd

// checkout.go implements the `mio checkout` command group.
//
// It covers all admin reads and actions in the checkout module:
//
//	orders:        list/retrieve     /api/teams/{team_id}/hubs/{hub_id}/orders[/{id}]
//	subscriptions: list/retrieve     …/subscriptions[/{id}]
//	               cancel            POST …/subscriptions/{id}/cancel
//	payments:      list/retrieve     …/payments[/{id}]
//	               refund            POST …/payments/{id}/refund
//	webhooks:      list/retrieve     …/payment-webhooks[/{id}]
//	               replay            POST …/payment-webhooks/{id}/replay
//	accounts:      list/retrieve     /api/teams/{team_id}/payment-accounts[/{id}]
//	               onboarding-link   POST …/payment-accounts/onboarding-link
//	                                 (web/JWT-only — the backend rejects API-key
//	                                 principals on this route, MIO-2655, and this
//	                                 CLI is API-key-only, so the command always
//	                                 fails fast with a clear error; MIO-2717)
//	stripe-sync:   import            POST /api/teams/{team_id}/checkout/sync/import-from-stripe
//	               import-status     GET  …/checkout/sync/import-runs/{run_id}
//	               adopt-product     POST /api/teams/{team_id}/products/adopt-from-stripe
//
// Hub-scoped commands (orders, subscriptions, payments, webhooks) require --hub;
// they return exit code 2 via requireHub if it is absent.
//
// Destructive / irreversible actions (cancel, refund, replay) honour --yes;
// without it in a non-interactive shell they exit with code 5.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// orders sub-resource
	checkoutOrdersCmd.AddCommand(
		checkoutOrdersListCmd,
		checkoutOrdersRetrieveCmd,
	)
	checkoutCmd.AddCommand(checkoutOrdersCmd)

	// subscriptions sub-resource
	checkoutSubscriptionsCmd.AddCommand(
		checkoutSubscriptionsListCmd,
		checkoutSubscriptionsRetrieveCmd,
		checkoutSubscriptionsCancelCmd,
	)
	checkoutCmd.AddCommand(checkoutSubscriptionsCmd)

	// payments sub-resource
	checkoutPaymentsCmd.AddCommand(
		checkoutPaymentsListCmd,
		checkoutPaymentsRetrieveCmd,
		checkoutPaymentsRefundCmd,
	)
	checkoutCmd.AddCommand(checkoutPaymentsCmd)

	// payment-webhooks sub-resource
	checkoutWebhooksCmd.AddCommand(
		checkoutWebhooksListCmd,
		checkoutWebhooksRetrieveCmd,
		checkoutWebhooksReplayCmd,
	)
	checkoutCmd.AddCommand(checkoutWebhooksCmd)

	// payment-accounts sub-resource
	checkoutAccountsCmd.AddCommand(
		checkoutAccountsListCmd,
		checkoutAccountsRetrieveCmd,
		checkoutAccountsOnboardingLinkCmd,
	)
	checkoutCmd.AddCommand(checkoutAccountsCmd)

	// stripe-sync sub-resource
	checkoutStripeSyncCmd.AddCommand(
		checkoutStripeSyncImportCmd,
		checkoutStripeSyncImportStatusCmd,
		checkoutStripeSyncAdoptProductCmd,
	)
	checkoutCmd.AddCommand(checkoutStripeSyncCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(checkoutCmd)
}

func init() {
	// Pagination flags for list commands.
	addPaginationFlags(checkoutOrdersListCmd)
	addPaginationFlags(checkoutSubscriptionsListCmd)
	addPaginationFlags(checkoutPaymentsListCmd)
	addPaginationFlags(checkoutWebhooksListCmd)
	addPaginationFlags(checkoutAccountsListCmd)

	// refund flags
	checkoutPaymentsRefundCmd.Flags().Int("amount", 0, "Amount to refund in the payment's currency minor unit (e.g. cents). Omit to refund in full.")
	checkoutPaymentsRefundCmd.Flags().String("reason", "", "Reason for the refund (e.g. duplicate, fraudulent, requested_by_customer). (required)")
	_ = checkoutPaymentsRefundCmd.MarkFlagRequired("reason")

	// onboarding-link flags (documented for reference — the backend requires
	// all three — but this command always fails fast client-side; see the
	// WEB/JWT-ONLY note on the command itself).
	checkoutAccountsOnboardingLinkCmd.Flags().String("hub-id", "", "Hub ID the onboarding link is for — canonical UUID; slugs are not resolved. (required)")
	checkoutAccountsOnboardingLinkCmd.Flags().String("return-url", "", "URL Stripe returns the user to after completing onboarding. (required)")
	checkoutAccountsOnboardingLinkCmd.Flags().String("refresh-url", "", "URL Stripe sends the user to if the onboarding link expires. (required)")

	// stripe-sync import flags
	checkoutStripeSyncImportCmd.Flags().String("hub-id", "", "Hub ID whose Stripe data should be imported. (required)")

	// adopt-product flags
	checkoutStripeSyncAdoptProductCmd.Flags().String("stripe-product-id", "", "Stripe product ID to adopt into the mio catalogue. (required)")
	checkoutStripeSyncAdoptProductCmd.Flags().String("hub-id", "", "Hub ID (UUID) the adopted product belongs to. (required)")
}

// ---- root group ---------------------------------------------------------------

var checkoutCmd = &cobra.Command{
	Use:   "checkout",
	Short: "Manage checkout: orders, subscriptions, payments, and Stripe integration.",
	Long: `Read and act on checkout data for the active team.

Hub-scoped resources (orders, subscriptions, payments, payment-webhooks) require
a hub context: pass --hub <id> or run 'mio config set current_hub <id>'.

Team-scoped resources (payment-accounts, stripe-sync) need only a team context.`,
}

// ---- path helpers -------------------------------------------------------------

// checkoutHubBase returns the hub-scoped base path used by most checkout resources.
//
//	/api/teams/{team_id}/hubs/{hub_id}
func checkoutHubBase(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s", teamID, hubID)
}

// ordersPath returns the orders collection or single-resource path.
func ordersPath(teamID, hubID, id string) string {
	base := checkoutHubBase(teamID, hubID) + "/orders"
	if id != "" {
		return base + "/" + id
	}
	return base
}

// subscriptionsPath returns the subscriptions collection or single-resource path.
func subscriptionsPath(teamID, hubID, id string) string {
	base := checkoutHubBase(teamID, hubID) + "/subscriptions"
	if id != "" {
		return base + "/" + id
	}
	return base
}

// paymentsPath returns the payments collection or single-resource path.
func paymentsPath(teamID, hubID, id string) string {
	base := checkoutHubBase(teamID, hubID) + "/payments"
	if id != "" {
		return base + "/" + id
	}
	return base
}

// webhooksPath returns the payment-webhooks collection or single-resource path.
func webhooksPath(teamID, hubID, id string) string {
	base := checkoutHubBase(teamID, hubID) + "/payment-webhooks"
	if id != "" {
		return base + "/" + id
	}
	return base
}

// accountsPath returns the payment-accounts collection or single-resource path.
//
//	/api/teams/{team_id}/payment-accounts[/{id}]
func accountsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/payment-accounts", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ---- context helpers ----------------------------------------------------------

// checkoutHubContext is the shared boilerplate for hub-scoped checkout sub-commands:
// build the context, require auth, resolve team id and hub id.
func checkoutHubContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", "", err
	}
	if err := c.requireAuth(); err != nil {
		return nil, "", "", err
	}
	teamID, err := c.requireTeam()
	if err != nil {
		return nil, "", "", err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, "", "", err
	}
	return c, teamID, hubID, nil
}

// checkoutTeamContext is the shared boilerplate for team-scoped checkout sub-commands.
func checkoutTeamContext(cmd *cobra.Command) (*cmdContext, string, error) {
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

// ---- orders -------------------------------------------------------------------

var checkoutOrdersCmd = &cobra.Command{
	Use:   "orders",
	Short: "Read orders for a hub.",
	Long:  "List and retrieve orders placed through a hub's checkout.",
}

var checkoutOrdersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List orders for a hub.",
	Long:  "List all orders for the active team and hub. Requires --hub.",
	Example: `  # List orders using context
  mio checkout orders list

  # Paginate results
  mio checkout orders list --limit 25 --after <cursor>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, ordersPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var checkoutOrdersRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an order by id.",
	Long:    "Retrieve a single order by its id. Requires --hub.",
	Example: `  mio checkout orders retrieve ord_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, ordersPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- subscriptions ------------------------------------------------------------

var checkoutSubscriptionsCmd = &cobra.Command{
	Use:   "subscriptions",
	Short: "Read and cancel subscriptions for a hub.",
	Long:  "List, retrieve, and cancel recurring subscriptions for a hub.",
}

var checkoutSubscriptionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List subscriptions for a hub.",
	Long:  "List all subscriptions for the active team and hub. Requires --hub.",
	Example: `  mio checkout subscriptions list
  mio checkout subscriptions list --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, subscriptionsPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var checkoutSubscriptionsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a subscription by id.",
	Long:    "Retrieve a single subscription by its id. Requires --hub.",
	Example: `  mio checkout subscriptions retrieve sub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, subscriptionsPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutSubscriptionsCancelCmd = &cobra.Command{
	Use:   "cancel <id>",
	Short: "Cancel a subscription.",
	Long: `Cancel a subscription immediately. This is irreversible.

Requires --hub. Pass --yes to skip the confirmation prompt in non-interactive
environments (scripts, CI, AI agents).`,
	Example: `  mio checkout subscriptions cancel sub_abc123
  mio checkout subscriptions cancel sub_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Cancel subscription %s?", args[0])); err != nil {
			return err
		}

		path := subscriptionsPath(teamID, hubID, args[0]) + "/cancel"
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Cancelled subscription %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- payments -----------------------------------------------------------------

var checkoutPaymentsCmd = &cobra.Command{
	Use:   "payments",
	Short: "Read and refund payments for a hub.",
	Long:  "List, retrieve, and issue refunds for payments captured through a hub.",
}

var checkoutPaymentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List payments for a hub.",
	Long:  "List all payments for the active team and hub. Requires --hub.",
	Example: `  mio checkout payments list
  mio checkout payments list --limit 25 --after <cursor>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, paymentsPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var checkoutPaymentsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a payment by id.",
	Long:    "Retrieve a single payment by its id. Requires --hub.",
	Example: `  mio checkout payments retrieve pay_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, paymentsPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutPaymentsRefundCmd = &cobra.Command{
	Use:   "refund <id>",
	Short: "Refund a payment.",
	Long: `Issue a full or partial refund for a payment. This is irreversible.

Requires --hub and --reason. Use --amount to issue a partial refund (in the
currency's minor unit, e.g. cents); omit --amount to refund the full amount.

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  # Full refund
  mio checkout payments refund pay_abc123 --reason requested_by_customer

  # Partial refund of $5.00 USD
  mio checkout payments refund pay_abc123 --reason requested_by_customer --amount 500

  # Non-interactive (CI/agent)
  mio checkout payments refund pay_abc123 --reason duplicate --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Refund payment %s?", args[0])); err != nil {
			return err
		}

		// The backend RefundRequest body is REQUIRED: a refunds envelope whose
		// attributes carry a mandatory `reason` (extra="forbid") and an optional
		// `amount` (omit for a full refund). Always send the envelope with reason;
		// add amount only when --amount was provided. Never send a nil body.
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "reason")
		setIntFlag(cmd, attrs, "amount")

		path := paymentsPath(teamID, hubID, args[0]) + "/refund"
		res, err := c.client.Action(c.ctx, "POST", path, attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Refunded payment %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- payment-webhooks ---------------------------------------------------------

var checkoutWebhooksCmd = &cobra.Command{
	Use:   "webhooks",
	Short: "Read and replay payment webhook events for a hub.",
	Long:  "List, retrieve, and replay inbound Stripe webhook events for a hub.",
}

var checkoutWebhooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List payment webhook events for a hub.",
	Long:  "List all recorded payment webhook events for the active team and hub. Requires --hub.",
	Example: `  mio checkout webhooks list
  mio checkout webhooks list --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, webhooksPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var checkoutWebhooksRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a webhook event by id.",
	Long:    "Retrieve a single recorded payment webhook event by its id. Requires --hub.",
	Example: `  mio checkout webhooks retrieve whe_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, webhooksPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutWebhooksReplayCmd = &cobra.Command{
	Use:   "replay <id>",
	Short: "Replay a payment webhook event.",
	Long: `Re-deliver a previously received payment webhook event to the handler.

Useful for recovering from transient processing failures. Requires --hub.

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio checkout webhooks replay whe_abc123
  mio checkout webhooks replay whe_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := checkoutHubContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Replay webhook event %s?", args[0])); err != nil {
			return err
		}

		path := webhooksPath(teamID, hubID, args[0]) + "/replay"
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Replayed webhook event %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- payment-accounts ---------------------------------------------------------

var checkoutAccountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "Manage payment accounts for a team.",
	Long: `List and retrieve payment accounts (Stripe Connect) for the active team.

Payment accounts are team-scoped (no --hub required). Connecting a new Stripe
account (onboarding-link) is web/JWT-only — see 'onboarding-link --help'.`,
}

var checkoutAccountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List payment accounts for the active team.",
	Long:  "List all payment accounts connected to the active team.",
	Example: `  mio checkout accounts list
  mio checkout accounts list --limit 10`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := checkoutTeamContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, accountsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var checkoutAccountsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a payment account by id.",
	Long:    "Retrieve a single payment account by its id.",
	Example: `  mio checkout accounts retrieve acct_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := checkoutTeamContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, accountsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutAccountsOnboardingLinkCmd = &cobra.Command{
	Use:   "onboarding-link",
	Short: "Generate a Stripe Connect onboarding link. (web/JWT-only — always fails from this CLI; see below)",
	Long: `Generate a Stripe Connect account onboarding or re-onboarding link for the
active team. Returns a short-lived URL the team owner should visit to complete
or update their Stripe account setup.

WEB/JWT-ONLY: the backend rejects API-key principals on this route (MIO-2655) —
a leaked team API key must not be able to attach an attacker's Stripe payout
account, so connecting a Stripe account requires a user JWT (the MIO-2599
posture, matching api-keys/oauth-clients/external-login-providers). The mio
CLI authenticates exclusively via team API keys ("mio_sk_..." — see 'mio
login'; any JWT minted during the email+password login flow is discarded
immediately after minting the stored key), so this command can never satisfy
that requirement. It always fails fast with a clear error instead of sending
the request and surfacing a raw 403 — complete Stripe Connect onboarding in
the member.dev dashboard instead.

For reference, the backend requires --hub-id (the hub's canonical UUID —
slugs are NOT resolved for this endpoint), --return-url and --refresh-url,
sent inside a JSON:API onboarding_links envelope.`,
	Example: `  mio checkout accounts onboarding-link --hub-id 3fa85f64-5717-4562-b3fc-2c963f66afa6 --return-url https://app.example.com/return --refresh-url https://app.example.com/refresh

  # The above always fails (web/JWT-only, MIO-2655) — use the member.dev
  # dashboard to connect a Stripe account instead.`,
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		// The backend requires a user JWT on this route (MIO-2655/MIO-2599
		// posture): a leaked team API key must not be able to attach an
		// attacker's Stripe payout account. The mio CLI is API-key-only (see
		// login.go — any JWT minted during the email+password login flow is
		// discarded immediately after minting the stored mio_sk_... key), so
		// this command can never satisfy that requirement. Fail fast with a
		// clear, actionable message instead of sending the request and
		// surfacing a raw 403. (MIO-2717)
		return errs.New(errs.ExitAuth,
			"connecting a Stripe account is a web/JWT-only operation for security "+
				"(a leaked API key must not be able to attach a payout account) — "+
				"complete Stripe Connect onboarding in the member.dev dashboard, not the CLI")
	},
}

// ---- stripe-sync --------------------------------------------------------------

var checkoutStripeSyncCmd = &cobra.Command{
	Use:   "stripe-sync",
	Short: "Stripe data import and product adoption tools.",
	Long: `Tools for importing historical Stripe data and adopting existing Stripe
products into the mio catalogue.

These are team-scoped operations (no --hub required).`,
}

var checkoutStripeSyncImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Start a Stripe data import run.",
	Long: `Trigger an asynchronous import of historical Stripe data (customers, subscriptions,
payments) for a hub into the active team's mio account.

Use 'mio checkout stripe-sync import-status <run_id>' to poll the import progress.`,
	Example: `  mio checkout stripe-sync import --hub-id hub_abc123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := checkoutTeamContext(cmd)
		if err != nil {
			return err
		}

		// Flat body: backend StartImportRequest is a plain {hub_id} model.
		attrs := map[string]any{}
		setMappedString(cmd, attrs, "hub-id", "hub_id")

		if !cmd.Flags().Changed("hub-id") {
			return errs.New(errs.ExitUsage, "nothing to import: --hub-id is required")
		}

		path := fmt.Sprintf("/api/teams/%s/checkout/sync/import-from-stripe", teamID)
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Stripe import started.\n")
			return nil
		}
		return c.render(cmd, res)
	},
}

var checkoutStripeSyncImportStatusCmd = &cobra.Command{
	Use:     "import-status <run_id>",
	Short:   "Get the status of a Stripe import run.",
	Long:    "Retrieve the current status and progress of a Stripe import run by its run id.",
	Example: `  mio checkout stripe-sync import-status run_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := checkoutTeamContext(cmd)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/teams/%s/checkout/sync/import-runs/%s", teamID, args[0])
		res, err := c.client.Retrieve(c.ctx, path)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var checkoutStripeSyncAdoptProductCmd = &cobra.Command{
	Use:   "adopt-product",
	Short: "Adopt an existing Stripe product into the mio catalogue.",
	Long: `Import a single Stripe product (with its prices) into the mio product catalogue
without triggering a full data import. Useful for adopting individual products
during a staged migration.

--stripe-product-id and --hub-id are required.`,
	Example: `  mio checkout stripe-sync adopt-product --stripe-product-id prod_abc123 --hub-id hub_abc123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := checkoutTeamContext(cmd)
		if err != nil {
			return err
		}

		// Flat body: backend AdoptProductRequest is a plain
		// {stripe_product_id, hub_id} model with extra="forbid".
		attrs := map[string]any{}
		setMappedString(cmd, attrs, "stripe-product-id", "stripe_product_id")
		setMappedString(cmd, attrs, "hub-id", "hub_id")

		if !cmd.Flags().Changed("stripe-product-id") || !cmd.Flags().Changed("hub-id") {
			return errs.New(errs.ExitUsage, "nothing to adopt: --stripe-product-id and --hub-id are required")
		}

		path := fmt.Sprintf("/api/teams/%s/products/adopt-from-stripe", teamID)
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return err
		}
		if res == nil {
			return errs.New(errs.ExitGeneric, "adopt-product: server returned no data")
		}
		return c.render(cmd, res)
	},
}
