package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The agent-skill side of `mio update` (MIO-2874, MIO-2875).
//
// The skill file is what an agent reads to learn the CLI's surface, so a skill
// that disagrees with the binary beside it advertises verbs the binary does not
// have — and the failures look like CLI bugs rather than version skew.
//
// The load-bearing constraint: skillBody is //go:embed-ed, so the running
// process only ever holds ITS OWN version's content. It therefore cannot write a
// correct skill for a version it just installed, and must delegate to the new
// binary. Re-stamping the old body with the new version string would produce a
// file that lies about which verbs exist AND carries a matching content hash —
// no mismatch signal at all.

func seedManagedSkill(t *testing.T, path, ver string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := writeSkillFile(path, renderSkill(ver)); err != nil {
		t.Fatalf("seed managed skill: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	return string(data)
}

func claudeSkillPath(home string) string {
	return filepath.Join(home, ".claude", "skills", skillDirName, skillFileName)
}

// isolateSkillHome points both targets at a temp dir so nothing touches the
// developer's real ~/.claude or $CODEX_HOME.
func isolateSkillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	return home
}

func stubRefreshExec(t *testing.T, fn func(bin, target string) error) *[][2]string {
	t.Helper()
	calls := &[][2]string{}
	old := skillRefreshExec
	t.Cleanup(func() { skillRefreshExec = old })
	skillRefreshExec = func(bin, target string) error {
		*calls = append(*calls, [2]string{bin, target})
		return fn(bin, target)
	}
	return calls
}

// MIO-2874: the refresh must be performed BY the newly installed binary, not by
// the running one re-stamping its own embedded body.
func TestRefreshManagedSkills_DelegatesToTheNewBinary(t *testing.T) {
	home := isolateSkillHome(t)
	path := claudeSkillPath(home)
	before := seedManagedSkill(t, path, "0.12.1")

	const newBin = "/opt/mio/bin/mio"
	calls := stubRefreshExec(t, func(_, target string) error {
		if target != "claude" {
			return nil
		}
		// Stand in for the new binary writing its own content.
		return writeSkillFile(path, "---\nname: mio\n"+skillVersionKey+": 9.9.9\n"+
			skillHashKey+": deadbeef\n---\nNEW BINARY CONTENT\n")
	})

	var out bytes.Buffer
	refreshManagedSkills(&out, newBin)

	found := false
	for _, c := range *calls {
		if c[0] == newBin && c[1] == "claude" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the NEW binary %q to be asked to write its own skill; calls=%v (MIO-2874)", newBin, *calls)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) == before {
		t.Errorf("skill was not refreshed at all")
	}
	if strings.Contains(string(after), skillBody) {
		t.Errorf("skill still carries the RUNNING binary's embedded body — the old process re-stamped it instead of delegating (MIO-2874)")
	}
	if !strings.Contains(out.String(), "9.9.9") {
		t.Errorf("output must report the version the file ACTUALLY says, not the running one; got: %q", out.String())
	}
}

// MIO-2875: a file the user owns is never replaced, and they are told.
func TestRefreshManagedSkills_NeverTouchesAUserOwnedFile(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(t *testing.T, path string) string
		wantMsg string
	}{
		{
			name: "hand-authored (no managed frontmatter)",
			seed: func(t *testing.T, path string) string {
				body := "# my own skill\n\nhand written, not mio's\n"
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatalf("seed: %v", err)
				}
				return body
			},
			wantMsg: "was not refreshed",
		},
		{
			name: "managed but locally edited",
			seed: func(t *testing.T, path string) string {
				body := seedManagedSkill(t, path, "0.12.1") + "\n<!-- my local edit -->\n"
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatalf("seed: %v", err)
				}
				return body
			},
			wantMsg: "was not refreshed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateSkillHome(t)
			path := claudeSkillPath(home)
			before := tc.seed(t, path)

			calls := stubRefreshExec(t, func(_, _ string) error {
				t.Errorf("the new binary must NOT be invoked for a user-owned file (MIO-2875)")
				return nil
			})

			var out bytes.Buffer
			refreshManagedSkills(&out, "/opt/mio/bin/mio")

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read after: %v", err)
			}
			if string(after) != before {
				t.Errorf("update REPLACED a user-owned skill file (MIO-2875)\n--- before ---\n%s\n--- after ---\n%s",
					headLines(before, 4), headLines(string(after), 4))
			}
			if len(*calls) != 0 {
				t.Errorf("expected no handoff for a user-owned file; got %v", *calls)
			}
			if !strings.Contains(out.String(), tc.wantMsg) {
				t.Errorf("user must be told the file was left alone; got: %q", out.String())
			}
		})
	}
}

// MIO-2875: the skill lives outside any --prefix, so the path must be named.
func TestRefreshManagedSkills_AnnouncesThePathOutsidePrefix(t *testing.T) {
	home := isolateSkillHome(t)
	path := claudeSkillPath(home)
	seedManagedSkill(t, path, "0.12.1")

	stubRefreshExec(t, func(_, target string) error {
		if target == "claude" {
			return writeSkillFile(path, renderSkill("9.9.9"))
		}
		return nil
	})

	var out bytes.Buffer
	refreshManagedSkills(&out, "/opt/mio/bin/mio")

	if !strings.Contains(out.String(), path) {
		t.Errorf("update must name the skill path it wrote — it is outside --prefix (MIO-2875); got: %q", out.String())
	}
}

// A refresh that cannot happen must say so, not leave a silently stale skill.
func TestRefreshManagedSkills_ReportsWhenItCannotRefresh(t *testing.T) {
	// Seeded on CODEX deliberately: `mio skills install --force` defaults to
	// --target claude, so a remediation printed about the codex skill that omits
	// --target is worse than useless — it reports success for claude, leaves the
	// codex file stale, and OVERWRITES a hand-edited claude skill if one exists.
	// Seeding claude here would let that ship green.
	t.Run("no usable new binary", func(t *testing.T) {
		home := isolateSkillHome(t)
		path := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
		before := seedManagedSkill(t, path, "0.12.1")

		var out bytes.Buffer
		refreshManagedSkills(&out, "") // updater could not report a binary

		after, _ := os.ReadFile(path)
		if string(after) != before {
			t.Errorf("must not rewrite the skill when the new binary is unknown")
		}
		if !strings.Contains(out.String(), "Could not locate") {
			t.Errorf("must report that the skill was left stale; got: %q", out.String())
		}
		assertTargetedRemediation(t, out.String(), "codex", path)
		assertNoInstallNudge(t, out.String())
	})

	t.Run("handoff fails", func(t *testing.T) {
		home := isolateSkillHome(t)
		path := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
		seedManagedSkill(t, path, "0.12.1")
		stubRefreshExec(t, func(_, _ string) error { return errors.New("exec boom") })

		var out bytes.Buffer
		refreshManagedSkills(&out, "/opt/mio/bin/mio")

		if !strings.Contains(out.String(), "Could not refresh") {
			t.Errorf("a failed handoff must be reported; got: %q", out.String())
		}
		assertTargetedRemediation(t, out.String(), "codex", path)
		assertNoInstallNudge(t, out.String())
	})
}

func headLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// The production handoff argv is the load-bearing line of MIO-2874, and every
// other test in this file stubs it out — so a typo'd subcommand, or a dropped
// --target that makes the codex refresh rewrite the claude file, would ship with
// the suite fully green. This one exercises the REAL skillRefreshExec against a
// real built binary. It is the only oracle that argv has.
func TestSkillRefreshExec_RealBinaryArgv(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "mio")
	build := exec.Command("go", "build",
		"-ldflags", "-X github.com/Searchie-Inc/mio-cli/internal/version.Version=9.9.9",
		"-o", bin, ".")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stand-in binary: %v\n%s", err, out)
	}

	home := isolateSkillHome(t)
	claude := claudeSkillPath(home)
	codex := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
	seedManagedSkill(t, claude, "0.0.1")
	seedManagedSkill(t, codex, "0.0.1")

	var out bytes.Buffer
	refreshManagedSkills(&out, bin) // real exec, no stub

	for label, p := range map[string]string{"claude": claude, "codex": codex} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s skill: %v", label, err)
		}
		got, ok := skillFileVersion(string(data))
		if !ok || got != "9.9.9" {
			t.Errorf("%s skill should have been rewritten by the new binary to 9.9.9, got %q (ok=%v) — the handoff argv is wrong",
				label, got, ok)
		}
	}
	if !strings.Contains(out.String(), claude) || !strings.Contains(out.String(), codex) {
		t.Errorf("both refreshed paths should be named; got: %q", out.String())
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// A failure path must never follow "could not refresh the skill at <path>" with
// "a skill is available — run 'mio skills install'". The second line is false:
// the skill IS installed, at the path just named. Introduced by dropping the
// counter that tracked "a managed install exists" independently of whether the
// refresh succeeded.
func assertNoInstallNudge(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "run 'mio skills install' to add it") {
		t.Errorf("contradictory nudge: told the user no skill is installed, one line after naming the installed file\n%s", out)
	}
}

// installedBinaryPath decides WHICH binary writes the skill, so a defect here
// hands the refresh to the wrong mio and reports its version as installed. It
// had no test at all until this case: reverting the fix left the whole suite
// green while restoring a live bug.
func TestInstalledBinaryPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("binary name and PATH semantics differ on windows")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "mio")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}

	t.Run("relative prefix resolves to an absolute path, not a PATH lookup", func(t *testing.T) {
		t.Chdir(dir)
		got := installedBinaryPath(".")
		if !filepath.IsAbs(got) {
			t.Fatalf("must be absolute — a separator-free name is resolved through $PATH by os/exec, "+
				"so `mio update --prefix .` would hand the refresh to a different mio; got %q", got)
		}
		if resolved, err := filepath.EvalSymlinks(got); err == nil {
			if want, werr := filepath.EvalSymlinks(bin); werr == nil && resolved != want {
				t.Errorf("resolved to %q, want %q", resolved, want)
			}
		}
	})

	t.Run("absolute prefix", func(t *testing.T) {
		if got := installedBinaryPath(dir); got != bin {
			t.Errorf("installedBinaryPath(%q) = %q, want %q", dir, got, bin)
		}
	})

	t.Run("missing / directory / non-regular yield empty", func(t *testing.T) {
		if got := installedBinaryPath(t.TempDir()); got != "" {
			t.Errorf("missing binary should yield \"\", got %q", got)
		}
		d2 := t.TempDir()
		if err := os.Mkdir(filepath.Join(d2, "mio"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if got := installedBinaryPath(d2); got != "" {
			t.Errorf("a directory named mio should yield \"\", got %q", got)
		}
	})
}

// The already-current case must be quiet about refreshing but must NOT look like
// "nothing is installed" — it is the commonest outcome of all (re-running an
// update you already have).
func TestRefreshManagedSkills_AlreadyCurrentIsQuietButNotAlarming(t *testing.T) {
	home := isolateSkillHome(t)
	path := claudeSkillPath(home)
	seedManagedSkill(t, path, "0.12.1")

	// Child succeeds and changes nothing.
	stubRefreshExec(t, func(_, _ string) error { return nil })

	var out bytes.Buffer
	refreshManagedSkills(&out, "/opt/mio/bin/mio")

	if strings.Contains(out.String(), "Refreshed") {
		t.Errorf("must not claim a refresh when the file did not change; got: %q", out.String())
	}
	assertNoInstallNudge(t, out.String())
}

// A hand-edited file must be reported even when ANOTHER target refreshed or was
// already current — otherwise the combination is silent and the user never
// learns their edited skill was skipped.
func TestRefreshManagedSkills_ReportsHandEditedAlongsideAHealthyTarget(t *testing.T) {
	home := isolateSkillHome(t)
	claude := claudeSkillPath(home)
	codex := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)

	seedManagedSkill(t, claude, "0.12.1") // healthy, will be "already current"
	edited := seedManagedSkill(t, codex, "0.12.1") + "\n<!-- mine -->\n"
	if err := os.WriteFile(codex, []byte(edited), 0o644); err != nil {
		t.Fatalf("seed edited: %v", err)
	}

	stubRefreshExec(t, func(_, _ string) error { return nil })

	var out bytes.Buffer
	refreshManagedSkills(&out, "/opt/mio/bin/mio")

	// Presence is not enough — the remediation must be USABLE. `mio skills
	// install --force` defaults to --target claude, so a bare suggestion printed
	// about the codex skill reports "already up to date" for claude and leaves
	// the edited file untouched (and, when only codex is installed, creates a
	// claude skill the user never had).
	got := out.String()
	if !strings.Contains(got, "edited locally") {
		t.Fatalf("a hand-edited skill must be reported even when another target is healthy; got: %q", got)
	}
	if !strings.Contains(got, codex) {
		t.Errorf("the message must name the PATH it is about; got: %q", got)
	}
	if !strings.Contains(got, "--target codex") {
		t.Errorf("the remediation must target codex — `--force` alone defaults to claude and fixes nothing; got: %q", got)
	}
	if strings.Contains(got, claude) {
		t.Errorf("the healthy claude skill must not be named as edited; got: %q", got)
	}
}

// An unreadable skill file is not "not installed" — a file is there, it just
// could not be classified. Staying silent (or claiming none is installed) is the
// same false report assertNoInstallNudge exists to prevent.
func TestRefreshManagedSkills_ReportsAnUnreadableSkill(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores mode bits")
	}
	home := isolateSkillHome(t)
	// CODEX, not claude: `--force` defaults to claude, so a probe seeded on
	// claude is satisfied by both the correct `--target %s` and a hardcoded
	// `--target claude`. Verified — that hardcode ships the whole suite green.
	path := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
	seedManagedSkill(t, path, "0.12.1")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	var out bytes.Buffer
	refreshManagedSkills(&out, "/opt/mio/bin/mio")

	if !strings.Contains(out.String(), "Could not read") {
		t.Errorf("an unreadable skill must be reported, not silently skipped; got: %q", out.String())
	}
	assertTargetedRemediation(t, out.String(), "codex", path)
	assertNoInstallNudge(t, out.String())
}

// Every remediation this function prints must name the target it is about.
// `mio skills install --force` defaults to --target claude: printed about the
// Codex skill it reports success for Claude and fixes nothing, and where a
// hand-edited Claude skill exists it destroys it. Measured — following the bare
// form removed a sentinel from a hand-edited file. So a target-less remediation
// is a data-loss bug, not a wording nit.
func assertTargetedRemediation(t *testing.T, out, target, path string) {
	t.Helper()
	if !strings.Contains(out, "--target "+target) {
		t.Errorf("remediation must carry --target %s — a bare --force acts on claude; got: %q", target, out)
	}
	if !strings.Contains(out, path) {
		t.Errorf("remediation must name the path it is about; got: %q", out)
	}
}

// The child can report success and still leave a file we cannot read back. That
// path used to `continue` silently — and because `installed` was already counted,
// the run printed NOTHING AT ALL, breaking the guarantee README and llms.txt both
// state: a failure always prints a line.
func TestRefreshManagedSkills_ReportsAFailedReadBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores mode bits")
	}
	home := isolateSkillHome(t)
	// Codex, so a hardcoded --target claude cannot satisfy the assertion.
	path := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
	seedManagedSkill(t, path, "0.12.1")
	stubRefreshExec(t, func(_, _ string) error { return os.Chmod(path, 0o000) })
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	var out bytes.Buffer
	refreshManagedSkills(&out, "/opt/mio/bin/mio")

	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("a failed read-back printed nothing — README and llms.txt promise a failure always prints")
	}
	if !strings.Contains(out.String(), "could not read it back") {
		t.Errorf("the failure must be named; got: %q", out.String())
	}
	assertTargetedRemediation(t, out.String(), "codex", path)
	assertNoInstallNudge(t, out.String())
}
