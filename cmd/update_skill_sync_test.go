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

func isolateSkillHome(t *testing.T) string {
	t.Helper()
	home, _ := isolateSkillSandbox(t)
	return home
}

func isolateSkillSandbox(t *testing.T) (home, project string) {
	t.Helper()
	home = resolvedTempDir(t)
	project = resolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_CONFIG_HOME", resolvedTempDir(t))
	t.Chdir(project)
	return home, project
}

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	return dir
}

func projectSkillPath(dir, target string) string {
	return filepath.Join(dir, "."+target, "skills", skillDirName, skillFileName)
}

type refreshCall struct {
	bin string
	loc skillLocation
}

func stubRefreshExec(t *testing.T, fn func(bin string, loc skillLocation) error) *[]refreshCall {
	t.Helper()
	calls := &[]refreshCall{}
	old := skillRefreshExec
	t.Cleanup(func() { skillRefreshExec = old })
	skillRefreshExec = func(bin string, loc skillLocation) error {
		*calls = append(*calls, refreshCall{bin, loc})
		return fn(bin, loc)
	}
	return calls
}

func TestRefreshManagedSkills_DelegatesToTheNewBinary(t *testing.T) {
	home := isolateSkillHome(t)
	path := claudeSkillPath(home)
	before := seedManagedSkill(t, path, "0.12.1")

	const newBin = "/opt/mio/bin/mio"
	calls := stubRefreshExec(t, func(_ string, loc skillLocation) error {
		if loc.target != "claude" || loc.project {
			return nil
		}
		return writeSkillFile(path, "---\nname: mio\n"+skillVersionKey+": 9.9.9\n"+
			skillHashKey+": deadbeef\n---\nNEW BINARY CONTENT\n")
	})

	var out bytes.Buffer
	refreshManagedSkills(&out, &out, newBin)

	found := false
	for _, c := range *calls {
		if c.bin == newBin && c.loc.target == "claude" && !c.loc.project {
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

			calls := stubRefreshExec(t, func(_ string, _ skillLocation) error {
				t.Errorf("the new binary must NOT be invoked for a user-owned file (MIO-2875)")
				return nil
			})

			var out bytes.Buffer
			refreshManagedSkills(&out, &out, "/opt/mio/bin/mio")

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

func TestRefreshManagedSkills_AnnouncesThePathOutsidePrefix(t *testing.T) {
	home := isolateSkillHome(t)
	path := claudeSkillPath(home)
	seedManagedSkill(t, path, "0.12.1")

	stubRefreshExec(t, func(_ string, loc skillLocation) error {
		if loc.target == "claude" && !loc.project {
			return writeSkillFile(path, renderSkill("9.9.9"))
		}
		return nil
	})

	var out bytes.Buffer
	refreshManagedSkills(&out, &out, "/opt/mio/bin/mio")

	if !strings.Contains(out.String(), path) {
		t.Errorf("update must name the skill path it wrote — it is outside --prefix (MIO-2875); got: %q", out.String())
	}
}

func TestRefreshManagedSkills_ReportsWhenItCannotRefresh(t *testing.T) {
	t.Run("no usable new binary", func(t *testing.T) {
		home := isolateSkillHome(t)
		path := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
		before := seedManagedSkill(t, path, "0.12.1")

		var out bytes.Buffer
		refreshManagedSkills(&out, &out, "")

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
		stubRefreshExec(t, func(_ string, _ skillLocation) error { return errors.New("exec boom") })

		var out bytes.Buffer
		refreshManagedSkills(&out, &out, "/opt/mio/bin/mio")

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

func TestSkillRefreshExec_RealBinaryArgv(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "mio")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-tags", childKeyringTag,
		"-ldflags", "-X github.com/Searchie-Inc/mio-cli/internal/version.Version=9.9.9",
		"-o", bin, ".")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stand-in binary: %v\n%s", err, out)
	}
	requireChildOnFileKeyring(t, bin)

	home, project := isolateSkillSandbox(t)
	claude := claudeSkillPath(home)
	codex := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
	projClaude := projectSkillPath(project, "claude")
	projCodex := projectSkillPath(project, "codex")
	for _, p := range []string{claude, codex, projClaude, projCodex} {
		seedManagedSkill(t, p, "0.0.1")
	}

	var out bytes.Buffer
	refreshManagedSkills(&out, &out, bin)

	all := map[string]string{"claude": claude, "codex": codex, "claude (project)": projClaude, "codex (project)": projCodex}
	for label, p := range all {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s skill: %v", label, err)
		}
		got, ok := skillFileVersion(string(data))
		if !ok || got != "9.9.9" {
			t.Errorf("%s skill at %s should have been rewritten by the new binary to 9.9.9, got %q (ok=%v) — the handoff argv is wrong",
				label, p, got, ok)
		}
		if !strings.Contains(out.String(), p) {
			t.Errorf("the refreshed %s path %s should be named; got: %q", label, p, out.String())
		}
	}

	for _, p := range []string{projClaude, projCodex} {
		seedManagedSkill(t, p, "0.0.1")
	}
	locs := refreshLocations()
	t.Chdir(resolvedTempDir(t))
	for _, loc := range locs {
		if !loc.project {
			continue
		}
		if err := skillRefreshExec(bin, loc); err != nil {
			t.Errorf("handoff for %+v: %v", loc, err)
		}
		if got, _ := skillFileVersion(readFile(t, loc.path)); got != "9.9.9" {
			t.Errorf("the %s project handoff did not rewrite %s (still %q) — the child was not started in the project root %s",
				loc.target, loc.path, got, loc.root)
		}
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

func assertNoInstallNudge(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "run 'mio skills install' to add it") {
		t.Errorf("contradictory nudge: told the user no skill is installed, one line after naming the installed file\n%s", out)
	}
}

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

func TestRefreshManagedSkills_AlreadyCurrentIsQuietButNotAlarming(t *testing.T) {
	home := isolateSkillHome(t)
	path := claudeSkillPath(home)
	seedManagedSkill(t, path, "0.12.1")

	stubRefreshExec(t, func(_ string, _ skillLocation) error { return nil })

	var out bytes.Buffer
	refreshManagedSkills(&out, &out, "/opt/mio/bin/mio")

	if strings.Contains(out.String(), "Refreshed") {
		t.Errorf("must not claim a refresh when the file did not change; got: %q", out.String())
	}
	assertNoInstallNudge(t, out.String())
}

func TestRefreshManagedSkills_ReportsHandEditedAlongsideAHealthyTarget(t *testing.T) {
	home := isolateSkillHome(t)
	claude := claudeSkillPath(home)
	codex := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)

	seedManagedSkill(t, claude, "0.12.1")
	edited := seedManagedSkill(t, codex, "0.12.1") + "\n<!-- mine -->\n"
	if err := os.WriteFile(codex, []byte(edited), 0o644); err != nil {
		t.Fatalf("seed edited: %v", err)
	}

	stubRefreshExec(t, func(_ string, _ skillLocation) error { return nil })

	var out bytes.Buffer
	refreshManagedSkills(&out, &out, "/opt/mio/bin/mio")

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

func TestRefreshManagedSkills_ReportsAnUnreadableSkill(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores mode bits")
	}
	home := isolateSkillHome(t)
	path := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
	seedManagedSkill(t, path, "0.12.1")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	var out bytes.Buffer
	refreshManagedSkills(&out, &out, "/opt/mio/bin/mio")

	if !strings.Contains(out.String(), "Could not read") {
		t.Errorf("an unreadable skill must be reported, not silently skipped; got: %q", out.String())
	}
	assertTargetedRemediation(t, out.String(), "codex", path)
	assertNoInstallNudge(t, out.String())
}

func assertTargetedRemediation(t *testing.T, out, target, path string) {
	t.Helper()
	if !strings.Contains(out, "--target "+target) {
		t.Errorf("remediation must carry --target %s — a bare --force acts on claude; got: %q", target, out)
	}
	if !strings.Contains(out, path) {
		t.Errorf("remediation must name the path it is about; got: %q", out)
	}
}

func TestRefreshManagedSkills_ReportsAFailedReadBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores mode bits")
	}
	home := isolateSkillHome(t)
	path := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName)
	seedManagedSkill(t, path, "0.12.1")
	stubRefreshExec(t, func(_ string, _ skillLocation) error { return os.Chmod(path, 0o000) })
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	var out bytes.Buffer
	refreshManagedSkills(&out, &out, "/opt/mio/bin/mio")

	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("a failed read-back printed nothing — README and llms.txt promise a failure always prints")
	}
	if !strings.Contains(out.String(), "could not read it back") {
		t.Errorf("the failure must be named; got: %q", out.String())
	}
	assertTargetedRemediation(t, out.String(), "codex", path)
	assertNoInstallNudge(t, out.String())
}
