package cmd

// auth.go — `mio auth token` (MIO-2995).
//
// Prints the API key `mio login` stored, and nothing else, so a headless
// session can export it ONCE and stop touching the credential store:
//
//	export MIO_API_KEY="$(mio auth token)"
//
// It is the `gh auth token` shape on purpose — that is the verb agents already
// reach for. It lives in its own `auth` group rather than on an existing
// command because every existing home is wrong for it: `api-keys` manages
// SERVER-side keys and promises the secret is never re-exposed after create;
// `whoami` needs the network and renders a JSON document; `config` is
// documented as never holding the key; and a bare root-level `token` would
// squat a generic name next to the JWT access token `login` also handles.
// login/logout/register/whoami stay at the root — moving them would break
// every script that calls them.
//
// Before this there was no non-interactive way out: the key sits in an
// encrypted file (or an OS store whose ACL admits only the mio binary), so no
// shell tool can read it back.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	authCmd.AddCommand(authTokenCmd)
	rootCmd.AddCommand(authCmd)
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Work with the credential `mio login` stored.",
}

var authTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Print the stored API key to stdout, for export into MIO_API_KEY.",
	Long: `Print the API key stored by 'mio login' (or 'mio register') to stdout, followed
by a newline, and nothing else — whatever --output says — so a script or agent
can export it once and stop reading the credential store:

  export MIO_API_KEY="$(mio auth token)"

It reads ONLY the stored key: --api-key and MIO_API_KEY are not echoed back.
If MIO_API_KEY is already set to a different key, a note on stderr says so
(it takes precedence over the stored key everywhere else). It makes no
network request and does not check that the key is still valid — run
'mio whoami' for that.

Where the stored key lives depends on the build: the release binaries keep it
in an encrypted file under the config dir, $XDG_CONFIG_HOME/mio/keyring/api-key
or ~/.config/mio/keyring/api-key, except that Windows uses the Credential
Manager and a Linux session that can reach a Secret Service (or KWallet) on its
D-Bus session bus uses that. Only a macOS build compiled from source with cgo
uses the macOS Keychain. 'mio whoami' reports the one in use as key_source.

With no stored key it exits 3 with nothing on stdout, and the error names the
store it read. A stored key that does not decode, or whose key file is missing
or invalid, also exits 3 ('mio login' replaces it). A filesystem error while
reading the store (permission denied, I/O error) exits 1: re-authenticating
cannot fix that.`,
	Example: `  export MIO_API_KEY="$(mio auth token)"
  mio auth token >/dev/null && echo "a key is stored"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		key, store, err := config.LoadAPIKey()
		if err != nil {
			if config.StoredKeyUnusable(err) {
				return errs.Wrap(errs.ExitAuth, err)
			}
			return errs.Wrap(errs.ExitGeneric, err)
		}
		if key == "" {
			return errs.New(errs.ExitAuth, "no API key stored: run `mio login` to store one (%s)", store.MissingKeyDetail())
		}
		if env := os.Getenv(config.EnvAPIKey); env != "" && env != key {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"note: %s is already set to a different key, and takes precedence over the stored key printed here\n",
				config.EnvAPIKey)
		}
		fmt.Fprintln(cmd.OutOrStdout(), key)
		return nil
	},
}
