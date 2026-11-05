package cmd

// apps.go implements the `mio apps` command group: embedded app records and
// their installs (MIO-6609, epic MIO-6602).
//
// Routes (team-owner surface; see mio-backend app/embedded_apps):
//
//	apps create          POST   /api/teams/{team_id}/embedded-apps
//	apps list            GET    /api/teams/{team_id}/embedded-apps
//	apps retrieve        GET    /api/teams/{team_id}/embedded-apps/{app_id}
//	apps update          PATCH  /api/teams/{team_id}/embedded-apps/{app_id}
//	apps delete          DELETE /api/teams/{team_id}/embedded-apps/{app_id}
//	apps secrets update  POST   /api/teams/{team_id}/embedded-apps/{app_id}/rotate-secret
//	apps installs create|list|retrieve|update|delete
//	                            …/embedded-apps/{app_id}/installs[/{install_id}]
//
// JSON:API resource types: "embedded_apps", "embedded_app_installs". Bodies are
// sent flat (the backend accepts a flat object or an envelope).
//
// Secret rotation is not a fixed-verb action, so it is the `secrets` sub-resource
// with the `update` action. client_secret and webhook_signing_secret come back
// only on create and rotation: they are printed once, with a warning on stderr,
// and never stored by the CLI.

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// Closed sets, mirrored from mio-backend app/embedded_apps/types.py.
var (
	appInstallPolicyTypes = []string{"everyone", "product"}
	appSecretKinds        = []string{"client", "webhook"}
)

func init() {
	appsCmd.AddCommand(
		appsCreateCmd,
		appsListCmd,
		appsRetrieveCmd,
		appsUpdateCmd,
		appsDeleteCmd,
		appsSecretsCmd,
		appsInstallsCmd,
	)
	appsSecretsCmd.AddCommand(appsSecretsUpdateCmd)
	appsInstallsCmd.AddCommand(
		appsInstallsCreateCmd,
		appsInstallsListCmd,
		appsInstallsRetrieveCmd,
		appsInstallsUpdateCmd,
		appsInstallsDeleteCmd,
	)
	rootCmd.AddCommand(appsCmd)
}

// ── path helpers ──────────────────────────────────────────────────────────────

// appsPath returns /api/teams/{team_id}/embedded-apps[/{id}].
func appsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/embedded-apps", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// appInstallsPath returns …/embedded-apps/{app_id}/installs[/{install_id}].
func appInstallsPath(teamID, appID, id string) string {
	base := appsPath(teamID, appID) + "/installs"
	if id != "" {
		return base + "/" + id
	}
	return base
}

// appsTeamContext is the shared preamble: context, auth, team.
func appsTeamContext(cmd *cobra.Command) (*cmdContext, string, error) {
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

// appIDPattern is the id shape the backend accepts (IdentifierStr: 1-36 chars),
// narrowed to characters that are safe in a URL path segment, so an id can never
// add a path segment, a query or a fragment.
var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,35}$`)

// checkAppIDs rejects a malformed id before any request is built.
func checkAppIDs(kind string, ids ...string) error {
	for _, id := range ids {
		if !appIDPattern.MatchString(id) {
			return errs.New(errs.ExitUsage, "%s id %q is not valid: use 1-36 letters, digits, '_' or '-'", kind, id)
		}
	}
	return nil
}

// checkChoice rejects a value outside a closed set with a usage error.
func checkChoice(flag, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return errs.New(errs.ExitUsage, "--%s must be one of: %s (got %q)", flag, strings.Join(allowed, ", "), value)
}

// warnSecretsShownOnce prints the store-it-now warning on stderr when the
// response carries a plaintext secret. Nothing is written anywhere else.
func warnSecretsShownOnce(cmd *cobra.Command, res *client.Resource) {
	if res == nil {
		return
	}
	var shown []string
	for _, k := range []string{"client_secret", "webhook_signing_secret"} {
		if v, ok := res.Attributes[k].(string); ok && v != "" {
			shown = append(shown, k)
		}
	}
	if len(shown) == 0 {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"warning: %s shown once, now. mio does not store it and the API will not show it again; copy it to your secret store. Lost? Run `mio apps secrets update <app_id> --secret <client|webhook>`.\n",
		strings.Join(shown, " and "))
}

// ── group ─────────────────────────────────────────────────────────────────────

var appsCmd = &cobra.Command{
	Use:   "apps",
	Short: "Manage embedded apps for the active team.",
	Long: `Register, list, update and delete embedded apps, and manage where they are installed.

An embedded app is a third-party page that opens inside a hub. Each app belongs
to one hub and is installed there with 'mio apps installs create'. All commands
need the team owner, signed in with a user access token (a team API key is
refused with 403, exit 3).

The client secret and webhook signing secret are shown once, on create and when
rotated. Copy them then.`,
}

// ── create ────────────────────────────────────────────────────────────────────

var appsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Register an embedded app.",
	Long: `Register an embedded app for a hub (--hub, or the hub in context).

--launch-url must be https, and its origin must be one of the --allowed-origin
values (repeatable, 1-5 exact https origins, no path). --webhook-url is
optional; when set, a webhook signing secret is minted too.

client_secret (and webhook_signing_secret) appear only in this response.`,
	Example: `  mio apps create --hub hub_123 --name "Progress Board" \
    --launch-url https://apps.example.com/board \
    --allowed-origin https://apps.example.com \
    --redirect-uri https://apps.example.com/callback

  mio apps create --hub hub_123 --name "Notifier" --launch-url https://n.example.com/ \
    --allowed-origin https://n.example.com --webhook-url https://n.example.com/hook`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		hubID, err := c.requireHub()
		if err != nil {
			return err
		}
		attrs := map[string]any{"hub_id": hubID}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "launch-url")
		setStringFlag(cmd, attrs, "webhook-url")
		origins, _ := cmd.Flags().GetStringArray("allowed-origin")
		attrs["allowed_origins"] = origins
		if uris, _ := cmd.Flags().GetStringArray("redirect-uri"); len(uris) > 0 {
			attrs["redirect_uris"] = uris
		}
		res, err := c.client.CreateWith(c.ctx, client.StyleFlat, appsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		if err := c.render(cmd, res); err != nil {
			return err
		}
		warnSecretsShownOnce(cmd, res)
		return nil
	},
}

// ── list ──────────────────────────────────────────────────────────────────────

var appsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List embedded apps for the active team.",
	Long:  "List the team's embedded apps. Secrets are never included.",
	Example: `  mio apps list
  mio apps list --limit=20
  mio apps list --after=<cursor>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		query := url.Values{}
		addPageFlags(cmd, query)
		col, err := c.client.List(c.ctx, appsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ── retrieve ──────────────────────────────────────────────────────────────────

var appsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <app_id>",
	Short:   "Retrieve an embedded app by id.",
	Long:    "Retrieve one embedded app. Secrets are never included.",
	Example: `  mio apps retrieve app_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, appsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── update ────────────────────────────────────────────────────────────────────

var appsUpdateCmd = &cobra.Command{
	Use:   "update <app_id>",
	Short: "Update an embedded app.",
	Long: `Change an app's name, launch URL, allowed origins or webhook URL. Only the
flags you pass are sent, and at least one is required. --allowed-origin
replaces the whole list. --webhook-url "" removes the webhook and its secret.
The hub cannot be changed.`,
	Example: `  mio apps update app_abc123 --name "Progress Board v2"
  mio apps update app_abc123 --launch-url https://apps.example.com/v2 --allowed-origin https://apps.example.com
  mio apps update app_abc123 --webhook-url ""`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		if stringArg(args[0]) == "" {
			return errs.New(errs.ExitUsage, "app id is blank (an unset variable?)")
		}
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "launch-url")
		setNullableMappedString(cmd, attrs, "webhook-url", "webhook_url")
		if cmd.Flags().Changed("allowed-origin") {
			origins, _ := cmd.Flags().GetStringArray("allowed-origin")
			attrs["allowed_origins"] = origins
		}
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: pass --name, --launch-url, --allowed-origin and/or --webhook-url")
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.UpdateWith(c.ctx, client.StyleFlat, appsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── delete ────────────────────────────────────────────────────────────────────

var appsDeleteCmd = &cobra.Command{
	Use:   "delete <app_id>",
	Short: "Delete an embedded app.",
	Long: `Delete the app, remove its OAuth client and disable its installs. Members
lose access to it at once.

This action is irreversible. Pass --yes to skip the confirmation prompt in
non-interactive environments.`,
	Example: `  mio apps delete app_abc123
  mio apps delete app_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Delete embedded app %s?", args[0])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, appsPath(teamID, args[0])); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{"id": args[0], "object": "embedded_app", "deleted": true})
	},
}

// ── secrets ───────────────────────────────────────────────────────────────────

var appsSecretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage an embedded app's secrets.",
	Long:  "Replace an app's client secret or webhook signing secret.",
}

var appsSecretsUpdateCmd = &cobra.Command{
	Use:   "update <app_id>",
	Short: "Rotate an app's client or webhook secret.",
	Long: `Replace one secret (--secret client|webhook). The old secret stops working
at once, so update the app's own configuration right after. The new secret is
printed once and not stored.

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio apps secrets update app_abc123 --secret client
  mio apps secrets update app_abc123 --secret webhook --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		kind, _ := cmd.Flags().GetString("secret")
		if err := checkChoice("secret", kind, appSecretKinds); err != nil {
			return err
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Rotate the %s secret of app %s? The old one stops working immediately.", kind, args[0])); err != nil {
			return err
		}
		path := appsPath(teamID, args[0]) + "/rotate-secret"
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, map[string]any{"secret": kind})
		if err != nil {
			return err
		}
		if err := c.render(cmd, res); err != nil {
			return err
		}
		warnSecretsShownOnce(cmd, res)
		return nil
	},
}

// ── installs ──────────────────────────────────────────────────────────────────

var appsInstallsCmd = &cobra.Command{
	Use:   "installs",
	Short: "Manage where an embedded app is installed.",
	Long: `Install an app into its hub and control who can open it.

--policy-type everyone lets every active member open it; --policy-type product
limits it to members holding --policy-product-id. --approved-scope grants the
app a scope (available: progress:read). An install starts disabled unless
--enabled is passed. Disabling it, or removing a scope, revokes credentials
already issued to the app.`,
}

func appInstallAttrs(cmd *cobra.Command) (map[string]any, error) {
	attrs := map[string]any{}
	if cmd.Flags().Changed("policy-type") {
		pt, _ := cmd.Flags().GetString("policy-type")
		if err := checkChoice("policy-type", pt, appInstallPolicyTypes); err != nil {
			return nil, err
		}
		attrs["policy_type"] = pt
	}
	setStringFlag(cmd, attrs, "policy-product-id")
	if cmd.Flags().Changed("approved-scope") {
		scopes, _ := cmd.Flags().GetStringArray("approved-scope")
		if scopes == nil {
			scopes = []string{}
		}
		attrs["approved_scopes"] = scopes
	}
	setBoolFlag(cmd, attrs, "enabled")
	return attrs, nil
}

var appsInstallsCreateCmd = &cobra.Command{
	Use:   "create <app_id>",
	Short: "Install an app into its hub.",
	Long:  "Install the app into the hub it was registered for. An app has one hub, so one install.",
	Example: `  mio apps installs create app_abc123 --policy-type everyone --approved-scope progress:read --enabled
  mio apps installs create app_abc123 --policy-type product --policy-product-id prod_123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		attrs, err := appInstallAttrs(cmd)
		if err != nil {
			return err
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.CreateWith(c.ctx, client.StyleFlat, appInstallsPath(teamID, args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var appsInstallsListCmd = &cobra.Command{
	Use:     "list <app_id>",
	Short:   "List an app's installs.",
	Long:    "List the app's installs. An app has one hub, so at most one. The route is not paged.",
	Example: `  mio apps installs list app_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		col, err := c.client.List(c.ctx, appInstallsPath(teamID, args[0], ""), nil)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var appsInstallsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <app_id> <install_id>",
	Short:   "Retrieve an install.",
	Long:    "Retrieve one install of an embedded app.",
	Example: `  mio apps installs retrieve app_abc123 inst_xyz789`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		if err := checkAppIDs("install", args[1]); err != nil {
			return err
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, appInstallsPath(teamID, args[0], args[1]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var appsInstallsUpdateCmd = &cobra.Command{
	Use:   "update <app_id> <install_id>",
	Short: "Change an install's policy, scopes or enabled state.",
	Long: `Only the flags you pass are sent, and at least one is required.
--policy-type and --policy-product-id are set together: --policy-product-id is
required with --policy-type product and refused with everyone.
--approved-scope replaces the whole list. --enabled=false disables the install.`,
	Example: `  mio apps installs update app_abc123 inst_xyz789 --enabled=false
  mio apps installs update app_abc123 inst_xyz789 --policy-type product --policy-product-id prod_123
  mio apps installs update app_abc123 inst_xyz789 --approved-scope progress:read`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		if err := checkAppIDs("install", args[1]); err != nil {
			return err
		}
		attrs, err := appInstallAttrs(cmd)
		if err != nil {
			return err
		}
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: pass --policy-type, --policy-product-id, --approved-scope and/or --enabled")
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.UpdateWith(c.ctx, client.StyleFlat, appInstallsPath(teamID, args[0], args[1]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var appsInstallsDeleteCmd = &cobra.Command{
	Use:   "delete <app_id> <install_id>",
	Short: "Uninstall an app.",
	Long: `Remove the install. Credentials already issued to the app stop working.

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio apps installs delete app_abc123 inst_xyz789 --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkAppIDs("app", args[0]); err != nil {
			return err
		}
		if err := checkAppIDs("install", args[1]); err != nil {
			return err
		}
		c, teamID, err := appsTeamContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Uninstall %s from app %s?", args[1], args[0])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, appInstallsPath(teamID, args[0], args[1])); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{"id": args[1], "object": "embedded_app_install", "deleted": true})
	},
}

// ── flags ─────────────────────────────────────────────────────────────────────

func init() {
	appsCreateCmd.Flags().String("name", "", "App name (1-255 chars). (required)")
	appsCreateCmd.Flags().String("launch-url", "", "https URL the app opens at; its origin must be in --allowed-origin. (required)")
	appsCreateCmd.Flags().StringArray("allowed-origin", nil, "Exact https origin the app may run on, e.g. https://apps.example.com. Repeatable, 1-5. (required)")
	appsCreateCmd.Flags().String("webhook-url", "", "Optional https URL that receives webhooks; mints a webhook signing secret.")
	appsCreateCmd.Flags().StringArray("redirect-uri", nil, "Allowed OAuth redirect URI (https). Repeatable, up to 10.")
	markFlagsRequired(appsCreateCmd, "name", "launch-url", "allowed-origin")

	addPaginationFlags(appsListCmd)

	appsUpdateCmd.Flags().String("name", "", "New app name.")
	appsUpdateCmd.Flags().String("launch-url", "", "New https launch URL.")
	appsUpdateCmd.Flags().StringArray("allowed-origin", nil, "Replace the allowed origins. Repeatable, 1-5.")
	appsUpdateCmd.Flags().String("webhook-url", "", `New https webhook URL; "" removes the webhook and its secret.`)

	appsSecretsUpdateCmd.Flags().String("secret", "", "Which secret to replace: client or webhook. (required)")
	markFlagsRequired(appsSecretsUpdateCmd, "secret")
	_ = appsSecretsUpdateCmd.RegisterFlagCompletionFunc("secret", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return appSecretKinds, cobra.ShellCompDirectiveNoFileComp
	})

	for _, c := range []*cobra.Command{appsInstallsCreateCmd, appsInstallsUpdateCmd} {
		c.Flags().String("policy-type", "", "Who can open the app: everyone or product.")
		c.Flags().String("policy-product-id", "", "Product members must hold (required with --policy-type product).")
		c.Flags().StringArray("approved-scope", nil, "Scope granted to the app, e.g. progress:read. Repeatable.")
		c.Flags().Bool("enabled", false, "Enable the install (=false disables it).")
		_ = c.RegisterFlagCompletionFunc("policy-type", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return appInstallPolicyTypes, cobra.ShellCompDirectiveNoFileComp
		})
	}
}
