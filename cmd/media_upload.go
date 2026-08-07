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
	"io"
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

const (
	// autoMultipartThreshold is the size above which `files upload` switches to
	// multipart automatically (single-part presigned PUT handles up to 5 GB, so
	// this is well within S3 limits — it's about resumable chunking, not a cap).
	autoMultipartThreshold = 100 * 1024 * 1024 // 100 MB
	// minPartSizeMB is S3's minimum multipart part size (all parts but the last).
	minPartSizeMB = 5
)

func init() {
	mediaFilesCmd.AddCommand(
		mediaFilesUploadCmd,
		mediaFilesReplaceCmd,
		mediaFilesFinalizeCmd,
		mediaFilesTranscodeCmd,
		mediaFilesRegisterSyntheticCmd,
	)

	mediaFilesUploadCmd.Flags().String("title", "", "File title (default: the file's base name).")
	mediaFilesUploadCmd.Flags().String("mime-type", "", "Content type (default: sniffed from the file).")
	mediaFilesUploadCmd.Flags().String("folder-id", "", "Place the file in this folder after upload.")
	mediaFilesUploadCmd.Flags().Bool("wait", false,
		"Wait until the file finishes processing (upload/transcode READY). For video, waits ~30s "+
			"(or --timeout, whichever is smaller) for transcoding to START — bounds are checked at poll "+
			"boundaries, so allow one extra poll interval; if it never starts — video processing "+
			"disabled, or a backed-up queue — this warns and returns 0 rather than waiting out --timeout.")
	mediaFilesUploadCmd.Flags().Duration("timeout", 5*time.Minute, "Max time to --wait for processing.")
	mediaFilesUploadCmd.Flags().Bool("multipart", false, "Force a multipart (chunked) upload regardless of size.")
	mediaFilesUploadCmd.Flags().Int("part-size-mb", 16, "Multipart chunk size in MB (min 5).")

	mediaFilesReplaceCmd.Flags().String("mime-type", "", "Content type of the replacement (default: sniffed).")
	mediaFilesReplaceCmd.Flags().String("filename", "", "Original filename to record (default: the file's base name).")
	mediaFilesReplaceCmd.Flags().Bool("multipart", false, "Force a multipart (chunked) replace regardless of size.")
	mediaFilesReplaceCmd.Flags().Int("part-size-mb", 16, "Multipart chunk size in MB (min 5).")

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
the returned presigned URL, and finalize. For video, finalize triggers transcoding
asynchronously; pass --wait to block until processing reaches READY.

--wait keeps polling a video whose transcode has not started yet, but waits at
most 30s (or --timeout, whichever is smaller) for it to start, then warns and
returns 0 — a backend with video processing disabled never sets a transcode
status at all, so waiting longer would only burn --timeout and then fail. That
warning is a bound, not a verdict: a busy transcode queue produces it too, so
re-check with 'mio media files retrieve <id>' rather than reading exit 0 as
"transcoded".

Single-part upload only (multipart for very large files is a follow-on).

New files default to visibility: private — make one public later with
'mio media files update <id> --visibility public'. For a member or visitor to
actually open the content, every layer in its path must also be non-private: the
file, the enclosing playlist (if any), and the hub publication
('mio media hub-playlists publish --visibility', which itself defaults to
members). See the media-workflow guide's visibility section.`,
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

		forceMultipart, _ := cmd.Flags().GetBool("multipart")
		if pmb, _ := cmd.Flags().GetInt("part-size-mb"); (forceMultipart || fi.Size() > autoMultipartThreshold) && pmb < minPartSizeMB {
			return errs.New(errs.ExitUsage, "--part-size-mb must be >= %d (S3 minimum part size)", minPartSizeMB)
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		var fileID string
		var res *client.Resource
		if forceMultipart || fi.Size() > autoMultipartThreshold {
			partMB, _ := cmd.Flags().GetInt("part-size-mb")
			fileID, res, err = uploadMultipart(c, teamID, path, title, mimeType, fi.Size(), int64(partMB)*1024*1024)
		} else {
			fileID, res, err = uploadSinglePart(c, teamID, path, title, mimeType, fi.Size())
		}
		if err != nil {
			return err
		}

		// Optional: place in a folder (create does not accept folder_id).
		if folderID := flagValue(cmd, "folder-id"); folderID != "" {
			if res, err = c.client.UpdateWithID(c.ctx, filesPath(teamID, fileID), fileID, map[string]any{"folder_id": folderID}); err != nil {
				return err
			}
		}

		// Optional: block until processing reaches READY.
		if wait, _ := cmd.Flags().GetBool("wait"); wait {
			timeout, _ := cmd.Flags().GetDuration("timeout")
			if res, err = waitForFileReady(c, cmd.ErrOrStderr(), teamID, fileID, timeout); err != nil {
				return err
			}
		}
		return c.render(cmd, res)
	},
}

// ---- replace ----------------------------------------------------------------

var mediaFilesReplaceCmd = &cobra.Command{
	Use:   "replace <file_id> <path>",
	Short: "Replace an existing file's content.",
	Long: `Replace the bytes of an existing file with a new local file, keeping the same
file id — the media is relinked atomically on finalize. Single-part upload.`,
	Example: `  mio media files replace file_abc123 ./updated.png`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate the file BEFORE resolving auth/team so a bad path fires no request.
		fileID, path := args[0], args[1]
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
		filename := flagValue(cmd, "filename")
		if filename == "" {
			filename = filepath.Base(path)
		}
		mimeType := flagValue(cmd, "mime-type")
		if mimeType == "" {
			mimeType = sniffMime(path)
		}

		forceMultipart, _ := cmd.Flags().GetBool("multipart")
		if pmb, _ := cmd.Flags().GetInt("part-size-mb"); (forceMultipart || fi.Size() > autoMultipartThreshold) && pmb < minPartSizeMB {
			return errs.New(errs.ExitUsage, "--part-size-mb must be >= %d (S3 minimum part size)", minPartSizeMB)
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		var res *client.Resource
		if forceMultipart || fi.Size() > autoMultipartThreshold {
			partMB, _ := cmd.Flags().GetInt("part-size-mb")
			res, err = replaceMultipart(c, teamID, fileID, path, filename, mimeType, fi.Size(), int64(partMB)*1024*1024)
		} else {
			res, err = replaceSinglePart(c, teamID, fileID, path, filename, mimeType, fi.Size())
		}
		if err != nil {
			return err
		}
		return renderFileOrFetch(cmd, c, teamID, fileID, res)
	},
}

// replaceInitPath returns /api/teams/{team}/files/{id}/replace.
func replaceInitPath(teamID, fileID string) string { return filesPath(teamID, fileID) + "/replace" }

// replaceFinalizePath returns /api/teams/{team}/files/{id}/replace/{replacement_media_id}/finalize.
func replaceFinalizePath(teamID, fileID, replacementID string) string {
	return fmt.Sprintf("%s/replace/%s/finalize", filesPath(teamID, fileID), replacementID)
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

// ---- upload helpers ---------------------------------------------------------

// uploadSinglePart runs the single presigned-PUT flow: create → PUT → finalize.
// Returns the file id and the finalized resource.
func uploadSinglePart(c *cmdContext, teamID, path, title, mimeType string, size int64) (string, *client.Resource, error) {
	created, err := c.client.Create(c.ctx, filesPath(teamID, ""), map[string]any{
		"title":      title,
		"mime_type":  mimeType,
		"size_bytes": size,
	})
	if err != nil {
		return "", nil, err
	}
	uploadURL, _ := created.Meta["upload_url"].(string)
	if uploadURL == "" {
		return created.ID, nil, errs.New(errs.ExitGeneric, "create response did not include a presigned upload_url")
	}
	if _, err := client.PutFileToURL(c.ctx, uploadURL, path, mimeType); err != nil {
		return created.ID, nil, err
	}
	res, err := c.client.Action(c.ctx, http.MethodPost, filesPath(teamID, created.ID)+"/finalize", nil)
	if err != nil {
		return created.ID, nil, err
	}
	if res == nil {
		res, err = c.client.Retrieve(c.ctx, filesPath(teamID, created.ID))
	}
	return created.ID, res, err
}

// uploadMultipart runs the chunked flow: init → per-part presign+PUT+ETag →
// complete → finalize. Any failure aborts the multipart upload so no orphaned
// upload is left behind.
func uploadMultipart(c *cmdContext, teamID, path, title, mimeType string, size, partSize int64) (string, *client.Resource, error) {
	created, err := c.client.Create(c.ctx, multipartInitPath(teamID), map[string]any{
		"title":      title,
		"mime_type":  mimeType,
		"size_bytes": size,
	})
	if err != nil {
		return "", nil, err
	}
	fileID := created.ID
	uploadID, _ := created.Meta["upload_id"].(string)
	if uploadID == "" {
		return fileID, nil, errs.New(errs.ExitGeneric, "multipart init did not return an upload_id")
	}

	f, err := os.Open(path)
	if err != nil {
		return fileID, nil, errs.New(errs.ExitGeneric, "open %s: %s", path, err)
	}
	defer f.Close()

	abort := func() { _ = c.client.Delete(c.ctx, multipartUploadPath(teamID, fileID, uploadID)) }

	parts, err := streamParts(c, f, partSize, mimeType, func(partNumber int) string {
		return multipartPartPath(teamID, fileID, uploadID, partNumber)
	})
	if err != nil {
		abort()
		return fileID, nil, err
	}

	// Complete assembles the object (flat {parts:[…]} body, not a JSON:API envelope).
	if _, err := c.client.ActionWith(c.ctx, client.StyleFlat, http.MethodPost,
		multipartCompletePath(teamID, fileID, uploadID), map[string]any{"parts": parts}); err != nil {
		abort()
		return fileID, nil, err
	}
	// Finalize is shared with single-part: HEAD-check + flip READY + emit event.
	res, err := c.client.Action(c.ctx, http.MethodPost, filesPath(teamID, fileID)+"/finalize", nil)
	if err != nil {
		return fileID, nil, err
	}
	if res == nil {
		res, err = c.client.Retrieve(c.ctx, filesPath(teamID, fileID))
	}
	return fileID, res, err
}

// multipartInitPath returns /api/teams/{team}/files/multipart.
func multipartInitPath(teamID string) string { return filesPath(teamID, "") + "/multipart" }

// multipartUploadPath returns /api/teams/{team}/files/{id}/multipart/{upload_id}
// (the abort target and the base for parts/complete).
func multipartUploadPath(teamID, fileID, uploadID string) string {
	return fmt.Sprintf("%s/multipart/%s", filesPath(teamID, fileID), uploadID)
}

func multipartPartPath(teamID, fileID, uploadID string, partNumber int) string {
	return fmt.Sprintf("%s/parts/%d", multipartUploadPath(teamID, fileID, uploadID), partNumber)
}

func multipartCompletePath(teamID, fileID, uploadID string) string {
	return multipartUploadPath(teamID, fileID, uploadID) + "/complete"
}

// streamParts uploads the open file in partSize chunks: it POSTs partPathFor(n)
// to presign each part, PUTs the chunk, and collects {part_number, etag}. Shared
// by upload-multipart and replace-multipart (the only difference is the part
// path). Callers own init/complete/finalize and any abort.
func streamParts(c *cmdContext, f *os.File, partSize int64, mimeType string, partPathFor func(partNumber int) string) ([]map[string]any, error) {
	buf := make([]byte, partSize)
	parts := make([]map[string]any, 0)
	for partNumber := 1; ; partNumber++ {
		n, readErr := io.ReadFull(f, buf)
		if n > 0 {
			partRes, err := c.client.Action(c.ctx, http.MethodPost, partPathFor(partNumber), nil)
			if err != nil {
				return nil, err
			}
			partURL := ""
			if partRes != nil {
				partURL, _ = partRes.Meta["part_url"].(string)
			}
			if partURL == "" {
				return nil, errs.New(errs.ExitGeneric, "part %d did not return a part_url", partNumber)
			}
			etag, err := client.PutBytesToURL(c.ctx, partURL, buf[:n], mimeType)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]any{"part_number": partNumber, "etag": etag})
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return nil, errs.New(errs.ExitGeneric, "read: %s", readErr)
		}
	}
	if len(parts) == 0 {
		return nil, errs.New(errs.ExitUsage, "nothing to upload (empty file)")
	}
	return parts, nil
}

// replaceSinglePart runs the single presigned-PUT replace flow: init → PUT →
// replace/finalize (atomic relink). Returns the relinked file resource.
func replaceSinglePart(c *cmdContext, teamID, fileID, path, filename, mimeType string, size int64) (*client.Resource, error) {
	repl, err := c.client.Create(c.ctx, replaceInitPath(teamID, fileID), map[string]any{
		"original_filename": filename,
		"mime_type":         mimeType,
		"size_bytes":        size,
	})
	if err != nil {
		return nil, err
	}
	uploadURL, _ := repl.Meta["upload_url"].(string)
	if uploadURL == "" {
		return nil, errs.New(errs.ExitGeneric, "replace init did not return a presigned upload_url")
	}
	if _, err := client.PutFileToURL(c.ctx, uploadURL, path, mimeType); err != nil {
		return nil, err
	}
	return c.client.Action(c.ctx, http.MethodPost, replaceFinalizePath(teamID, fileID, repl.ID), nil)
}

// replaceMultipart runs the chunked replace flow: init → per-part → complete →
// replace/finalize. There is no replace-multipart abort route, so a failure just
// surfaces (the backend reaps the pending replacement).
func replaceMultipart(c *cmdContext, teamID, fileID, path, filename, mimeType string, size, partSize int64) (*client.Resource, error) {
	repl, err := c.client.Create(c.ctx, replaceMultipartInitPath(teamID, fileID), map[string]any{
		"original_filename": filename,
		"mime_type":         mimeType,
		"size_bytes":        size,
	})
	if err != nil {
		return nil, err
	}
	replID := repl.ID
	uploadID, _ := repl.Meta["upload_id"].(string)
	if uploadID == "" {
		return nil, errs.New(errs.ExitGeneric, "replace multipart init did not return an upload_id")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, errs.New(errs.ExitGeneric, "open %s: %s", path, err)
	}
	defer f.Close()

	parts, err := streamParts(c, f, partSize, mimeType, func(partNumber int) string {
		return replaceMultipartPartPath(teamID, fileID, replID, uploadID, partNumber)
	})
	if err != nil {
		return nil, err
	}
	// Unlike upload-multipart, the replace-multipart complete is TERMINAL: it
	// relinks the file and returns the updated file resource (no separate
	// finalize — calling one 404s the already-consumed replacement).
	return c.client.ActionWith(c.ctx, client.StyleFlat, http.MethodPost,
		replaceMultipartCompletePath(teamID, fileID, replID, uploadID), map[string]any{"parts": parts})
}

// replaceMultipartInitPath returns /api/teams/{team}/files/{id}/replace/multipart.
func replaceMultipartInitPath(teamID, fileID string) string {
	return replaceInitPath(teamID, fileID) + "/multipart"
}

// replaceMultipartBase returns .../files/{id}/replace/{replacement_media_id}/multipart/{upload_id}.
func replaceMultipartBase(teamID, fileID, replID, uploadID string) string {
	return fmt.Sprintf("%s/replace/%s/multipart/%s", filesPath(teamID, fileID), replID, uploadID)
}

func replaceMultipartPartPath(teamID, fileID, replID, uploadID string, partNumber int) string {
	return fmt.Sprintf("%s/parts/%d", replaceMultipartBase(teamID, fileID, replID, uploadID), partNumber)
}

func replaceMultipartCompletePath(teamID, fileID, replID, uploadID string) string {
	return replaceMultipartBase(teamID, fileID, replID, uploadID) + "/complete"
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

// mediaPollInterval is the gap between status polls. A var, not a const, so
// tests can drive the real loop without wall-clock sleeps.
var mediaPollInterval = 2 * time.Second

// transcodeStartGrace bounds how long --wait will sit on a VIDEO whose
// status_transcode is still null, waiting for the transcode to be enqueued.
//
// It exists because null cannot be read as one thing (MIO-3001). The backend's
// finalize sets status_upload=READY and EMITS MediaUploaded; a best-effort
// handler enqueues the transcode job, and only that job sets status_transcode.
// So a freshly finalized video reads null for a moment — but on a backend with
// FEATURE_MEDIA_VIDEO_ENABLED off (its default, and production's setting until
// P10) that handler returns early and the status stays null FOREVER. Waiting for
// it unconditionally would burn the whole --timeout and then fail, turning every
// production video upload from a fast success into a slow failure.
//
// The grace covers in-process dispatch plus the queue pickup, normally
// sub-second, so this is ~2 orders of magnitude of headroom while keeping the
// cost on a video-disabled backend small and bounded.
var transcodeStartGrace = 30 * time.Second

// transcribeSettled reports whether status_transcribe has reached a state that
// will not change again.
//
// The same two-meanings trap as status_transcode, one field over: "" is "no
// transcription applies", but READY is NOT the only terminal value. mio-backend
// sets NOT_APPLICABLE ("READY" if words else "NOT_APPLICABLE",
// app/media/admin_router.py) — the NORMAL outcome for a video with no speech —
// and REJECTED (app/media/transcription_service.py). Waiting for those to become
// READY burns the entire --timeout and then exits 1 on a fully successful
// upload: the exact "fast success becomes a slow failure" shape this command was
// fixed to stop producing.
//
// FAILED is deliberately absent: it is terminal too, but the caller reports it as
// an error before reaching here. PENDING is the only in-flight value the backend
// actually writes for this field — PROCESSING appears only in design docs — so it
// is the case that matters when narrowing this set.
//
// (MIO-2571's "null means not applicable" reading is intended and unchanged; this
// is about non-null terminal values, which is a different question.)
func transcribeSettled(tr string) bool {
	switch tr {
	case "", "READY", "NOT_APPLICABLE", "REJECTED":
		return true
	}
	return false // PENDING / PROCESSING — still moving
}

// awaitsTranscode reports whether a null status_transcode on THIS file means
// "not enqueued yet" rather than "not applicable".
//
// It keys off mime_type because that is what the API exposes: asset_kind — the
// field the backend itself branches on — is internal and never serialized. The
// two agree exactly, since _asset_kind_from_mime maps mime.startswith("video/")
// onto "video" (app/media/service.py).
func awaitsTranscode(res *client.Resource) (waits, kindKnown bool) {
	mime := strings.ToLower(resAttrString(res, "mime_type"))
	return strings.HasPrefix(mime, "video/"), mime != ""
}

// waitForFileReady polls the file until upload/transcode/transcribe all reach a
// terminal READY (or errors on FAILED / a --timeout).
//
// A null status_transcode is treated as "nothing to wait for" for every asset
// kind but video, and for video only once transcodeStartGrace has elapsed
// without a transcode appearing — in which case it warns and returns rather than
// failing, because the upload itself genuinely succeeded.
func waitForFileReady(c *cmdContext, w io.Writer, teamID, fileID string, timeout time.Duration) (*client.Resource, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	// The transcode window opens when the UPLOAD is done, not when the command
	// started: a slow upload would otherwise consume the transcode's grace before
	// the transcode could possibly have been enqueued.
	var windowStart, windowEnd time.Time
	// Separate flags: one shared `warned` let an early unknown-mime note suppress
	// the give-up note for a later poll's video, returning silently with an
	// untranscoded file — the original bug wearing a different hat.
	warnedUnknownKind, warnedGaveUp := false, false

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

		// Open the transcode window on the first READY observation, bounded by the
		// caller's own deadline so a --timeout shorter than the grace still wins.
		if up == "READY" && windowEnd.IsZero() {
			// time.Until is NEGATIVE once the deadline has passed (a --timeout
			// shorter than the upload itself). That needs no clamp: a windowEnd in
			// the past simply means "give up on the transcode now", which is
			// correct, and the give-up message reports time ELAPSED rather than
			// this planned figure, so a negative value can never reach an operator.
			windowStart = time.Now()
			windowEnd = windowStart.Add(min(transcodeStartGrace, time.Until(deadline)))
		}

		transcodeDone := tc == "READY"
		if tc == "" {
			waits, kindKnown := awaitsTranscode(res)
			switch {
			case !kindKnown:
				// No mime_type to classify by. DEFENSIVE: the media column is NOT
				// NULL, so a real backend cannot serve this — but the published
				// OpenAPI declares mime_type nullable and not required, so the
				// contract we are handed permits it.
				//
				// Do not wait — but say so, because
				// silence here is indistinguishable from "this kind never
				// transcodes" and the file may well be an untranscoded video.
				if !warnedUnknownKind {
					fmt.Fprintf(w, "warning: this file reports no mime_type, so whether it transcodes "+
						"cannot be determined; returning without waiting. Check with "+
						"`mio media files retrieve %s`.\n", fileID)
					warnedUnknownKind = true
				}
				transcodeDone = true
			case !waits:
				transcodeDone = true // this asset kind never transcodes
			case !windowEnd.IsZero() && time.Now().After(windowEnd):
				// Give up on the transcode, not on the upload. Exit 0 with a loud
				// note: failing here would break every video upload on a backend
				// with video processing disabled, where the file is nonetheless
				// stored and usable.
				//
				// The reported duration is what was ACTUALLY waited — measured,
				// not planned. Printing the constant would have a caller who
				// passed --timeout 3s diagnose a disabled backend from a wait
				// that never gave the queue a chance; printing the planned window
				// would still be wrong by up to one poll interval, and negative
				// when --timeout was already spent on the upload.
				if !warnedGaveUp {
					fmt.Fprintf(w, "warning: transcoding had not started for this video after %s — "+
						"returning with status_transcode unset. The file uploaded fine, but it is NOT transcoded: "+
						"video processing may be disabled on this backend, or its transcode queue may be backed up. "+
						"Check with `mio media files retrieve %s`.\n", time.Since(windowStart).Round(time.Millisecond), fileID)
					warnedGaveUp = true
				}
				transcodeDone = true
			}
		}

		if up == "READY" && transcodeDone && transcribeSettled(tr) {
			return res, nil
		}
		if time.Now().After(deadline) {
			return res, errs.New(errs.ExitGeneric, "timed out after %s (upload=%s transcode=%s transcribe=%s)",
				timeout, statusOr(up), statusOr(tc), statusOr(tr))
		}
		select {
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		case <-time.After(mediaPollInterval):
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
