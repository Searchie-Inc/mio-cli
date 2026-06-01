package cmd

// gendocs.go registers a hidden `mio gen-docs` command that writes the full
// cobra command reference as Markdown files to a target directory.
//
// It is intentionally hidden (Hidden: true) so it does not appear in `mio
// --help` output but remains discoverable via `mio gen-docs --help`. This
// follows the same convention as the --debug flag in root.go.
//
// Usage:
//
//	mio gen-docs --dir ./docs/cli
//
// Each top-level command (and its sub-commands) produces one .md file.
// GenMarkdownTree handles the full tree rooted at rootCmd, so new resource
// files self-register as usual and their docs are automatically included
// without any changes here.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// genDocsDir holds the value of --dir for the gen-docs command.
var genDocsDir string

// genDocsCmd emits Markdown documentation for the full mio command tree.
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

		// Create the target directory if it does not exist yet.
		if err := os.MkdirAll(genDocsDir, 0o755); err != nil {
			return errs.Wrap(errs.ExitGeneric, fmt.Errorf("creating output directory %q: %w", genDocsDir, err))
		}

		// GenMarkdownTree walks the entire command tree rooted at rootCmd and
		// writes one .md file per command into genDocsDir.
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
