package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/output"
)

func init() {
	hubsCmd.AddCommand(
		hubsCreateCmd,
		hubsListCmd,
		hubsRetrieveCmd,
		hubsUpdateCmd,
		hubsDeleteCmd,
	)

	hubsPoliciesCmd.AddCommand(
		hubsPoliciesGetCmd,
		hubsPoliciesUpdateCmd,
		hubsPoliciesGateCmd,
	)
	hubsCmd.AddCommand(hubsPoliciesCmd)

	hubsRedirectOriginsCmd.AddCommand(
		hubsRedirectOriginsGetCmd,
		hubsRedirectOriginsSetCmd,
	)
	hubsCmd.AddCommand(hubsRedirectOriginsCmd)

	hubsEmailSettingsCmd.AddCommand(
		hubsEmailSettingsGetCmd,
		hubsEmailSettingsUpdateCmd,
	)
	hubsCmd.AddCommand(hubsEmailSettingsCmd)

	rootCmd.AddCommand(hubsCmd)
}

var hubsCmd = &cobra.Command{
	Use:   "hubs",
	Short: "Manage hubs.",
	Long: `Create, list, retrieve, update and delete hubs for the active team.

Every verb except 'delete' takes the hub id as an OPTIONAL positional: give it
explicitly to target any hub, or omit it to use the ambient hub context (--hub,
or current_hub in config) like the other hub-scoped command groups. 'delete'
requires the id positionally — it is irreversible.`,
	Example: `  mio hubs list
  mio hubs create --name "My Community" --slug my-community
  mio hubs retrieve hub_abc123
  mio hubs retrieve --hub hub_abc123
  mio hubs update hub_abc123 --name "Renamed Community"
  mio hubs delete hub_abc123`,
}

func hubsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

func hubsContext(cmd *cobra.Command) (*cmdContext, string, error) {
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

func (c *cmdContext) hubTargetID(cmd *cobra.Command, hubArg string, supplied bool) (string, error) {
	if supplied {
		if strings.TrimSpace(hubArg) == "" {
			return "", errs.New(errs.ExitUsage, "%s", blankHubIDMessage(cmd))
		}
		return hubArg, nil
	}

	id, err := c.requireHub()
	if errors.Is(err, errNoHubInContext) {
		return "", errs.New(errs.ExitUsage,
			"no hub id: pass it positionally (`%s <hub_id>`), pass --hub <id>, or run "+
				"`mio config set current_hub <id>` — run 'mio hubs list' to see the ids",
			cmd.CommandPath())
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func hubsOptionalIDArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return errs.New(errs.ExitUsage,
			"`%s` accepts at most one hub id, got %d", cmd.CommandPath(), len(args))
	}
	if len(args) == 1 && strings.TrimSpace(args[0]) == "" {
		return errs.New(errs.ExitUsage, "%s", blankHubIDMessage(cmd))
	}
	return nil
}

func blankHubIDMessage(cmd *cobra.Command) string {
	return fmt.Sprintf(
		"empty hub id argument to `%s`: a blank positional is not a request to use "+
			"--hub/current_hub — pass a real hub id, or omit the argument entirely to use "+
			"the hub in context", cmd.CommandPath())
}

func optionalArg(args []string, i int) (string, bool) {
	if i < len(args) {
		return args[i], true
	}
	return "", false
}

func injectHubDerivedState(res *client.Resource) {
	if res == nil || res.Attributes == nil {
		return
	}
	res.Attributes["registration_enabled"] = hubRegistrationEnabled(res.Attributes)
	if v, ok := res.Attributes["is_private"].(bool); ok {
		res.Attributes["published"] = !v
	}
}

func injectHubDerivedStateAll(col *client.Collection) {
	if col == nil {
		return
	}
	for i := range col.Data {
		injectHubDerivedState(&col.Data[i])
	}
}

func hubRegistrationEnabled(attrs map[string]any) bool {
	settings, ok := attrs["settings"].(map[string]any)
	if !ok {
		return false
	}
	reg, ok := settings["registration"].(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := reg["enabled"].(bool)
	return enabled
}

func printHubCreateHint(cmd *cobra.Command, res *client.Resource) {
	if res == nil {
		return
	}
	slug, _ := res.Attributes["slug"].(string)
	isPrivate, _ := res.Attributes["is_private"].(bool)
	w := cmd.ErrOrStderr()
	if isPrivate {
		fmt.Fprintf(w, "Created hub %s — private/unpublished (not reachable by members yet).\n", res.ID)
		if slug != "" {
			fmt.Fprintf(w, "  Slug: %s\n", slug)
		}
		fmt.Fprintf(w, "  Publish it with: mio hubs update %s --published\n", res.ID)
	} else {
		fmt.Fprintf(w, "Created hub %s — published.\n", res.ID)
		if slug != "" {
			fmt.Fprintf(w, "  Slug: %s\n", slug)
		}
	}
	fmt.Fprintln(w, "  Note: the public hub URL is not returned by the API and cannot be derived by the CLI; combine the slug above with your hub-frontend host.")
}

var hubsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a hub.",
	Long:  "Create a new hub for the active team.",
	Example: `  mio hubs create --name "My Community" --slug my-community
  mio hubs create --name "Support Hub" --slug support --description "Help articles"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		base := map[string]any{}
		setMappedString(cmd, base, "name", "title")
		setStringFlag(cmd, base, "slug")
		setStringFlag(cmd, base, "description")
		setMappedBoolInverted(cmd, base, "published", "is_private")
		setNullableMappedString(cmd, base, "discussions-default-title", "discussions_default_title")
		setNullableMappedString(cmd, base, "discussions-default-description", "discussions_default_description")

		branding, err := parseJSONObjectFlag(cmd, "branding-json")
		if err != nil {
			return err
		}
		navigation, err := parseJSONObjectFlag(cmd, "navigation-json")
		if err != nil {
			return err
		}
		settings, err := parseJSONObjectFlag(cmd, "settings-json")
		if err != nil {
			return err
		}
		meta, err := parseJSONObjectFlag(cmd, "meta-json")
		if err != nil {
			return err
		}

		var logo *string
		if cmd.Flags().Changed("logo-url") {
			v, ferr := cmd.Flags().GetString("logo-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--logo-url: %s", ferr)
			}
			logo = &v
		}
		var favicon *string
		if cmd.Flags().Changed("favicon-url") {
			v, ferr := cmd.Flags().GetString("favicon-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--favicon-url: %s", ferr)
			}
			favicon = &v
		}
		strictKeys, _ := cmd.Flags().GetBool("strict-keys")

		attrs, err := buildHubCreateAttrs(hubCreateParams{
			Base:       base,
			Branding:   branding,
			Navigation: navigation,
			Settings:   settings,
			Meta:       meta,
			Logo:       logo,
			Favicon:    favicon,
			Strict:     strictKeys,
		}, cmd.ErrOrStderr())
		if err != nil {
			return err
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Create(c.ctx, hubsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		injectHubDerivedState(res)
		if rerr := c.render(cmd, res); rerr != nil {
			return rerr
		}
		if c.out.Format == output.FormatTable {
			printHubCreateHint(cmd, res)
		}
		return nil
	},
}

type hubCreateParams struct {
	Base map[string]any

	Branding   map[string]any
	Navigation map[string]any
	Settings   map[string]any
	Meta       map[string]any

	Logo    *string
	Favicon *string

	Strict bool
}

func buildHubCreateAttrs(p hubCreateParams, warnW io.Writer) (map[string]any, error) {
	attrs := make(map[string]any, len(p.Base)+4)
	for k, v := range p.Base {
		attrs[k] = v
	}
	if p.Branding != nil {
		attrs["branding"] = p.Branding
	}
	if p.Navigation != nil {
		attrs["navigation"] = p.Navigation
	}
	if p.Settings != nil {
		attrs["settings"] = p.Settings
	}
	if p.Meta != nil {
		attrs["meta"] = p.Meta
	}

	if nav, ok := attrs["navigation"].(map[string]any); ok {
		if err := validateNavigationBlob(nav); err != nil {
			return nil, err
		}
		slug, _ := attrs["slug"].(string)
		if err := validateNavigationHrefs(nav, slug); err != nil {
			return nil, err
		}
	}

	if p.Logo != nil || p.Favicon != nil {
		branding := attrMap(attrs["branding"])
		if p.Logo != nil {
			branding["logo_url"] = *p.Logo
		}
		if p.Favicon != nil {
			branding["favicon_url"] = *p.Favicon
		}
		attrs["branding"] = branding
	}

	if b, ok := attrs["branding"].(map[string]any); ok {
		if err := validateBlobKeys(warnW, brandingKeyCheck, b, p.Strict); err != nil {
			return nil, err
		}
	}
	if s, ok := attrs["settings"].(map[string]any); ok {
		if err := validateBlobKeys(warnW, settingsKeyCheck, s, p.Strict); err != nil {
			return nil, err
		}
	}
	if m, ok := attrs["meta"].(map[string]any); ok {
		if err := validateBlobKeys(warnW, metaKeyCheck, m, p.Strict); err != nil {
			return nil, err
		}
	}

	if len(attrs) == 0 {
		return nil, errs.New(errs.ExitUsage, "nothing to create: set at least --name")
	}

	return attrs, nil
}

var hubsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List hubs.",
	Long:    "List all hubs for the active team.",
	Example: `  mio hubs list`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, hubsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		injectHubDerivedStateAll(col)
		return c.render(cmd, col)
	},
}

var hubsRetrieveCmd = &cobra.Command{
	Use:   "retrieve [id]",
	Short: "Retrieve a hub by id.",
	Long: `Retrieve a single hub.

The hub id may be given positionally; when it is omitted the hub is taken from
the ambient context (--hub, or current_hub in config), like every other
hub-scoped command.`,
	Example: `  mio hubs retrieve hub_abc123
  mio hubs retrieve --hub hub_abc123
  mio hubs retrieve                      # uses current_hub from config`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, hubsPath(teamID, hubID))
		if err != nil {
			return err
		}
		injectHubDerivedState(res)
		return c.render(cmd, res)
	},
}

var hubsUpdateCmd = &cobra.Command{
	Use:   "update [id]",
	Short: "Update a hub by id.",
	Long: `Update one or more fields on a hub. Only the flags you provide are changed (partial update).

The hub id may be given positionally; when it is omitted the hub is taken from
the ambient context (--hub, or current_hub in config).`,
	Example: `  mio hubs update hub_abc123 --name "New Name"
  mio hubs update hub_abc123 --published=true
  mio hubs update hub_abc123 --homepage content_index
  mio hubs update hub_abc123 --homepage custom --homepage-page-id <page_id>
  mio hubs update --hub hub_abc123 --name "New Name"`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		attrs := map[string]any{}
		setMappedString(cmd, attrs, "name", "title")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		if cmd.Flags().Changed("published") {
			if published, perr := cmd.Flags().GetBool("published"); perr == nil {
				for k, v := range publishedStateAttrs(published) {
					attrs[k] = v
				}
			}
		}
		setNullableMappedString(cmd, attrs, "discussions-default-title", "discussions_default_title")
		setNullableMappedString(cmd, attrs, "discussions-default-description", "discussions_default_description")
		homepage, err := hubHomepageFromFlags(cmd)
		if err != nil {
			return err
		}
		if homepage != nil {
			attrs["homepage"] = homepage
		}

		nav, err := parseJSONObjectFlag(cmd, "navigation-json")
		if err != nil {
			return err
		}
		navSlug, navSlugKnown := attrs["slug"].(string)
		if nav != nil {
			if err := validateNavigationBlob(nav); err != nil {
				return err
			}
		}

		branding, err := parseJSONObjectFlag(cmd, "branding-json")
		if err != nil {
			return err
		}
		settings, err := parseJSONObjectFlag(cmd, "settings-json")
		if err != nil {
			return err
		}
		meta, err := parseJSONObjectFlag(cmd, "meta-json")
		if err != nil {
			return err
		}
		var logo *string
		if cmd.Flags().Changed("logo-url") {
			v, ferr := cmd.Flags().GetString("logo-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--logo-url: %s", ferr)
			}
			logo = &v
		}
		var favicon *string
		if cmd.Flags().Changed("favicon-url") {
			v, ferr := cmd.Flags().GetString("favicon-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--favicon-url: %s", ferr)
			}
			favicon = &v
		}
		var registration *bool
		if cmd.Flags().Changed("registration-enabled") {
			v, ferr := cmd.Flags().GetBool("registration-enabled")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--registration-enabled: %s", ferr)
			}
			registration = &v
		}
		unsetPaths, err := parseUnsetFlag(cmd)
		if err != nil {
			return err
		}
		strictKeys, _ := cmd.Flags().GetBool("strict-keys")

		if err := checkPoliciesOnUpdate(cmd.ErrOrStderr(), settings, unsetPaths, strictKeys); err != nil {
			return err
		}

		rmw := branding != nil || settings != nil || meta != nil || logo != nil ||
			favicon != nil || registration != nil || len(unsetPaths) > 0

		if len(attrs) == 0 && !rmw && nav == nil {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		res, err := applyHubBlobs(c.ctx, c.client, teamID, hubID, navSlug, blobPatches{
			Base:         attrs,
			Branding:     branding,
			Settings:     settings,
			Meta:         meta,
			Navigation:   nav,
			SlugKnown:    navSlugKnown,
			Logo:         logo,
			Favicon:      favicon,
			Registration: registration,
			Unset:        unsetPaths,
			Strict:       strictKeys,
		}, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		injectHubDerivedState(res)
		return c.render(cmd, res)
	},
}

func hubHomepageFromFlags(cmd *cobra.Command) (map[string]any, error) {
	kindSet := cmd.Flags().Changed("homepage")
	pageIDSet := cmd.Flags().Changed("homepage-page-id")
	if !kindSet && !pageIDSet {
		return nil, nil
	}
	if !kindSet {
		return nil, errs.New(errs.ExitUsage,
			"--homepage-page-id needs --homepage custom: it names the page for a custom homepage")
	}
	kind, err := cmd.Flags().GetString("homepage")
	if err != nil {
		return nil, errs.New(errs.ExitUsage, "--homepage: %s", err)
	}
	homepage := map[string]any{"kind": kind}
	switch {
	case kind == "custom" && !pageIDSet:
		return nil, errs.New(errs.ExitUsage,
			"--homepage custom needs --homepage-page-id <uuid>: the page to show as the hub's homepage")
	case kind != "custom" && pageIDSet:
		return nil, errs.New(errs.ExitUsage,
			"--homepage-page-id only goes with --homepage custom; --homepage %s takes no page id", kind)
	case kind == "custom":
		pageID, err := cmd.Flags().GetString("homepage-page-id")
		if err != nil {
			return nil, errs.New(errs.ExitUsage, "--homepage-page-id: %s", err)
		}
		homepage["page_id"] = pageID
	}
	return homepage, nil
}

func publishedStateAttrs(published bool) map[string]any {
	return map[string]any{"is_private": !published}
}

type blobPatches struct {
	Base map[string]any

	Branding map[string]any
	Settings map[string]any
	Meta     map[string]any

	Navigation map[string]any

	SlugKnown bool

	Logo         *string
	Favicon      *string
	Registration *bool

	Unset []unsetPath

	Strict bool

	Current *client.Resource

	NavHrefsChecked bool
}

func applyHubBlobs(ctx context.Context, cl *client.Client, teamID, hubID, hubSlug string, p blobPatches, warnW io.Writer) (*client.Resource, error) {
	navSet := p.Navigation != nil

	if navSet && p.SlugKnown && !p.NavHrefsChecked {
		if err := validateNavigationHrefs(p.Navigation, hubSlug); err != nil {
			return nil, err
		}
	}

	if err := validateBlobKeys(warnW, brandingKeyCheck, p.Branding, p.Strict); err != nil {
		return nil, err
	}
	if err := validateBlobKeys(warnW, settingsKeyCheck, p.Settings, p.Strict); err != nil {
		return nil, err
	}
	if err := validateBlobKeys(warnW, metaKeyCheck, p.Meta, p.Strict); err != nil {
		return nil, err
	}

	attrs := make(map[string]any, len(p.Base)+1)
	for k, v := range p.Base {
		attrs[k] = v
	}
	if navSet {
		attrs["navigation"] = p.Navigation
	}

	rmw := p.Branding != nil || p.Settings != nil || p.Meta != nil ||
		p.Logo != nil || p.Favicon != nil || p.Registration != nil || len(p.Unset) > 0
	navNeedsRetrieve := navSet && !p.SlugKnown && !p.NavHrefsChecked

	if rmw || navNeedsRetrieve {
		cur := p.Current
		if cur == nil {
			var err error
			if cur, err = cl.Retrieve(ctx, hubsPath(teamID, hubID)); err != nil {
				return nil, err
			}
		}
		if navNeedsRetrieve {
			slug, _ := cur.Attributes["slug"].(string)
			if err := validateNavigationHrefs(p.Navigation, slug); err != nil {
				return nil, err
			}
		}
		if p.Branding != nil || p.Logo != nil || p.Favicon != nil {
			b := attrMap(cur.Attributes["branding"])
			if p.Branding != nil {
				b = deepMergeMap(b, p.Branding)
			}
			if p.Logo != nil {
				b["logo_url"] = *p.Logo
			}
			if p.Favicon != nil {
				b["favicon_url"] = *p.Favicon
			}
			attrs["branding"] = b
		}
		if p.Settings != nil || p.Registration != nil {
			s := attrMap(cur.Attributes["settings"])
			if p.Settings != nil {
				s = deepMergeMap(s, p.Settings)
			}
			if p.Registration != nil {
				reg := attrMap(s["registration"])
				reg["enabled"] = *p.Registration
				s["registration"] = reg
			}
			attrs["settings"] = s
		}
		if p.Meta != nil {
			attrs["meta"] = deepMergeMap(attrMap(cur.Attributes["meta"]), p.Meta)
		}
		for _, u := range p.Unset {
			blob, ok := attrs[u.blob].(map[string]any)
			if !ok {
				blob = attrMap(cur.Attributes[u.blob])
			}
			attrs[u.blob] = deleteAtPath(blob, u.segments)
		}
	}

	return cl.Update(ctx, hubsPath(teamID, hubID), attrs)
}

var hubsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a hub by id.",
	Long: `Permanently delete a hub. This action cannot be undone. Pass --yes to skip the confirmation prompt.

Unlike the other hubs verbs this one does NOT fall back to --hub/current_hub:
the hub id must be given positionally.`,
	Example: `  mio hubs delete hub_abc123
  mio hubs delete hub_abc123 --yes`,
	Args: func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 1 && strings.TrimSpace(args[0]) != "":
			return nil
		case len(args) == 1:
			return errs.New(errs.ExitUsage, "%s", blankHubIDMessage(cmd))
		case len(args) == 0:
			return errs.New(errs.ExitUsage,
				"hubs delete requires the hub id positionally: `mio hubs delete <id>`. "+
					"It does NOT fall back to --hub/current_hub the way the other hubs verbs do — "+
					"deleting a hub is irreversible, so the target must be named explicitly")
		default:
			return errs.New(errs.ExitUsage,
				"hubs delete takes exactly one hub id, got %d", len(args))
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete hub %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, hubsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted hub %s.\n", args[0])
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{hubsCreateCmd, hubsUpdateCmd} {
		cmd.Flags().String("name", "", "Hub display name.")
		cmd.Flags().String("slug", "", "Hub URL slug (unique within the team).")
		cmd.Flags().String("description", "", "Short description of the hub.")
		cmd.Flags().String("logo-url", "", "URL of the hub's logo image (branding.logo_url).")
		cmd.Flags().String("favicon-url", "", "URL of the hub's favicon image (branding.favicon_url). Read-modify-write on update; sibling branding keys are preserved.")
		cmd.Flags().Bool("published", false, "Whether the hub is publicly published.")
		cmd.Flags().String("discussions-default-title", "", "Default title for the hub's discussions surface (MIO-2274). Pass \"\" to clear.")
		cmd.Flags().String("discussions-default-description", "", "Default description for the hub's discussions surface. Pass \"\" to clear.")
		strictHelp := strictKeysHelpText
		if cmd == hubsUpdateCmd {
			strictHelp = strictKeysUpdateHelpText
		}
		cmd.Flags().Bool("strict-keys", false, strictHelp)
	}

	for _, f := range []struct{ name, desc string }{
		{"branding-json", "Hub branding as a JSON object — colors, fonts, logo. Inline JSON or @file. Accepted keys: " + brandingKeysHelp + ". Unknown keys warn (error with --strict-keys)."},
		{"navigation-json", "Hub navigation as a JSON object — header/footer menu items. Inline JSON or @file."},
		{"settings-json", "Hub settings as a JSON object — header/footer chrome, appearance, policies. Inline JSON or @file. " + settingsKeysHelpText},
		{"meta-json", "Hub meta as a JSON object — feature guards. Inline JSON or @file. Accepted keys: " + metaKeysHelp + ". Unknown keys warn (error with --strict-keys)."},
	} {
		hubsCreateCmd.Flags().String(f.name, "", f.desc)
	}

	hubsUpdateCmd.Flags().String("navigation-json",
		"", "Hub navigation as a JSON object — header/footer menu items (each needs a \"type\"). Inline JSON or @file. Replaces the hub's navigation. For item-by-item edits use 'mio hubs navigation add/remove/reorder'.")
	hubsUpdateCmd.Flags().String("branding-json",
		"", "Hub branding keys to merge (read-modify-write) as a JSON object. Inline JSON or @file. Accepted keys: "+brandingKeysHelp+". Unknown keys warn (error with --strict-keys).")
	hubsUpdateCmd.Flags().String("settings-json",
		"", "Hub settings keys to merge (read-modify-write) as a JSON object. Inline JSON or @file. "+settingsKeysUpdateHelpText)
	hubsUpdateCmd.Flags().String("meta-json",
		"", "Hub meta keys to merge (read-modify-write) as a JSON object. Inline JSON or @file. Accepted keys: "+metaKeysHelp+". Unknown keys warn (error with --strict-keys).")

	hubsUpdateCmd.Flags().String("homepage", "", "Set the hub's homepage: one of custom, discussions_index, content_index. custom needs --homepage-page-id; the API validates the kind.")
	hubsUpdateCmd.Flags().String("homepage-page-id", "", "Page id (uuid) to use as the homepage with --homepage custom.")

	hubsUpdateCmd.Flags().Bool("registration-enabled", false, "Enable (--registration-enabled) or disable (--registration-enabled=false) member self-registration (settings.registration.enabled). Read-modify-write: sibling settings/registration keys are preserved.")

	hubsUpdateCmd.Flags().StringArray("unset", nil, "Delete a key from a hub blob by dotted path, e.g. --unset settings.registration.enabled (the first segment selects the blob: branding|settings|meta). Repeatable and comma-separated. Read-modify-write; applied AFTER --*-json merges and scalar flags.")

	addPaginationFlags(hubsListCmd)
}

var hubsPoliciesCmd = &cobra.Command{
	Use:   "policies",
	Short: "Manage hub legal policies.",
	Long:  "Read, create or update legal policies (Terms of Service, Privacy Policy) for a hub, and toggle the hub-level enforcement gate.",
}

func hubsPoliciesPath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/policies", teamID, hubID)
}

var validPolicyTypes = map[string]bool{
	"tos":            true,
	"privacy_policy": true,
}

var hubsPoliciesUpdateCmd = &cobra.Command{
	Use:   "update [hub_id]",
	Short: "Update (or create) a hub legal policy.",
	Long: `Create or replace a hub legal policy (Terms of Service or Privacy Policy).

The hub identifier may be given positionally so you can target any hub
regardless of the active context; omit it to use the ambient hub (--hub, or
current_hub in config).

Policy content may be supplied inline or read from a file by prefixing the path
with '@':  --content @policy.md

Exactly one of --content or --reset-content must be provided:
  --content        Supply the policy body (inline string or @file).
  --reset-content  Revert the policy to the backend default (sends content: null).`,
	Example: `  mio hubs policies update hub_abc123 --policy-type tos --content "# Terms of Service\n…"
  mio hubs policies update hub_abc123 --policy-type tos --content @tos.md --require-acceptance
  mio hubs policies update hub_abc123 --policy-type privacy_policy --content @privacy.md
  mio hubs policies update hub_abc123 --policy-type tos --reset-content
  mio hubs policies update --hub hub_abc123 --policy-type tos --content @tos.md`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		policyType, ferr := cmd.Flags().GetString("policy-type")
		if ferr != nil {
			return errs.New(errs.ExitUsage, "--policy-type: %s", ferr.Error())
		}
		if !validPolicyTypes[policyType] {
			return errs.New(errs.ExitUsage,
				"--policy-type %q is not valid: must be one of tos, privacy_policy", policyType)
		}

		contentChanged := cmd.Flags().Changed("content")
		resetChanged := cmd.Flags().Changed("reset-content")

		if contentChanged && resetChanged {
			return errs.New(errs.ExitUsage, "--content and --reset-content are mutually exclusive: provide exactly one")
		}
		if !contentChanged && !resetChanged {
			return errs.New(errs.ExitUsage, "provide exactly one of --content or --reset-content")
		}

		policy := hubPolicy{PolicyType: policyType}

		if !resetChanged {
			rawContent, ferr := cmd.Flags().GetString("content")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--content: %s", ferr.Error())
			}

			content := rawContent
			if after, ok := strings.CutPrefix(rawContent, "@"); ok {
				b, rerr := os.ReadFile(after)
				if rerr != nil {
					return errs.New(errs.ExitGeneric, "read %s: %s", after, rerr.Error())
				}
				content = string(b)
			}
			policy.Content = &content
		}

		if cmd.Flags().Changed("require-acceptance") {
			if ra, rerr := cmd.Flags().GetBool("require-acceptance"); rerr == nil {
				policy.RequireAcceptance = &ra
			}
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		res, err := applyHubPolicies(c.ctx, c.client, teamID, hubID, policy)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

type hubPolicy struct {
	PolicyType string

	Content *string

	RequireAcceptance *bool
}

func applyHubPolicies(ctx context.Context, cl *client.Client, teamID, hubID string, p hubPolicy) (*client.Resource, error) {
	attrs := map[string]any{
		"policy_type": p.PolicyType,
	}
	if p.Content != nil {
		attrs["content"] = *p.Content
	} else {
		attrs["content"] = nil
	}
	if p.RequireAcceptance != nil {
		attrs["require_acceptance"] = *p.RequireAcceptance
	}
	return cl.Update(ctx, hubsPoliciesPath(teamID, hubID), attrs)
}

func init() {
	hubsPoliciesUpdateCmd.Flags().String("policy-type", "", "Policy type: tos or privacy_policy. Required.")
	_ = hubsPoliciesUpdateCmd.MarkFlagRequired("policy-type")

	hubsPoliciesUpdateCmd.Flags().String("content", "", "Policy body in Markdown. Prefix with '@' to read from a file (e.g. @tos.md). Mutually exclusive with --reset-content.")
	hubsPoliciesUpdateCmd.Flags().Bool("reset-content", false, "Revert the policy to the backend default (sends content: null). Mutually exclusive with --content.")

	hubsPoliciesUpdateCmd.Flags().Bool("require-acceptance", false, "Require hub members to accept the policy before accessing content (TOS only).")
}

func hubsPoliciesGatePath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/policies/gate", teamID, hubID)
}

var hubsPoliciesGetCmd = &cobra.Command{
	Use:   "get [hub_id]",
	Short: "Read a hub's legal policies and the enforcement gate.",
	Long: `Read both policy documents (Terms of Service, Privacy Policy) and the
hub-level enforcement gate, as ACTUALLY STORED.

This is the team-owner admin read (MIO-2394), not the member-portal one: the
portal read serves defaults and forces enabled=true for public display, so this
is the only way to answer "is enforcement really on for this hub?".

Always returns exactly TWO items — tos and privacy_policy — even for a hub that
has never configured either; an unconfigured document is served with rendered
default text. There is no empty case to handle.

` + "`enabled`" + ` is the ONE hub-level gate repeated on every item, not a per-policy
flag: the backend stores a single boolean (see ` + "`hubs policies gate`" + `).

CAN THIS TELL A CUSTOM DOCUMENT FROM THE PLATFORM DEFAULT? Not by itself, and
the fields mislead in ways worth knowing before you rely on them:

  version   NOT a discriminator. The backend assigns a version ONLY for tos
            AND ONLY when require_acceptance is set. A hand-written privacy
            policy, or a tos saved without --require-acceptance, stores no
            version — and the read projects an absent version as "default-v1".
            So custom text routinely reads as "default-v1".
  content   Always populated: your stored text if any, otherwise the rendered
            platform default. Its presence proves nothing; reading it does.
  require_acceptance
            Tri-state. true/false mean a real edit was recorded. null means
            UNKNOWN — never written, or written before that value began being
            persisted (MIO-2523). So non-null proves an edit happened; null
            does not prove one did not.

In practice: READ THE CONTENT AND LOOK AT IT. (` + "`hubs scaffold --hub`" + ` does not
rely on this read: it reads the RAW stored settings.policies off the hub, where
an unconfigured document has no content, and keeps any text the hub has
(MIO-2818). Only ` + "`--reapply-template`" + ` still resets a policy its template
declares no text for to the platform default. When the template's tos requires
acceptance, that reset moves the version to "default-v1" and re-prompts every
member who had accepted ONLY if this read showed another version first — one
set by saving custom text with --require-acceptance, and kept by any later save
or reset without the flag. At "default-v1" nobody who had accepted is asked
again — but a member who never accepted is prompted whenever the gate is on,
whatever the version.)

The hub identifier may be given positionally; omit it to use the ambient hub
(--hub, or current_hub in config).

Output is a LIST, so index it: ` + "`-o plain --jq '.[0].enabled'`" + `. A bare
` + "`--jq .enabled`" + ` does not yield null — it EXITS 1 with
"expected an object but got: array".`,
	Example: `  mio hubs policies get hub_abc123
  mio hubs policies get --hub hub_abc123
  mio hubs policies get hub_abc123 -o plain --jq '.[0].enabled'
  mio hubs policies get hub_abc123 --jq '[.[] | {policy_type, version, require_acceptance}]'
  mio hubs policies get hub_abc123 -o plain --jq '.[] | select(.policy_type=="tos") | .content'`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		res, err := c.client.List(c.ctx, hubsPoliciesPath(teamID, hubID), nil)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var hubsPoliciesGateCmd = &cobra.Command{
	Use:   "gate [hub_id]",
	Short: "Toggle the hub policy enforcement gate.",
	Long: `Enable or disable hub-level policy enforcement (settings.policies.enabled).

This only flips the enforcement gate; it does not change policy content, the
TOS version, or member acceptance state. The hub identifier may be given
positionally; omit it to use the ambient hub (--hub, or current_hub in config).

--enabled is required and must be given explicitly:
  --enabled           turn enforcement ON
  --enabled=false     turn enforcement OFF`,
	Example: `  mio hubs policies gate hub_abc123 --enabled
  mio hubs policies gate hub_abc123 --enabled=false
  mio hubs policies gate --hub hub_abc123 --enabled`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("enabled") {
			return errs.New(errs.ExitUsage, "missing required flag: --enabled (use --enabled or --enabled=false)")
		}
		enabled, ferr := cmd.Flags().GetBool("enabled")
		if ferr != nil {
			return errs.New(errs.ExitUsage, "--enabled: %s", ferr.Error())
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		res, err := applyHubPolicyGate(c.ctx, c.client, teamID, hubID, enabled)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Set policy gate on hub %s to enabled=%t.\n", hubID, enabled)
			return nil
		}
		return c.render(cmd, res)
	},
}

func applyHubPolicyGate(ctx context.Context, cl *client.Client, teamID, hubID string, enabled bool) (*client.Resource, error) {
	return cl.Action(ctx, "PATCH", hubsPoliciesGatePath(teamID, hubID), map[string]any{"enabled": enabled})
}

func init() {
	hubsPoliciesGateCmd.Flags().Bool("enabled", false, "Enable (--enabled) or disable (--enabled=false) policy enforcement. Required.")
}

var hubsRedirectOriginsCmd = &cobra.Command{
	Use:   "redirect-origins",
	Short: "Manage the magic-link redirect-origin allowlist.",
	Long:  "Read or full-replace the magic-link redirect-origin allowlist for a hub (owner-only, MIO-616).",
}

func hubsRedirectOriginsPath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/redirect-origins", teamID, hubID)
}

var hubsRedirectOriginsGetCmd = &cobra.Command{
	Use:   "get [hub_id]",
	Short: "Read the redirect-origin allowlist for a hub.",
	Long: `Return the current magic-link redirect-origin allowlist for a hub (may be empty). Owner-only.

The hub identifier may be given positionally; omit it to use the ambient hub
(--hub, or current_hub in config).`,
	Example: `  mio hubs redirect-origins get hub_abc123
  mio hubs redirect-origins get --hub hub_abc123`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, hubsRedirectOriginsPath(teamID, hubID))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var hubsRedirectOriginsSetCmd = &cobra.Command{
	Use:   "set [hub_id]",
	Short: "Full-replace the redirect-origin allowlist for a hub.",
	Long: `Full-replace the magic-link redirect-origin allowlist for a hub (owner-only).

This is an atomic replacement: the supplied origins REPLACE the entire stored
list. Every entry is validated at write time (scheme, host, IPv6 brackets, no
wildcards); an invalid entry returns a 422 with a per-index error pointer and
nothing is persisted.

Provide the new list as a comma-separated --origins value, or pass --clear to
empty the allowlist (which rejects all magic-link redirects at runtime).
Exactly one of --origins or --clear is required.

The hub identifier may be given positionally; omit it to use the ambient hub
(--hub, or current_hub in config).`,
	Example: `  mio hubs redirect-origins set hub_abc123 --origins "https://app.example.com,https://portal.example.com"
  mio hubs redirect-origins set hub_abc123 --clear
  mio hubs redirect-origins set --hub hub_abc123 --clear`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		originsChanged := cmd.Flags().Changed("origins")
		clearChanged := cmd.Flags().Changed("clear")
		if originsChanged && clearChanged {
			return errs.New(errs.ExitUsage, "--origins and --clear are mutually exclusive: provide exactly one")
		}
		if !originsChanged && !clearChanged {
			return errs.New(errs.ExitUsage, "provide the allowlist with --origins or --clear")
		}

		origins := []string{}
		if originsChanged {
			raw, ferr := cmd.Flags().GetString("origins")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--origins: %s", ferr.Error())
			}
			for _, part := range strings.Split(raw, ",") {
				if o := strings.TrimSpace(part); o != "" {
					origins = append(origins, o)
				}
			}
			if len(origins) == 0 {
				return errs.New(errs.ExitUsage, "--origins is empty: pass at least one origin, or use --clear to empty the allowlist")
			}
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		res, err := c.client.Action(c.ctx, "PUT",
			hubsRedirectOriginsPath(teamID, hubID), map[string]any{"origins": origins})
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Replaced redirect-origin allowlist for hub %s (%d origin(s)).\n", hubID, len(origins))
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	hubsRedirectOriginsSetCmd.Flags().String("origins", "", "Comma-separated list of allowed redirect origins. Full-replaces the stored list.")
	hubsRedirectOriginsSetCmd.Flags().Bool("clear", false, "Clear the allowlist (send an empty list). Mutually exclusive with --origins.")
}

var hubsEmailSettingsCmd = &cobra.Command{
	Use:   "email-settings",
	Short: "Manage per-hub email sender identity.",
	Long:  "Get or update the per-hub email sender identity (from_name, reply_to) for a hub (MIO-1229).",
}

func hubsEmailSettingsPath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/email-settings", teamID, hubID)
}

var hubsEmailSettingsGetCmd = &cobra.Command{
	Use:   "get [hub_id]",
	Short: "Get the per-hub email sender identity.",
	Long: `Retrieve the email sender identity (from_name, reply_to) for a hub.

Per-hub sender settings override the team-level defaults for all emails
sent from that hub (MIO-1229).

The hub identifier may be given positionally; omit it to use the ambient hub
(--hub, or current_hub in config).`,
	Example: `  mio hubs email-settings get hub_abc123
  mio hubs email-settings get --hub hub_abc123`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, hubsEmailSettingsPath(teamID, hubID))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var hubsEmailSettingsUpdateCmd = &cobra.Command{
	Use:   "update [hub_id]",
	Short: "Update the per-hub email sender identity.",
	Long: `Update the email sender identity (from_name, reply_to) for a hub.

Only the flags you provide are changed (partial update). Pass an empty
string to explicitly clear a field (e.g. --reply-to="" clears the reply-to
address). Merges into the hub's settings.email; other settings keys are
preserved (MIO-1229).

The hub identifier may be given positionally; omit it to use the ambient hub
(--hub, or current_hub in config).`,
	Example: `  mio hubs email-settings update hub_abc123 --from-name "My Community"
  mio hubs email-settings update hub_abc123 --from-name "Support" --reply-to support@example.com
  mio hubs email-settings update hub_abc123 --reply-to ""
  mio hubs email-settings update --hub hub_abc123 --from-name "My Community"`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setNullableMappedString(cmd, attrs, "from-name", "from_name")
		setNullableMappedString(cmd, attrs, "reply-to", "reply_to")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least --from-name or --reply-to")
		}

		res, err := c.client.Update(c.ctx, hubsEmailSettingsPath(teamID, hubID), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	hubsEmailSettingsUpdateCmd.Flags().String("from-name", "", "Sender display name override for this hub (from_name).")
	hubsEmailSettingsUpdateCmd.Flags().String("reply-to", "", "Reply-to email address override for this hub. Pass an empty string to clear.")
}
