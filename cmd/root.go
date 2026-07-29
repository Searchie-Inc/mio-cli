// Package cmd holds the cobra command tree for the mio CLI. root.go defines the
// root command, the persistent global flags every subcommand inherits, and the
// shared per-invocation context (resolved config + client + output options)
// that resource commands pull via cmdContext.
//
// Resource command files (products.go, contacts.go, …) self-register their
// command trees in their own init() via rootCmd.AddCommand, so adding a
// resource never requires editing this file. This is the property that lets 15
// agents add resources in parallel without merge conflicts on a shared registry.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/output"
	"github.com/Searchie-Inc/mio-cli/internal/version"
)

// globalFlags holds the parsed values of the persistent root flags. A single
// package-level instance is bound by cobra and read by cmdContext.
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

// rootCmd is the `mio` root command. Resource files attach to it in init().
var rootCmd = &cobra.Command{
	Use:   "mio",
	Short: "mio — the Membership.io command-line interface",
	Long: `mio is the Membership.io command-line interface.

It is agent-first: JSON output by default off a TTY, environment-variable auth
(MIO_API_KEY), and stable exit codes so scripts and AI agents can branch
deterministically. Authenticate once with 'mio login', or export MIO_API_KEY.`,
	SilenceUsage:  true, // we print usage ourselves only for usage errors
	SilenceErrors: true, // main.go renders the error envelope to stderr
	Version:       version.Version,
}

func init() {
	// Flag-parse errors (unknown flag, bad flag value) flow through this hook.
	// Tag them with ExitUsage so the bad-flags exit-code contract holds. The
	// other Cobra usage errors (required-flag-not-set, wrong arg count, unknown
	// command) are NOT routed here — Cobra returns them straight from Execute();
	// main.go classifies any non-*CLIError as a usage error to cover those. See
	// the probe in cmd/root_test.go.
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return errs.New(errs.ExitUsage, "%s", err.Error())
	})
}

// Execute runs the root command and returns the error (if any) for main.go to
// map onto a process exit code. It does not call os.Exit itself.
func Execute() error {
	ensureGroupGuards()
	return rootCmd.Execute()
}

// groupGuardsOnce ensures the group guards are attached exactly once, no matter
// how many times Execute()/RootCmd() are called (the in-process contract tests
// call RootCmd().Execute() directly, bypassing Execute()).
var groupGuardsOnce sync.Once

// ensureGroupGuards attaches the group guards to the fully-built command tree
// the first time it is called. It is safe to call from both Execute() (the
// production path) and RootCmd() (the test path) — the sync.Once makes it
// idempotent, and by the time either runs every resource init() has registered
// its subcommands.
func ensureGroupGuards() {
	groupGuardsOnce.Do(func() {
		attachGroupGuards(rootCmd)
	})
}

// attachGroupGuards walks the fully-built command tree and makes every "group"
// command (one with subcommands but no Run/RunE of its own — e.g. `contacts`,
// `teams`, `teams members`, `checkout`, `config`, and the bare root `mio`) fail
// with ExitUsage (exit 2) when invoked with no subcommand or an unknown one.
//
// Without this, cobra treats a group command that receives unrecognised args as
// a successful no-op (exit 0), which silently swallows typos like
// `mio contacts frobnicate`. Agents and CI cannot distinguish that from success.
//
// The guard prints help to stderr (not stdout) so it never contaminates the
// machine-readable stdout contract, and returns a *CLIError carrying ExitUsage
// so main.go maps it to exit 2 and renders it through the normal error path.
func attachGroupGuards(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		attachGroupGuards(sub)
	}

	// A "group" is a command that has subcommands but defines no runnable action
	// of its own. Leaf commands (with RunE/Run) and already-guarded groups are
	// left untouched.
	if !cmd.HasSubCommands() || cmd.RunE != nil || cmd.Run != nil {
		return
	}

	cmd.RunE = func(c *cobra.Command, args []string) error {
		// Help requested explicitly (`mio contacts --help`) is handled by cobra
		// before RunE; reaching here means the group was invoked with no
		// subcommand or an unknown one. Print usage to STDERR — never stdout —
		// so the machine-readable stdout contract stays empty on this error.
		fmt.Fprint(c.ErrOrStderr(), c.UsageString())
		if len(args) == 0 {
			return errs.New(errs.ExitUsage,
				"%q requires a subcommand; see the usage above", c.CommandPath())
		}
		return errs.New(errs.ExitUsage,
			"unknown subcommand %q for %q; see the usage above", args[0], c.CommandPath())
	}
}

// RootCmd exposes the root command for docs generation and tests. It attaches
// the group guards on first use so callers that drive the tree directly (docs
// generation, in-process tests) get the same unknown-subcommand behaviour as
// the production Execute() path.
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
	pf.BoolVar(&flags.raw, "raw", false, "Emit the raw JSON:API envelope instead of the flattened resource.")
	pf.StringVar(&flags.profile, "profile", "", "Named config profile to use.")
	pf.BoolVar(&flags.debug, "debug", false, "Enable verbose request/response logging to stderr.")
	_ = pf.MarkHidden("debug")

	// `mio version` in addition to the --version flag cobra wires from Version.
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

// cmdContext is the resolved per-invocation state a resource command needs. It
// is built lazily by newContext so commands like `version`/`config` that don't
// hit the API never touch the keychain.
type cmdContext struct {
	ctx      context.Context
	cfg      *config.Config
	resolved config.Resolved
	client   *client.Client
	out      output.Options
}

// newContext resolves config + flags into a ready-to-use context: the resolved
// credentials/scope, an HTTP client, and the output options (with the TTY-aware
// format default applied). It does not require an API key — commands that need
// auth call requireAuth.
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
		// Legacy encrypted credentials → exit 3 (same as missing/invalid auth)
		// so agents and CI handle it like any other auth failure.
		if errors.Is(err, config.ErrLegacyCredentials) {
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

// requireAuth returns a usage/auth error if no API key was resolved, so a
// command that needs the API can fail fast with exit code 3.
//
// ORDER MATTERS (MIO-2694). --anonymous is an explicit request to run
// UNAUTHENTICATED, so the precondition is skipped entirely and the request goes
// out with no Authorization header — the API, not the CLI, decides 200 vs 401.
// Previously this check ran ahead of anything that consulted --anonymous, so the
// flag could never reach the HTTP client: `--anonymous` always died here with
// "no API key found", even when pointed at a dead host (no DNS attempt was ever
// made). That defeats the entire point of the flag, whose value is precisely
// that the request IS sent so the caller can observe the API's own answer.
//
// An explicit --api-key still wins: config.Resolve keeps it, so APIKey is
// non-empty and the client authenticates as documented in the flag help.
func (c *cmdContext) requireAuth() error {
	if c.resolved.Anonymous {
		return nil
	}
	if c.resolved.APIKey == "" {
		return errs.New(errs.ExitAuth,
			"no API key found: set --api-key, export MIO_API_KEY, or run `mio login`")
	}
	return nil
}

// requireTeam returns the resolved team id, or a usage error if none can be
// determined. Resolution order:
//
//  1. A value already in context (--team / env / config). If that value is a
//     human NAME or SLUG rather than an id, it is resolved to an id via
//     ResolveTeam (raw ids pass straight through with no API call).
//  2. If still unset, auto-default: list the caller's teams and, if EXACTLY one
//     exists, use it (zero-config for the common single-team user). Zero or
//     more-than-one keeps the existing helpful error.
//
// The resolved id is written back into c.resolved.TeamID so later lookups
// (whoami, requireHub) see the canonical id and downstream calls are consistent.
func (c *cmdContext) requireTeam() (string, error) {
	if c.resolved.TeamID != "" {
		id, err := c.client.ResolveTeam(c.ctx, c.resolved.TeamID)
		if err != nil {
			return "", errs.Wrap(errs.ExitUsage, err)
		}
		c.resolved.TeamID = id
		return id, nil
	}

	// Auto-default: a single-team user needs no --team at all.
	if id, ok := c.singleTeamDefault(); ok {
		c.resolved.TeamID = id
		return id, nil
	}

	return "", errs.New(errs.ExitUsage,
		"no team id in context: pass --team <id> or run `mio config set current_team <id>` "+
			"— run 'mio teams list' then 'mio teams switch <id>'")
}

// requireHub returns the resolved hub id, or a usage error if none can be
// determined. Resolution mirrors requireTeam:
//
//  1. A value in context (--hub / config), resolving a NAME/SLUG to an id via
//     ResolveHub (raw ids pass through with no API call).
//  2. If still unset, auto-default to the team's single hub when exactly one
//     exists.
//
// A hub is team-scoped, so this first resolves the team (which itself may
// auto-default) to know where to look.
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

	// Auto-default needs a team to scope the hub list.
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

// errNoHubInContext is the sentinel requireHub returns when NO hub can be
// resolved from ANY source (--hub, config current_hub, single-hub auto-default).
//
// It is a package-level value rather than an inline errs.New so callers that ALSO
// accept a positional hub id (the `mio hubs …` verbs) can recognise it with
// errors.Is and widen the message to name the positional form too (MIO-2732).
// A *CLIError is a pointer, so errors.Is matches it by identity; nothing mutates
// it, so sharing the value across call sites is safe.
var errNoHubInContext = errs.New(errs.ExitUsage,
	"no hub id in context: pass --hub <id> or run `mio config set current_hub <id>` "+
		"— run 'mio hubs list' then 'mio config set current_hub <id>'")

// singleTeamDefault returns the id of the caller's sole team when EXACTLY one
// exists, so a single-team user never has to pass --team. Zero or more-than-one
// teams (or any list error) returns ok=false so the caller falls back to the
// existing helpful error. A successful auto-default is persisted to config so
// subsequent commands skip the list call.
func (c *cmdContext) singleTeamDefault() (string, bool) {
	col, err := c.client.List(c.ctx, "/api/teams", nil)
	if err != nil || len(col.Data) != 1 {
		return "", false
	}
	id := col.Data[0].ID
	c.persistDefaultTeam(id)
	return id, true
}

// singleHubDefault returns the id of the team's sole hub when EXACTLY one
// exists. Zero or more-than-one (or a list error) returns ok=false.
func (c *cmdContext) singleHubDefault(teamID string) (string, bool) {
	col, err := c.client.List(c.ctx, fmt.Sprintf("/api/teams/%s/hubs", teamID), nil)
	if err != nil || len(col.Data) != 1 {
		return "", false
	}
	return col.Data[0].ID, true
}

// persistDefaultTeam best-effort writes an auto-defaulted team id to config so
// the next command resolves it from config instead of re-listing. A failure to
// persist is non-fatal — the in-process resolution already succeeded.
func (c *cmdContext) persistDefaultTeam(teamID string) {
	if c.cfg == nil || c.cfg.CurrentTeam != "" {
		return
	}
	c.cfg.CurrentTeam = teamID
	_ = c.cfg.Save()
}

// render writes v using the command's resolved output options. Any failure from
// the output layer (jq compile error, json encode failure, unknown format) is a
// runtime/output problem, not a flags-usage problem — wrap it in a *CLIError
// with ExitGeneric so it is NOT misclassified as a Cobra usage error (exit 2) by
// main.go's non-*CLIError → ExitUsage rule.
//
// We nil-check before wrapping: errs.Wrap returns a typed *CLIError nil when
// passed a nil error, and a typed-nil *CLIError returned from RunE becomes a
// non-nil error interface value, causing cobra's errors.Is chain to panic in
// (*CLIError).Unwrap when the receiver is a typed nil.
func (c *cmdContext) render(cmd *cobra.Command, v any) error {
	if err := output.Render(cmd.OutOrStdout(), v, c.out); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	return nil
}

// resolveFormat applies the --output value, defaulting to json off a TTY and
// table on a TTY when the flag is unset.
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

// isTTY reports whether w is an interactive terminal. Only *os.File can be a
// TTY; anything else (pipe, buffer in tests) is treated as non-interactive.
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

// confirmDestructive enforces the non-interactive safety contract: destructive
// operations require --yes. Off a TTY without --yes it returns exit code 5; on
// a TTY it prompts. With --yes it always proceeds.
func confirmDestructive(cmd *cobra.Command, prompt string) error {
	if flags.yes {
		return nil
	}
	if !isTTY(cmd.OutOrStdout()) {
		return errs.New(errs.ExitNeedsConfir,
			"%s — refusing in a non-interactive shell; pass --yes to proceed", prompt)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N]: ", prompt)
	var answer string
	_, _ = fmt.Fscanln(cmd.InOrStdin(), &answer)
	switch answer {
	case "y", "Y", "yes", "YES":
		return nil
	default:
		return errs.New(errs.ExitNeedsConfir, "aborted")
	}
}
