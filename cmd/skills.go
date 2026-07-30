package cmd

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
install automatically.

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
unmanaged file is never overwritten unless you pass --force.`,
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

// refreshManagedSkills is called by `mio update` after a successful self-update.
// For each installed target it refreshes an unmodified managed install to the
// new embedded skill, never clobbers a hand-edited or unmanaged file, and never
// writes to a target that was never installed. If nothing is installed anywhere
// it prints a one-line nudge. It is best-effort: any error is swallowed so a
// skill hiccup can never fail the update itself.
func refreshManagedSkills(w io.Writer) {
	content := renderSkill(version.Version)

	var refreshed, current, modified int
	for _, target := range []string{"claude", "codex"} {
		path, err := skillDestPath(target, false) // user scope only
		if err != nil {
			continue
		}
		state, err := classifySkillFile(path)
		if err != nil {
			continue
		}
		switch state {
		case skillMissing:
			// Never installed for this target — do not write.
		case skillManagedUnmodified:
			if existing, rerr := os.ReadFile(path); rerr == nil && string(existing) == content {
				current++ // already current, nothing to do
				continue
			}
			if werr := writeSkillFile(path, content); werr == nil {
				refreshed++
				fmt.Fprintf(w, "Refreshed mio skill for %s at %s (version %s)\n",
					targetLabel(target), path, version.Version)
			}
		case skillManagedModified, skillUnmanaged:
			modified++
		}
	}

	switch {
	case refreshed > 0 || current > 0:
		// At least one managed install exists; refresh lines (if any) already
		// printed. Do not nag.
		if modified > 0 && refreshed == 0 {
			fmt.Fprintln(w, "Your mio CLI agent skill was edited locally and was not refreshed — run 'mio skills install --force' to update it.")
		}
	case modified > 0:
		fmt.Fprintln(w, "Your mio CLI agent skill was edited locally and was not refreshed — run 'mio skills install --force' to update it.")
	default:
		fmt.Fprintln(w, "A mio CLI agent skill is available — run 'mio skills install' to add it to Claude Code.")
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

// skillDestPath resolves the SKILL.md path for a target and scope.
func skillDestPath(target string, project bool) (string, error) {
	switch target {
	case "claude":
		if project {
			return filepath.Join(".claude", "skills", skillDirName, skillFileName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(home, ".claude", "skills", skillDirName, skillFileName), nil
	case "codex":
		if project {
			return filepath.Join(".codex", "skills", skillDirName, skillFileName), nil
		}
		return filepath.Join(codexHome(), "skills", skillDirName, skillFileName), nil
	default:
		return "", fmt.Errorf("unknown target %q", target)
	}
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
