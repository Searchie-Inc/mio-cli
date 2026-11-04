package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/spf13/cobra"
)

func init() {
	mediaCmd.AddCommand(remoteConnectionsCmd)
	remoteConnectionsCmd.AddCommand(remoteListCmd, remoteConnectCmd, remoteDisconnectCmd, remoteBrowseCmd, remoteImportCmd)
	remoteConnectCmd.Flags().String("url", "", "Podcast feed URL (required).")
	remoteConnectCmd.Flags().Bool("no-auto-import", false, "Disable automatic imports.")
	remoteConnectCmd.Flags().StringArray("target-playlist-id", nil, "Target playlist id (repeatable, at most 10).")
	addPaginationFlags(remoteBrowseCmd)
	remoteImportCmd.Flags().StringArray("external-id", nil, "Episode external id (repeatable).")
	remoteImportCmd.Flags().StringArray("external-ids", nil, "Comma-separated episode external ids.")
}

var remoteConnectionsCmd = &cobra.Command{Use: "remote-connections", Short: "Manage remote podcast connections."}

func remotePath(team, id string) string {
	p := fmt.Sprintf("/api/teams/%s/remote-connections", team)
	if id != "" {
		p += "/" + id
	}
	return p
}
func remoteID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errs.New(errs.ExitUsage, "connection id is blank (an unset variable?)")
	}
	return nil
}

var remoteListCmd = &cobra.Command{Use: "list", Short: "List remote connections.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	col, err := c.client.List(c.ctx, remotePath(team, ""), nil)
	if err != nil {
		return err
	}
	return c.render(cmd, col)
}}
var remoteConnectCmd = &cobra.Command{Use: "connect", Short: "Connect a podcast feed.", Long: "Connect a podcast feed. This requires a USER access token from POST /api/v1/auth/login, passed with --api-key; an API key cannot connect. List, browse, import and disconnect accept API keys.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	feed := flagValue(cmd, "url")
	if strings.TrimSpace(feed) == "" {
		return errs.New(errs.ExitUsage, "--url must be non-blank")
	}
	ids, _ := cmd.Flags().GetStringArray("target-playlist-id")
	if len(ids) > 10 {
		return errs.New(errs.ExitUsage, "--target-playlist-id accepts at most 10 distinct ids")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			return errs.New(errs.ExitUsage, "--target-playlist-id must contain distinct non-blank ids")
		}
		seen[id] = true
	}
	attrs := map[string]any{"provider": "podcast", "url": feed}
	if cmd.Flags().Changed("no-auto-import") {
		v, _ := cmd.Flags().GetBool("no-auto-import")
		if v {
			attrs["auto_import"] = false
		}
	}
	if len(ids) > 0 {
		attrs["target_playlist_ids"] = ids
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	res, err := c.client.CreateWith(c.ctx, client.StyleFlat, remotePath(team, ""), attrs)
	if err != nil {
		var ce *errs.CLIError
		if errors.As(err, &ce) && ce.HTTPStatus == http.StatusForbidden {
			return fmt.Errorf("%w; hint: connect needs a USER access token; the stored/API key cannot connect a podcast. Get access_token from POST /api/v1/auth/login and pass it with --api-key; list, browse, import and disconnect accept the API key", err)
		}
		return err
	}
	return c.render(cmd, res)
}}
var remoteDisconnectCmd = &cobra.Command{Use: "disconnect <connection_id>", Short: "Disconnect a remote connection.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := remoteID(args[0]); err != nil {
		return err
	}
	if err := confirmDestructive(cmd, fmt.Sprintf("Disconnect %s?", args[0])); err != nil {
		return err
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	if err = c.client.Delete(c.ctx, remotePath(team, args[0])); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Disconnected %s.\n", args[0])
	return nil
}}
var remoteBrowseCmd = &cobra.Command{Use: "browse <connection_id>", Short: "Browse podcast episodes.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := remoteID(args[0]); err != nil {
		return err
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	q := url.Values{}
	addPageFlags(cmd, q)
	col, err := c.client.List(c.ctx, remotePath(team, args[0])+"/browse", q)
	if err != nil {
		return err
	}
	return c.render(cmd, col)
}}
var remoteImportCmd = &cobra.Command{Use: "import <connection_id>", Short: "Import podcast episodes.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := remoteID(args[0]); err != nil {
		return err
	}
	ids, _ := cmd.Flags().GetStringArray("external-id")
	groups, _ := cmd.Flags().GetStringArray("external-ids")
	for _, group := range groups {
		ids = append(ids, strings.Split(group, ",")...)
	}
	// The API is authoritative; mirror its batch constraints to name the flags before a round trip.
	if len(ids) == 0 || len(ids) > 50 {
		return errs.New(errs.ExitUsage, "--external-id/--external-ids require 1 to 50 distinct ids")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			return errs.New(errs.ExitUsage, "--external-id/--external-ids must contain distinct non-blank ids")
		}
		seen[id] = true
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	col, err := c.client.ActionCollection(c.ctx, http.MethodPost, remotePath(team, args[0])+"/import", map[string]any{"external_ids": ids})
	if err != nil {
		return err
	}
	n := 0
	for _, item := range col.Data {
		if item.Attributes["status"] == "rejected" {
			n++
		}
	}
	if n > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %d of %d episodes were rejected (see .attributes.error)\n", n, len(col.Data))
	}
	return c.render(cmd, col)
}}
