package cmd

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const authPasswordRouteHint = "this API has no password-reset route (it lands with mio-backend MIO-3571); " +
	"check `mio version` and the API you are pointed at (`mio whoami` prints api_base)"

const authForgotPasswordNotConfiguredHint = "on this route a 500 is the API saying password reset is not configured on that deployment " +
	"(no reset landing page set), not a transient failure: report it, retrying will not help"

const authResetPasswordExpiredHint = "the reset link expired (they last one hour), was already used, or is not one this API issued: " +
	"request a fresh one with `mio auth forgot-password --email <addr>` (redeeming a link expires every other one)"

const authResetPasswordRejectedHint = "the API rejected the new password: it must be at least 8 characters; " +
	"the token must be the value after `#` in the emailed link (or the whole link)"

func init() {
	authForgotPasswordCmd.Flags().String("email", "", "Email address of the account to recover. Prompted for on a terminal when omitted.")
	authResetPasswordCmd.Flags().String("token", "", "The token from the reset email — the value after `#` in the link — or the whole link, quoted.")
	authCmd.AddCommand(authForgotPasswordCmd, authResetPasswordCmd)
}

var authForgotPasswordCmd = &cobra.Command{
	Use:   "forgot-password",
	Short: "Email a password reset link to a platform user.",
	Long: `Ask the API to email a password reset link to a platform user (a team owner
or staff account — the kind 'mio login' signs in), for when the password is
lost and there is no browser to recover it with.

Unauthenticated: it sends no API key, even when one is stored or exported, so
it works from the very session that is locked out. The API always answers as
if the email was sent, whether or not the address has an account, and this
command reports the same — it never reveals whether an account exists.

The email carries a link ending in '#<token>'. The token is single-use and
valid for ONE HOUR; asking again does not cancel an earlier link, but
redeeming any one of them expires the rest. Hand it to
'mio auth reset-password --token', then run 'mio login' with the new
password. The reset signs the account out of every existing session; an API
key already stored by 'mio login' keeps working.

Rate limited by the API to 5 requests an hour per IP (exit 6). A 500 means
the deployment has no reset landing page configured — not a transient failure.
A 404 means the API you are pointed at has no password-reset route yet
(it lands with mio-backend MIO-3571).`,
	Example: `  mio auth forgot-password --email you@example.com
  # then, with the token from the email (the part after # in the link):
  mio auth reset-password --token 'https://admin.example.com/reset-password#<token>'
  mio login`,
	Args: cobra.NoArgs,
	RunE: runAuthForgotPassword,
}

var authResetPasswordCmd = &cobra.Command{
	Use:   "reset-password",
	Short: "Set a new password with the token from a reset email.",
	Long: `Set a new password for a platform user with the token from a password reset
email (see 'mio auth forgot-password').

--token takes the bare token or the WHOLE link from the email, quoted: the
token is the part after '#' (the URL fragment), and the command extracts it.
A link with nothing after '#' is a usage error (exit 2) before any request —
a mail client or browser stripped the fragment; copy the link from the email
itself.

The new password is NEVER taken on the command line (there is no --password
flag: it would land in shell history and in 'ps'). On a terminal it is
prompted for twice, unechoed. Headless, set MIO_PASSWORD — the variable
'mio login' and 'mio register' read a password from too — so the reset and
the login after it can share one exported secret; with neither a terminal nor
MIO_PASSWORD the command exits 2 without a request.

Unauthenticated: it sends no API key, even when one is stored or exported.

On success (the API's 204) the account's every existing session is signed out
and NOTHING is minted or stored: run 'mio login' next. An API key already
stored by 'mio login' is not a session and keeps working.

Failures, in the API's words plus a hint:
  410  the token expired (one hour), was already used, was expired by another
       link being redeemed, or is unknown — the API does not say which; run
       'mio auth forgot-password' again. Its envelope carries code
       'password_reset_expired'.
  422  the password is under 8 characters (exit 2).
  404  the API has no password-reset route yet (mio-backend MIO-3571).`,
	Example: `  # Paste the whole link from the email (quoted: it contains #)
  mio auth reset-password --token 'https://admin.example.com/reset-password#<token>'

  # Or just the token
  mio auth reset-password --token '<token>'

  # Headless: the new password from the environment, never on argv
  MIO_PASSWORD='a-new-password' mio auth reset-password --token '<token>'`,
	Args: cobra.NoArgs,
	RunE: runAuthResetPassword,
}

func authPasswordClient() (*client.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, err)
	}
	resolved, err := cfg.Resolve(config.Overrides{
		Anonymous: true,
		APIBase:   flags.apiBase,
		TeamID:    flags.team,
		Profile:   flags.profile,
	})
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, err)
	}
	return client.New(resolved.APIBase, "", client.WithDebug(flags.debug)), nil
}

func runAuthForgotPassword(cmd *cobra.Command, _ []string) error {
	email := strings.TrimSpace(flagValue(cmd, "email"))
	if email == "" {
		if !isTTY(cmd.ErrOrStderr()) {
			return errs.New(errs.ExitUsage, "forgot-password needs --email when not run interactively")
		}
		fmt.Fprint(cmd.ErrOrStderr(), "Email: ")
		line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		email = strings.TrimSpace(line)
		if email == "" {
			return errs.New(errs.ExitUsage, "no email entered")
		}
	}

	cli, err := authPasswordClient()
	if err != nil {
		return err
	}
	if err := cli.ForgotPassword(cmd.Context(), email); err != nil {
		switch errs.HTTPStatusOf(err) {
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			return errs.NewHTTP(errs.HTTPStatusOf(err), "%w\nhint: %s", err, authPasswordRouteHint)
		case http.StatusInternalServerError:
			return errs.NewHTTP(errs.HTTPStatusOf(err), "%w\nhint: %s", err, authForgotPasswordNotConfiguredHint)
		}
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"If an account exists for %s, a password reset email is on its way. "+
			"Its link is valid for one hour and works once.\n"+
			"Next: mio auth reset-password --token '<the link, or the part after # in it>'\n", email)
	return nil
}

func runAuthResetPassword(cmd *cobra.Command, _ []string) error {
	token, err := resetTokenFromArg(flagValue(cmd, "token"))
	if err != nil {
		return err
	}

	password := os.Getenv(envPassword)
	if password == "" {
		if !isTTY(cmd.ErrOrStderr()) {
			return errs.New(errs.ExitUsage,
				"reset-password needs the new password from a terminal prompt or from %s when not run interactively (it is never taken on the command line)", envPassword)
		}
		password, err = promptNewPassword(cmd, bufio.NewReader(cmd.InOrStdin()))
		if err != nil {
			return err
		}
	}

	cli, err := authPasswordClient()
	if err != nil {
		return err
	}
	if err := cli.ResetPassword(cmd.Context(), token, password); err != nil {
		switch errs.HTTPStatusOf(err) {
		case http.StatusGone:
			return errs.NewHTTP(errs.HTTPStatusOf(err), "%w\nhint: %s", err, authResetPasswordExpiredHint)
		case http.StatusUnprocessableEntity:
			return errs.NewHTTP(errs.HTTPStatusOf(err), "%w\nhint: %s", err, authResetPasswordRejectedHint)
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			return errs.NewHTTP(errs.HTTPStatusOf(err), "%w\nhint: %s", err, authPasswordRouteHint)
		}
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(),
		"Password reset. Every existing session for the account was signed out; nothing was minted or stored.\n"+
			"Next: mio login (an API key already stored by mio login keeps working).")
	return nil
}

func resetTokenFromArg(arg string) (string, error) {
	raw := strings.TrimSpace(arg)
	if raw == "" {
		return "", errs.New(errs.ExitUsage, "--token is required: the value after # in the reset email's link, or the whole link (quoted)")
	}
	if i := strings.Index(raw, "#"); i >= 0 {
		token := strings.TrimSpace(raw[i+1:])
		if token == "" {
			return "", errs.New(errs.ExitUsage, "the link has nothing after #: the token is the URL fragment, which a mail client or browser may have dropped — copy the whole link from the email")
		}
		return token, nil
	}
	if strings.Contains(raw, "://") {
		return "", errs.New(errs.ExitUsage, "that link carries no #<token> fragment — copy the whole link from the email, including the part after #, and quote it")
	}
	return raw, nil
}

func promptNewPassword(cmd *cobra.Command, reader *bufio.Reader) (string, error) {
	fmt.Fprint(cmd.ErrOrStderr(), "New password: ")
	password, err := readSecret(cmd, reader)
	if err != nil {
		return "", errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintln(cmd.ErrOrStderr())
	if password == "" {
		return "", errs.New(errs.ExitUsage, "no password entered")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Confirm new password: ")
	confirm, err := readSecret(cmd, reader)
	if err != nil {
		return "", errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintln(cmd.ErrOrStderr())
	if confirm != password {
		return "", errs.New(errs.ExitUsage, "passwords do not match")
	}
	return password, nil
}
