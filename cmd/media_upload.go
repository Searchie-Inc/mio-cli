package cmd

// media_upload.go — media ingest commands (MIO-2267):
//
//	files upload <path>       create → presigned S3 PUT → finalize, or multipart
//	                          (init → parts → complete → finalize)
//	files replace <id> <path> replace init → PUT → replace finalize, or multipart
//	                          (init → parts → terminal complete)
//	files finalize <id>       POST /api/teams/{team}/files/{id}/finalize
//	files transcode <id>      POST /api/teams/{team}/files/{id}/transcode  (202)
//	files register-synthetic  POST /api/admin/teams/{team}/files/synthetic (MIO-2285)
//
// upload and replace pick their path with useMultipart: multipart when
// --multipart is set or the file is larger than autoMultipartThreshold, one
// presigned PUT otherwise. Every help surface that states the threshold renders
// it from the constant (MIO-4155). All routes are team-scoped and accept the
// CLI's team-owner API key.

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
	// autoMultipartThreshold is the size above which `files upload` and
	// `files replace` switch to multipart automatically (single-part presigned PUT
	// handles up to 5 GB, so this is well within S3 limits — it's about chunking,
	// not a cap). The help is rendered from it, so changing it here changes what
	// --help says. The hand-maintained doc lines are held to it by two tests:
	// TestMultipartThreshold_EverySurfaceStatesIt ("above N MB", and the
	// --part-size-mb default and minimum) and
	// TestMultipartThreshold_StatedBoundaryIsTheWire (the "N bytes" figure, and
	// where a file of exactly N bytes goes, checked against the wire).
	autoMultipartThreshold = 100 * 1024 * 1024 // 100 MB
	// minPartSizeMB is S3's minimum multipart part size (all parts but the last).
	minPartSizeMB = 5
	// defaultPartSizeMB is the --part-size-mb default for upload and replace.
	defaultPartSizeMB = 16
)

// useMultipart is THE dispatch rule for upload and replace: multipart when forced
// with --multipart or when the file is larger than autoMultipartThreshold, one
// presigned PUT otherwise. A file of exactly the threshold goes single-part.
func useMultipart(force bool, size int64) bool {
	return force || size > autoMultipartThreshold
}

// multipartThresholdMB renders autoMultipartThreshold in the unit --part-size-mb
// uses (1 MB = 1024*1024 bytes), e.g. "100 MB".
func multipartThresholdMB() string {
	return fmt.Sprintf("%d MB", autoMultipartThreshold/(1024*1024))
}

// uploadMultipartHelp and replaceMultipartHelp are the multipart paragraphs of
// upload's and replace's --help, rendered from the constants so the help cannot
// state a threshold, default or minimum the command does not use (MIO-4155:
// upload's help said "Single-part upload only" while dispatching multipart).
func uploadMultipartHelp() string {
	return fmt.Sprintf(`Multipart is automatic above %[1]s: a file larger than %[2]d bytes is sent in
parts (S3 multipart: init, presign and PUT each part, complete, then finalize);
a file of that size or smaller goes up as one presigned PUT. --multipart forces
the multipart path at any size. --part-size-mb sets the part size in MB
(default %[3]d, minimum %[4]d, S3's floor for every part but the last) and is
read only on the multipart path.`,
		multipartThresholdMB(), autoMultipartThreshold, defaultPartSizeMB, minPartSizeMB)
}

func replaceMultipartHelp() string {
	return fmt.Sprintf(`Multipart is automatic above %[1]s, as for 'files upload': a replacement larger
than %[2]d bytes is sent in parts (S3 multipart: init, presign and PUT each
part, then complete, which relinks the file itself; there is no separate
finalize); one of that size or smaller goes up as one presigned PUT and is then
finalized. --multipart forces the multipart path at any size. --part-size-mb
sets the part size in MB (default %[3]d, minimum %[4]d) and is read only on the
multipart path.`,
		multipartThresholdMB(), autoMultipartThreshold, defaultPartSizeMB, minPartSizeMB)
}

// addWaitFlags registers --wait and --timeout. upload and replace both call it
// and both run waitForFileReady, so the two commands cannot state different
// wait semantics or defaults (MIO-4172).
func addWaitFlags(c *cobra.Command) {
	c.Flags().Bool("wait", false,
		"Wait until the file finishes processing (upload/transcode READY). For video, waits ~30s "+
			"(or --timeout, whichever is smaller) for transcoding to START — bounds are checked at poll "+
			"boundaries, so allow one extra poll interval; if it never starts — video processing "+
			"disabled, or a backed-up queue — this warns and returns 0 rather than waiting out --timeout.")
	c.Flags().Duration("timeout", 5*time.Minute, "Max time to --wait for processing.")
}

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
	addWaitFlags(mediaFilesUploadCmd)
	mediaFilesUploadCmd.Flags().Bool("multipart", false,
		"Upload in parts at any size (without it, only files above "+multipartThresholdMB()+" go multipart).")
	mediaFilesUploadCmd.Flags().Int("part-size-mb", defaultPartSizeMB,
		fmt.Sprintf("Multipart part size in MB (min %d); read only on the multipart path.", minPartSizeMB))

	mediaFilesReplaceCmd.Flags().String("mime-type", "", "Content type of the replacement (default: sniffed).")
	mediaFilesReplaceCmd.Flags().String("filename", "", "Original filename to record (default: the file's base name).")
	addWaitFlags(mediaFilesReplaceCmd)
	mediaFilesReplaceCmd.Flags().Bool("multipart", false,
		"Replace in parts at any size (without it, only files above "+multipartThresholdMB()+" go multipart).")
	mediaFilesReplaceCmd.Flags().Int("part-size-mb", defaultPartSizeMB,
		fmt.Sprintf("Multipart part size in MB (min %d); read only on the multipart path.", minPartSizeMB))

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

// SyntheticFileInput is the register-synthetic write body as data (MIO-3065).
// Title is required; every pointer field is sent only when non-nil, so an
// omitted one keeps the endpoint's own default (asset_kind "document",
// visibility "private", mime_type application/octet-stream, original_filename =
// title). Same unset-means-omit convention as SpaceInput/PlaylistInput.
type SyntheticFileInput struct {
	Title            string
	AssetKind        *string
	Visibility       *string
	MimeType         *string
	OriginalFilename *string
	Description      *string
}

// buildSyntheticFileAttrs assembles the synthetic-file register body from s and
// applies the two checks the CLI can make without a request: a title (the
// endpoint's own min_length 1) and, when asset_kind is given, one the endpoint
// accepts. A pure builder — it takes data, not flags — so the SCAFFOLD's
// playlist documents get exactly the checks `files register-synthetic` does,
// the arrangement buildSpaceAttrs / buildPageAttrs / buildPlaylistCreateAttrs
// already have.
func buildSyntheticFileAttrs(s SyntheticFileInput) (map[string]any, error) {
	if s.Title == "" {
		return nil, errs.New(errs.ExitUsage, "--title is required")
	}
	if s.AssetKind != nil && *s.AssetKind != "" && !syntheticAssetKinds[*s.AssetKind] {
		return nil, errs.New(errs.ExitUsage, "invalid --asset-kind %q: must be document or pdf", *s.AssetKind)
	}
	attrs := map[string]any{"title": s.Title}
	for _, f := range []struct {
		key string
		val *string
	}{
		{"asset_kind", s.AssetKind},
		{"visibility", s.Visibility},
		{"mime_type", s.MimeType},
		{"original_filename", s.OriginalFilename},
		{"description", s.Description},
	} {
		if f.val != nil {
			attrs[f.key] = *f.val
		}
	}
	return attrs, nil
}

// ---- upload -----------------------------------------------------------------

var mediaFilesUploadCmd = &cobra.Command{
	Use:   "upload <path>",
	Short: "Upload a local file into the team media library.",
	Long: `Ingest a local file end-to-end: create the file record, stream the bytes to
the returned presigned URL, and finalize. For video, finalize enqueues a
transcode only when the backend has video processing enabled (it is off by
default); pass --wait to block until processing reaches READY.

--wait keeps polling a video whose transcode has not started yet, but waits at
most 30s (or --timeout, whichever is smaller) for it to start, then warns and
returns 0 — a backend with video processing disabled never sets a transcode
status at all, so waiting longer would only burn --timeout and then fail. That
warning is a bound, not a verdict: a busy transcode queue produces it too, so
re-check with 'mio media files retrieve <id>' rather than reading exit 0 as
"transcoded".

` + uploadMultipartHelp() + `

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
		if pmb, _ := cmd.Flags().GetInt("part-size-mb"); useMultipart(forceMultipart, fi.Size()) && pmb < minPartSizeMB {
			return errs.New(errs.ExitUsage, "--part-size-mb must be >= %d (S3 minimum part size)", minPartSizeMB)
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		var fileID string
		var res *client.Resource
		if useMultipart(forceMultipart, fi.Size()) {
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
			if res, err = waitForFileReady(c, cmd.ErrOrStderr(), teamID, fileID, "", timeout); err != nil {
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
file id — the media is relinked atomically once the new bytes are in.

The file gets a NEW media_id, and what described the old bytes is reset:
status_transcode, status_transcribe and duration_seconds read null, and the old
transcript is detached, until the new bytes are processed; the file's timed
cards are cleared. For video, the relink enqueues a new transcode only when the
backend has video processing enabled (it is off by default); pass --wait to
block until processing reaches READY.

--wait works as it does for 'files upload', including waiting at most 30s (or
--timeout, whichever is smaller) for a video's transcode to START (see 'mio
media files upload --help'), with one more condition: it first waits until the
file points at the replacement's media_id. The backend commits the relink just
after it answers, so a read made straight away can still show the OLD media,
often already READY; the old media's statuses, FAILED included, are not counted.

` + replaceMultipartHelp(),
	Example: `  mio media files replace file_abc123 ./updated.png
  mio media files replace file_abc123 ./recut.mp4 --wait --timeout 15m`,
	Args: cobra.ExactArgs(2),
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
		if pmb, _ := cmd.Flags().GetInt("part-size-mb"); useMultipart(forceMultipart, fi.Size()) && pmb < minPartSizeMB {
			return errs.New(errs.ExitUsage, "--part-size-mb must be >= %d (S3 minimum part size)", minPartSizeMB)
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		var res *client.Resource
		var replacementID string
		if useMultipart(forceMultipart, fi.Size()) {
			partMB, _ := cmd.Flags().GetInt("part-size-mb")
			replacementID, res, err = replaceMultipart(c, teamID, fileID, path, filename, mimeType, fi.Size(), int64(partMB)*1024*1024)
		} else {
			replacementID, res, err = replaceSinglePart(c, teamID, fileID, path, filename, mimeType, fi.Size())
		}
		if err != nil {
			return err
		}

		// Optional: block until the REPLACEMENT is processed — not whatever media
		// the file pointed at when the first poll landed.
		if wait, _ := cmd.Flags().GetBool("wait"); wait {
			timeout, _ := cmd.Flags().GetDuration("timeout")
			if res, err = waitForFileReady(c, cmd.ErrOrStderr(), teamID, fileID, replacementID, timeout); err != nil {
				return err
			}
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
	Long:    "Finalize a file whose bytes were already PUT to its presigned URL — verifies the object, marks it READY, and, for video, enqueues a transcode only when the backend has video processing enabled (it is off by default).",
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
seeder's stub-document path; requires a team-owner key.

--mime-type defaults to the endpoint's own application/octet-stream when
omitted. For a placeholder text lesson (no real bytes — the body IS its
description), pass --mime-type text/markdown: that is the convention 'hub
scaffold' uses for playlists[].documents[] template placeholders (MIO-3116),
and mime_type is the field mime-keyed branches downstream (document viewers,
transcode-wait checks) actually read.`,
	Example: `  mio media files register-synthetic --title "Terms.pdf" --asset-kind pdf
  mio media files register-synthetic --title "Add your first lesson" --mime-type text/markdown --description "A placeholder lesson."`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate before resolving auth/team so a bad flag fires no request. The
		// checks and the omit-if-unchanged mapping both live in the shared builder.
		attrs, berr := buildSyntheticFileAttrs(SyntheticFileInput{
			Title:            flagValue(cmd, "title"),
			AssetKind:        changedString(cmd, "asset-kind"),
			Visibility:       changedString(cmd, "visibility"),
			MimeType:         changedString(cmd, "mime-type"),
			OriginalFilename: changedString(cmd, "original-filename"),
			Description:      changedString(cmd, "description"),
		})
		if berr != nil {
			return berr
		}

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
// replace/finalize (atomic relink). Returns the replacement's media id (the init
// resource's id — the media the file points at once the relink commits) and the
// relinked file resource.
func replaceSinglePart(c *cmdContext, teamID, fileID, path, filename, mimeType string, size int64) (string, *client.Resource, error) {
	repl, err := c.client.Create(c.ctx, replaceInitPath(teamID, fileID), map[string]any{
		"original_filename": filename,
		"mime_type":         mimeType,
		"size_bytes":        size,
	})
	if err != nil {
		return "", nil, err
	}
	uploadURL, _ := repl.Meta["upload_url"].(string)
	if uploadURL == "" {
		return repl.ID, nil, errs.New(errs.ExitGeneric, "replace init did not return a presigned upload_url")
	}
	if _, err := client.PutFileToURL(c.ctx, uploadURL, path, mimeType); err != nil {
		return repl.ID, nil, err
	}
	res, err := c.client.Action(c.ctx, http.MethodPost, replaceFinalizePath(teamID, fileID, repl.ID), nil)
	return repl.ID, res, err
}

// replaceMultipart runs the chunked replace flow: init → per-part → terminal
// complete, which relinks the file itself (no separate replace/finalize). There
// is no replace-multipart abort route, so a failure just surfaces (the backend
// reaps the pending replacement). Returns the replacement's media id and the
// relinked file resource, as replaceSinglePart does.
func replaceMultipart(c *cmdContext, teamID, fileID, path, filename, mimeType string, size, partSize int64) (string, *client.Resource, error) {
	repl, err := c.client.Create(c.ctx, replaceMultipartInitPath(teamID, fileID), map[string]any{
		"original_filename": filename,
		"mime_type":         mimeType,
		"size_bytes":        size,
	})
	if err != nil {
		return "", nil, err
	}
	replID := repl.ID
	uploadID, _ := repl.Meta["upload_id"].(string)
	if uploadID == "" {
		return replID, nil, errs.New(errs.ExitGeneric, "replace multipart init did not return an upload_id")
	}

	f, err := os.Open(path)
	if err != nil {
		return replID, nil, errs.New(errs.ExitGeneric, "open %s: %s", path, err)
	}
	defer f.Close()

	parts, err := streamParts(c, f, partSize, mimeType, func(partNumber int) string {
		return replaceMultipartPartPath(teamID, fileID, replID, uploadID, partNumber)
	})
	if err != nil {
		return replID, nil, err
	}
	// Unlike upload-multipart, the replace-multipart complete is TERMINAL: it
	// relinks the file and returns the updated file resource (no separate
	// finalize — calling one 404s the already-consumed replacement).
	res, err := c.client.ActionWith(c.ctx, client.StyleFlat, http.MethodPost,
		replaceMultipartCompletePath(teamID, fileID, replID, uploadID), map[string]any{"parts": parts})
	return replID, res, err
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
//
// wantMediaID is "" for upload. replace passes the replacement's media id
// (MIO-4172): until the file reports that media_id, a poll describes the media
// being REPLACED and is not evaluated at all. mio-backend commits the relink
// after the response is sent (get_db commits in a yield dependency that FastAPI
// exits once the response has gone out), so the first poll can still see the
// old media — usually already READY, and sometimes FAILED, which is why the
// replace was made. Evaluating it would either return the pre-replace file with
// exit 0 or fail on the old media's status, and a READY read of it would open
// the transcode window before the new transcode could be enqueued.
func waitForFileReady(c *cmdContext, w io.Writer, teamID, fileID, wantMediaID string, timeout time.Duration) (*client.Resource, error) {
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
		if got := resAttrString(res, "media_id"); wantMediaID != "" && got != wantMediaID {
			if time.Now().After(deadline) {
				return res, errs.New(errs.ExitGeneric,
					"timed out after %s waiting for file %s to point at the replacement media %s; it still reports media_id %s",
					timeout, fileID, wantMediaID, statusOr(got))
			}
			if err := pollPause(c); err != nil {
				return nil, err
			}
			continue
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
						"returning with status_transcode unset. The file is stored and usable, but it is NOT transcoded: "+
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
		if err := pollPause(c); err != nil {
			return nil, err
		}
	}
}

// pollPause waits one mediaPollInterval, or returns early with the command
// context's error.
func pollPause(c *cmdContext) error {
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	case <-time.After(mediaPollInterval):
		return nil
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
