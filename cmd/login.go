package cmd

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func resolveTeamID(cmd *cobra.Command, cli *client.Client, accessToken, flagTeamID string) (id, name string, err error) {
	if flagTeamID != "" {
		return flagTeamID, "", nil
	}

	if tokenTeam := client.TeamIDFromAccessToken(accessToken); tokenTeam != "" {
		return tokenTeam, "", nil
	}

	return resolveOwnedTeamID(cmd, cli, accessToken)
}

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
		return "", "", errs.New(errs.ExitAuth,
			"login succeeded but you don't own any team — minting an API key requires team ownership; ask the team owner to mint one for you, or re-run with --team <id> for a team you own")
	case 1:
		return owned[0].ID, owned[0].Name, nil
	default:
		if !isInteractiveStdin(cmd) {
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

The key is stored in an OS credential store when this build can open one,
otherwise in an encrypted file under the config dir — always the file for the
macOS release binaries, which are built without the Keychain backend. 'mio
whoami' names the store as key_source; 'mio auth token' prints the stored key.
Off a TTY with no resolvable key or credentials, login exits with code 3.

mio keeps ONE stored key. When logging in would replace a stored key that belongs
to a DIFFERENT team (or one the API rejects or cannot verify), login asks first,
before it mints a key: y/N on a terminal, otherwise exit 5 unless --yes. To name
the stored key's team it asks the API about it — only on an email+password login
(where the stored key is the one ordinary commands would send to this API base);
with --anonymous, or when you pass a key (--api-key / MIO_API_KEY), the stored
key is never sent and the question is generic. Declining leaves the stored key and current_team as
they were. (A second login running at the same moment can still overwrite the
key; the check is not a lock.)`,
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
		if !config.StoredKeyUnusable(err) {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Note: %v\nLogging in will replace it.\n", err)
	}

	if envKey := firstNonEmpty(flags.apiKey, os.Getenv(config.EnvAPIKey)); envKey != "" {
		return validateAndStore(cmd, resolved.APIBase, envKey, cmd.InOrStdin())
	}

	headlessEmail := flagValue(cmd, "email")
	if headlessEmail == "" {
		headlessEmail = os.Getenv(envEmail)
	}
	headlessPassword, _ := cmd.Flags().GetString("password")
	if headlessPassword == "" {
		headlessPassword = os.Getenv(envPassword)
	}
	if headlessEmail != "" && headlessPassword != "" {
		return loginPasswordHeadless(cmd, resolved.APIBase, resolved.TeamID, headlessEmail, headlessPassword, cfg, storedKeyGuardFor(cmd, resolved.APIBase, cmd.InOrStdin()))
	}

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

func loginPasswordHeadless(cmd *cobra.Command, apiBase, teamID, email, password string, cfg *config.Config, guard storedKeyGuard) error {
	cli := client.New(apiBase, "", client.WithDebug(flags.debug))
	loginRes, err := cli.Login(cmd.Context(), email, password)
	if err != nil {
		return err
	}

	displayTeam, err := mintAndStore(cmd, cli, loginRes.AccessToken, teamID, cfg, guard)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s. Using team %s. Saved API key.\n", email, displayTeam)
	return nil
}

func loginPasteKey(cmd *cobra.Command, reader *bufio.Reader, apiBase string) error {
	fmt.Fprint(cmd.ErrOrStderr(), "Paste your API key: ")
	line, _ := reader.ReadString('\n')
	key := strings.TrimSpace(line)
	if key == "" {
		return errs.New(errs.ExitUsage, "no key entered")
	}
	return validateAndStore(cmd, apiBase, key, reader)
}

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

	displayTeam, err := mintAndStore(cmd, cli, loginRes.AccessToken, teamID, cfg, storedKeyGuardFor(cmd, apiBase, reader))
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s. Using team %s. Saved API key.\n", email, displayTeam)
	return nil
}

func mintAndStore(cmd *cobra.Command, cli *client.Client, accessToken, flagTeamID string, cfg *config.Config, guard storedKeyGuard) (displayTeam string, err error) {
	resolvedTeam, teamName, err := resolveTeamID(cmd, cli, accessToken, flagTeamID)
	if err != nil {
		return "", err
	}
	if err := guard(resolvedTeam); err != nil {
		return "", err
	}

	keyName := fmt.Sprintf("mio-cli@%s", hostname())
	minted, err := cli.MintAPIKey(cmd.Context(), accessToken, resolvedTeam, keyName)
	if err != nil && flags.team == "" && errs.HTTPStatusOf(err) == http.StatusForbidden {
		fmt.Fprintln(cmd.ErrOrStderr(), "Note: your active team isn't one you own — checking which team(s) you do own.")
		var rerr error
		resolvedTeam, teamName, rerr = resolveOwnedTeamID(cmd, cli, accessToken)
		if rerr != nil {
			return "", rerr
		}
		if err := guard(resolvedTeam); err != nil {
			return "", err
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

func validateAndStore(cmd *cobra.Command, apiBase, key string, in io.Reader) error {
	cli := client.New(apiBase, key, client.WithDebug(flags.debug))
	if _, err := cli.Me(cmd.Context()); err != nil {
		return err
	}
	if stored, serr := config.GetAPIKey(); serr != nil || stored != key {
		if err := confirmReplaceStoredKey(cmd, apiBase, "", in, false); err != nil {
			return err
		}
	}
	if err := config.SetAPIKey(key); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Logged in: API key validated and stored.")
	return nil
}

func isInteractiveStdin(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

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
