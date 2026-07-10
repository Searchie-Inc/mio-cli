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
mints a "mio-cli@<host>" API key for that team and stores it in the OS keychain
(file fallback when none is available), exactly like ` + "`mio login`" + `. Your
password is never echoed or saved.

Because it logs you in, register REPLACES any API key already stored — you end
up authenticated as the newly-created account.

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
	// A legacy-credentials error means the stale blob was cleared; that is
	// irrelevant here since register mints a fresh key regardless. Any other
	// resolution error is fatal.
	if err != nil && !errors.Is(err, config.ErrLegacyCredentials) {
		return errs.Wrap(errs.ExitGeneric, err)
	}

	// Resolve credentials: flag > env. The password is NEVER echoed or stored.
	email := firstNonEmpty(flagValue(cmd, "email"), os.Getenv(envEmail))
	password, _ := cmd.Flags().GetString("password")
	if password == "" {
		password = os.Getenv(envPassword)
	}
	firstName, _ := cmd.Flags().GetString("first-name")
	lastName, _ := cmd.Flags().GetString("last-name")

	// The mint step must target the NEWLY registered account's team, which the
	// register token carries as a claim. So we honour only an explicit --team here
	// (flags.team) and never resolved.TeamID: the latter inherits a stale
	// current_team from config (left by a prior login), and resolveTeamID treats
	// any non-empty value as overriding the token claim — which would mint the key
	// on a team the new account does not belong to (403), breaking auto-login.
	mintTeam := flags.team

	// Headless when both email and password are already available.
	if email != "" && password != "" {
		return registerAndLogin(cmd, resolved.APIBase, mintTeam, email, password, firstName, lastName, cfg)
	}

	// Interactive registration requires an interactive terminal to prompt for the
	// missing fields. We key off the stderr stream — where the prompts are written
	// — so that off a terminal we cannot meaningfully prompt and instead return a
	// usage error (exit 2) that fires no HTTP request. Gating on a command stream
	// (rather than os.Stdin) also keeps the branch deterministic under `go test`,
	// which connects os.Stdin to /dev/null (a char device isTTY treats as a TTY);
	// confirmDestructive gates on cmd.OutOrStdout() for the same reason.
	if !isTTY(cmd.ErrOrStderr()) {
		return errs.New(errs.ExitUsage,
			"register needs --email and --password (or MIO_EMAIL/MIO_PASSWORD) when not run interactively")
	}

	email, password, firstName, lastName, err = promptRegistration(cmd, email, firstName, lastName)
	if err != nil {
		return err
	}
	return registerAndLogin(cmd, resolved.APIBase, mintTeam, email, password, firstName, lastName, cfg)
}

// promptRegistration collects the registration fields interactively on a TTY:
// email (if not already provided), password entered twice (never echoed) and
// required to match, and optional first/last name (only prompted when not
// already provided via flags). It performs no network I/O.
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

	// Optional names — prompt only when not already supplied via flags.
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

// registerAndLogin creates the account and then auto-logs-in. The register route
// returns the same token payload as login, so its access token feeds straight
// into the shared mint-and-store flow — no second login round-trip. The password
// is never stored.
func registerAndLogin(cmd *cobra.Command, apiBase, teamID, email, password, firstName, lastName string, cfg *config.Config) error {
	// Defensive guard: never fire a request with a missing credential.
	if email == "" || password == "" {
		return errs.New(errs.ExitUsage, "register requires both an email and a password")
	}

	cli := client.New(apiBase, "", client.WithDebug(flags.debug))
	regRes, err := cli.Register(cmd.Context(), email, password, firstName, lastName)
	if err != nil {
		return err
	}

	displayTeam, err := mintAndStore(cmd, cli, regRes.AccessToken, teamID, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Registered %s. Logged in. Using team %s. Saved API key.\n", email, displayTeam)
	return nil
}

// readSecret reads a secret without echoing when stdin is a real terminal,
// falling back to a plain line read from the shared reader otherwise.
//
// Unlike the package-level readPassword, it reads the non-terminal fallback from
// a CALLER-OWNED bufio.Reader. register prompts for a password AND a
// confirmation (and optional names) in sequence; wrapping cmd.InOrStdin() in a
// fresh bufio per call would let the first read buffer — and then discard —
// input meant for the next, so piped/non-TTY sequences must share one reader.
func readSecret(cmd *cobra.Command, reader *bufio.Reader) (string, error) {
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		return string(b), err
	}
	line, err := reader.ReadString('\n')
	return strings.TrimSpace(line), err
}
