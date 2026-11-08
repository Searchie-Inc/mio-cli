package cmd

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var configUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validateConfigValue(key, value string) error {
	switch key {
	case "api_base":
		if strings.ContainsAny(value, "?#") {
			return errs.New(errs.ExitUsage, "invalid api_base %q: must be a plain http(s) base URL (scheme://host[:port]), with no query or fragment", value)
		}
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
			return errs.New(errs.ExitUsage, "invalid api_base %q: must be a plain http(s) base URL (scheme://host[:port]), with no query or fragment", value)
		}
	case "current_team", "current_hub":
		if !configUUIDRe.MatchString(value) {
			return errs.New(errs.ExitUsage, "invalid %s %q: must be a UUID (e.g. 019e204f-9ea0-7601-ac0f-ab522eece374)", key, value)
		}
	}
	return nil
}

var configKeys = []string{"current_team", "current_hub", "api_base"}

func init() {
	configCmd.AddCommand(configSetCmd, configGetCmd, configListCmd)
	rootCmd.AddCommand(configCmd)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Read and write CLI configuration.",
	Long: `Manage the persistent CLI configuration stored at
$XDG_CONFIG_HOME/mio/config.toml (default ~/.config/mio/config.toml).

Writable keys: current_team, current_hub, api_base. The API key itself is a
secret and is managed by 'mio login' / 'mio logout', not by this command.`,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a config value (current_team|current_hub|api_base).",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, value := args[0], args[1]
		if err := validateConfigValue(key, value); err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		switch key {
		case "current_team":
			cfg.CurrentTeam = value
		case "current_hub":
			cfg.CurrentHub = value
		case "api_base":
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
		"current_team": cfg.CurrentTeam,
		"current_hub":  cfg.CurrentHub,
		"api_base":     cfg.APIBase,
	}
}

func sortedConfigKeys() []string {
	out := append([]string(nil), configKeys...)
	sort.Strings(out)
	return out
}
