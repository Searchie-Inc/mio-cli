package cmd

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

type storedKeyGuard func(newTeamID string) error

func noStoredKeyGuard(string) error { return nil }

func storedKeyGuardFor(cmd *cobra.Command, apiBase string, in io.Reader) storedKeyGuard {
	return func(newTeamID string) error {
		return confirmReplaceStoredKey(cmd, apiBase, newTeamID, in, !flags.anonymous)
	}
}

var stdinIsTerminal = isInteractiveStdin

func confirmReplaceStoredKey(cmd *cobra.Command, apiBase, newTeamID string, in io.Reader, verify bool) error {
	stored, err := config.GetAPIKey()
	var what string
	switch {
	case err != nil && config.StoredKeyUnusable(err):
		return nil
	case err != nil:
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
			what = fmt.Sprintf("a stored API key that could not be verified against %s (%s)", apiBase, describeFailure(merr))
		}
	}

	prompt := fmt.Sprintf("This will REPLACE %s. mio keeps ONE key and cannot restore it afterwards; if that key is your only way into the account (no password set), you could be locked out for good. "+
		"To keep it, export it first (MIO_API_KEY=\"$(mio auth token)\"; export MIO_API_KEY) or save it somewhere safe", what)
	return confirmReplacement(cmd, prompt, in)
}

func describeFailure(err error) string {
	if st := errs.HTTPStatusOf(err); st != 0 {
		return fmt.Sprintf("HTTP %d", st)
	}
	return "the request failed"
}

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
