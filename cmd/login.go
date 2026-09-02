package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// resolveTeamID determines the team id to use when minting an API key after a
// successful email+password login. Precedence:
//
//  1. explicit --team flag (or config current_team already resolved).
//  2. team_id embedded in the JWT access token.
//  3. GET /api/teams with the JWT bearer, narrowed to the teams the caller
//     OWNS (resolveOwnedTeamID):
//     - exactly one owned team → use it automatically.
//     - multiple owned teams   → prompt on TTY; error with list on non-TTY.
//     - zero owned teams       → clear error.
//
// Neither (1) nor (2) is proof of OWNERSHIP — minting an API key is
// owner-gated server-side (mio-backend require_team_owner), while both a
// caller-supplied --team and the JWT's team_id claim (the account's
// last-active team, see TeamIDFromAccessToken) only require membership:
// `teams switch` can point the claim (and thus a STORED current_team, since
// that is exactly what `teams switch` persists) at a team the caller
// belongs to but does not own (MIO-3585, e.g. after switching into a shared
// team, or a web signup landing the account in a team it doesn't own with no
// owned team of its own). resolveTeamID does not itself check ownership for
// (1) or (2) — mintAndStore catches the resulting 403 and retries via
// resolveOwnedTeamID, which is where ownership is actually verified. Only a
// --team flag TRULY PASSED ON THIS INVOCATION (flags.team, checked directly
// in mintAndStore — never the config-resolved value threaded through here)
// is never second-guessed that way; if it names a team the caller doesn't
// own, MintAPIKey's error is left to speak for itself.
//
// Returns the resolved team id, its display name (may be empty if resolved
// from the token claim), and any error.
func resolveTeamID(cmd *cobra.Command, cli *client.Client, accessToken, flagTeamID string) (id, name string, err error) {
	// 1. Explicit flag / config.
	if flagTeamID != "" {
		return flagTeamID, "", nil
	}

	// 2. JWT claim.
	if tokenTeam := client.TeamIDFromAccessToken(accessToken); tokenTeam != "" {
		return tokenTeam, "", nil
	}

	// 3. List teams via API.
	return resolveOwnedTeamID(cmd, cli, accessToken)
}

// resolveOwnedTeamID lists the teams visible to the JWT bearer and narrows
// them to the ones the caller OWNS. Minting an API key is owner-gated
// server-side, so a team the caller merely belongs to can never succeed here
// (MIO-3585) — unlike resolveTeamID's claim/flag-based guesses, this is the
// path that actually confirms ownership, via each team's owner_id attribute
// compared against the JWT's own `sub` claim.
//
// This mirrors mio-backend's CURRENT require_team_owner check exactly
// (owner_id == sub, full stop). That dependency's own docstring flags itself
// as Phase 1 — a later Phase 1.5 is expected to widen it to consult
// team_members.role_id for multi-admin support. If that lands, this filter
// needs the same widening or it will start rejecting teams the API would
// accept for an admin who isn't the literal owner.
func resolveOwnedTeamID(cmd *cobra.Command, cli *client.Client, accessToken string) (id, name string, err error) {
	teams, lerr := cli.ListTeams(cmd.Context(), accessToken)
	if lerr != nil {
		return "", "", errs.Wrap(errs.ExitGeneric, fmt.Errorf("login succeeded but could not list teams: %w", lerr))
	}
	subject := client.SubjectFromAccessToken(accessToken)
	owned := make([]client.TeamInfo, 0, len(teams))
	for _, t := range teams {
		if subject != "" && t.OwnerID == subject {
			owned = append(owned, t)
		}
	}

	switch len(owned) {
	case 0:
		if len(teams) == 0 {
			return "", "", errs.New(errs.ExitGeneric,
				"login succeeded but your account has no teams — contact support")
		}
		return "", "", errs.New(errs.ExitUsage,
			"login succeeded but you don't own any team — minting an API key requires team ownership; ask the team owner to mint one for you, or re-run with --team <id> for a team you own")
	case 1:
		return owned[0].ID, owned[0].Name, nil
	default:
		// Multiple owned teams: prompt on TTY, error on non-TTY.
		if !isTTY(cmd.InOrStdin()) {
			var sb strings.Builder
			sb.WriteString("login succeeded but you own multiple teams — re-run with --team <id>:\n")
			for _, t := range owned {
				fmt.Fprintf(&sb, "  %s  %s\n", t.ID, t.Name)
			}
			return "", "", errs.New(errs.ExitUsage, "%s", strings.TrimRight(sb.String(), "\n"))
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "You own multiple teams. Choose one:")
		for i, t := range owned {
			fmt.Fprintf(cmd.ErrOrStderr(), "  [%d] %s  %s\n", i+1, t.ID, t.Name)
		}
		fmt.Fprint(cmd.ErrOrStderr(), "Team number: ")
		reader := bufio.NewReader(cmd.InOrStdin())
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		for i, t := range owned {
			if line == fmt.Sprintf("%d", i+1) {
				return t.ID, t.Name, nil
			}
		}
		return "", "", errs.New(errs.ExitUsage, "invalid choice")
	}
}

// EnvEmail and EnvPassword are the environment variables for headless login.
const (
	envEmail    = "MIO_EMAIL"
	envPassword = "MIO_PASSWORD"
)

func init() {
	loginCmd.Flags().String("email", "", "Email address for headless (non-interactive) login. Overrides MIO_EMAIL.")
	loginCmd.Flags().String("password", "", "Password for headless (non-interactive) login. Overrides MIO_PASSWORD. Never echoed or stored.")
	rootCmd.AddCommand(loginCmd, logoutCmd)
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate the CLI and store an API key.",
	Long: `Authenticate the mio CLI.

Resolution order:
  1. If MIO_API_KEY is set (or --api-key flag), it is validated and stored.
  2. If --email and --password are set (or MIO_EMAIL/MIO_PASSWORD env vars),
     login runs non-interactively: no TTY prompts, password is never echoed
     or saved.
  3. Otherwise, on a TTY, you are offered two paths:
       (a) paste an existing mio_sk_... API key, or
       (b) email + password → the CLI mints a key named "mio-cli@<host>"
           on your team and stores it (your password is never saved).

The key is stored in the OS keychain (file fallback when none is available).
Off a TTY with no resolvable key or credentials, login exits with code 3.`,
	Args: cobra.NoArgs,
	RunE: runLogin,
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Delete the stored API key.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := config.DeleteAPIKey(); err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Logged out: stored API key deleted.")
		return nil
	},
}

func runLogin(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	resolved, err := cfg.Resolve(config.Overrides{
		APIKey:  flags.apiKey,
		APIBase: flags.apiBase,
		TeamID:  flags.team,
		Profile: flags.profile,
	})
	if err != nil {
		if errors.Is(err, config.ErrLegacyCredentials) {
			// The stale blob has already been deleted; resolved is populated with
			// APIBase/TeamID but no key.  Inform the user once and fall through to
			// the interactive login prompt as if no key was stored.
			fmt.Fprintln(cmd.ErrOrStderr(), "Note: your stored credentials used an old format and have been cleared. Please log in again.")
		} else {
			return errs.Wrap(errs.ExitGeneric, err)
		}
	}

	// Path 1: env / flag key — validate and store.
	if envKey := firstNonEmpty(flags.apiKey, os.Getenv(config.EnvAPIKey)); envKey != "" {
		return validateAndStore(cmd, resolved.APIBase, envKey)
	}

	// Path 2: headless email+password (--email/--password flags or MIO_EMAIL/MIO_PASSWORD env).
	// Resolve email: flag > env.
	headlessEmail := flagValue(cmd, "email")
	if headlessEmail == "" {
		headlessEmail = os.Getenv(envEmail)
	}
	// Resolve password: flag > env. Password is NEVER echoed or stored.
	headlessPassword, _ := cmd.Flags().GetString("password")
	if headlessPassword == "" {
		headlessPassword = os.Getenv(envPassword)
	}
	if headlessEmail != "" && headlessPassword != "" {
		return loginPasswordHeadless(cmd, resolved.APIBase, resolved.TeamID, headlessEmail, headlessPassword, cfg)
	}

	// Interactive paths require a TTY. Off a TTY we cannot prompt → exit 3.
	if !isTTY(os.Stdin) {
		return errs.New(errs.ExitAuth,
			"no API key available and not a TTY: set MIO_API_KEY, or provide --email and --password (or MIO_EMAIL/MIO_PASSWORD), or run `mio login` interactively")
	}

	fmt.Fprintln(cmd.ErrOrStderr(), "How would you like to authenticate?")
	fmt.Fprintln(cmd.ErrOrStderr(), "  [1] Paste an existing API key")
	fmt.Fprintln(cmd.ErrOrStderr(), "  [2] Email + password (mint a new key)")
	fmt.Fprint(cmd.ErrOrStderr(), "Choose [1/2]: ")

	reader := bufio.NewReader(cmd.InOrStdin())
	choice, _ := reader.ReadString('\n')
	switch strings.TrimSpace(choice) {
	case "1":
		return loginPasteKey(cmd, reader, resolved.APIBase)
	case "2":
		return loginPassword(cmd, reader, resolved.APIBase, resolved.TeamID, cfg)
	default:
		return errs.New(errs.ExitUsage, "invalid choice; expected 1 or 2")
	}
}

// loginPasswordHeadless performs the email+password → mint flow without any TTY
// prompts. Used when both --email/MIO_EMAIL and --password/MIO_PASSWORD are set.
// The password is never echoed or stored.
func loginPasswordHeadless(cmd *cobra.Command, apiBase, teamID, email, password string, cfg *config.Config) error {
	cli := client.New(apiBase, "", client.WithDebug(flags.debug))
	loginRes, err := cli.Login(cmd.Context(), email, password)
	if err != nil {
		return err
	}

	displayTeam, err := mintAndStore(cmd, cli, loginRes.AccessToken, teamID, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s. Using team %s. Saved API key.\n", email, displayTeam)
	return nil
}

// loginPasteKey reads a pasted key, validates it, and stores it.
func loginPasteKey(cmd *cobra.Command, reader *bufio.Reader, apiBase string) error {
	fmt.Fprint(cmd.ErrOrStderr(), "Paste your API key: ")
	line, _ := reader.ReadString('\n')
	key := strings.TrimSpace(line)
	if key == "" {
		return errs.New(errs.ExitUsage, "no key entered")
	}
	return validateAndStore(cmd, apiBase, key)
}

// loginPassword performs the email+password → mint flow.
func loginPassword(cmd *cobra.Command, reader *bufio.Reader, apiBase, teamID string, cfg *config.Config) error {
	fmt.Fprint(cmd.ErrOrStderr(), "Email: ")
	emailLine, _ := reader.ReadString('\n')
	email := strings.TrimSpace(emailLine)

	fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
	password, err := readPassword(cmd)
	if err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintln(cmd.ErrOrStderr())

	cli := client.New(apiBase, "", client.WithDebug(flags.debug))
	loginRes, err := cli.Login(cmd.Context(), email, password)
	if err != nil {
		return err
	}

	displayTeam, err := mintAndStore(cmd, cli, loginRes.AccessToken, teamID, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s. Using team %s. Saved API key.\n", email, displayTeam)
	return nil
}

// mintAndStore resolves the team for the given JWT access token, mints a
// "mio-cli@<host>" API key, stores it in the OS keychain, persists the resolved
// team to config, and returns a human display string for the team ("Name (id)"
// or the bare id when the name is unknown). It is the shared tail of every
// password→key flow — `login` (interactive + headless) and `register` — which
// differ only in their final confirmation wording.
func mintAndStore(cmd *cobra.Command, cli *client.Client, accessToken, flagTeamID string, cfg *config.Config) (displayTeam string, err error) {
	resolvedTeam, teamName, err := resolveTeamID(cmd, cli, accessToken, flagTeamID)
	if err != nil {
		return "", err
	}

	keyName := fmt.Sprintf("mio-cli@%s", hostname())
	minted, err := cli.MintAPIKey(cmd.Context(), accessToken, resolvedTeam, keyName)
	// Gate the retry on the raw --team FLAG (flags.team), never on flagTeamID:
	// flagTeamID is often the CONFIG-resolved team (login passes
	// resolved.TeamID, which falls back to a stored current_team — exactly
	// what `teams switch` persists), and that stored default is just as
	// unproven a guess as the JWT claim itself. Only a --team truly passed on
	// THIS invocation is a deliberate choice worth never second-guessing.
	if err != nil && flags.team == "" && errs.HTTPStatusOf(err) == http.StatusForbidden {
		// resolvedTeam came from a guess (the JWT's team_id claim, a stored
		// current_team, or an unfiltered team list) that only proves
		// MEMBERSHIP; minting is owner-gated, and this 403 means the guess
		// wasn't owned (MIO-3585). Recover by asking explicitly which teams
		// the caller OWNS rather than surfacing the backend's raw ownership
		// rejection.
		fmt.Fprintln(cmd.ErrOrStderr(), "Note: your active team isn't one you own — checking which team(s) you do own.")
		var rerr error
		resolvedTeam, teamName, rerr = resolveOwnedTeamID(cmd, cli, accessToken)
		if rerr != nil {
			return "", rerr
		}
		minted, err = cli.MintAPIKey(cmd.Context(), accessToken, resolvedTeam, keyName)
	}
	if err != nil {
		return "", err
	}
	secret, _ := minted.Attributes["secret"].(string)
	if secret == "" {
		return "", errs.New(errs.ExitGeneric, "key minted but no secret returned by the server")
	}

	if err := config.SetAPIKey(secret); err != nil {
		return "", errs.Wrap(errs.ExitGeneric, err)
	}
	cfg.CurrentTeam = resolvedTeam
	if err := cfg.Save(); err != nil {
		return "", errs.Wrap(errs.ExitGeneric, err)
	}

	displayTeam = resolvedTeam
	if teamName != "" {
		displayTeam = fmt.Sprintf("%s (%s)", teamName, resolvedTeam)
	}
	return displayTeam, nil
}

// validateAndStore validates a key against /api/auth/me and, on success, stores
// it in the keychain.
func validateAndStore(cmd *cobra.Command, apiBase, key string) error {
	cli := client.New(apiBase, key, client.WithDebug(flags.debug))
	if _, err := cli.Me(cmd.Context()); err != nil {
		return err
	}
	if err := config.SetAPIKey(key); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Logged in: API key validated and stored.")
	return nil
}

// readPassword reads a password without echoing when stdin is a terminal,
// falling back to a plain line read otherwise.
func readPassword(cmd *cobra.Command) (string, error) {
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		return string(b), err
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	return strings.TrimSpace(line), err
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}
