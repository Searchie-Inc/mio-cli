package cmd

// pages_catalog.go — the `mio pages catalog` group (MIO-2340): consume the
// page-builder catalog (mio-page-catalog, the cross-repo source of truth) to
//
//   - scaffold a real node-tree from a template recipe (the tree/publish-door
//     artifact the visual builder produces — charter D1), via a Go port of the
//     reference applier (internal/catalog);
//   - list the author templates + recommendations per page type;
//   - list the compiled section types (the imperative-door writable allow-list).
//
// Catalog source resolution (live fetch → last-good cache → embedded vendored)
// lives in internal/catalog; this file wires the CLI flags, the HTTP fetcher
// adapter, and rendering. Scaffolding is emit-only — it prints the tree to
// stdout for `pages tree set` to apply; it never writes to the backend itself.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/output"
)

func init() {
	pagesCatalogCmd.AddCommand(
		pagesCatalogScaffoldCmd,
		pagesCatalogTemplatesCmd,
		pagesCatalogSectionTypesCmd,
	)
	pagesCmd.AddCommand(pagesCatalogCmd)

	// Catalog-source flags, shared (persistent) across the whole group.
	pf := pagesCatalogCmd.PersistentFlags()
	pf.Bool("offline", false, "Use the embedded, digest-pinned vendored catalog only (no network).")
	pf.String("catalog", "", "Path to a catalog.json to use instead of live-fetch/vendored.")

	pagesCatalogScaffoldCmd.Flags().String("template", "", "Catalog template id to scaffold (section or page template). Required.")
	pagesCatalogScaffoldCmd.Flags().String("variant", "", "Template variant (a data-source type or layout preset).")
	pagesCatalogTemplatesCmd.Flags().String("page-type", "", "Show only templates recommended for this page type.")
	pagesCatalogSectionTypesCmd.Flags().Bool("writable-only", false, "List only the writable (imperative-door) section types.")
}

var pagesCatalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Inspect the page-builder catalog and scaffold node-trees from it.",
	Long: `Consume the page-builder catalog — the cross-repo source of truth for the
templates, section types, and page types the builder understands.

  scaffold       instantiate a template into a node-tree (for 'pages tree set')
  templates      list author templates + recommendations per page type
  section-types  list compiled section types (the writable imperative-door set)

The catalog is fetched live (GET /api/page-builder/catalog, cached by ETag) and
falls back to an embedded, digest-pinned copy offline. --offline forces the
embedded copy; --catalog <file> overrides both.`,
}

var pagesCatalogScaffoldCmd = &cobra.Command{
	Use:   "scaffold",
	Short: "Scaffold a node-tree from a catalog template.",
	Long: `Instantiate a catalog template — its declarative starter recipe, or a
selected --variant — into a fresh node-tree via the reference applier (the same
artifact the visual builder produces, not imperative sections) and print it to
stdout. Every node gets a fresh UUIDv7 id.

A PAGE template (page-*) is emitted as a complete, settable tree wrapped as
{"root": ...}; pipe it straight into 'pages tree set':

  mio pages catalog scaffold --template page-homepage > tree.json
  mio pages tree set <page_id> --if-match <v> --file tree.json

A SECTION template is emitted as a bare node subtree to drop into a page root's
children. Scaffolding is offline-capable (falls back to the vendored catalog).`,
	Example: `  mio pages catalog scaffold --template page-homepage
  mio pages catalog scaffold --template hero --variant playlist`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		templateID := strings.TrimSpace(getString(cmd, "template"))
		if templateID == "" {
			return errs.New(errs.ExitUsage, "--template is required (see 'mio pages catalog templates')")
		}
		variant := strings.TrimSpace(getString(cmd, "variant"))

		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		cat, err := resolveCatalog(cmd, c)
		if err != nil {
			return err
		}

		tmpl, ok := cat.TemplateByID(templateID)
		if !ok {
			return errs.New(errs.ExitUsage,
				"unknown template %q — run 'mio pages catalog templates' for valid ids", templateID)
		}
		if variant != "" {
			if _, ok := tmpl.Variants[variant]; !ok {
				valid := tmpl.VariantKeys()
				if len(valid) == 0 {
					return errs.New(errs.ExitUsage, "template %q has no variants", templateID)
				}
				return errs.New(errs.ExitUsage,
					"unknown variant %q for template %q — valid variants: %s", variant, templateID, strings.Join(valid, ", "))
			}
		}

		node, err := catalog.InstantiateTemplate(tmpl, variant, catalog.NewUUIDv7Gen())
		if err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}

		// A page template is a whole-page root → wrap it in the tree envelope so
		// the output is directly settable via 'pages tree set --file'. A section
		// template is a fragment → emit the bare node.
		var out any = node
		if tmpl.IsPage {
			out = map[string]any{"root": node}
		}

		// A scaffolded tree is always JSON (never a table); honor --jq.
		return output.Render(cmd.OutOrStdout(), out, output.Options{Format: output.FormatJSON, JQ: flags.jq})
	},
}

var pagesCatalogTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List catalog author templates and their recommendations.",
	Long: `List the author templates in the catalog. With --page-type, list only the
section templates recommended for that page type (ordered by recommendation),
followed by the page template for it — the "what can I build here" view. Without
it, list every section and page template.`,
	Example: `  mio pages catalog templates
  mio pages catalog templates --page-type homepage`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		cat, err := resolveCatalog(cmd, c)
		if err != nil {
			return err
		}

		pageType := strings.TrimSpace(getString(cmd, "page-type"))
		var rows []any
		if pageType != "" {
			for _, t := range cat.RecommendedTemplates(pageType) {
				rows = append(rows, templateRow(t))
			}
			if pt, ok := cat.PageTemplateForType(pageType); ok {
				rows = append(rows, templateRow(pt))
			}
		} else {
			for _, t := range cat.Templates {
				rows = append(rows, templateRow(t))
			}
			for _, t := range cat.PageTemplates {
				rows = append(rows, templateRow(t))
			}
		}
		return c.render(cmd, rows)
	},
}

var pagesCatalogSectionTypesCmd = &cobra.Command{
	Use:   "section-types",
	Short: "List catalog compiled section types (imperative-door vocabulary).",
	Long: `List the compiled section types from the catalog. 'writable' marks the types
accepted by the imperative 'pages sections create --type' door. Use
--writable-only for just that allow-list.`,
	Example: `  mio pages catalog section-types
  mio pages catalog section-types --writable-only`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		cat, err := resolveCatalog(cmd, c)
		if err != nil {
			return err
		}

		writableOnly := getBool(cmd, "writable-only")
		var rows []any
		for _, s := range cat.SectionTypes {
			if writableOnly && !s.Writable {
				continue
			}
			rows = append(rows, sectionTypeRow(s))
		}
		return c.render(cmd, rows)
	},
}

// ---- catalog resolution wiring ---------------------------------------------

// catalogFetcher adapts *client.Client to catalog.Fetcher so internal/catalog
// stays free of any dependency on the HTTP layer.
type catalogFetcher struct{ c *client.Client }

func (f catalogFetcher) FetchCatalog(ctx context.Context, ifNoneMatch string) (catalog.FetchResult, error) {
	r, err := f.c.FetchCatalog(ctx, ifNoneMatch)
	if err != nil {
		return catalog.FetchResult{}, err
	}
	return catalog.FetchResult{Body: r.Body, ETag: r.ETag, NotModified: r.NotModified}, nil
}

// resolveCatalog loads the active catalog per --offline/--catalog and the
// live→cache→vendored precedence, wiring the client as the live fetcher and
// reporting provenance to stderr (never stdout, so piped scaffold output stays
// pure data).
func resolveCatalog(cmd *cobra.Command, c *cmdContext) (*catalog.Catalog, error) {
	offline := getBool(cmd, "offline")
	override := strings.TrimSpace(getString(cmd, "catalog"))

	opts := catalog.ResolveOptions{
		Offline:      offline,
		OverrideFile: override,
		CacheDir:     catalogCacheDir(),
		Warnf: func(format string, a ...any) {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
		},
	}
	if !offline && override == "" && c != nil {
		opts.Fetcher = catalogFetcher{c: c.client}
	}

	cat, src, err := catalog.Resolve(cmd.Context(), opts)
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "catalog: %s (version %s)\n", src, cat.Meta.CatalogVersion)
	return cat, nil
}

// catalogCacheDir returns the on-disk catalog cache directory. MIO_CATALOG_CACHE_DIR
// overrides it (a testing / air-gapped hook); "" disables caching.
func catalogCacheDir() string {
	if d := os.Getenv("MIO_CATALOG_CACHE_DIR"); d != "" {
		return d
	}
	return catalog.DefaultCacheDir()
}

func templateRow(t catalog.Template) map[string]any {
	kind := "section"
	if t.IsPage {
		kind = "page"
	}
	row := map[string]any{
		"id":        t.ID,
		"kind":      kind,
		"label":     t.Label,
		"lifecycle": t.Lifecycle,
	}
	if t.IsPage {
		row["page_type"] = t.PageType
	} else {
		row["compiled_section_type"] = t.CompiledSectionType
		row["applicable_page_types"] = strings.Join(t.ApplicablePageTypes, ",")
		if t.Recommendation != nil {
			row["recommendation"] = t.Recommendation.Tier
			row["order"] = t.Recommendation.Order
		}
	}
	if vs := t.VariantKeys(); len(vs) > 0 {
		row["variants"] = strings.Join(vs, ",")
	}
	return row
}

func sectionTypeRow(s catalog.SectionType) map[string]any {
	return map[string]any{
		"id":            s.ID,
		"writable":      s.Writable,
		"lifecycle":     s.Lifecycle,
		"anon_safe":     s.AnonSafe,
		"compiled_from": strings.Join(s.CompiledFrom, ","),
	}
}

// getString / getBool read a flag, ignoring the (impossible for registered
// flags) lookup error to keep call sites terse.
func getString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

func getBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}
