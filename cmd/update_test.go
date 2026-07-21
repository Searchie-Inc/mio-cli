package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateCommandInvokesInstallerWithPrefixAndVersion(t *testing.T) {
	resetGlobalFlags()
	// update now runs refreshManagedSkills after a (mocked) successful update.
	// Isolate HOME and CODEX_HOME to temp dirs so the test can never touch — let
	// alone refresh — a real managed skill install in the developer's or CI home.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	oldRunner := selfUpdateRunner
	t.Cleanup(func() { selfUpdateRunner = oldRunner })

	var got updateOptions
	selfUpdateRunner = func(_ context.Context, opts updateOptions, _ io.Writer, _ io.Writer) error {
		got = opts
		return nil
	}

	root := RootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"update", "--prefix", "/tmp/mio-bin", "--version", "0.2.1"})
	defer root.SetArgs(nil)

	if err := root.Execute(); err != nil {
		t.Fatalf("update returned error: %v", err)
	}
	if got.Prefix != "/tmp/mio-bin" {
		t.Errorf("Prefix = %q, want /tmp/mio-bin", got.Prefix)
	}
	if got.Version != "0.2.1" {
		t.Errorf("Version = %q, want 0.2.1", got.Version)
	}
}

func TestUpdateCommandDefaultsPrefixToExecutableDir(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	got, err := resolveUpdatePrefix("")
	if err != nil {
		t.Fatalf("resolveUpdatePrefix: %v", err)
	}
	if got != filepath.Dir(exe) {
		t.Errorf("default prefix = %q, want executable dir %q", got, filepath.Dir(exe))
	}
}

func TestUpdateCommandIsRegistered(t *testing.T) {
	cmd, _, err := RootCmd().Find([]string{"update"})
	if err != nil {
		t.Fatalf("find update command: %v", err)
	}
	if cmd == nil || cmd.Use == "mio" {
		t.Fatalf("update command was not registered")
	}
}
