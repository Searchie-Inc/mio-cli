package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func init() {
	mediaFilesChaptersCmd.AddCommand(mediaFilesChaptersImportCmd, mediaFilesChaptersAutoCmd)
	mediaFilesChaptersImportCmd.Flags().String("vtt", "", "VTT text or @file.vtt.")
	markFlagsRequired(mediaFilesChaptersImportCmd, "vtt")
	mediaFilesChaptersAutoCmd.Flags().Bool("off", false, "Turn off transcript-derived chapters.")
}

var mediaFilesChaptersImportCmd = &cobra.Command{Use: "import <file_id>", Short: "Replace authored chapters from VTT.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := requireFileID(args[0]); err != nil {
		return err
	}
	vtt, err := vttFlag(cmd)
	if err != nil {
		return err
	}
	if err := confirmDestructive(cmd, fmt.Sprintf("Replace authored chapters for file %s?", args[0])); err != nil {
		return err
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	col, err := c.client.ActionCollection(c.ctx, http.MethodPost, fileChaptersPath(team, args[0])+"/import", map[string]any{"vtt": vtt})
	if err != nil {
		return err
	}
	return c.render(cmd, col)
}}
var mediaFilesChaptersAutoCmd = &cobra.Command{Use: "auto <file_id>", Short: "Rebuild chapters from the transcript, or turn them off.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := requireFileID(args[0]); err != nil {
		return err
	}
	off, _ := cmd.Flags().GetBool("off")
	method := http.MethodPost
	if off {
		method = http.MethodDelete
	} else if err := confirmDestructive(cmd, fmt.Sprintf("Rebuild chapters for file %s?", args[0])); err != nil {
		return err
	}
	c, team, err := mediaContext(cmd)
	if err != nil {
		return err
	}
	col, err := c.client.ActionCollection(c.ctx, method, fileChaptersPath(team, args[0])+"/auto", nil)
	if err != nil {
		return err
	}
	return c.render(cmd, col)
}}
