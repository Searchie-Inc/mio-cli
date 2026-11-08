package cmd

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
  mio whoami -o plain --jq .team_id`,
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
			"key_source": keySource(c.resolved),
			"hub_id":     c.resolved.HubID,
		}

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

		teamID := c.resolved.TeamID
		if ks := keySource(c.resolved); ks == "flag (--api-key)" || ks == "env ("+config.EnvAPIKey+")" {
			if v, ok := me["team_id"].(string); ok && v != "" {
				teamID = v
			}
		}
		info["team_id"] = teamID

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

func keySource(r config.Resolved) string {
	if flags.apiKey != "" {
		return "flag (--api-key)"
	}
	if flags.anonymous {
		return "none (--anonymous)"
	}
	if os.Getenv(config.EnvAPIKey) != "" {
		return "env (" + config.EnvAPIKey + ")"
	}
	if r.APIKey != "" && r.KeyStore != nil {
		return r.KeyStore.Label()
	}
	return "none"
}
