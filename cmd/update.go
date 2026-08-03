package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	Long: `Update mio to the latest released binary.

By default this installs the latest released mio binary into the directory that
contains the currently running executable. Use --version to pin a specific
release, or --prefix to install into a different directory.

macOS and Linux re-run the official release installer. Windows updates natively
(download, SHA-256 verify against checksums.txt, then swap the .exe), so no Unix
shell and no curl are required.`,
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
			// Keep an exit code the runner already decided: the native Windows
			// updater returns ExitUsage for deterministic local-input failures
			// (bad --prefix/--version, unsupported arch, non-file destination)
			// that fire no request, and the contract says those exit 2. The Unix
			// install-script path only ever returns untyped exec errors, so it
			// still maps to ExitGeneric exactly as before (Codex review round 2).
			var ce *errs.CLIError
			if errors.As(err, &ce) {
				return ce
			}
			return errs.Wrap(errs.ExitGeneric, err)
		}
		// After a successful self-update, keep any managed agent skill in sync.
		// The refresh is delegated to the binary we just installed — this
		// process still holds the OLD embedded skill body and cannot render the
		// new one (MIO-2874). Best-effort: never fails the update.
		refreshManagedSkills(cmd.OutOrStdout(), installedBinaryPath(opts.Prefix))
		return nil
	},
}

func init() {
	updateCmd.Flags().StringVar(&updateFlags.Version, "version", "", "Release version to install, e.g. 0.2.1. Defaults to latest.")
	updateCmd.Flags().StringVar(&updateFlags.Prefix, "prefix", "", "Install directory. Defaults to the current executable's directory.")
	rootCmd.AddCommand(updateCmd)
}

// installedBinaryPath is where the updater just wrote the new binary. Returns ""
// when it is not a usable executable, so the caller reports the skill was left
// alone instead of silently leaving a stale one (MIO-2874).
func installedBinaryPath(prefix string) string {
	name := "mio"
	if runtime.GOOS == "windows" {
		name = "mio.exe"
	}
	p := filepath.Join(prefix, name)
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return ""
	}
	return p
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
	// Windows has no `sh` (and usually no `curl`), so the shell pipeline below
	// cannot run there at all — it failed with `exec: "sh": executable file not
	// found in %PATH%` and the Windows build could never self-update (MIO-2688).
	// Take the Go-native download+replace path instead. Every other platform
	// keeps re-running scripts/install.sh unchanged, including the darwin
	// Gatekeeper mitigation from MIO-2603.
	if useNativeUpdater(runtime.GOOS) {
		return runNativeUpdate(ctx, nativeUpdateConfig{
			Prefix:  opts.Prefix,
			Version: opts.Version,
			GOOS:    runtime.GOOS,
			GOARCH:  runtime.GOARCH,
			Out:     stdout,
		})
	}

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
