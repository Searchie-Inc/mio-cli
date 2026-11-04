package cmd

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/spf13/cobra"
)

func init() {
	mediaFilesCmd.AddCommand(mediaFilesCaptionsCmd)
	mediaFilesCaptionsCmd.AddCommand(mediaFilesCaptionsGetCmd, mediaFilesCaptionsSetCmd, mediaFilesCaptionsRemoveCmd)
	mediaFilesCaptionsSetCmd.Flags().String("language", "", "Caption language tag.")
	mediaFilesCaptionsSetCmd.Flags().String("vtt", "", "VTT text or @file.vtt.")
	markFlagsRequired(mediaFilesCaptionsSetCmd, "language", "vtt")
}

func requireFileID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errs.New(errs.ExitUsage, "file id is blank (an unset variable?)")
	}
	return nil
}

func vttFlag(cmd *cobra.Command) (string, error) {
	raw, _ := cmd.Flags().GetString("vtt")
	if strings.HasPrefix(strings.TrimSpace(raw), "@") {
		path := strings.TrimPrefix(strings.TrimSpace(raw), "@")
		data, err := os.ReadFile(path)
		if err != nil {
			return "", errs.New(errs.ExitUsage, "--vtt: read %s: %s", path, err)
		}
		raw = string(data)
	}
	if strings.TrimSpace(raw) == "" {
		return "", errs.New(errs.ExitUsage, "--vtt must not be empty")
	}
	return raw, nil
}

var mediaFilesCaptionsCmd = &cobra.Command{Use: "captions", Short: "Manage a file's captions."}
var mediaFilesCaptionsGetCmd = &cobra.Command{Use: "get <file_id>", Short: "Get a file's caption track.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := requireFileID(args[0]); err != nil {
		return err
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	col, err := c.client.List(c.ctx, filesPath(team, args[0])+"/captions", nil)
	if err != nil {
		return err
	}
	if len(col.Data) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: file %s has no caption track\n", args[0])
		return nil
	}
	res := col.Data[0]
	res.RawBody = col.RawBody
	return c.render(cmd, &res)
}}
var mediaFilesCaptionsSetCmd = &cobra.Command{Use: "set <file_id>", Short: "Import a caption VTT.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := requireFileID(args[0]); err != nil {
		return err
	}
	language, _ := cmd.Flags().GetString("language")
	if strings.TrimSpace(language) == "" {
		return errs.New(errs.ExitUsage, "--language must not be empty")
	}
	vtt, err := vttFlag(cmd)
	if err != nil {
		return err
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	res, err := c.client.Action(c.ctx, http.MethodPut, filesPath(team, args[0])+"/captions", map[string]any{"language": language, "vtt": vtt})
	if err != nil {
		return err
	}
	return c.render(cmd, res)
}}
var mediaFilesCaptionsRemoveCmd = &cobra.Command{Use: "remove <file_id>", Short: "Remove imported captions.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := requireFileID(args[0]); err != nil {
		return err
	}
	if err := confirmDestructive(cmd, fmt.Sprintf("Remove captions from file %s?", args[0])); err != nil {
		return err
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	return c.client.Delete(c.ctx, filesPath(team, args[0])+"/captions")
}}
