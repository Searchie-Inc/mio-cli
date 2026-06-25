package cmd

// oauthclients.go implements the `mio oauth-clients` command group.
//
// Routes (Hub-as-IdP SSO, team-admin surface only — member-facing /oauth/* flows
// are browser/tool driven and are NOT exposed here):
//
//	create           POST   /api/teams/{team_id}/oauth-clients
//	list             GET    /api/teams/{team_id}/oauth-clients
//	retrieve         GET    /api/teams/{team_id}/oauth-clients/{id}
//	delete           DELETE /api/teams/{team_id}/oauth-clients/{id}
//	redirect-uris add    POST   /api/teams/{team_id}/oauth-clients/{client_pk}/redirect-uris
//	redirect-uris remove DELETE /api/teams/{team_id}/oauth-clients/{client_pk}/redirect-uris/{id}
//
// JSON:API resource types: "oauth_clients", "oauth_client_redirect_uris"
//
// NOTE: `first_party` and `allowed_scopes` are platform-admin-only fields
// (configurable only via PATCH /api/oauth-clients/{id} which requires platform
// admin auth). They are intentionally NOT exposed here.
//
// The client_secret is returned by the server only on create. Store it
// immediately — subsequent retrieve and list calls will not re-expose it.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// oauth-clients <action>
	oauthClientsCmd.AddCommand(
		oauthClientsCreateCmd,
		oauthClientsListCmd,
		oauthClientsRetrieveCmd,
		oauthClientsDeleteCmd,
		oauthClientsRedirectURIsCmd,
	)

	// oauth-clients redirect-uris <action>
	oauthClientsRedirectURIsCmd.AddCommand(
		oauthClientsRedirectURIsAddCmd,
		oauthClientsRedirectURIsRemoveCmd,
	)

	rootCmd.AddCommand(oauthClientsCmd)
}

// ── path helpers ──────────────────────────────────────────────────────────────

// oauthClientsPath returns /api/teams/{team_id}/oauth-clients[/{id}].
func oauthClientsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/oauth-clients", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// redirectURIsPath returns /api/teams/{team_id}/oauth-clients/{client_pk}/redirect-uris[/{id}].
func redirectURIsPath(teamID, clientPK, id string) string {
	base := fmt.Sprintf("/api/teams/%s/oauth-clients/%s/redirect-uris", teamID, clientPK)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ── group ─────────────────────────────────────────────────────────────────────

var oauthClientsCmd = &cobra.Command{
	Use:   "oauth-clients",
	Short: "Manage OAuth clients for the active team.",
	Long: `Create, list, retrieve, and delete OAuth 2.0 clients for the active team.

OAuth clients enable Hub-as-IdP SSO: external tools and services can authenticate
their users via your hub using standard OAuth 2.0 / OIDC flows.

The client_secret is returned only once at creation time. Store it immediately —
subsequent retrieve and list calls will not re-expose the secret.`,
}

// ── create ────────────────────────────────────────────────────────────────────

var oauthClientsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an OAuth client.",
	Long: `Create a new OAuth 2.0 client for the active team.

The client_secret is returned exactly once in the response. Store it immediately —
it cannot be retrieved again after this call.

Public clients (--public) use PKCE and do not have a client_secret (the server
returns an empty or absent secret for them).`,
	Example: `  # Confidential client (server-side app)
  mio oauth-clients create --name "My App" \
    --redirect-uri https://myapp.example.com/callback

  # Public client (PKCE — mobile / SPA)
  mio oauth-clients create --name "My Mobile App" --public \
    --redirect-uri myapp://callback

  # With custom branding
  mio oauth-clients create --name "Partner Portal" \
    --brand-label "Partner Login" \
    --logo-url https://cdn.example.com/logo.png \
    --redirect-uri https://partner.example.com/callback

  # Add more redirect URIs after creation
  mio oauth-clients redirect-uris add oc_abc123 --uri https://myapp.example.com/callback2`,
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
		setStringFlag(cmd, attrs, "name")
		// --public maps to the backend's is_public attribute name.
		setMappedBool(cmd, attrs, "public", "is_public")
		setStringFlag(cmd, attrs, "brand-label")
		setStringFlag(cmd, attrs, "logo-url")

		if v, _ := attrs["name"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--name is required")
		}

		// --redirect-uri supplies the initial redirect URI. A single String flag
		// avoids the in-process test reset accumulation issue that pflag list types
		// (StringArray / StringSlice) have when resetCommandFlags calls Set with
		// the default value. Additional URIs can be added via `redirect-uris add`.
		if cmd.Flags().Changed("redirect-uri") {
			if v, err2 := cmd.Flags().GetString("redirect-uri"); err2 == nil && strings.TrimSpace(v) != "" {
				attrs["redirect_uris"] = []string{v}
			}
		}

		res, err := c.client.Create(c.ctx, oauthClientsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── list ──────────────────────────────────────────────────────────────────────

var oauthClientsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List OAuth clients for the active team.",
	Long:  "List all OAuth 2.0 clients belonging to the active team. Secrets are not included.",
	Example: `  mio oauth-clients list
  mio oauth-clients list --limit=20
  mio oauth-clients list --after=<cursor>`,
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

		col, err := c.client.List(c.ctx, oauthClientsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ── retrieve ──────────────────────────────────────────────────────────────────

var oauthClientsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an OAuth client by id.",
	Long:    "Retrieve metadata for a single OAuth client. The client_secret is not included.",
	Example: `  mio oauth-clients retrieve oc_abc123`,
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

		res, err := c.client.Retrieve(c.ctx, oauthClientsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── delete ────────────────────────────────────────────────────────────────────

var oauthClientsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete an OAuth client by id.",
	Long: `Permanently delete an OAuth client. Any active sessions or tokens issued
to this client will cease to work immediately.

This action is irreversible. Pass --yes to skip the confirmation prompt in
non-interactive environments.`,
	Example: `  # Interactive — prompts for confirmation
  mio oauth-clients delete oc_abc123

  # Non-interactive (CI) — skip the prompt
  mio oauth-clients delete oc_abc123 --yes`,
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

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete OAuth client %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, oauthClientsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted OAuth client %s.\n", args[0])
		return nil
	},
}

// ── redirect-uris sub-group ───────────────────────────────────────────────────

var oauthClientsRedirectURIsCmd = &cobra.Command{
	Use:   "redirect-uris",
	Short: "Manage redirect URIs for an OAuth client.",
	Long:  "Add and remove allowed redirect URIs on an OAuth client.",
}

// ── redirect-uris add ─────────────────────────────────────────────────────────

var oauthClientsRedirectURIsAddCmd = &cobra.Command{
	Use:   "add <client_id>",
	Short: "Add a redirect URI to an OAuth client.",
	Long:  "Add a new allowed redirect URI to an existing OAuth client.",
	Example: `  mio oauth-clients redirect-uris add oc_abc123 --uri https://myapp.example.com/callback
  mio oauth-clients redirect-uris add oc_abc123 --uri myapp://callback`,
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
		setStringFlag(cmd, attrs, "uri")

		if v, _ := attrs["uri"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--uri is required")
		}

		res, err := c.client.Create(c.ctx, redirectURIsPath(teamID, args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── redirect-uris remove ──────────────────────────────────────────────────────

var oauthClientsRedirectURIsRemoveCmd = &cobra.Command{
	Use:   "remove <client_id> <uri_id>",
	Short: "Remove a redirect URI from an OAuth client.",
	Long: `Remove an allowed redirect URI from an OAuth client.

This action is irreversible. Pass --yes to skip the confirmation prompt in
non-interactive environments.`,
	Example: `  # Interactive — prompts for confirmation
  mio oauth-clients redirect-uris remove oc_abc123 ru_xyz789

  # Non-interactive (CI) — skip the prompt
  mio oauth-clients redirect-uris remove oc_abc123 ru_xyz789 --yes`,
	Args: cobra.ExactArgs(2),
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

		clientID, uriID := args[0], args[1]

		if err := confirmDestructive(cmd, fmt.Sprintf("Remove redirect URI %s from OAuth client %s?", uriID, clientID)); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, redirectURIsPath(teamID, clientID, uriID)); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed redirect URI %s from OAuth client %s.\n", uriID, clientID)
		return nil
	},
}

// ── flag registration ─────────────────────────────────────────────────────────

func init() {
	// create flags
	oauthClientsCreateCmd.Flags().String("name", "", "Human-readable name for the OAuth client. (required)")
	oauthClientsCreateCmd.Flags().Bool("public", false, "Create a public client (PKCE — for mobile apps and SPAs). Confidential by default.")
	oauthClientsCreateCmd.Flags().String("brand-label", "", "Custom label shown on the OAuth consent/login screen.")
	oauthClientsCreateCmd.Flags().String("logo-url", "", "URL of a logo image shown on the OAuth consent/login screen.")
	// Single String (not StringSlice/StringArray) to avoid pflag in-process test
	// reset accumulation. Additional URIs can be added via `redirect-uris add`.
	oauthClientsCreateCmd.Flags().String("redirect-uri", "", "Initial allowed redirect URI. Use `redirect-uris add` to add more after creation.")

	// list pagination
	addPaginationFlags(oauthClientsListCmd)

	// redirect-uris add flags
	oauthClientsRedirectURIsAddCmd.Flags().String("uri", "", "The redirect URI to add. (required)")
}
