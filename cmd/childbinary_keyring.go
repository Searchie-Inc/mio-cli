//go:build mio_test_file_keyring

package cmd

// childbinary_keyring.go is compiled ONLY into the mio binary cmd's tests
// build and run as a child process (buildBinary passes -tags
// mio_test_file_keyring). Release builds (.goreleaser.yaml) and a plain
// `go build` never include it.
//
// TestMain pins the TEST process to the encrypted file keyring, but a child is
// a fresh process with the default backend list, and on a macOS cgo build, on
// Windows, or on a Linux desktop the first OS store it can open is the
// developer's real one: none of them is found through the HOME the test gives
// the child (MIO-2995). This pins the child the same way, and the hidden
// command lets the test read back the list the child will actually use.

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/config"
)

func init() {
	config.UseFileBackendOnly()
	rootCmd.AddCommand(&cobra.Command{
		Use:    "__test-keyring-backends",
		Short:  "Print the keyring backends this test binary may open (test builds only).",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(config.KeyringBackends())
		},
	})
}
