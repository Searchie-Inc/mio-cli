package cmd

// login_guard.go — protect the one stored API key from being silently
// overwritten (MIO-3572).
//
// mio keeps exactly ONE API key (a single credential-store slot). `mio login`
// and `mio register` replaced it without a word. QA's incident: a platform
// account that was driven entirely by a CLI key (no password ever set) had its
// only key overwritten by `mio register` on the same machine — permanently
// locked out, with the team's hubs still live and un-administrable.
//
// The guard runs BEFORE anything irreversible (before the account is
// registered, before an API key is minted) and asks the API what the stored key
// is, so the prompt can name the team it would destroy access to:
//
//   - no stored key, or one this build cannot read (legacy/undecodable — login
//     replacing that is the MIO-2995 recovery path) → proceed silently;
//   - stored key valid for the SAME team being logged into → proceed silently
//     (the ordinary re-login; the old key still works server-side);
//   - stored key valid for a DIFFERENT team → confirm;
//   - stored key the API rejects (401/403) or that cannot be verified (network,
//     5xx) → confirm: "rejected" is also what a key issued for another API base
//     looks like (dev vs prod), and the CLI cannot tell the two apart.
//
// Confirmation reuses the destructive-op contract (confirmDestructiveFrom): a
// TTY gets a y/N prompt, a non-interactive shell exits 5 unless --yes, and
// nothing has been sent that cannot be undone when it refuses.

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// storedKeyGuard is called with the team a login is about to store a key for
// ("" when it is not known yet — a registration creates a NEW team) and returns
// a non-nil error to stop before anything irreversible happens.
type storedKeyGuard func(newTeamID string) error

// noStoredKeyGuard is for callers that already confirmed (register asks once,
// up front, before it creates the account).
func noStoredKeyGuard(string) error { return nil }

// storedKeyGuardFor builds the guard for one login invocation. The stored key
// is only sent to the API (to learn its team) when this run would send it there
// anyway — never with --anonymous, which promises to ignore stored credentials.
func storedKeyGuardFor(cmd *cobra.Command, apiBase string, in io.Reader) storedKeyGuard {
	return func(newTeamID string) error {
		return confirmReplaceStoredKey(cmd, apiBase, newTeamID, in, !flags.anonymous)
	}
}

// stdinIsTerminal reports whether the answer can come from a person. A
// package variable so tests can stand in for a terminal.
var stdinIsTerminal = isInteractiveStdin

// confirmReplaceStoredKey implements the policy above. verify=false skips the
// lookup of the stored key's team, and the prompt is then generic ("the stored
// API key"). It is used wherever this run would NOT otherwise send the stored key
// to the API base: register (it documents that it sends no key, and always makes a
// new team, so the answer is "ask" either way), a login that carries an explicit
// key (--api-key / MIO_API_KEY: ordinary commands would send THAT key, not the
// stored one), and --anonymous (which promises to ignore stored credentials).
func confirmReplaceStoredKey(cmd *cobra.Command, apiBase, newTeamID string, in io.Reader, verify bool) error {
	stored, err := config.GetAPIKey()
	var what string
	switch {
	case err != nil && config.StoredKeyUnusable(err):
		// Legacy/undecodable blob: nothing usable to protect; replacing it is the
		// MIO-2995 recovery path.
		return nil
	case err != nil:
		// Something is stored (or the store itself misbehaves) and mio cannot
		// read it — a permissions problem, say. `MIO_API_KEY=<key> mio login` is
		// the DOCUMENTED way out of exactly that (MIO-2995; `mio auth token
		// --help`), and the old credential cannot be exported either, so a
		// confirmation would only block the recovery. It is never silent, though:
		// the note names what is being replaced, and that fixing the file's
		// permissions first would have kept it.
		fmt.Fprintf(cmd.ErrOrStderr(), "Note: replacing a stored credential mio could not read (%v). If it was a valid key, fix the file's permissions first to keep it.\n", err)
		return nil
	case stored == "":
		return nil
	case !verify:
		what = "the stored API key (this signs the machine into a different account and its new team)"
	default:
		cli := client.New(apiBase, stored, client.WithDebug(flags.debug))
		me, merr := cli.Me(cmd.Context())
		switch {
		case merr == nil:
			storedTeam, _ := me["team_id"].(string)
			if newTeamID != "" && storedTeam == newTeamID {
				return nil
			}
			what = "the stored API key for " + describeStoredTeam(cmd, cli, storedTeam)
		case errs.HTTPStatusOf(merr) == 401 || errs.HTTPStatusOf(merr) == 403:
			what = fmt.Sprintf("a stored API key that %s did not accept (revoked or expired — or issued for a different API base)", apiBase)
		default:
			// Status only: the error text can carry a response body, and a
			// proxy or server that reflects its request headers would put the
			// stored key into this message.
			what = fmt.Sprintf("a stored API key that could not be verified against %s (%s)", apiBase, describeFailure(merr))
		}
	}

	prompt := fmt.Sprintf("This will REPLACE %s. mio keeps ONE key and cannot restore it afterwards; if that key is your only way into the account (no password set), you could be locked out for good. "+
		"To keep it, export it first (MIO_API_KEY=\"$(mio auth token)\"; export MIO_API_KEY) or save it somewhere safe", what)
	return confirmReplacement(cmd, prompt, in)
}

// describeFailure names a failed request without its body.
func describeFailure(err error) string {
	if st := errs.HTTPStatusOf(err); st != 0 {
		return fmt.Sprintf("HTTP %d", st)
	}
	return "the request failed"
}

// confirmReplacement is the y/N question. It is deliberately stricter than the
// generic destructive-op prompt (confirmDestructive), which treats any character
// device on stdout — /dev/null included — as a terminal: here an answer is only
// accepted from a REAL terminal on stdin, so `printf 'y\n' | mio login … >/dev/null`
// cannot approve a replacement. Non-interactive callers pass --yes (or get exit 5).
// The whole line must be exactly y/yes.
func confirmReplacement(cmd *cobra.Command, prompt string, in io.Reader) error {
	if flags.yes {
		return nil
	}
	if !stdinIsTerminal(cmd) {
		return errs.New(errs.ExitNeedsConfir,
			"%s — refusing in a non-interactive shell; pass --yes to proceed", prompt)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N]: ", prompt)
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	line, rerr := reader.ReadString('\n')
	// A complete line only: an answer cut short by EOF is not consent.
	if rerr != nil {
		return errs.New(errs.ExitNeedsConfir, "aborted")
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return errs.New(errs.ExitNeedsConfir, "aborted")
	}
}

// describeStoredTeam names the team a stored key belongs to, best-effort: the
// name lookup failing must never block the prompt, the id is the answer.
func describeStoredTeam(cmd *cobra.Command, cli *client.Client, teamID string) string {
	if teamID == "" {
		return "an account (the API did not report its team)"
	}
	if res, err := cli.Retrieve(cmd.Context(), teamsPath(teamID)); err == nil && res != nil {
		if name, _ := res.Attributes["name"].(string); name != "" {
			return fmt.Sprintf("team %s (%s)", name, teamID)
		}
	}
	return "team " + teamID
}
