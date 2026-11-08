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
	registerCmd.Flags().String("email", "", "Email address for the new account. Overrides MIO_EMAIL.")
	registerCmd.Flags().String("password", "", "Password for the new account. Overrides MIO_PASSWORD. Never echoed or stored.")
	registerCmd.Flags().String("first-name", "", "First name for the new account (optional).")
	registerCmd.Flags().String("last-name", "", "Last name for the new account (optional).")
	rootCmd.AddCommand(registerCmd)
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Create a new mio account and log in.",
	Long: `Create a new mio account via email + password, then log in as that account.

Registration is unauthenticated — it does NOT require an existing API key. On
success the backend provisions the account (and a personal team), and the CLI
mints a "mio-cli@<host>" API key for that team and stores it in the credential
store (see 'mio login --help'), exactly like ` + "`mio login`" + `. Your
password is never echoed or saved.

Because it logs you in, register REPLACES any API key already stored — you end
up authenticated as the newly-created account. mio keeps ONE key, so when one is
already stored register ASKS first (before the account is created): y/N on a
terminal, otherwise exit 5 unless --yes. It does not look the stored key up, so
that key is never sent to the API base this run points at. If that key is your
only way into its account (no password set), export it first:
MIO_API_KEY="$(mio auth token)"; export MIO_API_KEY.

Resolution order:
  1. If --email and --password are set (or MIO_EMAIL/MIO_PASSWORD env vars),
     registration runs non-interactively: no TTY prompts.
  2. Otherwise, on a TTY, you are prompted for email, password (typed twice to
     confirm), and optional first/last name.

Off a TTY with no --email/--password (or env), register exits with code 2.`,
	Example: `  # Interactive
  mio register

  # Headless (CI / scripting)
  mio register --email you@example.com --password 's3cr3tpass' --first-name Ada --last-name Lovelace

  # Headless via env vars
  MIO_EMAIL=you@example.com MIO_PASSWORD='s3cr3tpass' mio register`,
	Args: cobra.NoArgs,
	RunE: runRegister,
}

func runRegister(cmd *cobra.Command, _ []string) error {
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
	if err != nil && !config.StoredKeyUnusable(err) {
		return errs.Wrap(errs.ExitGeneric, err)
	}

	email := firstNonEmpty(flagValue(cmd, "email"), os.Getenv(envEmail))
	password, _ := cmd.Flags().GetString("password")
	if password == "" {
		password = os.Getenv(envPassword)
	}
	firstName, _ := cmd.Flags().GetString("first-name")
	lastName, _ := cmd.Flags().GetString("last-name")

	mintTeam := flags.team

	if email != "" && password != "" {
		if err := confirmReplaceStoredKey(cmd, resolved.APIBase, "", cmd.InOrStdin(), false); err != nil {
			return err
		}
		return registerAndLogin(cmd, resolved.APIBase, mintTeam, email, password, firstName, lastName, cfg)
	}

	if !isTTY(cmd.ErrOrStderr()) {
		return errs.New(errs.ExitUsage,
			"register needs --email and --password (or MIO_EMAIL/MIO_PASSWORD) when not run interactively")
	}

	if err := confirmReplaceStoredKey(cmd, resolved.APIBase, "", cmd.InOrStdin(), false); err != nil {
		return err
	}

	email, password, firstName, lastName, err = promptRegistration(cmd, email, firstName, lastName)
	if err != nil {
		return err
	}
	return registerAndLogin(cmd, resolved.APIBase, mintTeam, email, password, firstName, lastName, cfg)
}

func promptRegistration(cmd *cobra.Command, email, firstName, lastName string) (string, string, string, string, error) {
	reader := bufio.NewReader(cmd.InOrStdin())

	if email == "" {
		fmt.Fprint(cmd.ErrOrStderr(), "Email: ")
		line, _ := reader.ReadString('\n')
		email = strings.TrimSpace(line)
	}
	if email == "" {
		return "", "", "", "", errs.New(errs.ExitUsage, "no email entered")
	}

	fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
	password, err := readSecret(cmd, reader)
	if err != nil {
		return "", "", "", "", errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintln(cmd.ErrOrStderr())
	if password == "" {
		return "", "", "", "", errs.New(errs.ExitUsage, "no password entered")
	}

	fmt.Fprint(cmd.ErrOrStderr(), "Confirm password: ")
	confirm, err := readSecret(cmd, reader)
	if err != nil {
		return "", "", "", "", errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintln(cmd.ErrOrStderr())
	if confirm != password {
		return "", "", "", "", errs.New(errs.ExitUsage, "passwords do not match")
	}

	if firstName == "" {
		fmt.Fprint(cmd.ErrOrStderr(), "First name (optional): ")
		line, _ := reader.ReadString('\n')
		firstName = strings.TrimSpace(line)
	}
	if lastName == "" {
		fmt.Fprint(cmd.ErrOrStderr(), "Last name (optional): ")
		line, _ := reader.ReadString('\n')
		lastName = strings.TrimSpace(line)
	}

	return email, password, firstName, lastName, nil
}

func registerAndLogin(cmd *cobra.Command, apiBase, teamID, email, password, firstName, lastName string, cfg *config.Config) error {
	if email == "" || password == "" {
		return errs.New(errs.ExitUsage, "register requires both an email and a password")
	}

	cli := client.New(apiBase, "", client.WithDebug(flags.debug))
	regRes, err := cli.Register(cmd.Context(), email, password, firstName, lastName)
	if err != nil {
		return err
	}

	displayTeam, err := mintAndStore(cmd, cli, regRes.AccessToken, teamID, cfg, noStoredKeyGuard)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Registered %s. Logged in. Using team %s. Saved API key.\n", email, displayTeam)
	return nil
}

func readSecret(cmd *cobra.Command, reader *bufio.Reader) (string, error) {
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		return string(b), err
	}
	line, err := reader.ReadString('\n')
	return strings.TrimSpace(line), err
}
