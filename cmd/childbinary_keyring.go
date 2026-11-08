//go:build mio_test_file_keyring

package cmd

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
