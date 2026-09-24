package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
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

// ./.claude can BE ~/.claude without the two paths being spelled alike: run
// from a symlink to $HOME (or, on Windows, a differently-cased path). The
// textual comparison in sameSkillFile misses that and only os.SameFile catches
// it; without it one file is handed off twice, or reported under two
// remediation commands, one of them a --project one aimed at the user copy.
func TestRefreshManagedSkills_HomeReachedThroughASymlinkIsHandledOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows, and os.Getwd ignores PWD there")
	}
	for _, edited := range []bool{false, true} {
		name := map[bool]string{false: "managed", true: "edited"}[edited]
		t.Run(name, func(t *testing.T) {
			home, _ := isolateSkillSandbox(t)
			link := filepath.Join(resolvedTempDir(t), "homelink")
			if err := os.Symlink(home, link); err != nil {
				t.Skipf("cannot create a symlink here: %v", err)
			}
			t.Chdir(link) // sets PWD, which os.Getwd returns unresolved
			path := claudeSkillPath(home)
			// Precondition: the project path must be SPELLED through the link,
			// or this probes the textual comparison instead of the aliasing.
			if loc, err := skillLocationFor("claude", true); err != nil || loc.path == path {
				t.Fatalf("precondition: want the project path spelled through %s, got %+v (err %v)", link, loc, err)
			}
			seedManagedSkill(t, path, "0.14.0")
			if edited {
				seedFile(t, path, readFile(t, path)+"\nedit\n")
			}
			calls := stubRefreshExec(t, func(_ string, loc skillLocation) error {
				return writeSkillFile(loc.path, renderSkill("9.9.9"))
			})

			var stdout, stderr bytes.Buffer
			refreshManagedSkills(&stdout, &stderr, "/opt/mio/bin/mio")

			all := stdout.String() + stderr.String()
			if len(*calls) > 1 {
				t.Errorf("one file reached through a symlinked cwd got %d handoffs: %+v", len(*calls), *calls)
			}
			if n := strings.Count(all, filepath.Join(".claude", "skills", skillDirName, skillFileName)); n != 1 {
				t.Errorf("the one Claude skill file should be named exactly once, got %d:\n%s", n, all)
			}
			if strings.Contains(all, "(project)") || strings.Contains(all, "--project") {
				t.Errorf("~/.claude reached through %s is the USER copy, not a project one:\n%s", link, all)
			}
		})
	}
}

// Every line refreshManagedSkills prints goes to ONE stream, chosen by what it
// says: a skill it refreshed, and the install nudge, are results on out; a
// skill it did NOT refresh is a warning on errOut. README, llms.txt and
// `mio update --help` promise the warning half ("a failure or a skipped
// hand-edited file always prints a line, on stderr"). Each branch is its own
// Fprintf, so each is probed here, in both scopes, with SEPARATE buffers — the
// older tests pass one buffer for both writers and cannot tell them apart.
//
// Seeded on codex: `--force` alone defaults to claude, so only a codex copy
// tells a correct `--target <t>` from a hardcoded one.
func TestRefreshManagedSkills_EveryBranchPrintsOnItsOwnStream(t *testing.T) {
	const bin = "/opt/mio/bin/mio"
	type branch struct {
		name      string
		needsMode bool                            // relies on mode bits, which root ignores
		seed      func(t *testing.T, path string) // the one skill file in play
		newBin    string                          // "" = the updater reported no binary
		child     func(path string) error         // the stubbed new binary; nil = must not run
		result    bool                            // a result (stdout), not a warning (stderr)
		want      []string                        // on the chosen stream
		notWant   []string                        // on neither stream
	}
	managed := func(t *testing.T, path string) { seedManagedSkill(t, path, "0.14.0") }
	branches := []branch{
		{name: "unreadable", needsMode: true, newBin: bin,
			seed: func(t *testing.T, path string) {
				managed(t, path)
				if err := os.Chmod(path, 0o000); err != nil {
					t.Fatalf("chmod: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
			},
			want: []string{"Could not read"}},
		{name: "no new binary", seed: managed, newBin: "",
			want: []string{"Could not locate the updated mio binary"}},
		{name: "handoff fails", seed: managed, newBin: bin,
			child: func(string) error { return errors.New("exec boom") },
			want:  []string{"Could not refresh", "exec boom"}},
		{name: "read-back fails", needsMode: true, seed: managed, newBin: bin,
			child: func(path string) error { return os.Chmod(path, 0o000) },
			want:  []string{"could not read it back"}},
		// Following a --force remediation replaces the file, so the line must
		// say so — and must not call a file mio never wrote "edited locally".
		{name: "managed but edited", newBin: bin,
			seed: func(t *testing.T, path string) { seedFile(t, path, seedManagedSkill(t, path, "0.14.0")+"\nedit\n") },
			want: []string{"edited locally", "overwrites your edits"}},
		{name: "not written by mio", newBin: bin,
			seed:    func(t *testing.T, path string) { seedFile(t, path, "# our own mio notes\n") },
			want:    []string{"was not installed by mio", "replace it with mio's skill", "overwrites it"},
			notWant: []string{"edited locally"}},
		{name: "refreshed", seed: managed, newBin: bin, result: true,
			child: func(path string) error { return writeSkillFile(path, renderSkill("9.9.9")) },
			want:  []string{"Refreshed mio skill", "(version 9.9.9)"}},
		{name: "refreshed, no version stamp", seed: managed, newBin: bin, result: true,
			child: func(path string) error { return writeSkillFile(path, "---\nname: mio\n---\nno stamp\n") },
			want:  []string{"Refreshed mio skill"}},
	}
	for _, scope := range []string{"user", "project"} {
		for _, b := range branches {
			t.Run(scope+"/"+b.name, func(t *testing.T) {
				if b.needsMode && os.Geteuid() == 0 {
					t.Skip("root ignores mode bits")
				}
				home, project := isolateSkillSandbox(t)
				path := filepath.Join(home, ".codex", "skills", skillDirName, skillFileName) // CODEX_HOME
				label := "Codex"
				fix := "'mio skills install --force --target codex'"
				if scope == "project" {
					path = projectSkillPath(project, "codex")
					label = "Codex (project)"
					fix = "'mio skills install --project --force --target codex' in " + project
				}
				b.seed(t, path)
				stubRefreshExec(t, func(_ string, loc skillLocation) error {
					if b.child == nil {
						t.Errorf("%s: no handoff expected, got %+v", b.name, loc)
						return nil
					}
					return b.child(loc.path)
				})

				var stdout, stderr bytes.Buffer
				refreshManagedSkills(&stdout, &stderr, b.newBin)

				chosen, other, chosenName, otherName := &stderr, &stdout, "stderr", "stdout"
				// A warning names the file as "<label> skill at <path>" plus the one
				// command that rewrites THAT file; a result as "for <label> at <path>".
				must := append([]string{label + " skill at " + path, fix}, b.want...)
				if b.result {
					chosen, other, chosenName, otherName = &stdout, &stderr, "stdout", "stderr"
					must = append([]string{"for " + label + " at " + path}, b.want...)
				}
				if other.Len() != 0 {
					t.Errorf("%s (%s scope): this line belongs on %s alone, but %s got %q", b.name, scope, chosenName, otherName, other.String())
				}
				for _, w := range must {
					if !strings.Contains(chosen.String(), w) {
						t.Errorf("%s (%s scope): %s must carry %q; %s=%q", b.name, scope, chosenName, w, chosenName, chosen.String())
					}
				}
				for _, nw := range b.notWant {
					if strings.Contains(stdout.String()+stderr.String(), nw) {
						t.Errorf("%s (%s scope): must not say %q; stdout=%q stderr=%q", b.name, scope, nw, stdout.String(), stderr.String())
					}
				}
			})
		}
	}
	t.Run("install nudge", func(t *testing.T) {
		isolateSkillSandbox(t)
		stubRefreshExec(t, func(string, skillLocation) error { t.Error("nothing to hand off"); return nil })
		var stdout, stderr bytes.Buffer
		refreshManagedSkills(&stdout, &stderr, bin)
		if !strings.Contains(stdout.String(), "A mio CLI agent skill is available") || stderr.Len() != 0 {
			t.Errorf("the install nudge belongs on stdout alone; stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})
}

// The streams above hold only if `mio update` hands refreshManagedSkills its
// OWN stdout and stderr. Driven through the real command with separate
// buffers, once where the refresh runs and once where it cannot.
func TestUpdateCommand_SkillResultsOnStdoutWarningsOnStderr(t *testing.T) {
	for _, binaryInstalled := range []bool{true, false} {
		name := map[bool]string{true: "new binary found", false: "new binary missing"}[binaryInstalled]
		t.Run(name, func(t *testing.T) {
			home, project := isolateSkillSandbox(t)
			prefix := resolvedTempDir(t)
			oldRunner := selfUpdateRunner
			t.Cleanup(func() { selfUpdateRunner = oldRunner })
			selfUpdateRunner = func(_ context.Context, opts updateOptions, _, _ io.Writer) error {
				if !binaryInstalled {
					return nil // the installer "succeeded" but wrote no binary
				}
				bin := filepath.Join(opts.Prefix, "mio")
				if runtime.GOOS == "windows" {
					bin += ".exe"
				}
				return os.WriteFile(bin, []byte("stand-in"), 0o755)
			}
			userClaude := claudeSkillPath(home)
			seedManagedSkill(t, userClaude, "0.14.0")
			projCodex := projectSkillPath(project, "codex")
			seedFile(t, projCodex, seedManagedSkill(t, projCodex, "0.14.0")+"\nedit\n")
			stubRefreshExec(t, func(_ string, loc skillLocation) error {
				return writeSkillFile(loc.path, renderSkill("9.9.9"))
			})

			stdout, stderr, err := runSkills(t, "update", "--prefix", prefix)
			if err != nil {
				t.Fatalf("update: %v", err)
			}

			// The hand-edited project copy is a warning either way.
			if !strings.Contains(stderr, projCodex) || !strings.Contains(stderr, "edited locally") {
				t.Errorf("`mio update` must warn about %s on stderr; stderr=%q", projCodex, stderr)
			}
			if strings.Contains(stdout, projCodex) {
				t.Errorf("`mio update` printed the warning about %s on stdout; stdout=%q", projCodex, stdout)
			}
			if binaryInstalled {
				if want := "Refreshed mio skill for Claude Code at " + userClaude; !strings.Contains(stdout, want) {
					t.Errorf("`mio update` must report the refresh on stdout (%q); stdout=%q", want, stdout)
				}
				if strings.Contains(stderr, "Refreshed mio skill") {
					t.Errorf("a refresh is a result, not a warning; stderr=%q", stderr)
				}
				return
			}
			if !strings.Contains(stderr, "Could not locate the updated mio binary") || !strings.Contains(stderr, userClaude) {
				t.Errorf("`mio update` must say on stderr that it could not refresh %s; stderr=%q", userClaude, stderr)
			}
			if got, want := strings.TrimSpace(stdout), "Updating mio in "+prefix+"..."; got != want {
				t.Errorf("a refresh that could not run must leave stdout with only %q; stdout=%q", want, stdout)
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
