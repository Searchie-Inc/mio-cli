package cmd

// media_upload.go — media ingest commands (MIO-2267):
//
//	files upload <path>       create → presigned S3 PUT → finalize (single-part)
//	files finalize <id>       POST /api/teams/{team}/files/{id}/finalize
//	files transcode <id>      POST /api/teams/{team}/files/{id}/transcode  (202)
//	files register-synthetic  POST /api/admin/teams/{team}/files/synthetic (MIO-2285)
//
// Single-part uploads only; multipart (very large files) + the replace lifecycle
// are a follow-on. All routes are team-scoped and accept the CLI's team-owner
// API key.

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// syntheticAssetKinds is the asset_kind enum the synthetic-file route accepts.
var syntheticAssetKinds = map[string]bool{"document": true, "pdf": true}

func init() {
	mediaFilesCmd.AddCommand(
		mediaFilesUploadCmd,
		mediaFilesFinalizeCmd,
		mediaFilesTranscodeCmd,
		mediaFilesRegisterSyntheticCmd,
	)

	mediaFilesUploadCmd.Flags().String("title", "", "File title (default: the file's base name).")
	mediaFilesUploadCmd.Flags().String("mime-type", "", "Content type (default: sniffed from the file).")
	mediaFilesUploadCmd.Flags().String("folder-id", "", "Place the file in this folder after upload.")
	mediaFilesUploadCmd.Flags().Bool("wait", false, "Wait until the file finishes processing (upload/transcode READY).")
	mediaFilesUploadCmd.Flags().Duration("timeout", 5*time.Minute, "Max time to --wait for processing.")

	mediaFilesRegisterSyntheticCmd.Flags().String("title", "", "File title. Required.")
	mediaFilesRegisterSyntheticCmd.Flags().String("asset-kind", "document", "Synthetic asset kind: document or pdf.")
	mediaFilesRegisterSyntheticCmd.Flags().String("visibility", "", "Visibility: private, public, or unlisted.")
	mediaFilesRegisterSyntheticCmd.Flags().String("mime-type", "", "Optional mime type.")
	mediaFilesRegisterSyntheticCmd.Flags().String("original-filename", "", "Optional original filename.")
	mediaFilesRegisterSyntheticCmd.Flags().String("description", "", "Optional description.")
}

// syntheticFilesPath returns /api/admin/teams/{team}/files/synthetic.
func syntheticFilesPath(teamID string) string {
	return fmt.Sprintf("/api/admin/teams/%s/files/synthetic", teamID)
}

// ---- upload -----------------------------------------------------------------

var mediaFilesUploadCmd = &cobra.Command{
	Use:   "upload <path>",
	Short: "Upload a local file into the team media library.",
	Long: `Ingest a local file end-to-end: create the file record, stream the bytes to
the returned presigned URL, and finalize. For video, finalize auto-triggers
transcoding; pass --wait to block until processing reaches READY.

Single-part upload only (multipart for very large files is a follow-on).`,
	Example: `  mio media files upload ./intro.mp4 --title "Intro"
  mio media files upload ./report.pdf --folder-id folder_abc --wait`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate the file BEFORE resolving auth/team so a bad path fires no request.
		path := args[0]
		fi, err := os.Stat(path)
		if err != nil {
			return errs.New(errs.ExitUsage, "cannot read file %q: %s", path, err)
		}
		if fi.IsDir() {
			return errs.New(errs.ExitUsage, "%q is a directory, not a file", path)
		}
		if fi.Size() == 0 {
			return errs.New(errs.ExitUsage, "%q is empty (size 0); nothing to upload", path)
		}
		title := flagValue(cmd, "title")
		if title == "" {
			title = filepath.Base(path)
		}
		mimeType := flagValue(cmd, "mime-type")
		if mimeType == "" {
			mimeType = sniffMime(path)
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		// 1. Create the file record → the presigned upload URL comes back in meta.
		created, err := c.client.Create(c.ctx, filesPath(teamID, ""), map[string]any{
			"title":      title,
			"mime_type":  mimeType,
			"size_bytes": fi.Size(),
		})
		if err != nil {
			return err
		}
		uploadURL, _ := created.Meta["upload_url"].(string)
		if uploadURL == "" {
			return errs.New(errs.ExitGeneric, "create response did not include a presigned upload_url")
		}

		// 2. Stream the bytes straight to S3 (that URL carries no mio auth).
		if _, err := client.PutFileToURL(c.ctx, uploadURL, path, mimeType); err != nil {
			return err
		}

		// 3. Finalize — flips status_upload to READY, auto-transcodes video.
		res, err := c.client.Action(c.ctx, http.MethodPost, filesPath(teamID, created.ID)+"/finalize", nil)
		if err != nil {
			return err
		}
		if res == nil {
			if res, err = c.client.Retrieve(c.ctx, filesPath(teamID, created.ID)); err != nil {
				return err
			}
		}

		// Optional: place in a folder (create does not accept folder_id).
		if folderID := flagValue(cmd, "folder-id"); folderID != "" {
			if res, err = c.client.Update(c.ctx, filesPath(teamID, created.ID), map[string]any{"folder_id": folderID}); err != nil {
				return err
			}
		}

		// Optional: block until processing reaches READY.
		if wait, _ := cmd.Flags().GetBool("wait"); wait {
			timeout, _ := cmd.Flags().GetDuration("timeout")
			if res, err = waitForFileReady(c, teamID, created.ID, timeout); err != nil {
				return err
			}
		}
		return c.render(cmd, res)
	},
}

// ---- finalize ---------------------------------------------------------------

var mediaFilesFinalizeCmd = &cobra.Command{
	Use:     "finalize <file_id>",
	Short:   "Finalize an already-uploaded file.",
	Long:    "Finalize a file whose bytes were already PUT to its presigned URL — verifies the object, marks it READY, and (for video) triggers transcoding.",
	Example: `  mio media files finalize file_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Action(c.ctx, http.MethodPost, filesPath(teamID, args[0])+"/finalize", nil)
		if err != nil {
			return err
		}
		return renderFileOrFetch(cmd, c, teamID, args[0], res)
	},
}

// ---- transcode --------------------------------------------------------------

var mediaFilesTranscodeCmd = &cobra.Command{
	Use:     "transcode <file_id>",
	Short:   "(Re)trigger transcoding for a video file.",
	Long:    "Manually (re)trigger transcoding for a video file. Returns 202; poll the file's status_transcode for progress. 409 if a transcode is already in flight.",
	Example: `  mio media files transcode file_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Action(c.ctx, http.MethodPost, filesPath(teamID, args[0])+"/transcode", nil)
		if err != nil {
			return err
		}
		return renderFileOrFetch(cmd, c, teamID, args[0], res)
	},
}

// ---- register-synthetic -----------------------------------------------------

var mediaFilesRegisterSyntheticCmd = &cobra.Command{
	Use:   "register-synthetic",
	Short: "Register a synthetic READY file (no upload).",
	Long: `Register a synthetic document file that is immediately READY with a
server-generated storage path — no upload/finalize/transcode. Mirrors the
seeder's stub-document path; requires a team-owner key.`,
	Example: `  mio media files register-synthetic --title "Terms.pdf" --asset-kind pdf`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate before resolving auth/team so a bad flag fires no request.
		title := flagValue(cmd, "title")
		if title == "" {
			return errs.New(errs.ExitUsage, "--title is required")
		}
		if k := flagValue(cmd, "asset-kind"); k != "" && !syntheticAssetKinds[k] {
			return errs.New(errs.ExitUsage, "invalid --asset-kind %q: must be document or pdf", k)
		}
		attrs := map[string]any{"title": title}
		setStringFlag(cmd, attrs, "asset-kind")
		setStringFlag(cmd, attrs, "visibility")
		setStringFlag(cmd, attrs, "mime-type")
		setStringFlag(cmd, attrs, "original-filename")
		setStringFlag(cmd, attrs, "description")

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, syntheticFilesPath(teamID), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- helpers ----------------------------------------------------------------

// renderFileOrFetch renders res, or retrieves the file first if the action
// returned no body (some action routes reply 202 with an empty body).
func renderFileOrFetch(cmd *cobra.Command, c *cmdContext, teamID, fileID string, res *client.Resource) error {
	if res == nil {
		var err error
		if res, err = c.client.Retrieve(c.ctx, filesPath(teamID, fileID)); err != nil {
			return err
		}
	}
	return c.render(cmd, res)
}

// sniffMime resolves a file's content type from its extension, falling back to
// content sniffing, then application/octet-stream. Any "; charset=…" suffix is
// stripped so the value fits the backend's mime_type field.
func sniffMime(path string) string {
	if ext := filepath.Ext(path); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			return trimMediaType(ct)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return trimMediaType(http.DetectContentType(buf[:n]))
}

func trimMediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

// waitForFileReady polls the file until upload/transcode/transcribe all reach a
// terminal READY (or errors on FAILED / a --timeout).
func waitForFileReady(c *cmdContext, teamID, fileID string, timeout time.Duration) (*client.Resource, error) {
	deadline := time.Now().Add(timeout)
	for {
		res, err := c.client.Retrieve(c.ctx, filesPath(teamID, fileID))
		if err != nil {
			return nil, err
		}
		up := resAttrString(res, "status_upload")
		tc := resAttrString(res, "status_transcode")
		tr := resAttrString(res, "status_transcribe")

		if up == "FAILED" || tc == "FAILED" || tr == "FAILED" {
			return res, errs.New(errs.ExitGeneric, "processing failed (upload=%s transcode=%s transcribe=%s)",
				statusOr(up), statusOr(tc), statusOr(tr))
		}
		if up == "READY" && (tc == "" || tc == "READY") && (tr == "" || tr == "READY") {
			return res, nil
		}
		if time.Now().After(deadline) {
			return res, errs.New(errs.ExitGeneric, "timed out after %s (upload=%s transcode=%s transcribe=%s)",
				timeout, statusOr(up), statusOr(tc), statusOr(tr))
		}
		select {
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func resAttrString(res *client.Resource, key string) string {
	if res == nil {
		return ""
	}
	s, _ := res.Attributes[key].(string)
	return s
}

func statusOr(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
