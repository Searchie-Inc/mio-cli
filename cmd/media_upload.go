package cmd

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

var syntheticAssetKinds = map[string]bool{"document": true, "pdf": true}

const (
	autoMultipartThreshold = 100 * 1024 * 1024
	minPartSizeMB          = 5
	defaultPartSizeMB      = 16
)

func useMultipart(force bool, size int64) bool {
	return force || size > autoMultipartThreshold
}

func multipartThresholdMB() string {
	return fmt.Sprintf("%d MB", autoMultipartThreshold/(1024*1024))
}

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
	mediaFilesRegisterSyntheticCmd.Flags().String("mime-type", "",
		"Mime type to store. Omitted, the endpoint stores application/octet-stream whatever --asset-kind "+
			"says, so pass application/pdf with --asset-kind pdf.")
	mediaFilesRegisterSyntheticCmd.Flags().String("original-filename", "", "Optional original filename.")
	mediaFilesRegisterSyntheticCmd.Flags().String("description", "", "Optional description.")
}

func syntheticFilesPath(teamID string) string {
	return fmt.Sprintf("/api/admin/teams/%s/files/synthetic", teamID)
}

type SyntheticFileInput struct {
	Title            string
	AssetKind        *string
	Visibility       *string
	MimeType         *string
	OriginalFilename *string
	Description      *string
}

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

		if folderID := flagValue(cmd, "folder-id"); folderID != "" {
			if res, err = c.client.UpdateWithID(c.ctx, filesPath(teamID, fileID), fileID, map[string]any{"folder_id": folderID}); err != nil {
				return err
			}
		}

		if wait, _ := cmd.Flags().GetBool("wait"); wait {
			timeout, _ := cmd.Flags().GetDuration("timeout")
			if res, err = waitForFileReady(c, cmd.ErrOrStderr(), teamID, fileID, "", timeout); err != nil {
				return err
			}
		}
		return c.render(cmd, res)
	},
}

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

		if wait, _ := cmd.Flags().GetBool("wait"); wait {
			timeout, _ := cmd.Flags().GetDuration("timeout")
			if res, err = waitForFileReady(c, cmd.ErrOrStderr(), teamID, fileID, replacementID, timeout); err != nil {
				return err
			}
		}
		return renderFileOrFetch(cmd, c, teamID, fileID, res)
	},
}

func replaceInitPath(teamID, fileID string) string { return filesPath(teamID, fileID) + "/replace" }

func replaceFinalizePath(teamID, fileID, replacementID string) string {
	return fmt.Sprintf("%s/replace/%s/finalize", filesPath(teamID, fileID), replacementID)
}

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

var mediaFilesRegisterSyntheticCmd = &cobra.Command{
	Use:   "register-synthetic",
	Short: "Register a synthetic READY file (no upload).",
	Long: `Register a synthetic document file that is immediately READY with a
server-generated storage path — no upload/finalize/transcode. Mirrors the
seeder's stub-document path; requires a team-owner key.

--mime-type defaults to the endpoint's own application/octet-stream when
omitted: the CLI sends no mime_type, and the endpoint does not derive one from
--asset-kind. A pdf registered without --mime-type application/pdf is stored as
a generic blob, which a consumer that reads mime_type does not recognise as a
pdf. For a placeholder text lesson (no real bytes — the body IS its
description), pass --mime-type text/markdown: that is the convention 'hub
scaffold' uses for playlists[].documents[] template placeholders (MIO-3116),
and mime_type is the field mime-keyed branches downstream (document viewers,
transcode-wait checks) actually read.`,
	Example: `  mio media files register-synthetic --title "Terms.pdf" --asset-kind pdf --mime-type application/pdf
  mio media files register-synthetic --title "Add your first lesson" --mime-type text/markdown --description "A placeholder lesson."`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
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

	if _, err := c.client.ActionWith(c.ctx, client.StyleFlat, http.MethodPost,
		multipartCompletePath(teamID, fileID, uploadID), map[string]any{"parts": parts}); err != nil {
		abort()
		return fileID, nil, err
	}
	res, err := c.client.Action(c.ctx, http.MethodPost, filesPath(teamID, fileID)+"/finalize", nil)
	if err != nil {
		return fileID, nil, err
	}
	if res == nil {
		res, err = c.client.Retrieve(c.ctx, filesPath(teamID, fileID))
	}
	return fileID, res, err
}

func multipartInitPath(teamID string) string { return filesPath(teamID, "") + "/multipart" }

func multipartUploadPath(teamID, fileID, uploadID string) string {
	return fmt.Sprintf("%s/multipart/%s", filesPath(teamID, fileID), uploadID)
}

func multipartPartPath(teamID, fileID, uploadID string, partNumber int) string {
	return fmt.Sprintf("%s/parts/%d", multipartUploadPath(teamID, fileID, uploadID), partNumber)
}

func multipartCompletePath(teamID, fileID, uploadID string) string {
	return multipartUploadPath(teamID, fileID, uploadID) + "/complete"
}

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
	res, err := c.client.ActionWith(c.ctx, client.StyleFlat, http.MethodPost,
		replaceMultipartCompletePath(teamID, fileID, replID, uploadID), map[string]any{"parts": parts})
	return replID, res, err
}

func replaceMultipartInitPath(teamID, fileID string) string {
	return replaceInitPath(teamID, fileID) + "/multipart"
}

func replaceMultipartBase(teamID, fileID, replID, uploadID string) string {
	return fmt.Sprintf("%s/replace/%s/multipart/%s", filesPath(teamID, fileID), replID, uploadID)
}

func replaceMultipartPartPath(teamID, fileID, replID, uploadID string, partNumber int) string {
	return fmt.Sprintf("%s/parts/%d", replaceMultipartBase(teamID, fileID, replID, uploadID), partNumber)
}

func replaceMultipartCompletePath(teamID, fileID, replID, uploadID string) string {
	return replaceMultipartBase(teamID, fileID, replID, uploadID) + "/complete"
}

func renderFileOrFetch(cmd *cobra.Command, c *cmdContext, teamID, fileID string, res *client.Resource) error {
	if res == nil {
		var err error
		if res, err = c.client.Retrieve(c.ctx, filesPath(teamID, fileID)); err != nil {
			return err
		}
	}
	return c.render(cmd, res)
}

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

var mediaPollInterval = 2 * time.Second

var transcodeStartGrace = 30 * time.Second

func transcribeSettled(tr string) bool {
	switch tr {
	case "", "READY", "NOT_APPLICABLE", "REJECTED":
		return true
	}
	return false
}

func awaitsTranscode(res *client.Resource) (waits, kindKnown bool) {
	mime := strings.ToLower(resAttrString(res, "mime_type"))
	return strings.HasPrefix(mime, "video/"), mime != ""
}

func waitForFileReady(c *cmdContext, w io.Writer, teamID, fileID, wantMediaID string, timeout time.Duration) (*client.Resource, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	var windowStart, windowEnd time.Time
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

		if up == "READY" && windowEnd.IsZero() {
			windowStart = time.Now()
			windowEnd = windowStart.Add(min(transcodeStartGrace, time.Until(deadline)))
		}

		transcodeDone := tc == "READY"
		if tc == "" {
			waits, kindKnown := awaitsTranscode(res)
			switch {
			case !kindKnown:
				if !warnedUnknownKind {
					fmt.Fprintf(w, "warning: this file reports no mime_type, so whether it transcodes "+
						"cannot be determined; returning without waiting. Check with "+
						"`mio media files retrieve %s`.\n", fileID)
					warnedUnknownKind = true
				}
				transcodeDone = true
			case !waits:
				transcodeDone = true
			case !windowEnd.IsZero() && time.Now().After(windowEnd):
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
