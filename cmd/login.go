package cmd

import (
	"bufio"
	"errors"
	"fmt"
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
//  3. GET /api/teams with the JWT bearer to enumerate the user's teams:
//     - exactly one → use it automatically.
//     - multiple    → prompt on TTY; error with list on non-TTY.
//     - zero        → clear error.
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
	teams, lerr := cli.ListTeams(cmd.Context(), accessToken)
	if lerr != nil {
		return "", "", errs.Wrap(errs.ExitGeneric, fmt.Errorf("login succeeded but could not list teams: %w", lerr))
	}
	switch len(teams) {
	case 0:
		return "", "", errs.New(errs.ExitGeneric,
			"login succeeded but your account has no teams — contact support")
	case 1:
		return teams[0].ID, teams[0].Name, nil
	default:
		// Multiple teams: prompt on TTY, error on non-TTY.
		if !isTTY(os.Stdin) {
			var sb strings.Builder
			sb.WriteString("login succeeded but multiple teams found — re-run with --team <id>:\n")
			for _, t := range teams {
				fmt.Fprintf(&sb, "  %s  %s\n", t.ID, t.Name)
			}
			return "", "", errs.New(errs.ExitUsage, "%s", strings.TrimRight(sb.String(), "\n"))
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Multiple teams found. Choose one:")
		for i, t := range teams {
			fmt.Fprintf(cmd.ErrOrStderr(), "  [%d] %s  %s\n", i+1, t.ID, t.Name)
		}
		fmt.Fprint(cmd.ErrOrStderr(), "Team number: ")
		reader := bufio.NewReader(cmd.InOrStdin())
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		for i, t := range teams {
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

	resolvedTeam, teamName, err := resolveTeamID(cmd, cli, loginRes.AccessToken, teamID)
	if err != nil {
		return err
	}

	keyName := fmt.Sprintf("mio-cli@%s", hostname())
	minted, err := cli.MintAPIKey(cmd.Context(), loginRes.AccessToken, resolvedTeam, keyName)
	if err != nil {
		return err
	}
	secret, _ := minted.Attributes["secret"].(string)
	if secret == "" {
		return errs.New(errs.ExitGeneric, "key minted but no secret returned by the server")
	}

	if err := config.SetAPIKey(secret); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	cfg.CurrentTeam = resolvedTeam
	if err := cfg.Save(); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}

	displayTeam := resolvedTeam
	if teamName != "" {
		displayTeam = fmt.Sprintf("%s (%s)", teamName, resolvedTeam)
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

	resolvedTeam, teamName, err := resolveTeamID(cmd, cli, loginRes.AccessToken, teamID)
	if err != nil {
		return err
	}

	keyName := fmt.Sprintf("mio-cli@%s", hostname())
	minted, err := cli.MintAPIKey(cmd.Context(), loginRes.AccessToken, resolvedTeam, keyName)
	if err != nil {
		return err
	}
	secret, _ := minted.Attributes["secret"].(string)
	if secret == "" {
		return errs.New(errs.ExitGeneric, "key minted but no secret returned by the server")
	}

	if err := config.SetAPIKey(secret); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	cfg.CurrentTeam = resolvedTeam
	if err := cfg.Save(); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}

	displayTeam := resolvedTeam
	if teamName != "" {
		displayTeam = fmt.Sprintf("%s (%s)", teamName, resolvedTeam)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s. Using team %s. Saved API key.\n", email, displayTeam)
	return nil
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
