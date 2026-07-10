package cmd

// products_deliverables.go — the `mio products deliverables` sub-group (MIO-2268).
//
// A deliverable is what a product GRANTS the buyer on purchase — the row that
// wires "paid for product X" to "gets hub access / content enrollment / a tag /
// a file / community access". They are nested under a product and hard-deleted
// on removal.
//
// Routes (app/products/router.py, prefix /api/teams/{team_id}):
//
//	list   GET    /products/{product_id}/deliverables
//	create POST   /products/{product_id}/deliverables
//	delete DELETE /products/{product_id}/deliverables/{deliverable_id}
//
// The write body derives JSON:API type "product_deliverables" from the
// products/deliverables path tail (typeOverride already in internal/client).

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// deliverableTypes is the backend DELIVERABLE_TYPES enum (app/products/schemas.py).
var deliverableTypes = map[string]bool{
	"hub_access":         true,
	"content_enrollment": true,
	"tag":                true,
	"file_download":      true,
	"community_access":   true,
}

func init() {
	productsDeliverablesCmd.AddCommand(
		productsDeliverablesListCmd,
		productsDeliverablesCreateCmd,
		productsDeliverablesDeleteCmd,
	)
	productsCmd.AddCommand(productsDeliverablesCmd)

	addPaginationFlags(productsDeliverablesListCmd)

	// create flags: deliverable_type is required.
	productsDeliverablesCreateCmd.Flags().String("type", "", "Deliverable type: hub_access, content_enrollment, tag, file_download, or community_access. Required.")
	productsDeliverablesCreateCmd.Flags().String("resource-id", "", "Id of the resource this deliverable grants (e.g. hub id, content id, tag id). Max 255 chars.")
	productsDeliverablesCreateCmd.Flags().String("resource-meta", "", "Optional JSON object of extra metadata for the deliverable (or @file.json).")
	productsDeliverablesCreateCmd.Flags().Int("duration-days", 0, "Access duration in days (>= 1). Omit for permanent access.")
	productsDeliverablesCreateCmd.Flags().Int("position", 0, "Ordering position among the product's deliverables (>= 0).")
}

// deliverablesPath returns /api/teams/{team}/products/{product}/deliverables[/{id}].
func deliverablesPath(teamID, productID, deliverableID string) string {
	base := fmt.Sprintf("/api/teams/%s/products/%s/deliverables", teamID, productID)
	if deliverableID != "" {
		return base + "/" + deliverableID
	}
	return base
}

var productsDeliverablesCmd = &cobra.Command{
	Use:   "deliverables",
	Short: "Manage a product's deliverables (what a purchase grants).",
	Long: `List, create, and delete the deliverables attached to a product.

A deliverable is the grant a buyer receives on purchase — hub access, a content
enrollment, a tag, a file download, or community access.`,
}

var productsDeliverablesListCmd = &cobra.Command{
	Use:     "list <product_id>",
	Short:   "List a product's deliverables.",
	Long:    "List all deliverables attached to a product.",
	Example: `  mio products deliverables list prod_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		productID := stringArg(args[0])
		if productID == "" {
			return errs.New(errs.ExitUsage, "product id must not be empty")
		}

		c, teamID, err := deliverablesContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, deliverablesPath(teamID, productID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var productsDeliverablesCreateCmd = &cobra.Command{
	Use:   "create <product_id>",
	Short: "Attach a deliverable to a product.",
	Long: `Attach a deliverable to a product.

--type is required. Allowed values:
  hub_access, content_enrollment, tag, file_download, community_access

--resource-id names the resource the deliverable grants (e.g. the hub id for
hub_access). --duration-days sets a time-limited grant; omit it for permanent
access.`,
	Example: `  mio products deliverables create prod_abc123 --type hub_access --resource-id hub_xyz
  mio products deliverables create prod_abc123 --type tag --resource-id tag_vip --duration-days 30`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate before resolving context so a usage error fires no request.
		productID := stringArg(args[0])
		if productID == "" {
			return errs.New(errs.ExitUsage, "product id must not be empty")
		}
		if !cmd.Flags().Changed("type") {
			return errs.New(errs.ExitUsage, "missing required flag: --type")
		}
		dtype := flagValue(cmd, "type")
		if !deliverableTypes[dtype] {
			return errs.New(errs.ExitUsage, "invalid --type %q: must be one of %s", dtype, deliverableTypesList())
		}
		if cmd.Flags().Changed("duration-days") {
			dd, err := cmd.Flags().GetInt("duration-days")
			if err != nil {
				return errs.New(errs.ExitUsage, "--duration-days: %s", err)
			}
			if dd < 1 {
				return errs.New(errs.ExitUsage, "invalid --duration-days %d: must be >= 1 (omit for permanent access)", dd)
			}
		}
		if err := validateNonNegativePosition(cmd); err != nil {
			return err
		}

		attrs := map[string]any{"deliverable_type": dtype}
		setMappedString(cmd, attrs, "resource-id", "resource_id")
		if err := setMappedJSONObjectFlag(cmd, attrs, "resource-meta", "resource_meta"); err != nil {
			return err
		}
		setMappedInt(cmd, attrs, "duration-days", "duration_days")
		setIntFlag(cmd, attrs, "position")

		c, teamID, err := deliverablesContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, deliverablesPath(teamID, productID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var productsDeliverablesDeleteCmd = &cobra.Command{
	Use:   "delete <product_id> <deliverable_id>",
	Short: "Remove a deliverable from a product.",
	Long: `Remove a deliverable from a product (hard-delete).

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio products deliverables delete prod_abc123 del_xyz789 --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		productID := stringArg(args[0])
		deliverableID := stringArg(args[1])
		if productID == "" || deliverableID == "" {
			return errs.New(errs.ExitUsage, "product id and deliverable id must not be empty")
		}

		c, teamID, err := deliverablesContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Delete deliverable %s from product %s?", deliverableID, productID)); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, deliverablesPath(teamID, productID, deliverableID)); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted deliverable %s.\n", deliverableID)
		return nil
	},
}

// deliverablesContext resolves context + team id for deliverable commands.
func deliverablesContext(cmd *cobra.Command) (*cmdContext, string, error) {
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

// deliverableTypesList returns the allowed deliverable types as a sorted,
// comma-separated string for error messages.
func deliverableTypesList() string {
	out := make([]string, 0, len(deliverableTypes))
	for k := range deliverableTypes {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
