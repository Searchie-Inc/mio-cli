package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	rootCmd.AddCommand(loginCmd, logoutCmd)
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate the CLI and store an API key.",
	Long: `Authenticate the mio CLI.

Resolution:
  1. If MIO_API_KEY is set, it is validated and stored.
  2. Otherwise, on a TTY, you are offered two paths:
       (a) paste an existing mio_sk_... API key, or
       (b) email + password → the CLI mints a key named "mio-cli@<host>"
           on your team and stores it (your password is never saved).

The key is stored in the OS keychain (file fallback when none is available).
Off a TTY with no resolvable key, login exits with code 3 and a JSON error.`,
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
		return errs.Wrap(errs.ExitGeneric, err)
	}

	// Path 1: env / flag key — validate and store.
	if envKey := firstNonEmpty(flags.apiKey, os.Getenv(config.EnvAPIKey)); envKey != "" {
		return validateAndStore(cmd, resolved.APIBase, envKey)
	}

	// Interactive paths require a TTY. Off a TTY we cannot prompt → exit 3.
	if !isTTY(os.Stdin) {
		return errs.New(errs.ExitAuth,
			"no API key available and not a TTY: set MIO_API_KEY or run `mio login` interactively")
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

	if teamID == "" {
		return errs.New(errs.ExitUsage,
			"login succeeded but no team id is set: re-run with --team <id> to mint a key")
	}

	keyName := fmt.Sprintf("mio-cli@%s", hostname())
	minted, err := cli.MintAPIKey(cmd.Context(), loginRes.AccessToken, teamID, keyName)
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
	cfg.CurrentTeam = teamID
	if err := cfg.Save(); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Logged in: minted key %q for team %s and stored it.\n", keyName, teamID)
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
