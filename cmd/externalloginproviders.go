package cmd

// externalloginproviders.go implements the `mio external-login-providers`
// command group.
//
// Routes (team-owner admin surface):
//
//	create   POST   /api/teams/{team_id}/external-login-providers
//	list     GET    /api/teams/{team_id}/external-login-providers
//	retrieve GET    /api/teams/{team_id}/external-login-providers/{id}
//	update   PATCH  /api/teams/{team_id}/external-login-providers/{id}
//	delete   DELETE /api/teams/{team_id}/external-login-providers/{id}
//
// JSON:API resource type: "external_login_providers"
//
// NOTE: client_secret is write-only — it is never returned by the server.
// The read-only `client_secret_set` boolean and `callback_url` fields are
// populated in every GET response.
//
// Generic OIDC/OAuth2 providers (generic_oidc, generic_oauth2) accept the
// extra flags --issuer, --discovery-url, --token-url, --userinfo-url,
// --authorize-url, --jwks-uri, --scopes, and --claim-map. These flags are
// silently ignored by the server for the first-party providers (google,
// facebook) that have built-in configs — but passing them for non-generic
// providers is still a client-side usage error (validate server-side).

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	externalLoginProvidersCmd.AddCommand(
		externalLoginProvidersCreateCmd,
		externalLoginProvidersListCmd,
		externalLoginProvidersRetrieveCmd,
		externalLoginProvidersUpdateCmd,
		externalLoginProvidersDeleteCmd,
	)
	rootCmd.AddCommand(externalLoginProvidersCmd)
}

// ── path helper ───────────────────────────────────────────────────────────────

// externalLoginProvidersPath returns
// /api/teams/{team_id}/external-login-providers[/{id}].
func externalLoginProvidersPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/external-login-providers", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ── group ─────────────────────────────────────────────────────────────────────

var externalLoginProvidersCmd = &cobra.Command{
	Use:   "external-login-providers",
	Short: "Manage external login providers for the active team.",
	Long: `Create, list, retrieve, update, and delete external login providers
for the active team.

External login providers allow hub members to sign in using a third-party
identity provider (Google, Facebook, or any generic OIDC/OAuth2 provider).

The client_secret is write-only and is never returned by the server.
Use --client-secret on create or update to set or rotate it. After creation
the read-only client_secret_set field indicates whether a secret is stored,
and callback_url gives the redirect URI to register with your provider.`,
}

// ── create ────────────────────────────────────────────────────────────────────

var externalLoginProvidersCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an external login provider.",
	Long: `Create a new external login provider for the active team.

The client_secret is write-only. After creation, client_secret_set will be
true and callback_url will contain the redirect URI to register with your
identity provider.

For generic_oidc providers you can supply --discovery-url alone (the backend
will fetch all endpoints from the OIDC discovery document) or provide
individual endpoint flags. For generic_oauth2 you must supply the individual
endpoint flags explicitly.

The --claim-map flag accepts a JSON object that maps identity-provider claim
names to mio contact fields, e.g.
  --claim-map '{"given_name":"first_name","family_name":"last_name"}'`,
	Example: `  # Google provider
  mio external-login-providers create \
    --kind google \
    --display-name "Sign in with Google" \
    --client-id "your-google-client-id" \
    --client-secret "your-google-client-secret"

  # Generic OIDC provider (with discovery URL)
  mio external-login-providers create \
    --kind generic_oidc \
    --display-name "Okta SSO" \
    --client-id "0oa..." \
    --client-secret "..." \
    --discovery-url "https://your-org.okta.com/.well-known/openid-configuration"

  # Generic OIDC provider (with claim map)
  mio external-login-providers create \
    --kind generic_oidc \
    --display-name "Company SSO" \
    --client-id "..." \
    --client-secret "..." \
    --issuer "https://sso.company.com" \
    --claim-map '{"given_name":"first_name","family_name":"last_name"}'`,
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
		setStringFlag(cmd, attrs, "kind")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "display-name")
		setStringFlag(cmd, attrs, "client-id")
		setStringFlag(cmd, attrs, "client-secret")

		// Generic provider flags.
		setStringFlag(cmd, attrs, "issuer")
		setStringFlag(cmd, attrs, "discovery-url")
		setStringFlag(cmd, attrs, "authorize-url")
		setStringFlag(cmd, attrs, "token-url")
		setStringFlag(cmd, attrs, "userinfo-url")
		setStringFlag(cmd, attrs, "jwks-uri")
		setStringFlag(cmd, attrs, "scopes")

		if err := setJSONObjectFlag(cmd, attrs, "claim-map"); err != nil {
			return err
		}

		// --kind, --display-name, --client-id, and --client-secret are required
		// client-side. The backend requires client_id and client_secret on create
		// for EVERY provider kind (google, facebook, generic_oidc, generic_oauth2)
		// — there is no kind that legitimately omits them — so we enforce both
		// up-front rather than letting an incomplete request reach the server.
		if v, _ := attrs["kind"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--kind is required (google|facebook|generic_oidc|generic_oauth2)")
		}
		if v, _ := attrs["display_name"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--display-name is required")
		}
		if v, _ := attrs["client_id"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--client-id is required")
		}
		if v, _ := attrs["client_secret"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--client-secret is required")
		}

		res, err := c.client.Create(c.ctx, externalLoginProvidersPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── list ──────────────────────────────────────────────────────────────────────

var externalLoginProvidersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List external login providers for the active team.",
	Long:  "List all external login providers belonging to the active team.",
	Example: `  mio external-login-providers list
  mio external-login-providers list --limit=20
  mio external-login-providers list --after=<cursor>`,
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

		col, err := c.client.List(c.ctx, externalLoginProvidersPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ── retrieve ──────────────────────────────────────────────────────────────────

var externalLoginProvidersRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an external login provider by id.",
	Long:    "Retrieve a single external login provider. The client_secret is never returned.",
	Example: `  mio external-login-providers retrieve elp_abc123`,
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

		res, err := c.client.Retrieve(c.ctx, externalLoginProvidersPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── update ────────────────────────────────────────────────────────────────────

var externalLoginProvidersUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an external login provider by id.",
	Long: `Partially update an external login provider. Only the flags you set are sent
(PATCH semantics — unset flags leave the field unchanged on the server).

Use --client-secret to rotate the provider secret. Use --enabled=false to
disable the provider without deleting it.`,
	Example: `  # Rotate client secret
  mio external-login-providers update elp_abc123 --client-secret "new-secret"

  # Disable provider
  mio external-login-providers update elp_abc123 --enabled=false

  # Update display name and claim map
  mio external-login-providers update elp_abc123 \
    --display-name "Corporate SSO" \
    --claim-map '{"email":"email","name":"full_name"}'`,
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
		setStringFlag(cmd, attrs, "display-name")
		setStringFlag(cmd, attrs, "client-id")
		setStringFlag(cmd, attrs, "client-secret")
		setBoolFlag(cmd, attrs, "enabled")

		// Generic provider flags.
		setStringFlag(cmd, attrs, "issuer")
		setStringFlag(cmd, attrs, "discovery-url")
		setStringFlag(cmd, attrs, "authorize-url")
		setStringFlag(cmd, attrs, "token-url")
		setStringFlag(cmd, attrs, "userinfo-url")
		setStringFlag(cmd, attrs, "jwks-uri")
		setStringFlag(cmd, attrs, "scopes")

		if err := setJSONObjectFlag(cmd, attrs, "claim-map"); err != nil {
			return err
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one flag")
		}

		res, err := c.client.Update(c.ctx, externalLoginProvidersPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── delete ────────────────────────────────────────────────────────────────────

var externalLoginProvidersDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete an external login provider by id.",
	Long: `Permanently delete an external login provider. Members who used this
provider to sign in will need to use an alternative login method.

This action is irreversible. Pass --yes to skip the confirmation prompt in
non-interactive environments.`,
	Example: `  # Interactive — prompts for confirmation
  mio external-login-providers delete elp_abc123

  # Non-interactive (CI) — skip the prompt
  mio external-login-providers delete elp_abc123 --yes`,
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

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete external login provider %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, externalLoginProvidersPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted external login provider %s.\n", args[0])
		return nil
	},
}

// ── flag registration ─────────────────────────────────────────────────────────

func init() {
	// create flags
	externalLoginProvidersCreateCmd.Flags().String("kind", "", "Provider kind: google|facebook|generic_oidc|generic_oauth2. (required)")
	externalLoginProvidersCreateCmd.Flags().String("slug", "", "URL-safe slug for the provider (auto-derived from display-name if omitted).")
	externalLoginProvidersCreateCmd.Flags().String("display-name", "", "Human-readable label shown on the login button. (required)")
	externalLoginProvidersCreateCmd.Flags().String("client-id", "", "OAuth2 client ID registered with the identity provider. (required)")
	externalLoginProvidersCreateCmd.Flags().String("client-secret", "", "OAuth2 client secret registered with the identity provider (write-only). (required)")

	// Generic provider flags — create
	externalLoginProvidersCreateCmd.Flags().String("issuer", "", "Issuer URL (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersCreateCmd.Flags().String("discovery-url", "", "OIDC discovery document URL. When set the backend auto-populates endpoints (generic_oidc only).")
	externalLoginProvidersCreateCmd.Flags().String("authorize-url", "", "Authorization endpoint URL (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersCreateCmd.Flags().String("token-url", "", "Token endpoint URL (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersCreateCmd.Flags().String("userinfo-url", "", "UserInfo endpoint URL (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersCreateCmd.Flags().String("jwks-uri", "", "JWKS endpoint URI for ID token verification (generic_oidc only).")
	externalLoginProvidersCreateCmd.Flags().String("scopes", "", "Space-separated OAuth2 scopes to request (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersCreateCmd.Flags().String("claim-map", "", "JSON object mapping IdP claim names to mio contact fields, e.g. '{\"given_name\":\"first_name\"}'.")

	// update flags — same set minus --kind/--slug (cannot be changed post-create)
	externalLoginProvidersUpdateCmd.Flags().String("display-name", "", "New human-readable label shown on the login button.")
	externalLoginProvidersUpdateCmd.Flags().String("client-id", "", "New OAuth2 client ID.")
	externalLoginProvidersUpdateCmd.Flags().String("client-secret", "", "New OAuth2 client secret (rotates the stored secret; write-only).")
	externalLoginProvidersUpdateCmd.Flags().Bool("enabled", false, "Enable (true) or disable (false) the provider.")

	// Generic provider flags — update
	externalLoginProvidersUpdateCmd.Flags().String("issuer", "", "Issuer URL (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersUpdateCmd.Flags().String("discovery-url", "", "OIDC discovery document URL (generic_oidc only).")
	externalLoginProvidersUpdateCmd.Flags().String("authorize-url", "", "Authorization endpoint URL (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersUpdateCmd.Flags().String("token-url", "", "Token endpoint URL (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersUpdateCmd.Flags().String("userinfo-url", "", "UserInfo endpoint URL (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersUpdateCmd.Flags().String("jwks-uri", "", "JWKS endpoint URI (generic_oidc only).")
	externalLoginProvidersUpdateCmd.Flags().String("scopes", "", "Space-separated OAuth2 scopes to request (generic_oidc / generic_oauth2 only).")
	externalLoginProvidersUpdateCmd.Flags().String("claim-map", "", "JSON object mapping IdP claim names to mio contact fields.")

	// list pagination
	addPaginationFlags(externalLoginProvidersListCmd)
}

// ── local helpers ─────────────────────────────────────────────────────────────

// setJSONObjectFlag parses a JSON-object string flag into attrs[name] iff it
// was set by the user. The value must be a valid JSON object; an error is
// returned if set but invalid.
func setJSONObjectFlag(cmd *cobra.Command, attrs map[string]any, name string) error {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	raw, err := cmd.Flags().GetString(name)
	if err != nil || raw == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return errs.New(errs.ExitUsage, "--%s must be a valid JSON object: %s", name, err)
	}
	// json.Unmarshal accepts the literal `null` for a map and leaves parsed nil.
	// A JSON object is required here, so reject null explicitly rather than sending
	// an empty/nil attribute to the server.
	if parsed == nil {
		return errs.New(errs.ExitUsage, "--%s must be a JSON object, not null", name)
	}
	attrs[attrKey(name)] = parsed
	return nil
}
