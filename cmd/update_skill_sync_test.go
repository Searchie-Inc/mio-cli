package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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
	t.Run("no usable new binary", func(t *testing.T) {
		home := isolateSkillHome(t)
		path := claudeSkillPath(home)
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
	})

	t.Run("handoff fails", func(t *testing.T) {
		home := isolateSkillHome(t)
		path := claudeSkillPath(home)
		seedManagedSkill(t, path, "0.12.1")
		stubRefreshExec(t, func(_, _ string) error { return errors.New("exec boom") })

		var out bytes.Buffer
		refreshManagedSkills(&out, "/opt/mio/bin/mio")

		if !strings.Contains(out.String(), "Could not refresh") {
			t.Errorf("a failed handoff must be reported; got: %q", out.String())
		}
	})
}

func headLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
