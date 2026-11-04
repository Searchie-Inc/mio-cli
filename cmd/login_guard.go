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
	"fmt"
	"io"

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

// storedKeyGuardFor builds the guard for one login invocation.
func storedKeyGuardFor(cmd *cobra.Command, apiBase string, in io.Reader) storedKeyGuard {
	return func(newTeamID string) error {
		return confirmReplaceStoredKey(cmd, apiBase, newTeamID, in)
	}
}

// confirmReplaceStoredKey implements the policy above.
func confirmReplaceStoredKey(cmd *cobra.Command, apiBase, newTeamID string, in io.Reader) error {
	stored, err := config.GetAPIKey()
	if err != nil || stored == "" {
		// Nothing readable to protect. An unreadable blob is replaced by design
		// (MIO-2995); any other read failure surfaces at the write that follows.
		return nil
	}
	cli := client.New(apiBase, stored, client.WithDebug(flags.debug))
	var what string
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
		what = fmt.Sprintf("a stored API key that could not be verified against %s (%v)", apiBase, merr)
	}

	prompt := fmt.Sprintf("This will REPLACE %s. mio keeps ONE key and cannot restore it afterwards; if that key is your only way into the account (no password set), you could be locked out for good. "+
		"To keep it, export it first (MIO_API_KEY=\"$(mio auth token)\"; export MIO_API_KEY) or save it somewhere safe", what)
	return confirmDestructiveFrom(cmd, prompt, in)
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
