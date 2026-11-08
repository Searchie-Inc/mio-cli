package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/output"
	"github.com/Searchie-Inc/mio-cli/internal/version"
)

type globalFlags struct {
	output    string
	apiKey    string
	anonymous bool
	apiBase   string
	team      string
	hub       string
	yes       bool
	jq        string
	raw       bool
	profile   string
	debug     bool
}

var flags globalFlags

var rootCmd = &cobra.Command{
	Use:   "mio",
	Short: "mio — the Membership.io command-line interface",
	Long: `mio is the Membership.io command-line interface.

It is agent-first: JSON output by default off a TTY, environment-variable auth
(MIO_API_KEY), and stable exit codes so scripts and AI agents can branch
deterministically. Authenticate once with 'mio login', or export MIO_API_KEY.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       version.Version,
}

func init() {
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return errs.New(errs.ExitUsage, "%s", err.Error())
	})
}

func Execute() error {
	ensureGroupGuards()
	sweepSupersededBinary(runtime.GOOS, currentExecutablePath())
	return rootCmd.Execute()
}

func RawRequested() bool { return flags.raw }

var groupGuardsOnce sync.Once

func ensureGroupGuards() {
	groupGuardsOnce.Do(func() {
		attachGroupGuards(rootCmd)
	})
}

func attachGroupGuards(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		attachGroupGuards(sub)
	}

	if !cmd.HasSubCommands() || cmd.RunE != nil || cmd.Run != nil {
		return
	}

	cmd.RunE = func(c *cobra.Command, args []string) error {
		fmt.Fprint(c.ErrOrStderr(), c.UsageString())
		if len(args) == 0 {
			return errs.New(errs.ExitUsage,
				"%q requires a subcommand; see the usage above", c.CommandPath())
		}
		return errs.New(errs.ExitUsage,
			"unknown subcommand %q for %q; see the usage above", args[0], c.CommandPath())
	}
}

func RootCmd() *cobra.Command {
	ensureGroupGuards()
	return rootCmd
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVarP(&flags.output, "output", "o", "", "Output format: json|table|plain (default: json off a TTY, table on a TTY).")
	pf.StringVar(&flags.apiKey, "api-key", "", "API key to authenticate with. Overrides MIO_API_KEY and the stored key.")
	pf.BoolVar(&flags.anonymous, "anonymous", false, "Ignore MIO_API_KEY and the stored key; resolve as unauthenticated (for diagnostics). An explicit --api-key still wins.")
	pf.StringVar(&flags.apiBase, "api-base", "", "API base URL. Overrides MIO_API_BASE_URL and config.")
	pf.StringVar(&flags.team, "team", "", "Team id context for team-scoped resources. Overrides config.")
	pf.StringVar(&flags.hub, "hub", "", "Hub id context for hub-scoped resources. Overrides config.")
	pf.BoolVarP(&flags.yes, "yes", "y", false, "Skip the confirmation prompt on destructive operations.")
	pf.StringVar(&flags.jq, "jq", "", "Filter JSON output through this gojq expression.")
	pf.BoolVar(&flags.raw, "raw", false, "Emit the raw JSON:API envelope instead of the flattened resource. On an API error, when stderr is not a terminal, stderr is the API's own error document plus meta.exit_code (and status on an error object that has none).")
	pf.StringVar(&flags.profile, "profile", "", "Named config profile to use.")
	pf.BoolVar(&flags.debug, "debug", false, "Enable verbose request/response logging to stderr.")
	_ = pf.MarkHidden("debug")

	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Fprintln(cmd.OutOrStdout(), version.String())
		return nil
	},
}

type cmdContext struct {
	ctx      context.Context
	cfg      *config.Config
	resolved config.Resolved
	client   *client.Client
	out      output.Options
}

func newContext(cmd *cobra.Command) (*cmdContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, err)
	}

	resolved, err := cfg.Resolve(config.Overrides{
		APIKey:    flags.apiKey,
		Anonymous: flags.anonymous,
		APIBase:   flags.apiBase,
		TeamID:    flags.team,
		HubID:     flags.hub,
		Profile:   flags.profile,
	})
	if err != nil {
		if config.StoredKeyUnusable(err) {
			return nil, errs.Wrap(errs.ExitAuth, err)
		}
		return nil, errs.Wrap(errs.ExitGeneric, err)
	}

	format, err := resolveFormat(cmd)
	if err != nil {
		return nil, err
	}

	cli := client.New(resolved.APIBase, resolved.APIKey, client.WithDebug(flags.debug))

	return &cmdContext{
		ctx:      cmd.Context(),
		cfg:      cfg,
		resolved: resolved,
		client:   cli,
		out: output.Options{
			Format: format,
			JQ:     flags.jq,
			Raw:    flags.raw,
		},
	}, nil
}

func (c *cmdContext) requireAuth() error {
	if c.resolved.Anonymous {
		return nil
	}
	if c.resolved.APIKey == "" {
		return errNoAPIKey(c.resolved.KeyStore)
	}
	return nil
}

func errNoAPIKey(store *config.Store) error {
	const msg = "no API key found: set --api-key, export MIO_API_KEY, or run `mio login`"
	if store == nil {
		return errs.New(errs.ExitAuth, msg)
	}
	return errs.New(errs.ExitAuth, "%s (%s)", msg, store.MissingKeyDetail())
}

func (c *cmdContext) requireTeam() (string, error) {
	if c.resolved.TeamID != "" {
		id, err := c.client.ResolveTeam(c.ctx, c.resolved.TeamID)
		if err != nil {
			return "", errs.Wrap(errs.ExitUsage, err)
		}
		c.resolved.TeamID = id
		return id, nil
	}

	if id, ok := c.singleTeamDefault(); ok {
		c.resolved.TeamID = id
		return id, nil
	}

	return "", errs.New(errs.ExitUsage,
		"no team id in context: pass --team <id> or run `mio config set current_team <id>` "+
			"— run 'mio teams list' then 'mio teams switch <id>'")
}

func (c *cmdContext) requireHub() (string, error) {
	if c.resolved.HubID != "" {
		teamID, terr := c.requireTeam()
		if terr != nil {
			return "", terr
		}
		id, err := c.client.ResolveHub(c.ctx, teamID, c.resolved.HubID)
		if err != nil {
			return "", errs.Wrap(errs.ExitUsage, err)
		}
		c.resolved.HubID = id
		return id, nil
	}

	teamID, terr := c.requireTeam()
	if terr != nil {
		return "", terr
	}
	if id, ok := c.singleHubDefault(teamID); ok {
		c.resolved.HubID = id
		return id, nil
	}

	return "", errNoHubInContext
}

var errNoHubInContext = errs.New(errs.ExitUsage,
	"no hub id in context: pass --hub <id> or run `mio config set current_hub <id>` "+
		"— run 'mio hubs list' then 'mio config set current_hub <id>'")

func (c *cmdContext) singleTeamDefault() (string, bool) {
	col, err := c.client.List(c.ctx, "/api/teams", nil)
	if err != nil || len(col.Data) != 1 {
		return "", false
	}
	id := col.Data[0].ID
	c.persistDefaultTeam(id)
	return id, true
}

func (c *cmdContext) singleHubDefault(teamID string) (string, bool) {
	col, err := c.client.List(c.ctx, fmt.Sprintf("/api/teams/%s/hubs", teamID), nil)
	if err != nil || len(col.Data) != 1 {
		return "", false
	}
	return col.Data[0].ID, true
}

func (c *cmdContext) persistDefaultTeam(teamID string) {
	if c.cfg == nil || c.cfg.CurrentTeam != "" {
		return
	}
	c.cfg.CurrentTeam = teamID
	_ = c.cfg.Save()
}

func (c *cmdContext) render(cmd *cobra.Command, v any) error {
	if err := output.Render(cmd.OutOrStdout(), v, c.out); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	if col, ok := v.(*client.Collection); ok && !c.out.Raw {
		if note := moreRowsNote(cmd, col); note != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), note)
		}
	}
	return nil
}

func resolveFormat(cmd *cobra.Command) (output.Format, error) {
	if flags.output == "" {
		if isTTY(cmd.OutOrStdout()) {
			return output.FormatTable, nil
		}
		return output.FormatJSON, nil
	}
	f, err := output.ParseFormat(flags.output)
	if err != nil {
		return "", errs.Wrap(errs.ExitUsage, err)
	}
	return f, nil
}

func isTTY(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func confirmDestructive(cmd *cobra.Command, prompt string) error {
	return confirmDestructiveFrom(cmd, prompt, cmd.InOrStdin())
}

func confirmDestructiveFrom(cmd *cobra.Command, prompt string, in io.Reader) error {
	if flags.yes {
		return nil
	}
	if !isTTY(cmd.OutOrStdout()) {
		return errs.New(errs.ExitNeedsConfir,
			"%s — refusing in a non-interactive shell; pass --yes to proceed", prompt)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N]: ", prompt)
	var answer string
	_, _ = fmt.Fscanln(in, &answer)
	switch answer {
	case "y", "Y", "yes", "YES":
		return nil
	default:
		return errs.New(errs.ExitNeedsConfir, "aborted")
	}
}
