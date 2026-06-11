package cmd

// whoami.go — the `mio whoami` command.
//
// whoami is the canonical "did my setup actually work?" command. It resolves
// and prints the effective identity and scope the CLI will use for the next
// API call:
//
//   - the authenticated user (GET /api/auth/me)
//   - the active team id + display name
//   - the active hub id + display name
//   - the API base URL
//   - the active config profile
//   - where the API key was resolved from (flag / env / keychain)
//
// Off a TTY it renders JSON by default (respecting --output), so agents can
// parse it; on a TTY it renders a friendly key/value table.
//
// MIO-847 — API key precedence for team_id:
// When the API key was resolved from the MIO_API_KEY env var (or --api-key
// flag), the key itself encodes identity. The team_id reported by whoami must
// reflect the team derived from the key (via the /api/auth/me response), NOT
// the current_team stored in the config file. Config-stored team is a default
// used when no explicit key is supplied; an explicit key overrides it.

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/config"
)

func init() {
	rootCmd.AddCommand(whoamiCmd)
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show the resolved identity and scope the CLI will use.",
	Long: `Print the effective identity and scope for the current configuration:
the authenticated user, the active team (id + name), the active hub (id + name),
the API base URL, the active profile, and where the API key was resolved from.

This is the canonical "did my setup work?" command. Off a TTY it prints JSON
(respecting --output) so agents can parse it.`,
	Example: `  mio whoami
  mio whoami --output json
  mio whoami --jq .team_id`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		info := map[string]any{
			"api_base":   c.resolved.APIBase,
			"profile":    flags.profile,
			"key_source": keySource(),
			"hub_id":     c.resolved.HubID,
		}

		// Authenticated principal. /api/auth/me is plain JSON. The whole point
		// of whoami is to confirm the credentials work, so an auth failure here
		// MUST propagate (e.g. an invalid/expired key → ExitAuth) rather than be
		// swallowed into partial, exit-0 output. Only the NAME lookups below are
		// best-effort; identity is not.
		me, merr := c.client.Me(c.ctx)
		if merr != nil {
			return merr
		}
		info["user"] = me
		if v, ok := me["email"].(string); ok {
			info["user_email"] = v
		}
		if v, ok := me["id"].(string); ok {
			info["user_id"] = v
		}

		// MIO-847 — team_id precedence:
		// When an explicit API key is in use (via --api-key flag or MIO_API_KEY
		// env), the key carries its own team identity. Use the team_id from the
		// /api/auth/me response as the authoritative team for this invocation,
		// ignoring whatever current_team is stored in the config file.
		// When using a keychain key (no explicit override), fall back to the
		// resolved config team id as before.
		teamID := c.resolved.TeamID
		if ks := keySource(); ks == "flag (--api-key)" || ks == "env ("+config.EnvAPIKey+")" {
			// Prefer the team_id the server reports for this key over config.
			if v, ok := me["team_id"].(string); ok && v != "" {
				teamID = v
			}
		}
		info["team_id"] = teamID

		// Resolve display names best-effort — a name lookup failing must NOT
		// fail whoami; the ids are the authoritative answer.
		if teamID != "" {
			if res, rerr := c.client.Retrieve(c.ctx, teamsPath(teamID)); rerr == nil && res != nil {
				if name, ok := res.Attributes["name"].(string); ok {
					info["team_name"] = name
				}
			}
		}
		if teamID != "" && c.resolved.HubID != "" {
			if res, rerr := c.client.Retrieve(c.ctx, hubsPath(teamID, c.resolved.HubID)); rerr == nil && res != nil {
				if name, ok := res.Attributes["name"].(string); ok {
					info["hub_name"] = name
				}
			}
		}

		return c.render(cmd, info)
	},
}

// keySource reports where the resolved API key came from, mirroring the
// precedence in config.Resolve (flag > env > keychain). It does NOT print the
// key itself — only its origin — so whoami output is safe to share.
func keySource() string {
	if flags.apiKey != "" {
		return "flag (--api-key)"
	}
	if os.Getenv(config.EnvAPIKey) != "" {
		return "env (" + config.EnvAPIKey + ")"
	}
	if key, err := config.GetAPIKey(); err == nil && key != "" {
		return "keychain"
	}
	return "none"
}
