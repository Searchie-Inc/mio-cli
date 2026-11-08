package cmd

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

func TestExitCode_UsageErrorsMapToExitUsage(t *testing.T) {
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
