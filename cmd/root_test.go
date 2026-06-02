package cmd

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// codeForExecuteErr mirrors main.exitCodeFor: a *CLIError keeps its code; any
// other error returned from Execute() is a Cobra usage error → ExitUsage. Kept
// in sync with main.go by the assertions below.
func codeForExecuteErr(err error) int {
	if err == nil {
		return errs.ExitOK
	}
	var ce *errs.CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return errs.ExitUsage
}

// runRoot executes the real root command tree against args and returns the
// error, with stdout/stderr captured so the test stays quiet.
func runRoot(t *testing.T, args ...string) error {
	t.Helper()
	root := RootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	defer root.SetArgs(nil)
	return root.Execute()
}

// TestExitCode_UsageErrorsMapToExitUsage verifies the central exit-code contract:
// every Cobra usage error (flag parse, required-flag, arg count, unknown command)
// resolves to ExitUsage (2), and a real *CLIError keeps its own code.
func TestExitCode_UsageErrorsMapToExitUsage(t *testing.T) {
	// A throwaway subcommand with a required flag + NoArgs, registered only for
	// this test, so we exercise required-flag and arg-count paths deterministically
	// without depending on any specific resource command's flags.
	probe := &cobra.Command{
		Use:  "probe-usage-error",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return nil },
	}
	probe.Flags().String("must", "", "required probe flag")
	_ = probe.MarkFlagRequired("must")
	rootCmd.AddCommand(probe)
	defer rootCmd.RemoveCommand(probe)

	cases := []struct {
		name string
		args []string
	}{
		{"required-flag-missing", []string{"probe-usage-error"}},
		{"unknown-flag", []string{"probe-usage-error", "--nope", "x", "--must", "y"}},
		{"bad-arg-count", []string{"probe-usage-error", "extra", "--must", "y"}},
		{"unknown-command", []string{"frobnicate-nonexistent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runRoot(t, tc.args...)
			if err == nil {
				t.Fatalf("expected an error for %v, got nil", tc.args)
			}
			if got := codeForExecuteErr(err); got != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); err=%v", got, errs.ExitUsage, err)
			}
		})
	}
}

// TestExitCode_FlagErrorFuncWrapsAsCLIError verifies the SetFlagErrorFunc hook
// turns flag-parse errors into a *CLIError carrying ExitUsage (so the code is
// correct even before main.go's fallback).
func TestExitCode_FlagErrorFuncWrapsAsCLIError(t *testing.T) {
	err := runRoot(t, "version", "--definitely-not-a-flag")
	if err == nil {
		t.Fatal("expected a flag error, got nil")
	}
	var ce *errs.CLIError
	if !errors.As(err, &ce) {
		t.Fatalf("flag error should be a *CLIError, got %T: %v", err, err)
	}
	if ce.Code != errs.ExitUsage {
		t.Errorf("flag error code = %d, want %d (ExitUsage)", ce.Code, errs.ExitUsage)
	}
}
