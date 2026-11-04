package cmd

// media_files_thumbnail.go — `mio media files thumbnail` (MIO-5832).
// The team-scoped file-thumbnail-attachments POST upserts an attachment with
// target_type=file and role=thumbnail. Its media_id is a Media PK, not a file id.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/spf13/cobra"
)

func init() {
	mediaFilesThumbnailCmd.AddCommand(mediaFilesThumbnailSetCmd, mediaFilesThumbnailRemoveCmd)
	mediaFilesCmd.AddCommand(mediaFilesThumbnailCmd)
	mediaFilesThumbnailSetCmd.Flags().String("image-file-id", "", "Uploaded image file id (resolved to its media id).")
	mediaFilesThumbnailSetCmd.Flags().String("image-media-id", "", "Image Media PK, validated by the backend.")
}

func fileThumbnailAttachmentsPath(teamID string) string {
	return fmt.Sprintf("/api/teams/%s/file-thumbnail-attachments", teamID)
}

var mediaFilesThumbnailCmd = &cobra.Command{Use: "thumbnail", Short: "Manage a file's episode art."}

var mediaFilesThumbnailSetCmd = &cobra.Command{
	Use: "set <file_id>", Short: "Set or replace a file's thumbnail.", Args: cobra.ExactArgs(1),
	Long: "Set episode art using exactly one of --image-file-id (preferred) or --image-media-id. The image must be ready and belong to the team.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(args[0]) == "" {
			return errs.New(errs.ExitUsage, "file id is blank (an unset variable?) — pass the id of the file to give a thumbnail")
		}
		hasFile, hasMedia := cmd.Flags().Changed("image-file-id"), cmd.Flags().Changed("image-media-id")
		fileID, mediaID := flagValue(cmd, "image-file-id"), flagValue(cmd, "image-media-id")
		if hasFile == hasMedia || (hasFile && strings.TrimSpace(fileID) == "") || (hasMedia && strings.TrimSpace(mediaID) == "") {
			return errs.New(errs.ExitUsage, "provide exactly one non-empty --image-file-id or --image-media-id")
		}
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		if hasFile {
			mediaID, err = resolveFileMediaID(c, teamID, fileID, "episode art")
			if err != nil {
				return err
			}
		}
		res, err := c.client.Create(c.ctx, fileThumbnailAttachmentsPath(teamID), map[string]any{"media_id": mediaID, "target_type": "file", "target_id": args[0], "role": "thumbnail"})
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var mediaFilesThumbnailRemoveCmd = &cobra.Command{
	Use: "remove <file_id>", Short: "Remove a file's thumbnail.", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(args[0]) == "" {
			return errs.New(errs.ExitUsage, "file id is blank (an unset variable?) — pass the id of the file whose thumbnail to remove")
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Remove thumbnail from file %s?", args[0])); err != nil {
			return err
		}
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		if _, err := c.client.Retrieve(c.ctx, filesPath(teamID, args[0])); err != nil {
			return err
		}
		query := url.Values{"target_type": {"file"}, "target_id": {args[0]}}
		for {
			col, err := c.client.List(c.ctx, attachmentsPath(teamID, ""), query)
			if err != nil {
				return err
			}
			for _, attachment := range col.Data {
				if attachment.Attributes["role"] == "thumbnail" && attachment.Attributes["target_id"] == args[0] {
					if err := c.client.Delete(c.ctx, attachmentsPath(teamID, attachment.ID)); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Removed thumbnail from file %s.\n", args[0])
					return nil
				}
			}
			page := col.NextPage()
			if !page.More {
				break
			}
			if page.Cursor == "" || page.Cursor == query.Get("page[after]") {
				return errs.New(errs.ExitGeneric, "cannot continue listing attachments: missing or repeated cursor")
			}
			query.Set("page[after]", page.Cursor)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "note: file %s has no thumbnail\n", args[0])
		return nil
	},
}
