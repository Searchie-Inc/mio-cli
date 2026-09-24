package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/version"
)

// MIO-4178: a skill installed with `mio skills install --project` lives in the
// project (./.claude/skills/mio/SKILL.md or ./.codex/skills/mio/SKILL.md), and
// `mio update` only ever refreshed the user-scope copy. QA's project copy sat
// at 0.14.0 through three updates, its template list stopped at page-faq, and
// the agent reading it concluded page-sales did not exist. Nothing said the
// copy was stale; `mio update` even offered to install a skill "available"
// one directory away from the stale one.
//
// Every test here runs in isolateSkillSandbox: HOME, USERPROFILE, CODEX_HOME,
// XDG_CONFIG_HOME and the working directory are all temp dirs.

// withSkillVersion sets the running CLI's version for one test. The skills
// install warning compares versions, and a `go test` build is "dev".
func withSkillVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func seedFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := writeSkillFile(path, body); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return body
}

// A managed, unmodified project copy at an older version is refreshed through
// the same handoff to the NEW binary as the user scope — started in the
// project root, so its relative ./.<agent> path lands on this file.
func TestRefreshManagedSkills_RefreshesAStaleProjectCopy(t *testing.T) {
	for _, target := range []string{"claude", "codex"} {
		t.Run(target, func(t *testing.T) {
			_, project := isolateSkillSandbox(t)
			path := projectSkillPath(project, target)
			before := seedManagedSkill(t, path, "0.14.0")

			const newBin = "/opt/mio/bin/mio"
			calls := stubRefreshExec(t, func(_ string, loc skillLocation) error {
				if !loc.project || loc.target != target {
					return nil
				}
				// Stand in for the new binary: `skills install --project` writes
				// relative to the directory it is started in.
				return writeSkillFile(projectSkillPath(loc.root, target), renderSkill("9.9.9"))
			})

			var stdout, stderr bytes.Buffer
			refreshManagedSkills(&stdout, &stderr, newBin)

			if readFile(t, path) == before {
				t.Fatalf("the %s project skill at %s was NOT refreshed by `mio update` run from %s (MIO-4178)\nstdout: %q\nstderr: %q",
					target, path, project, stdout.String(), stderr.String())
			}
			if got, _ := skillFileVersion(readFile(t, path)); got != "9.9.9" {
				t.Errorf("project skill version = %q, want the new binary's 9.9.9", got)
			}
			handed := false
			for _, c := range *calls {
				if c.bin == newBin && c.loc.project && c.loc.target == target && c.loc.root == project {
					handed = true
				}
			}
			if !handed {
				t.Errorf("expected a project-scope handoff to %s for %s rooted at %s; calls=%+v", newBin, target, project, *calls)
			}
			if !strings.Contains(stdout.String(), "Refreshed") || !strings.Contains(stdout.String(), path) {
				t.Errorf("stdout must name the refreshed project path %s; got %q", path, stdout.String())
			}
			assertNoInstallNudge(t, stdout.String()+stderr.String())
		})
	}
}

// A hand-edited or unrecognised project copy is never modified. stderr names it
// with the one command that rewrites THAT file: --project (or it rewrites the
// user copy), --force (or it refuses), --target (or it acts on claude), run in
// that project's directory.
func TestRefreshManagedSkills_NeverTouchesAHandEditedProjectCopy(t *testing.T) {
	seeds := map[string]func(t *testing.T, path string) string{
		"managed but edited": func(t *testing.T, path string) string {
			return seedFile(t, path, seedManagedSkill(t, path, "0.14.0")+"\n<!-- local edit -->\n")
		},
		"not written by mio": func(t *testing.T, path string) string {
			return seedFile(t, path, "# our own mio notes\n")
		},
	}
	for _, target := range []string{"claude", "codex"} {
		for name, seed := range seeds {
			t.Run(target+"/"+name, func(t *testing.T) {
				_, project := isolateSkillSandbox(t)
				path := projectSkillPath(project, target)
				before := seed(t, path)

				stubRefreshExec(t, func(_ string, loc skillLocation) error {
					if loc.project {
						t.Errorf("the new binary must NOT be handed a hand-edited project copy: %+v", loc)
						// Behave like the real child, which runs with --force.
						return writeSkillFile(projectSkillPath(loc.root, loc.target), renderSkill("9.9.9"))
					}
					return nil
				})

				var stdout, stderr bytes.Buffer
				refreshManagedSkills(&stdout, &stderr, "/opt/mio/bin/mio")

				if after := readFile(t, path); after != before {
					t.Fatalf("`mio update` MODIFIED the hand-edited %s project skill at %s (MIO-4178)\n--- before ---\n%s\n--- after ---\n%s",
						target, path, headLines(before, 4), headLines(after, 4))
				}
				want := "'mio skills install --project --force --target " + target + "' in " + project
				if !strings.Contains(stderr.String(), path) || !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr must name %s and the command %s; got %q", path, want, stderr.String())
				}
				if strings.Contains(stdout.String(), path) {
					t.Errorf("the remediation belongs on stderr, not stdout; stdout=%q", stdout.String())
				}
				assertNoInstallNudge(t, stdout.String()+stderr.String())
			})
		}
	}
}

// "A mio CLI agent skill is available — run 'mio skills install'" is false
// when a project copy exists: following it installs a SECOND, user-level copy
// and leaves the project one stale, which is exactly QA's state.
func TestRefreshManagedSkills_NoInstallNudgeWhenOnlyAProjectCopyExists(t *testing.T) {
	seeds := map[string]func(t *testing.T, path string){
		"managed, already current": func(t *testing.T, path string) { seedManagedSkill(t, path, "0.23.0") },
		"managed, edited": func(t *testing.T, path string) {
			seedFile(t, path, seedManagedSkill(t, path, "0.14.0")+"\nedit\n")
		},
		"not written by mio": func(t *testing.T, path string) { seedFile(t, path, "# mine\n") },
	}
	for _, target := range []string{"claude", "codex"} {
		for name, seed := range seeds {
			t.Run(target+"/"+name, func(t *testing.T) {
				_, project := isolateSkillSandbox(t)
				seed(t, projectSkillPath(project, target))
				// The child succeeds and changes nothing: "already current".
				stubRefreshExec(t, func(string, skillLocation) error { return nil })

				var stdout, stderr bytes.Buffer
				refreshManagedSkills(&stdout, &stderr, "/opt/mio/bin/mio")

				if strings.Contains(stdout.String()+stderr.String(), "A mio CLI agent skill is available") {
					t.Errorf("the install nudge fired although a %s project copy exists at %s (MIO-4178)\nstdout: %q\nstderr: %q",
						target, projectSkillPath(project, target), stdout.String(), stderr.String())
				}
			})
		}
	}
}

// A refresh result is output; a file left alone is a warning. User scope and
// project scope follow the same rule, so one run never splits its warnings
// across both streams.
func TestRefreshManagedSkills_WarningsGoToStderrResultsToStdout(t *testing.T) {
	home, project := isolateSkillSandbox(t)
	userClaude := claudeSkillPath(home)
	seedManagedSkill(t, userClaude, "0.14.0")
	userCodex := projectSkillPath(home, "codex") // CODEX_HOME is home/.codex
	seedFile(t, userCodex, seedManagedSkill(t, userCodex, "0.14.0")+"\nedit\n")
	projClaude := projectSkillPath(project, "claude")
	seedFile(t, projClaude, "# mine\n")

	stubRefreshExec(t, func(_ string, loc skillLocation) error {
		if loc.target == "claude" && !loc.project {
			return writeSkillFile(userClaude, renderSkill("9.9.9"))
		}
		return nil
	})

	var stdout, stderr bytes.Buffer
	refreshManagedSkills(&stdout, &stderr, "/opt/mio/bin/mio")

	if !strings.Contains(stdout.String(), "Refreshed mio skill for Claude Code at "+userClaude) {
		t.Errorf("the refresh result belongs on stdout; stdout=%q", stdout.String())
	}
	for _, p := range []string{userCodex, projClaude} {
		if !strings.Contains(stderr.String(), p) {
			t.Errorf("the warning about %s belongs on stderr; stderr=%q", p, stderr.String())
		}
		if strings.Contains(stdout.String(), p) {
			t.Errorf("the warning about %s leaked to stdout; stdout=%q", p, stdout.String())
		}
	}
	if strings.Contains(stderr.String(), "Refreshed mio skill") {
		t.Errorf("a successful refresh is not a warning; stderr=%q", stderr.String())
	}
}

// Run from the home directory, ./.claude IS ~/.claude. That one file must be
// handled once — not handed off twice, and not reported under two different
// remediation commands.
func TestRefreshManagedSkills_ProjectDirThatIsHomeIsHandledOnce(t *testing.T) {
	for _, edited := range []bool{false, true} {
		name := map[bool]string{false: "managed", true: "edited"}[edited]
		t.Run(name, func(t *testing.T) {
			_, project := isolateSkillSandbox(t)
			t.Setenv("HOME", project)
			t.Setenv("USERPROFILE", project)
			t.Setenv("CODEX_HOME", project+"/.codex")
			path := claudeSkillPath(project)
			seedManagedSkill(t, path, "0.14.0")
			if edited {
				seedFile(t, path, readFile(t, path)+"\nedit\n")
			}
			calls := stubRefreshExec(t, func(_ string, loc skillLocation) error {
				return writeSkillFile(path, renderSkill("9.9.9"))
			})

			var stdout, stderr bytes.Buffer
			refreshManagedSkills(&stdout, &stderr, "/opt/mio/bin/mio")

			if len(*calls) > 1 {
				t.Errorf("one file, %d handoffs: %+v", len(*calls), *calls)
			}
			all := stdout.String() + stderr.String()
			if n := strings.Count(all, path); n != 1 {
				t.Errorf("%s should be named exactly once, got %d:\n%s", path, n, all)
			}
		})
	}
}

// `mio skills install` for one scope says so when the OTHER scope holds an
// older managed copy for the same agent — QA ran `mio skills install` (user
// scope) and was never told the project copy their agent actually read was
// nine releases behind.
func TestSkillsInstall_WarnsAboutAnOlderCopyInTheOtherScope(t *testing.T) {
	type tc struct {
		name       string
		args       []string
		target     string
		otherIsPrj bool   // the stale copy is the project one
		otherVer   string // version stamped on the other copy
		edited     bool   // the other copy was hand-edited
		wantCmd    string // "" = no warning expected
	}
	cases := []tc{
		{"user install, stale project copy", []string{"skills", "install"}, "claude", true, "0.14.0", false,
			"'mio skills install --project --target claude'"},
		{"project install, stale user copy", []string{"skills", "install", "--project"}, "claude", false, "0.14.0", false,
			"'mio skills install --target claude'"},
		{"codex user install, stale codex project copy", []string{"skills", "install", "--user", "--target", "codex"}, "codex", true, "0.14.0", false,
			"'mio skills install --project --target codex'"},
		{"numeric, not lexical: 0.9.0 is older than 0.23.0", []string{"skills", "install"}, "claude", true, "0.9.0", false,
			"'mio skills install --project --target claude'"},
		{"edited stale copy: names --force and says it overwrites", []string{"skills", "install"}, "claude", true, "0.14.0", true,
			"'mio skills install --project --force --target claude'"},
		{"other copy current: silent", []string{"skills", "install"}, "claude", true, "0.23.0", false, ""},
		{"other copy newer: silent", []string{"skills", "install"}, "claude", true, "0.24.0", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, project := isolateSkillSandbox(t)
			withSkillVersion(t, "0.23.0")
			other := projectSkillPath(project, c.target)
			if !c.otherIsPrj {
				other = projectSkillPath(home, c.target) // ~/.claude, or CODEX_HOME=home/.codex
			}
			seedManagedSkill(t, other, c.otherVer)
			if c.edited {
				seedFile(t, other, readFile(t, other)+"\nedit\n")
			}
			before := readFile(t, other)

			// Twice: the first run installs, the second is "already up to date".
			// Both must warn.
			for run := 1; run <= 2; run++ {
				stdout, stderr, err := runSkills(t, c.args...)
				if err != nil {
					t.Fatalf("run %d: %v", run, err)
				}
				if c.wantCmd == "" {
					if strings.Contains(stderr, other) {
						t.Errorf("run %d: no warning expected for a %s copy; stderr=%q", run, c.otherVer, stderr)
					}
					continue
				}
				for _, want := range []string{other, c.otherVer, c.wantCmd} {
					if !strings.Contains(stderr, want) {
						t.Errorf("run %d: stderr must warn about the older copy with %q (MIO-4178); stderr=%q", run, want, stderr)
					}
				}
				if c.edited && !strings.Contains(stderr, "overwrite") {
					t.Errorf("run %d: an edited copy's --force command must say it overwrites the edits; stderr=%q", run, stderr)
				}
				if strings.Contains(stdout, other) {
					t.Errorf("run %d: the warning belongs on stderr; stdout=%q", run, stdout)
				}
			}
			if readFile(t, other) != before {
				t.Errorf("`skills install` must only WARN about the other scope, never write it")
			}
		})
	}
}

func TestSkillVersionOlder(t *testing.T) {
	cases := []struct {
		have, than string
		want       bool
	}{
		{"0.14.0", "0.23.0", true},
		{"0.9.0", "0.10.0", true}, // numeric, not lexical
		{"0.23.0", "0.23.0", false},
		{"0.24.0", "0.23.0", false},
		{"1.0.0", "0.99.99", false},
		{"v0.14.0", "0.23.0", true},
		{"0.14.0", "v0.23.0", true},
		{"0.23.0-rc.1", "0.23.0", true}, // a pre-release precedes its release
		{"0.23.0", "0.23.0-rc.1", false},
		{"0.23.0-rc.1", "0.23.0-rc.2", true},
		{"0.23.0-rc.9", "0.23.0-rc.10", true}, // identifiers compare numerically
		{"0.23.0-rc.10", "0.23.0-rc.9", false},
		{"dev", "0.23.0", false}, // not comparable: never claim "older"
		{"0.14.0", "dev", false},
		{"", "0.23.0", false},
		{"0.14", "0.23.0", false},
	}
	for _, c := range cases {
		if got := skillVersionOlder(c.have, c.than); got != c.want {
			t.Errorf("skillVersionOlder(%q, %q) = %v, want %v", c.have, c.than, got, c.want)
		}
	}
}

// The sandbox guard itself: TestMain's skillPathResolved hook must refuse a
// skill path outside the temp root or inside the checkout, and must stay quiet
// inside isolateSkillSandbox. Deliberately NOT sandboxed until the last case —
// resolving a path touches nothing, and the panic fires before any caller can.
func TestSkillPathGuard_RefusesARealLocation(t *testing.T) {
	expectPanic := func(t *testing.T, target string, project bool) {
		t.Helper()
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("resolving the %s skill (project=%v) outside the sandbox did not panic — the MIO-4178 guard is not installed", target, project)
			}
			if msg, _ := r.(string); !strings.Contains(msg, "skill test sandbox violated") {
				t.Fatalf("unexpected panic: %v", r)
			}
		}()
		_, _ = skillDestPath(target, project)
	}
	t.Run("project scope in the package dir", func(t *testing.T) {
		expectPanic(t, "claude", true)
		expectPanic(t, "codex", true)
	})
	t.Run("user scope under a real-looking HOME", func(t *testing.T) {
		t.Setenv("HOME", "/nonexistent-mio-4178-home")
		t.Setenv("USERPROFILE", "/nonexistent-mio-4178-home")
		expectPanic(t, "claude", false)
	})
	t.Run("codex user scope under a real-looking CODEX_HOME", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "/nonexistent-mio-4178-codex")
		expectPanic(t, "codex", false)
	})
	t.Run("inside the sandbox", func(t *testing.T) {
		isolateSkillSandbox(t)
		for _, target := range []string{"claude", "codex"} {
			for _, project := range []bool{false, true} {
				if _, err := skillDestPath(target, project); err != nil {
					t.Fatalf("skillDestPath(%s, %v): %v", target, project, err)
				}
			}
		}
	})
}
