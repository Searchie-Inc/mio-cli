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

//go:generate go run ../internal/docsgen/cmd/skilldocs -file skills/content/mio-skill.md
//go:embed skills/content/mio-skill.md
var skillBody string

const (
	skillName        = "mio"
	skillDescription = "Use when building or automating a Membership.io hub with the mio CLI — the CLI-only recipe to create a hub, playlists, media, pages and homepage, plus the render-contract silent-drop traps and the contact-id namespace trap."

	skillDirName  = "mio"
	skillFileName = "SKILL.md"

	skillVersionKey = "x-mio-skill-version"
	skillHashKey    = "x-mio-skill-content-hash"
)

type skillFileState int

const (
	skillMissing skillFileState = iota
	skillUnmanaged
	skillManagedUnmodified
	skillManagedModified
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

	if existing, rerr := os.ReadFile(path); rerr == nil && string(existing) == content {
		fmt.Fprintf(cmd.OutOrStdout(), "mio skill already up to date for %s at %s (version %s)\n",
			targetLabel(target), path, version.Version)
		warnStaleOtherScope(cmd.ErrOrStderr(), target, project, path)
		return nil
	}

	switch state {
	case skillMissing, skillManagedUnmodified:
	case skillManagedModified, skillUnmanaged:
		if !skillsInstallForce {
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
	if _, err := resolveSkillTarget(skillsPrintTarget); err != nil {
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), renderSkill(version.Version))
	return nil
}

const skillRefreshHandoffTimeout = 30 * time.Second

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

type skillLocation struct {
	target  string
	project bool
	path    string
	root    string
}

func (l skillLocation) label() string {
	if l.project {
		return targetLabel(l.target) + " (project)"
	}
	return targetLabel(l.target)
}

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

func (l skillLocation) installCommand(force bool) string {
	cmd := "'mio " + strings.Join(l.installArgs(force), " ") + "'"
	if l.project {
		cmd += " in " + l.root
	}
	return cmd
}

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

func sameSkillFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

func refreshManagedSkills(out, errOut io.Writer, newBin string) {
	var installed int
	for _, loc := range refreshLocations() {
		path := loc.path
		state, err := classifySkillFile(path)
		if err != nil {
			installed++
			fmt.Fprintf(errOut, "Could not read the %s skill at %s (%v) — left untouched; inspect it, or run %s to replace it.\n",
				loc.label(), path, err, loc.installCommand(true))
			continue
		}
		switch state {
		case skillMissing:
		case skillManagedUnmodified:
			installed++
			before, _ := os.ReadFile(path)
			if newBin == "" {
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
				fmt.Fprintf(errOut, "Refreshed the %s skill at %s but could not read it back (%v) — verify it, or run %s.\n",
					loc.label(), path, rerr, loc.installCommand(true))
				continue
			}
			if before != nil && string(after) == string(before) {
				continue
			}
			if ver, ok := skillFileVersion(string(after)); ok {
				fmt.Fprintf(out, "Refreshed mio skill for %s at %s (version %s)\n",
					loc.label(), path, ver)
			} else {
				fmt.Fprintf(out, "Refreshed mio skill for %s at %s\n", loc.label(), path)
			}
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
	if installed == 0 {
		fmt.Fprintln(out, "A mio CLI agent skill is available — run 'mio skills install' to add it to Claude Code.")
	}
}

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

func detectSkillTarget() string {
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
			return "claude"
		}
	}
	return "claude"
}

func resolveSkillScope(user, project bool) (bool, error) {
	if user && project {
		return false, errs.New(errs.ExitUsage, "--user and --project are mutually exclusive")
	}
	return project, nil
}

var skillPathResolved = func(string) {}

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

func skillFileVersion(content string) (string, bool) {
	fields, _, ok := splitFrontmatter(content)
	if !ok {
		return "", false
	}
	v, ok := fields[skillVersionKey]
	return v, ok
}

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

func splitFrontmatter(content string) (map[string]string, string, bool) {
	const delim = "---\n"
	if !strings.HasPrefix(content, delim) {
		return nil, content, false
	}
	rest := content[len(delim):]
	idx := strings.Index(rest, "\n"+delim)
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
		return false
	case tpre == "":
		return true
	default:
		return preReleaseLess(hpre, tpre)
	}
}

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
