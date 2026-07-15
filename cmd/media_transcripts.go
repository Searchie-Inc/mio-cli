package cmd

// media_transcripts.go — `mio media transcripts` command group (MIO-2289).
//
// Team-scoped transcript surface (backend app/media/router.py
// media_transcript_router, guard _require_team_member):
//
//	get      GET   /api/teams/{team}/media/{media_id}/transcript
//	vtt      GET   /api/teams/{team}/media/{media_id}/transcript.vtt      → {signed_url, expires_at}
//	content  GET   /api/teams/{team}/media/{media_id}/transcript/content
//	versions GET   /api/teams/{team}/media/{media_id}/transcript/versions[/{version}]
//	edit     PATCH /api/teams/{team}/media/{media_id}/transcript          (type transcripts)
//	revert   POST  /api/teams/{team}/media/{media_id}/transcript/revert   (type transcripts)
//
// Team-scoped; requires a team-member user JWT.

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	mediaTranscriptsCmd.AddCommand(
		mediaTranscriptsGetCmd,
		mediaTranscriptsVttCmd,
		mediaTranscriptsContentCmd,
		mediaTranscriptsVersionsCmd,
		mediaTranscriptsEditCmd,
		mediaTranscriptsRevertCmd,
	)
	mediaCmd.AddCommand(mediaTranscriptsCmd)

	mediaTranscriptsEditCmd.Flags().String("words", "", "JSON array of words (or @file.json). Each: {word,start_ms,end_ms,confidence?,speaker_label?}. Required.")
	mediaTranscriptsEditCmd.Flags().String("language", "", "Optional language code for the edited transcript (defaults to 'und').")
	mediaTranscriptsRevertCmd.Flags().Int("version", 0, "Transcript version to revert to (>= 1). Required.")
}

// transcriptBase returns /api/teams/{team}/media/{media_id}/transcript.
func transcriptBase(teamID, mediaID string) string {
	return fmt.Sprintf("/api/teams/%s/media/%s/transcript", teamID, mediaID)
}

var mediaTranscriptsCmd = &cobra.Command{
	Use:   "transcripts",
	Short: "Manage a media file's transcript.",
	Long: `Read a media file's transcript (current, .vtt signed URL, full word content,
version history) and author edits or revert to a prior version.

All commands are team-scoped and take the media file id.`,
	Example: `  mio media transcripts get file_abc123
  mio media transcripts edit file_abc123 --words @words.json
  mio media transcripts revert file_abc123 --version 2`,
}

// ---- get --------------------------------------------------------------------

var mediaTranscriptsGetCmd = &cobra.Command{
	Use:     "get <media_id>",
	Short:   "Get a media file's current transcript.",
	Long:    "Retrieve the current transcript metadata (version, status, language, word count, chapters, speakers) for a media file.",
	Example: `  mio media transcripts get file_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, transcriptBase(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- vtt --------------------------------------------------------------------

var mediaTranscriptsVttCmd = &cobra.Command{
	Use:     "vtt <media_id>",
	Short:   "Get a signed URL for the transcript WebVTT (.vtt) file.",
	Long:    "Return a short-lived signed URL (and its expiry) for the transcript's WebVTT (.vtt) captions. No bytes are downloaded — fetch the returned signed_url to get the .vtt.",
	Example: `  mio media transcripts vtt file_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, transcriptBase(teamID, args[0])+".vtt")
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- content ----------------------------------------------------------------

var mediaTranscriptsContentCmd = &cobra.Command{
	Use:     "content <media_id>",
	Short:   "Get the full transcript content (word tokens).",
	Long:    "Retrieve the full transcript content — the word-level tokens, chapters, and speaker labels — for a media file.",
	Example: `  mio media transcripts content file_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, transcriptBase(teamID, args[0])+"/content")
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- versions (list, or show a specific version) ----------------------------

var mediaTranscriptsVersionsCmd = &cobra.Command{
	Use:   "versions <media_id> [version]",
	Short: "List transcript versions, or show a specific version.",
	Long:  "With just <media_id>, list the transcript version history. With a <version> number, show that version's full content.",
	Example: `  mio media transcripts versions file_abc123
  mio media transcripts versions file_abc123 3`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		if len(args) == 2 {
			res, err := c.client.Retrieve(c.ctx, transcriptBase(teamID, args[0])+"/versions/"+args[1])
			if err != nil {
				return err
			}
			return c.render(cmd, res)
		}
		col, err := c.client.List(c.ctx, transcriptBase(teamID, args[0])+"/versions", nil)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- edit -------------------------------------------------------------------

var mediaTranscriptsEditCmd = &cobra.Command{
	Use:   "edit <media_id>",
	Short: "Edit a media file's transcript words (creates a new version).",
	Long: `Replace the transcript's word list, creating a new transcript version.

--words takes a JSON array (or @file.json). Each word is an object:
  {"word":"hello","start_ms":0,"end_ms":480,"confidence":0.98,"speaker_label":"A"}
word, start_ms and end_ms (milliseconds) are required; confidence and
speaker_label are optional. --language optionally sets the transcript language.`,
	Example: `  mio media transcripts edit file_abc123 --words '[{"word":"hello","start_ms":0,"end_ms":480}]'
  mio media transcripts edit file_abc123 --words @words.json --language en`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate the payload before resolving auth/team so a bad flag fires no request.
		words, err := parseJSONArrayFlag(cmd, "words")
		if err != nil {
			return err
		}
		attrs := map[string]any{"words": words}
		setStringFlag(cmd, attrs, "language")

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Update(c.ctx, transcriptBase(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- revert -----------------------------------------------------------------

var mediaTranscriptsRevertCmd = &cobra.Command{
	Use:     "revert <media_id>",
	Short:   "Revert a transcript to a prior version.",
	Long:    "Revert the transcript to a prior version, creating a new version whose content matches the target. --version is the target version number (>= 1).",
	Example: `  mio media transcripts revert file_abc123 --version 2`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate --version before resolving auth/team so a bad flag fires no request.
		if !cmd.Flags().Changed("version") {
			return errs.New(errs.ExitUsage, "--version is required (the transcript version to revert to)")
		}
		version, err := cmd.Flags().GetInt("version")
		if err != nil {
			return errs.New(errs.ExitUsage, "--version: %s", err)
		}
		if version < 1 {
			return errs.New(errs.ExitUsage, "--version must be >= 1")
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Action(c.ctx, http.MethodPost,
			transcriptBase(teamID, args[0])+"/revert", map[string]any{"version": version})
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}
