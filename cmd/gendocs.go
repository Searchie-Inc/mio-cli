package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var genDocsDir string

var genDocsCmd = &cobra.Command{
	Use:    "gen-docs",
	Short:  "Generate Markdown command reference into a directory.",
	Long:   "Write one Markdown file per command (and sub-command) to --dir using cobra/doc.GenMarkdownTree.",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if genDocsDir == "" {
			return errs.New(errs.ExitUsage, "--dir is required")
		}

		if err := os.MkdirAll(genDocsDir, 0o755); err != nil {
			return errs.Wrap(errs.ExitGeneric, fmt.Errorf("creating output directory %q: %w", genDocsDir, err))
		}

		if err := doc.GenMarkdownTree(rootCmd, genDocsDir); err != nil {
			return errs.Wrap(errs.ExitGeneric, fmt.Errorf("generating markdown docs: %w", err))
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Docs written to %s\n", genDocsDir)
		return nil
	},
}

func init() {
	genDocsCmd.Flags().StringVar(&genDocsDir, "dir", "", "Directory to write Markdown files into (created if absent).")
	rootCmd.AddCommand(genDocsCmd)
}
