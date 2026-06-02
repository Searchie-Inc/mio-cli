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
			"team_id":    c.resolved.TeamID,
			"hub_id":     c.resolved.HubID,
		}

		// Authenticated principal. /api/auth/me is plain JSON; surface the whole
		// user object under "user" plus a flattened "user_email"/"user_id" when
		// present for easy --jq access.
		if me, merr := c.client.Me(c.ctx); merr == nil {
			info["user"] = me
			if v, ok := me["email"].(string); ok {
				info["user_email"] = v
			}
			if v, ok := me["id"].(string); ok {
				info["user_id"] = v
			}
		}

		// Resolve display names best-effort — a name lookup failing must NOT
		// fail whoami; the ids are the authoritative answer.
		if c.resolved.TeamID != "" {
			if res, rerr := c.client.Retrieve(c.ctx, teamsPath(c.resolved.TeamID)); rerr == nil && res != nil {
				if name, ok := res.Attributes["name"].(string); ok {
					info["team_name"] = name
				}
			}
		}
		if c.resolved.TeamID != "" && c.resolved.HubID != "" {
			if res, rerr := c.client.Retrieve(c.ctx, hubsPath(c.resolved.TeamID, c.resolved.HubID)); rerr == nil && res != nil {
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
