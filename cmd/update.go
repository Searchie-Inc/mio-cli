package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const installScriptURL = "https://raw.githubusercontent.com/Searchie-Inc/mio-cli/main/scripts/install.sh"

type updateOptions struct {
	Prefix  string
	Version string
}

var updateFlags updateOptions

var selfUpdateRunner = defaultSelfUpdateRunner

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update mio to the latest released version.",
	Long: `Update mio by running the official release installer.

By default this installs the latest released mio binary into the directory that
contains the currently running executable. Use --version to pin a specific
release, or --prefix to install into a different directory.`,
	Example: `  mio update
  mio update --version 0.2.1
  mio update --prefix "$HOME/.local/bin"`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		prefix, err := resolveUpdatePrefix(updateFlags.Prefix)
		if err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}

		opts := updateOptions{
			Prefix:  prefix,
			Version: strings.TrimSpace(updateFlags.Version),
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updating mio in %s...\n", opts.Prefix)
		if err := selfUpdateRunner(cmd.Context(), opts, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		// After a successful self-update, keep any managed agent skill in sync:
		// refresh an unmodified install, never clobber a hand-edited one, and
		// nudge if none is installed. Best-effort — never fails the update.
		refreshManagedSkills(cmd.OutOrStdout())
		return nil
	},
}

func init() {
	updateCmd.Flags().StringVar(&updateFlags.Version, "version", "", "Release version to install, e.g. 0.2.1. Defaults to latest.")
	updateCmd.Flags().StringVar(&updateFlags.Prefix, "prefix", "", "Install directory. Defaults to the current executable's directory.")
	rootCmd.AddCommand(updateCmd)
}

func resolveUpdatePrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix != "" {
		return prefix, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate current executable: %w", err)
	}
	return filepath.Dir(exe), nil
}

func defaultSelfUpdateRunner(ctx context.Context, opts updateOptions, stdout, stderr io.Writer) error {
	pipeline, err := installerPipeline()
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", pipeline)
	cmd.Env = updateEnvironment(os.Environ(), opts)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func installerPipeline() (string, error) {
	if _, err := exec.LookPath("curl"); err == nil {
		return "curl -fsSL " + shellSingleQuote(installScriptURL) + " | sh", nil
	}
	if _, err := exec.LookPath("wget"); err == nil {
		return "wget -qO- " + shellSingleQuote(installScriptURL) + " | sh", nil
	}
	return "", errors.New("curl or wget is required to update mio")
}

func updateEnvironment(base []string, opts updateOptions) []string {
	env := append([]string{}, base...)
	env = append(env, "PREFIX="+opts.Prefix)
	if opts.Version != "" {
		env = append(env, "VERSION="+opts.Version)
	}
	return env
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
