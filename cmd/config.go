package cmd

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// configKeys are the writable config keys exposed via `mio config set/get`.
// They map to fields on config.Config. Keep this list and the switch in sync.
var configKeys = []string{"team", "hub", "api-base"}

func init() {
	configCmd.AddCommand(configSetCmd, configGetCmd, configListCmd)
	rootCmd.AddCommand(configCmd)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Read and write CLI configuration.",
	Long: `Manage the persistent CLI configuration stored at
$XDG_CONFIG_HOME/mio/config.toml (default ~/.config/mio/config.toml).

Writable keys: team, hub, api-base. The API key itself is a secret and is
managed by 'mio login' / 'mio logout', not by this command.`,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a config value (team|hub|api-base).",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, value := args[0], args[1]
		cfg, err := config.Load()
		if err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		switch key {
		case "team":
			cfg.CurrentTeam = value
		case "hub":
			cfg.CurrentHub = value
		case "api-base":
			cfg.APIBase = value
		default:
			return errs.New(errs.ExitUsage, "unknown config key %q (valid: %v)", key, configKeys)
		}
		if err := cfg.Save(); err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "set %s = %s\n", key, value)
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Get one config value, or all of them.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		values := configValues(cfg)
		if len(args) == 1 {
			v, ok := values[args[0]]
			if !ok {
				return errs.New(errs.ExitUsage, "unknown config key %q (valid: %v)", args[0], configKeys)
			}
			fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		}
		for _, k := range sortedConfigKeys() {
			fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", k, values[k])
		}
		return nil
	},
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all config values and the config file path.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		path, _ := config.Path()
		fmt.Fprintf(cmd.OutOrStdout(), "# %s\n", path)
		values := configValues(cfg)
		for _, k := range sortedConfigKeys() {
			fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", k, values[k])
		}
		return nil
	},
}

func configValues(cfg *config.Config) map[string]string {
	return map[string]string{
		"team":     cfg.CurrentTeam,
		"hub":      cfg.CurrentHub,
		"api-base": cfg.APIBase,
	}
}

func sortedConfigKeys() []string {
	out := append([]string(nil), configKeys...)
	sort.Strings(out)
	return out
}
