package cmd

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/version"
)

// skillBody is the canonical mio agent-skill body (markdown, no frontmatter). It
// is embedded into the binary so a release install carries the current skill and
// `mio skills install` / `mio update` can materialize it into the user's agent
// without any network fetch. The same body serves both Claude Code and Codex —
// both consume the identical SKILL.md frontmatter format, so only the install
// location differs per target.
//
// The skill's catalog-derived sections (node-kind settings, the surface
// background/gradient enums, the vocabulary lists) are GENERATED from the embedded
// page-builder catalog — see internal/docsgen. Re-run after any catalog re-pin:
//
//go:generate go run ../internal/docsgen/cmd/skilldocs -file skills/content/mio-skill.md
//go:embed skills/content/mio-skill.md
var skillBody string

const (
	// skillName / skillDescription populate the SKILL.md frontmatter both agents
	// parse. skillDescription is intentionally colon-free so it needs no YAML
	// quoting.
	skillName        = "mio"
	skillDescription = "Use when building or automating a Membership.io hub with the mio CLI — the CLI-only recipe to create a hub, playlists, media, pages and homepage, plus the render-contract silent-drop traps and the contact-id namespace trap."

	// skillDirName / skillFileName are the on-disk layout under a target's skills
	// directory: <skills>/mio/SKILL.md.
	skillDirName  = "mio"
	skillFileName = "SKILL.md"

	// Frontmatter markers that make an install "managed" by this CLI. The version
	// records which CLI wrote the file; the content hash (of the body only, minus
	// frontmatter) lets `mio update` tell an untouched managed install (safe to
	// auto-refresh) from a hand-edited one (never clobber).
	skillVersionKey = "x-mio-skill-version"
	skillHashKey    = "x-mio-skill-content-hash"
)

// skillFileState classifies an existing SKILL.md at a target path.
type skillFileState int

const (
	skillMissing           skillFileState = iota // no file at the path
	skillUnmanaged                               // present but not written by us (no markers)
	skillManagedUnmodified                       // our markers present and body hash matches → safe to refresh
	skillManagedModified                         // our markers present but body hand-edited → never clobber
)

var (
	skillsInstallTarget  string
	skillsInstallUser    bool
	skillsInstallProject bool
	skillsInstallForce   bool
	skillsPrintTarget    string
)

func init() {
	skillsInstallCmd.Flags().StringVar(&skillsInstallTarget, "target", "", "Agent to install into: claude|codex. Default: detect (claude).")
	skillsInstallCmd.Flags().BoolVar(&skillsInstallUser, "user", false, "Install into the user-level agent dir (default).")
	skillsInstallCmd.Flags().BoolVar(&skillsInstallProject, "project", false, "Install into the project-level agent dir (./.claude or ./.codex).")
	skillsInstallCmd.Flags().BoolVar(&skillsInstallForce, "force", false, "Overwrite an existing hand-edited or unmanaged skill file.")

	skillsPrintCmd.Flags().StringVar(&skillsPrintTarget, "target", "", "Agent whose skill to print: claude|codex. Default: detect (claude).")

	skillsCmd.AddCommand(skillsInstallCmd, skillsPrintCmd)
	rootCmd.AddCommand(skillsCmd)
}

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Install the mio CLI agent skill into your coding agent.",
	Long: `Install the embedded mio CLI agent skill into your coding agent.

The skill teaches an agent to build a render-faithful Membership.io hub with the
CLI alone and to avoid the silent render-contract traps. It is embedded in the
binary, so 'mio update' ships the current skill and can refresh an unmodified
install automatically: the user-level copy wherever you run it, and a
project-level copy (./.claude or ./.codex) ONLY when you run 'mio update' from
that project's directory.

Targets:
  claude — Claude Code: ~/.claude/skills/mio/SKILL.md (--user) or ./.claude/skills/mio/SKILL.md (--project)
  codex  — Codex:       $CODEX_HOME/skills/mio/SKILL.md (--user) or ./.codex/skills/mio/SKILL.md (--project)`,
}

var skillsInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Write the embedded mio skill into your agent's skills directory.",
	Long: `Write the embedded mio agent skill to the target agent's skills directory.

The write is idempotent: re-running with the same CLI version is a no-op, and an
unmodified managed install is refreshed in place. A hand-edited or pre-existing
unmanaged file is never overwritten unless you pass --force.

It writes ONE scope. When the other scope holds an older mio-managed copy for the
same agent (the ./.claude or ./.codex copy for a --user install, the user-level
one for --project), a warning on stderr names it and the command that refreshes
it; that copy is not written.`,
	Example: `  mio skills install
  mio skills install --target codex
  mio skills install --project
  mio skills install --force`,
	Args: cobra.NoArgs,
	RunE: runSkillsInstall,
}

var skillsPrintCmd = &cobra.Command{
	Use:   "print",
	Short: "Print the embedded mio skill (SKILL.md) to stdout.",
	Long:  `Print the embedded mio agent skill, including frontmatter, to stdout for inspection or piping.`,
	Example: `  mio skills print
  mio skills print > SKILL.md`,
	Args: cobra.NoArgs,
	RunE: runSkillsPrint,
}

func runSkillsInstall(cmd *cobra.Command, _ []string) error {
	target, err := resolveSkillTarget(skillsInstallTarget)
	if err != nil {
		return err
	}
	project, err := resolveSkillScope(skillsInstallUser, skillsInstallProject)
	if err != nil {
		return err
	}
	path, err := skillDestPath(target, project)
	if err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}

	content := renderSkill(version.Version)

	state, err := classifySkillFile(path)
	if err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}

	// Idempotent no-op: the file on disk is already byte-identical to what we
	// would write.
	if existing, rerr := os.ReadFile(path); rerr == nil && string(existing) == content {
		fmt.Fprintf(cmd.OutOrStdout(), "mio skill already up to date for %s at %s (version %s)\n",
			targetLabel(target), path, version.Version)
		warnStaleOtherScope(cmd.ErrOrStderr(), target, project, path)
		return nil
	}

	switch state {
	case skillMissing, skillManagedUnmodified:
		// Safe to write: absent, or a managed install the user has not edited.
	case skillManagedModified, skillUnmanaged:
		if !skillsInstallForce {
			// A rejected-but-correctable op ("pass --force"), not an unexpected
			// failure — use the usage exit code (2) so agents branch on it.
			return errs.New(errs.ExitUsage,
				"%s already exists and looks hand-edited or unmanaged; pass --force to overwrite", path)
		}
	}

	if err := writeSkillFile(path, content); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Installed mio skill for %s at %s (version %s)\n",
		targetLabel(target), path, version.Version)
	warnStaleOtherScope(cmd.ErrOrStderr(), target, project, path)
	return nil
}

func runSkillsPrint(cmd *cobra.Command, _ []string) error {
	// The body is identical across targets; validate --target for forward-compat
	// and a helpful error, then emit the rendered SKILL.md.
	if _, err := resolveSkillTarget(skillsPrintTarget); err != nil {
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), renderSkill(version.Version))
	return nil
}

// skillRefreshHandoffTimeout bounds the handoff. A newly installed binary that
// wedges must not hang `mio update` indefinitely — "best-effort" has to mean
// bounded, not merely error-swallowing.
const skillRefreshHandoffTimeout = 30 * time.Second

// skillRefreshExec runs the NEWLY INSTALLED binary to rewrite a managed skill.
// It is a package var so tests can observe the handoff without exec'ing — but
// note that stubbing it removes the argv below from every oracle, so the real
// invocation is covered separately by TestSkillRefreshExec_RealBinaryArgv.
//
// The argv is loc.installArgs(true): the very command the remediation lines
// print, so what the refresh runs and what it tells a user to run cannot drift
// apart. A project-scope skill path is relative to the working directory, so
// the child is started IN that location's project root — the directory whose
// file was classified — rather than wherever it happens to inherit.
var skillRefreshExec = func(bin string, loc skillLocation) error {
	ctx, cancel := context.WithTimeout(context.Background(), skillRefreshHandoffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, loc.installArgs(true)...)
	if loc.project {
		cmd.Dir = loc.root
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// skillLocation is one place a mio skill can live: a target agent in one
// scope. The user scope is ~/.claude or $CODEX_HOME; the project scope is
// ./.claude or ./.codex under root, the working directory it was resolved in.
type skillLocation struct {
	target  string
	project bool
	path    string // absolute for the project scope
	root    string // project scope only: the directory path is relative to
}

// label names the location in messages: the agent, plus "(project)" for a
// project-scope copy, since both scopes can hold a skill for one agent.
func (l skillLocation) label() string {
	if l.project {
		return targetLabel(l.target) + " (project)"
	}
	return targetLabel(l.target)
}

// installArgs is the `mio skills install` argv (without the binary) that writes
// THIS location. Every remediation must carry --target — `--force` alone
// defaults to claude and acts on the wrong file — and a project-scope one must
// carry --project, or it rewrites the user copy and leaves this one stale.
func (l skillLocation) installArgs(force bool) []string {
	args := []string{"skills", "install"}
	if l.project {
		args = append(args, "--project")
	}
	if force {
		args = append(args, "--force")
	}
	return append(args, "--target", l.target)
}

// installCommand is installArgs rendered as the command a user types. A
// project-scope command only reaches this file from its project root, so that
// is named too.
func (l skillLocation) installCommand(force bool) string {
	cmd := "'mio " + strings.Join(l.installArgs(force), " ") + "'"
	if l.project {
		cmd += " in " + l.root
	}
	return cmd
}

// skillLocationFor resolves one target and scope to a location. A project
// path is made absolute against the current working directory, so a message
// names the file unambiguously and the handoff runs where it was classified.
func skillLocationFor(target string, project bool) (skillLocation, error) {
	path, err := skillDestPath(target, project)
	if err != nil {
		return skillLocation{}, err
	}
	loc := skillLocation{target: target, project: project, path: path}
	if project {
		root, err := os.Getwd()
		if err != nil {
			return skillLocation{}, fmt.Errorf("resolve working directory: %w", err)
		}
		loc.root = root
		loc.path = filepath.Join(root, path)
	}
	return loc, nil
}

// refreshLocations is every location `mio update` refreshes, in report order:
// both targets in the user scope, then both in the project scope of the
// CURRENT WORKING DIRECTORY (MIO-4178). Only that directory: a project copy
// anywhere else is found only when `mio update` runs there — nothing walks
// parent directories or searches the disk.
//
// A project location that is the same file as a user one is dropped. Run from
// $HOME, ./.claude IS ~/.claude, and one file must not be handed off twice or
// reported under two different remediation commands.
func refreshLocations() []skillLocation {
	var user, project []skillLocation
	for _, target := range []string{"claude", "codex"} {
		if loc, err := skillLocationFor(target, false); err == nil {
			user = append(user, loc)
		}
	}
	for _, target := range []string{"claude", "codex"} {
		loc, err := skillLocationFor(target, true)
		if err != nil {
			continue
		}
		dup := false
		for _, u := range user {
			if sameSkillFile(u.path, loc.path) {
				dup = true
			}
		}
		if !dup {
			project = append(project, loc)
		}
	}
	return append(user, project...)
}

// sameSkillFile reports whether two skill paths name one file: textually, or,
// when both exist, through a symlinked or otherwise aliased directory.
func sameSkillFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// refreshManagedSkills keeps a managed agent skill in sync after a self-update.
//
// It CANNOT render the new skill itself. The skill body is //go:embed-ed, so the
// running process only ever holds its OWN version's content; `renderSkill` just
// stamps a version string onto that body. Re-stamping it with the installed
// version would produce the old content under the new version's label, with a
// matching content hash — a skill that lies about which verbs exist and offers
// no signal that it does. That is worse than leaving it stale (MIO-2874).
//
// So the new binary is asked to write its own skill. The classification stays
// here, because it does not depend on version: an unmodified managed install is
// refreshed, a file the user owns is never touched (MIO-2875).
//
// Both scopes are covered (MIO-4178): the user scope, and the project scope of
// the working directory (see refreshLocations). A `--project` install used to
// stay at the version that wrote it forever, with nothing saying so.
//
// A refresh RESULT ("Refreshed ...", and the install nudge) goes to out; every
// file left alone or not refreshed is a warning and goes to errOut, in both
// scopes, so machine-readable stdout stays clean and one run never splits its
// warnings across two streams.
//
// EVERY REMEDIATION below must carry the location's label, path AND its
// loc.installCommand — --target <t>, plus --project and the project directory
// for a project copy (the "Refreshed ..." lines and the install nudge are not
// remediations). A project remediation without --project rewrites the USER
// copy and leaves the named file stale.
//
// `mio skills install --force` defaults to --target claude, so a bare
// suggestion printed about the Codex skill answers "already up to date" for
// Claude and leaves the reported file untouched — success-looking output,
// problem unfixed. When no Claude skill exists it CREATES one the user never
// had, and when a hand-edited Claude skill exists it OVERWRITES it. That last
// one is data loss reached by following our own instruction, so treat a
// target-less remediation string here as a defect, not a nit.
func refreshManagedSkills(out, errOut io.Writer, newBin string) {
	// installed counts every location that HAS a skill, whatever happened to
	// it. Without it, a failed refresh falls through to the "you have no skill
	// installed" nudge one line after naming the file it could not refresh.
	var installed int
	for _, loc := range refreshLocations() {
		path := loc.path
		state, err := classifySkillFile(path)
		if err != nil {
			// Unreadable is NOT "not installed" — a file is there, we just could
			// not classify it. Count it and say so, or the run goes silent (or,
			// worse, claims nothing is installed at a path that has one).
			installed++
			fmt.Fprintf(errOut, "Could not read the %s skill at %s (%v) — left untouched; inspect it, or run %s to replace it.\n",
				loc.label(), path, err, loc.installCommand(true))
			continue
		}
		switch state {
		case skillMissing:
			// Never installed here — do not write.
		case skillManagedUnmodified:
			installed++
			before, _ := os.ReadFile(path)
			if newBin == "" {
				// No usable path to the updated binary; say so rather than
				// leaving a silently stale skill behind.
				fmt.Fprintf(errOut, "Could not locate the updated mio binary to refresh the %s skill at %s — run %s with the new binary.\n",
					loc.label(), path, loc.installCommand(true))
				continue
			}
			if err := skillRefreshExec(newBin, loc); err != nil {
				fmt.Fprintf(errOut, "Could not refresh the %s skill at %s (%v) — run %s.\n",
					loc.label(), path, err, loc.installCommand(true))
				continue
			}
			after, rerr := os.ReadFile(path)
			if rerr != nil {
				// The child reported success but we cannot read the result. Say so:
				// `installed` is already counted, so a bare `continue` here would
				// print NOTHING at all and break the documented guarantee that a
				// failure always prints a line.
				fmt.Fprintf(errOut, "Refreshed the %s skill at %s but could not read it back (%v) — verify it, or run %s.\n",
					loc.label(), path, rerr, loc.installCommand(true))
				continue
			}
			if before != nil && string(after) == string(before) {
				// The child reported success and changed nothing — already current.
				// Saying "Refreshed" here would be a small lie in the commonest
				// case of all: re-running an update you already have. Stay quiet;
				// `installed` above already prevents the "none installed" nudge.
				continue
			}
			if ver, ok := skillFileVersion(string(after)); ok {
				fmt.Fprintf(out, "Refreshed mio skill for %s at %s (version %s)\n",
					loc.label(), path, ver)
			} else {
				fmt.Fprintf(out, "Refreshed mio skill for %s at %s\n", loc.label(), path)
			}
		// Both remediations carry --force, which REPLACES the file, so each says
		// so. And a file with no mio stamp is not "edited locally": it may be a
		// project's own notes that happen to live at ./.claude/skills/mio.
		case skillManagedModified:
			installed++
			fmt.Fprintf(errOut, "Your %s skill at %s was edited locally and was not refreshed — run %s to update it, which overwrites your edits.\n",
				loc.label(), path, loc.installCommand(true))
		case skillUnmanaged:
			installed++
			fmt.Fprintf(errOut, "The %s skill at %s was not installed by mio and was not refreshed — run %s to replace it with mio's skill, which overwrites it.\n",
				loc.label(), path, loc.installCommand(true))
		}
	}
	// Only suggest installing one when there genuinely is none, in EITHER scope.
	// `installed` counts a skill existing regardless of what happened to it — a
	// failed refresh, or an unreadable file, must not contradict the line above.
	// A project copy counts too: offering to install a user copy beside a stale
	// project one leaves the agent reading the stale one (MIO-4178).
	if installed == 0 {
		fmt.Fprintln(out, "A mio CLI agent skill is available — run 'mio skills install' to add it to Claude Code.")
	}
}

// resolveSkillTarget validates and normalizes the --target flag. An empty value
// detects the target (per spec: Claude Code).
func resolveSkillTarget(t string) (string, error) {
	switch t {
	case "", "auto":
		return detectSkillTarget(), nil
	case "claude", "codex":
		return t, nil
	default:
		return "", errs.New(errs.ExitUsage, "unknown --target %q (valid: claude, codex)", t)
	}
}

// detectSkillTarget picks the default target. Per the current spec Claude Code is
// always the default; the ~/.claude probe documents the intended detection point.
func detectSkillTarget() string {
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
			return "claude"
		}
	}
	return "claude"
}

// resolveSkillScope returns whether to use the project-level path. --user is the
// default; --user and --project are mutually exclusive.
func resolveSkillScope(user, project bool) (bool, error) {
	if user && project {
		return false, errs.New(errs.ExitUsage, "--user and --project are mutually exclusive")
	}
	return project, nil
}

// skillPathResolved sees every skill path before anything reads or writes it:
// skillDestPath is the only place one is derived. A no-op in production. The
// cmd test binary replaces it with a check that panics on a path outside the
// test's temp sandbox, so a test that forgot to move HOME, CODEX_HOME or its
// working directory fails before it can touch a real skill (MIO-4178).
var skillPathResolved = func(string) {}

// skillDestPath resolves the SKILL.md path for a target and scope.
func skillDestPath(target string, project bool) (string, error) {
	var path string
	switch target {
	case "claude":
		if project {
			path = filepath.Join(".claude", "skills", skillDirName, skillFileName)
			break
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, ".claude", "skills", skillDirName, skillFileName)
	case "codex":
		if project {
			path = filepath.Join(".codex", "skills", skillDirName, skillFileName)
			break
		}
		path = filepath.Join(codexHome(), "skills", skillDirName, skillFileName)
	default:
		return "", fmt.Errorf("unknown target %q", target)
	}
	skillPathResolved(path)
	return path, nil
}

// codexHome resolves Codex's home directory, honoring $CODEX_HOME (its documented
// override) and falling back to ~/.codex.
func codexHome() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

func targetLabel(t string) string {
	switch t {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	default:
		return t
	}
}

// renderSkill builds the full SKILL.md (frontmatter + body) stamped with ver and
// the body content hash. The hash covers the body only, so it is re-derivable on
// read regardless of the version stamped in the frontmatter.
func renderSkill(ver string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + skillName + "\n")
	b.WriteString("description: " + skillDescription + "\n")
	b.WriteString(skillVersionKey + ": " + ver + "\n")
	b.WriteString(skillHashKey + ": " + sha256hex(skillBody) + "\n")
	b.WriteString("---\n")
	b.WriteString(skillBody)
	return b.String()
}

// skillFileVersion reports the version recorded in a skill file's frontmatter.
// Used to report what the refreshed file actually says, rather than what the
// running (pre-update) binary assumes it says.
func skillFileVersion(content string) (string, bool) {
	fields, _, ok := splitFrontmatter(content)
	if !ok {
		return "", false
	}
	v, ok := fields[skillVersionKey]
	return v, ok
}

// classifySkillFile reads the file at path and classifies whether it is safe to
// (over)write.
func classifySkillFile(path string) (skillFileState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return skillMissing, nil
	}
	if err != nil {
		return skillMissing, err
	}
	fields, body, ok := splitFrontmatter(string(data))
	if !ok {
		return skillUnmanaged, nil
	}
	_, hasVer := fields[skillVersionKey]
	stored, hasHash := fields[skillHashKey]
	if !hasVer || !hasHash {
		return skillUnmanaged, nil
	}
	if sha256hex(body) == stored {
		return skillManagedUnmodified, nil
	}
	return skillManagedModified, nil
}

// writeSkillFile atomically writes content to path, creating parent directories.
// It writes to a temp file in the SAME directory and renames on success, so a
// failure mid-write can never leave a truncated/corrupted skill in place (rename
// is atomic on the same filesystem). This matters most for the best-effort
// `mio update` refresh, which must never damage an existing install.
func writeSkillFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mio-skill-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup of the temp file if we bail before a successful rename.
	// After a successful rename tmpName no longer exists and Remove is a no-op.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// splitFrontmatter separates a leading `---`-delimited YAML frontmatter block
// from the body. It returns the parsed simple key/value fields, the body
// (everything after the closing delimiter, verbatim), and ok=false when the
// content has no well-formed frontmatter block.
func splitFrontmatter(content string) (map[string]string, string, bool) {
	const delim = "---\n"
	if !strings.HasPrefix(content, delim) {
		return nil, content, false
	}
	rest := content[len(delim):]
	idx := strings.Index(rest, "\n"+delim) // closing "\n---\n"
	if idx < 0 {
		return nil, content, false
	}
	block := rest[:idx]
	body := rest[idx+len("\n"+delim):]

	fields := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		c := strings.IndexByte(line, ':')
		if c < 0 {
			continue
		}
		key := strings.TrimSpace(line[:c])
		if key == "" {
			continue
		}
		fields[key] = strings.TrimSpace(line[c+1:])
	}
	return fields, body, true
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// warnStaleOtherScope tells `mio skills install` users when the OTHER scope
// holds an older managed copy for the same agent (MIO-4178). QA ran `mio skills
// install` (user scope) while their agent read a project copy nine releases
// behind; the install succeeded and said nothing, so the stale copy stayed.
// It only warns and never writes: the other copy may be deliberately pinned,
// and a project copy is often committed to a repo.
//
// A version that does not parse — "dev" on a source build, a hand-typed stamp —
// is not comparable, so no claim of "older" is made from it.
func warnStaleOtherScope(w io.Writer, target string, project bool, written string) {
	other, err := skillLocationFor(target, !project)
	if err != nil || sameSkillFile(other.path, written) {
		return
	}
	state, err := classifySkillFile(other.path)
	if err != nil || (state != skillManagedUnmodified && state != skillManagedModified) {
		return
	}
	data, err := os.ReadFile(other.path)
	if err != nil {
		return
	}
	ver, ok := skillFileVersion(string(data))
	if !ok || !skillVersionOlder(ver, version.Version) {
		return
	}
	if state == skillManagedModified {
		fmt.Fprintf(w, "Warning: the %s skill at %s is version %s, older than this CLI (%s), and was edited locally — %s refreshes it but overwrites your edits.\n",
			other.label(), other.path, ver, version.Version, other.installCommand(true))
		return
	}
	fmt.Fprintf(w, "Warning: the %s skill at %s is version %s, older than this CLI (%s) — run %s to refresh it.\n",
		other.label(), other.path, ver, version.Version, other.installCommand(false))
}

// skillVersionOlder reports whether skill version have precedes than, both
// "MAJOR.MINOR.PATCH" with an optional leading "v" and an optional
// "-prerelease" (which precedes its release, as in semver). Anything else is
// not comparable and reports false.
func skillVersionOlder(have, than string) bool {
	h, hpre, ok1 := parseSkillVersion(have)
	t, tpre, ok2 := parseSkillVersion(than)
	if !ok1 || !ok2 {
		return false
	}
	for i := range h {
		if h[i] != t[i] {
			return h[i] < t[i]
		}
	}
	switch {
	case hpre == tpre:
		return false
	case hpre == "":
		return false // a release is newer than any of its pre-releases
	case tpre == "":
		return true
	default:
		return preReleaseLess(hpre, tpre)
	}
}

// preReleaseLess orders two pre-release strings the semver way: dot-separated
// identifiers left to right, numeric ones numerically (rc.9 < rc.10) and below
// alphanumeric ones, and a shorter list first when one is a prefix of the other.
func preReleaseLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			return an < bn
		case aerr == nil:
			return true
		case berr == nil:
			return false
		default:
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

func parseSkillVersion(v string) (core [3]int, pre string, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return core, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return core, "", false
		}
		core[i] = n
	}
	return core, pre, true
}
