package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/version"
)

// runSkills drives the root command with the given args in-process, isolating
// flag state, and returns captured stdout/stderr and the execution error.
func runSkills(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	resetGlobalFlags()
	root := RootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	t.Cleanup(func() { root.SetArgs(nil) })
	err = root.Execute()
	return out.String(), errb.String(), err
}

func TestSkillsInstall_WritesClaudeUserTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, _, err := runSkills(t, "skills", "install")
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	path := filepath.Join(home, ".claude", "skills", "mio", "SKILL.md")
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("expected skill at %s: %v", path, rerr)
	}
	body := string(data)
	if !strings.Contains(body, skillVersionKey+": "+version.Version) {
		t.Errorf("skill missing version marker %q:\n%s", skillVersionKey, body[:min(400, len(body))])
	}
	if !strings.Contains(body, skillHashKey+": ") {
		t.Errorf("skill missing content-hash marker %q", skillHashKey)
	}
	if !strings.Contains(body, "name: "+skillName) {
		t.Errorf("skill missing name frontmatter")
	}
	if !strings.Contains(body, "Membership.io CLI") {
		t.Errorf("skill body not present")
	}
	if !strings.Contains(stdout, path) {
		t.Errorf("stdout should report the install path, got: %q", stdout)
	}

	// Idempotent: a second identical install is a no-op, not an error.
	stdout2, _, err2 := runSkills(t, "skills", "install")
	if err2 != nil {
		t.Fatalf("second install: %v", err2)
	}
	if !strings.Contains(stdout2, "already up to date") {
		t.Errorf("second install should be a no-op, got: %q", stdout2)
	}
}

func TestSkillsInstall_ProjectScopeWritesDotClaude(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if _, _, err := runSkills(t, "skills", "install", "--project"); err != nil {
		t.Fatalf("install --project: %v", err)
	}
	path := filepath.Join(dir, ".claude", "skills", "mio", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected project skill at %s: %v", path, err)
	}
}

func TestSkillsInstall_CodexTarget(t *testing.T) {
	codex := t.TempDir()
	t.Setenv("CODEX_HOME", codex)

	if _, _, err := runSkills(t, "skills", "install", "--target", "codex"); err != nil {
		t.Fatalf("install --target codex: %v", err)
	}
	path := filepath.Join(codex, "skills", "mio", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected codex skill at %s: %v", path, err)
	}
	if !strings.Contains(string(data), skillVersionKey+": ") {
		t.Errorf("codex skill missing version marker")
	}
}

func TestSkillsInstall_UnknownTargetIsUsageError(t *testing.T) {
	if _, _, err := runSkills(t, "skills", "install", "--target", "vim"); err == nil {
		t.Fatal("expected an error for unknown --target")
	}
}

func TestSkillsInstall_UserAndProjectMutuallyExclusive(t *testing.T) {
	if _, _, err := runSkills(t, "skills", "install", "--user", "--project"); err == nil {
		t.Fatal("expected an error when both --user and --project are set")
	}
}

func TestSkillsPrint_EmitsBody(t *testing.T) {
	stdout, _, err := runSkills(t, "skills", "print")
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	if !strings.HasPrefix(stdout, "---\n") {
		t.Errorf("print output should start with frontmatter, got: %q", stdout[:min(40, len(stdout))])
	}
	if !strings.Contains(stdout, "name: "+skillName) {
		t.Errorf("print output missing name frontmatter")
	}
	if !strings.Contains(stdout, skillBody) {
		t.Errorf("print output missing the skill body")
	}
}

func TestRefreshManagedSkills_RefreshesUnmodifiedManagedInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex")) // isolate codex target

	// Simulate a prior install from an older CLI version: managed markers, but an
	// untouched body (hash matches).
	path := filepath.Join(home, ".claude", "skills", "mio", "SKILL.md")
	if err := writeSkillFile(path, renderSkill("0.0.1")); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if st, _ := classifySkillFile(path); st != skillManagedUnmodified {
		t.Fatalf("seed file should classify as managedUnmodified, got %v", st)
	}

	var buf bytes.Buffer
	refreshManagedSkills(&buf)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read refreshed: %v", err)
	}
	if !strings.Contains(string(data), skillVersionKey+": "+version.Version) {
		t.Errorf("refreshed skill should carry current version %q", version.Version)
	}
	if strings.Contains(string(data), skillVersionKey+": 0.0.1") {
		t.Errorf("refreshed skill still carries the old version")
	}
	if !strings.Contains(buf.String(), "Refreshed") {
		t.Errorf("expected a refresh message, got: %q", buf.String())
	}
}

func TestRefreshManagedSkills_NeverClobbersHandEditedInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	// A managed install the user hand-edited: body hash no longer matches.
	edited := strings.Replace(renderSkill("0.0.1"),
		"# mio — Membership.io CLI", "# HAND EDITED", 1)
	path := filepath.Join(home, ".claude", "skills", "mio", "SKILL.md")
	if err := writeSkillFile(path, edited); err != nil {
		t.Fatalf("seed edited install: %v", err)
	}
	if st, _ := classifySkillFile(path); st != skillManagedModified {
		t.Fatalf("edited file should classify as managedModified, got %v", st)
	}

	var buf bytes.Buffer
	refreshManagedSkills(&buf)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after refresh: %v", err)
	}
	if string(data) != edited {
		t.Errorf("hand-edited skill must not be clobbered")
	}
	if !strings.Contains(buf.String(), "edited locally") {
		t.Errorf("expected an 'edited locally' note, got: %q", buf.String())
	}
}

func TestRefreshManagedSkills_NudgesWhenNeverInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var buf bytes.Buffer
	refreshManagedSkills(&buf)

	if !strings.Contains(buf.String(), "mio skills install") {
		t.Errorf("expected an install nudge, got: %q", buf.String())
	}
}

func TestSkillsInstall_ForceOverwritesHandEdited(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	edited := strings.Replace(renderSkill("0.0.1"),
		"# mio — Membership.io CLI", "# HAND EDITED", 1)
	path := filepath.Join(home, ".claude", "skills", "mio", "SKILL.md")
	if err := writeSkillFile(path, edited); err != nil {
		t.Fatalf("seed edited install: %v", err)
	}

	// Without --force the install refuses.
	if _, _, err := runSkills(t, "skills", "install"); err == nil {
		t.Fatal("install over a hand-edited file should fail without --force")
	}

	// With --force it overwrites with the current managed content.
	if _, _, err := runSkills(t, "skills", "install", "--force"); err != nil {
		t.Fatalf("install --force: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "HAND EDITED") {
		t.Errorf("--force should have overwritten the hand-edited body")
	}
	if st, _ := classifySkillFile(path); st != skillManagedUnmodified {
		t.Errorf("after --force the install should be managedUnmodified, got %v", st)
	}
}

func TestSplitFrontmatter_RoundTripsRenderedSkill(t *testing.T) {
	content := renderSkill("1.2.3")
	fields, body, ok := splitFrontmatter(content)
	if !ok {
		t.Fatal("rendered skill should have parseable frontmatter")
	}
	if body != skillBody {
		t.Errorf("recovered body != embedded body")
	}
	if fields[skillVersionKey] != "1.2.3" {
		t.Errorf("version field = %q, want 1.2.3", fields[skillVersionKey])
	}
	if fields[skillHashKey] != sha256hex(skillBody) {
		t.Errorf("hash field does not match body hash")
	}
}
