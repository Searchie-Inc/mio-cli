package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const eventsContactTokenEnv = "MIO_CONTACT_TOKEN"

func init() {
	eventsCmd.AddCommand(
		eventsCreateCmd,
		eventsListCmd,
		eventsRetrieveCmd,
		eventsUpdateCmd,
		eventsCancelCmd,
	)

	eventsRSVPCmd.AddCommand(
		eventsRSVPSetCmd,
		eventsRSVPWithdrawCmd,
	)
	eventsCmd.AddCommand(eventsRSVPCmd)

	eventsRSVPsCmd.AddCommand(eventsRSVPsListCmd)
	eventsCmd.AddCommand(eventsRSVPsCmd)

	rootCmd.AddCommand(eventsCmd)
}

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Manage hub events.",
	Long: `Create, list, retrieve, update, and cancel events within a hub.

Hub-scoped: --hub is required (or a configured current_hub). The member
routes (list, retrieve, rsvp, and create/update/cancel with MIO_CONTACT_TOKEN)
have NO team_id segment in their API path — only --hub matters there. The
team-key authoring route (create/update/cancel with no MIO_CONTACT_TOKEN) is
/api/admin/teams/{team}/hubs/{hub}/events and does carry the team.

create/update/cancel require hub owner/admin/moderator permissions. See
'mio events rsvp --help' for the member-facing RSVP actions.`,
}

func adminEventsPath(teamID, hubID, id string) string {
	base := fmt.Sprintf("/api/admin/teams/%s/hubs/%s/events", teamID, hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

func eventsAuthoringUsesTeamKey() bool {
	return !flags.anonymous && strings.TrimSpace(os.Getenv(eventsContactTokenEnv)) == ""
}

func eventsAuthoringContext(cmd *cobra.Command) (*cmdContext, func(id string) string, error) {
	if !eventsAuthoringUsesTeamKey() {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return nil, nil, err
		}
		return c, func(id string) string { return eventsPath(hubID, id) }, nil
	}
	c, err := newContext(cmd)
	if err != nil {
		return nil, nil, err
	}
	if err := c.requireAuth(); err != nil {
		return nil, nil, err
	}
	teamID, err := c.requireTeam()
	if err != nil {
		return nil, nil, err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, nil, err
	}
	return c, func(id string) string { return adminEventsPath(teamID, hubID, id) }, nil
}

func teamKeyHintIf(err error) error {
	if eventsAuthoringUsesTeamKey() {
		return withTeamKeyHint(err)
	}
	return err
}

func withTeamKeyHint(err error) error {
	if err == nil || errs.HTTPStatusOf(err) != http.StatusForbidden {
		return err
	}
	return fmt.Errorf("%w; hint: authoring events with a team API key needs the key to carry the can_author_events flag (and the key to belong to this team). "+
		"The team owner grants it with `mio api-keys update <key-id> --can-author-events` (find the id with `mio api-keys list`), "+
		"or mints a new key with `mio api-keys create --name <n> --can-author-events` — but key management needs a USER access token "+
		"(the access_token from POST /api/v1/auth/login, passed with --api-key): a team API key, this one included, cannot do it. "+
		"Alternatively set %s to a member access token to author as that member", err, eventsContactTokenEnv)
}

func eventsPath(hubID, id string) string {
	base := fmt.Sprintf("/api/hubs/%s/events", hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

func eventsRSVPPath(hubID, eventID string) string {
	return eventsPath(hubID, eventID) + "/rsvp"
}

func eventsRSVPsPath(hubID, eventID string) string {
	return eventsPath(hubID, eventID) + "/rsvps"
}

func eventsContext(cmd *cobra.Command) (*cmdContext, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", err
	}

	contactToken := strings.TrimSpace(os.Getenv(eventsContactTokenEnv))

	if !c.resolved.Anonymous && contactToken == "" {
		return nil, "", errs.New(errs.ExitAuth,
			"mio events requires a member (contact) token. Set MIO_CONTACT_TOKEN with a "+
				"member access token (member login for the CLI is tracked in MIO-3178); "+
				"a team API key cannot act as a specific contact (RSVP, event authorship) "+
				"on the Events v1 API")
	}

	if _, err := c.requireTeam(); err != nil {
		return nil, "", err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, "", err
	}

	if !c.resolved.Anonymous && contactToken != "" {
		c.client = client.New(c.resolved.APIBase, contactToken, client.WithDebug(flags.debug))
	}

	return c, hubID, nil
}

const eventsMaxHosts = 3

var eventsAttrFlags = []string{
	"title", "starts-at", "ends-at", "timezone", "location-type",
	"description", "cover-image-url", "location-url", "location-address",
	"visibility", "segment-id", "rsvp-tag-id",
}

func setEventAttrs(cmd *cobra.Command, attrs map[string]any) error {
	for _, f := range eventsAttrFlags {
		setStringFlag(cmd, attrs, f)
	}
	setIntFlag(cmd, attrs, "capacity")
	setBoolFlag(cmd, attrs, "attendee-list-visible")

	if cmd.Flags().Changed("cover-media-id") {
		id, err := cmd.Flags().GetString("cover-media-id")
		if err != nil {
			return errs.Wrap(errs.ExitGeneric, err)
		}
		if strings.TrimSpace(id) == "" {
			return errs.New(errs.ExitUsage, "--cover-media-id must not be blank")
		}
		attrs["cover_media_id"] = id
	}

	if cmd.Flags().Changed("host-contact-id") {
		raw, ferr := cmd.Flags().GetString("host-contact-id")
		if ferr != nil {
			return errs.Wrap(errs.ExitGeneric, ferr)
		}
		id := strings.TrimSpace(raw)
		if id == "" {
			return errs.New(errs.ExitUsage,
				"--host-contact-id was set to an empty value; remove it or supply a real contact id")
		}
		attrs["host_contact_id"] = id
	}

	if !cmd.Flags().Changed("host-contact-ids") {
		return nil
	}
	ids, ferr := cmd.Flags().GetStringSlice("host-contact-ids")
	if ferr != nil {
		return errs.Wrap(errs.ExitGeneric, ferr)
	}
	cleaned := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return errs.New(errs.ExitUsage,
				"--host-contact-ids contains an empty value; remove it or supply a real contact id")
		}
		if _, dup := seen[id]; dup {
			return errs.New(errs.ExitUsage,
				"--host-contact-ids contains a duplicate contact id (%s); list each host exactly once", id)
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}
	if len(cleaned) == 0 {
		return errs.New(errs.ExitUsage,
			"--host-contact-ids was set but no contact id was given; an event always needs at least one host")
	}
	if len(cleaned) > eventsMaxHosts {
		return errs.New(errs.ExitUsage,
			"--host-contact-ids accepts at most %d contact ids (got %d)", eventsMaxHosts, len(cleaned))
	}
	attrs["host_contact_ids"] = cleaned
	return nil
}

var eventsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a hub event.",
	Long: `Create a new event in the active hub.

--title, --starts-at, --ends-at, --timezone, and --location-type are required.

--starts-at and --ends-at accept an RFC3339 datetime string (e.g.
2026-09-01T18:00:00Z), passed through to the API as-is.

Allowed values for --location-type: url, address.
Allowed values for --visibility:     all_members, segment (requires --segment-id).

HOSTS. An event carries 1 to 3 hosts. Name them with EITHER --host-contact-id
(one contact id) OR --host-contact-ids (the full ordered set, comma-separated
or repeated; position 0 is the primary host). The two flags are mutually
exclusive — passing both is a usage error, checked before any request is sent.
Omit both and the event is hosted by the calling contact, which is what every
CLI-created event did before these flags existed.

Every named contact must already be a current, active member of this hub; the
API rejects the WHOLE request otherwise (no partial accept). Being a host is
attribution, not permission — it grants the contact no extra authority.

Requires hub owner/admin/moderator permissions. Auth: a team API key that carries
can_author_events works with no MIO_CONTACT_TOKEN (team-key route; a 403 explains
the flag — see 'mio api-keys update --help'); with MIO_CONTACT_TOKEN set the member
route is used as before.`,
	Example: `  mio events create --hub hub_123 --title "Community Meetup" \
    --starts-at 2026-09-01T18:00:00Z --ends-at 2026-09-01T20:00:00Z \
    --timezone America/New_York --location-type url --location-url https://zoom.us/j/123

  mio events create --hub hub_123 --title "Members-Only Session" \
    --starts-at 2026-09-01T18:00:00Z --ends-at 2026-09-01T20:00:00Z \
    --timezone America/New_York --location-type address --location-address "123 Main St" \
    --visibility segment --segment-id seg_abc123 --capacity 50

  # Host it as somebody other than the calling contact.
  mio events create --hub hub_123 --title "Guest Lecture" \
    --starts-at 2026-09-01T18:00:00Z --ends-at 2026-09-01T20:00:00Z \
    --timezone America/New_York --location-type url --location-url https://zoom.us/j/123 \
    --host-contact-id con_guest

  # Three co-hosts, comma-separated (repeating the flag works too).
  mio events create --hub hub_123 --title "Panel" \
    --starts-at 2026-09-01T18:00:00Z --ends-at 2026-09-01T20:00:00Z \
    --timezone America/New_York --location-type url --location-url https://zoom.us/j/123 \
    --host-contact-ids con_a,con_b,con_c`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		attrs := map[string]any{}
		if err := setEventAttrs(cmd, attrs); err != nil {
			return err
		}

		c, pathFor, err := eventsAuthoringContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Create(c.ctx, pathFor(""), attrs)
		if err != nil {
			return teamKeyHintIf(err)
		}
		return c.render(cmd, res)
	},
}

var eventsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List events in a hub.",
	Long: `List all events in the active hub, optionally filtered by status and sorted.

Allowed values for --status: upcoming, past.
Allowed values for --sort:   starts_at, -starts_at.`,
	Example: `  mio events list --hub hub_123
  mio events list --hub hub_123 --status upcoming --sort starts_at
  mio events list --hub hub_123 --status past --sort -starts_at --limit 25`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		if cmd.Flags().Changed("status") {
			v, ferr := cmd.Flags().GetString("status")
			if ferr == nil {
				switch v {
				case "upcoming", "past":
					query.Set("filter[status]", v)
				default:
					return errs.New(errs.ExitUsage, "--status must be one of: upcoming, past (got %q)", v)
				}
			}
		}
		if cmd.Flags().Changed("sort") {
			v, ferr := cmd.Flags().GetString("sort")
			if ferr == nil {
				switch v {
				case "starts_at", "-starts_at":
					query.Set("sort", v)
				default:
					return errs.New(errs.ExitUsage, "--sort must be one of: starts_at, -starts_at (got %q)", v)
				}
			}
		}

		col, err := c.client.List(c.ctx, eventsPath(hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var eventsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an event by id.",
	Long:    "Retrieve a single event from the active hub by its id.",
	Example: `  mio events retrieve evt_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, eventsPath(hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var eventsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an event by id.",
	Long: `Partially update an event. Only the flags you supply are changed (PATCH
semantics) — an unset flag is never sent, so it can never be null'd out by
accident.

Allowed values for --location-type (when provided): url, address.
Allowed values for --visibility (when provided):     all_members, segment.

HOSTS. --host-contact-id and --host-contact-ids REPLACE the event's whole host
set — they are not a per-host add/remove. The two are mutually exclusive
(passing both is a usage error, checked before any request is sent), and the
replacement set must hold 1 to 3 distinct contact ids, each a current, active
member of this hub. There is no way to leave an event with no host: omit both
flags to leave the existing hosts untouched.

Requires hub owner/admin/moderator permissions. Auth: a team API key that carries
can_author_events works with no MIO_CONTACT_TOKEN (team-key route; a 403 explains
the flag — see 'mio api-keys update --help'); with MIO_CONTACT_TOKEN set the member
route is used as before.`,
	Example: `  mio events update evt_abc123 --hub hub_123 --title "New Title"
  mio events update evt_abc123 --hub hub_123 --capacity 100 --visibility all_members

  # Hand the event to a single new host, replacing whoever hosted it before.
  mio events update evt_abc123 --hub hub_123 --host-contact-id con_new

  # Replace the host set with two co-hosts (repeating the flag works too).
  mio events update evt_abc123 --hub hub_123 --host-contact-ids con_a,con_b`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		attrs := map[string]any{}
		if err := setEventAttrs(cmd, attrs); err != nil {
			return err
		}

		clear, _ := cmd.Flags().GetBool("clear-cover-media")
		if clear {
			if err := confirmDestructive(cmd, "Clear the one-shot cover media binding? It cannot be restored with the same media id."); err != nil {
				return err
			}
			attrs["cover_media_id"] = nil
		}
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		c, pathFor, err := eventsAuthoringContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Update(c.ctx, pathFor(args[0]), attrs)
		if err != nil {
			return teamKeyHintIf(err)
		}
		return c.render(cmd, res)
	},
}

var eventsCancelCmd = &cobra.Command{
	Use:   "cancel <id>",
	Short: "Cancel an event.",
	Long: `Cancel an event. This is an action verb (not a delete) — the event record is
retained with a cancelled status.

Requires hub owner/admin/moderator permissions. Pass --yes to skip the
confirmation prompt in non-interactive environments (scripts, CI, AI agents).

Auth: with a team API key that carries can_author_events (no MIO_CONTACT_TOKEN),
this goes to the team-key route, which takes no body — so --message is refused
there; set MIO_CONTACT_TOKEN to cancel with an organiser message.`,
	Example: `  mio events cancel evt_abc123 --hub hub_123
  mio events cancel evt_abc123 --hub hub_123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var attrs map[string]any
		if cmd.Flags().Changed("message") {
			message, err := cmd.Flags().GetString("message")
			if err != nil {
				return errs.Wrap(errs.ExitGeneric, err)
			}
			if strings.TrimSpace(message) == "" {
				return errs.New(errs.ExitUsage, "--message must not be blank")
			}
			attrs = map[string]any{"message": message}
			if eventsAuthoringUsesTeamKey() {
				return errs.New(errs.ExitUsage, "--message cannot be sent with a team API key: the team-key cancel route takes no body. "+
					"Set "+eventsContactTokenEnv+" to a member access token to cancel with an organiser message, or cancel without --message")
			}
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Cancel event %s?", args[0])); err != nil {
			return err
		}
		c, pathFor, err := eventsAuthoringContext(cmd)
		if err != nil {
			return err
		}

		path := pathFor(args[0]) + "/cancel"
		res, err := c.client.Action(c.ctx, "POST", path, attrs)
		if err != nil {
			return teamKeyHintIf(err)
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Cancelled event %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

var eventsRSVPCmd = &cobra.Command{
	Use:   "rsvp",
	Short: "Set or withdraw your own RSVP for an event.",
	Long: `Set or withdraw the authenticated member's RSVP for an event.

These actions act as the AUTHENTICATED MEMBER CONTACT (login-based auth) — they
operate on the calling contact's own RSVP. Unlike events create/update/cancel,
they do NOT require hub owner/admin/moderator permissions.`,
}

var eventsRSVPSetCmd = &cobra.Command{
	Use:   "set <event_id>",
	Short: "Set your RSVP status for an event.",
	Long: `Set the authenticated member's RSVP status for an event.

--status is required: going, not_going, or maybe.

Acts as the authenticated member contact (login-based auth); does not require
hub owner/admin/moderator permissions.`,
	Example: `  mio events rsvp set evt_abc123 --hub hub_123 --status going
  mio events rsvp set evt_abc123 --hub hub_123 --status not_going
  mio events rsvp set evt_abc123 --hub hub_123 --status maybe`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		status, ferr := cmd.Flags().GetString("status")
		if ferr != nil {
			return errs.Wrap(errs.ExitGeneric, ferr)
		}
		switch status {
		case "going", "not_going", "maybe":
		default:
			return errs.New(errs.ExitUsage, "--status must be one of: going, not_going, maybe (got %q)", status)
		}

		attrs := map[string]any{"status": status}
		res, err := c.client.Action(c.ctx, "PUT", eventsRSVPPath(hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "RSVP set to %s for event %s.\n", status, args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

var eventsRSVPWithdrawCmd = &cobra.Command{
	Use:   "withdraw <event_id>",
	Short: "Withdraw your RSVP for an event.",
	Long: `Withdraw the authenticated member's RSVP for an event. This is irreversible.

Acts as the authenticated member contact (login-based auth); does not require
hub owner/admin/moderator permissions.

Pass --yes to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio events rsvp withdraw evt_abc123 --hub hub_123
  mio events rsvp withdraw evt_abc123 --hub hub_123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Withdraw RSVP for event %s?", args[0])); err != nil {
			return err
		}

		res, err := c.client.Action(c.ctx, "DELETE", eventsRSVPPath(hubID, args[0]), nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Withdrew RSVP for event %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

var eventsRSVPsCmd = &cobra.Command{
	Use:   "rsvps",
	Short: "Read RSVPs recorded for an event.",
	Long: `List RSVPs recorded for an event. Only "going" RSVPs are returned — a
"not_going" (including withdrawn) or "maybe" RSVP never appears here.

Hub owners/admins/moderators can always list. An ordinary active member can
list too when the host left the attendee list visible for this event
(--attendee-list-visible on create/update) — this is not an admin-only read.`,
}

var eventsRSVPsListCmd = &cobra.Command{
	Use:   "list <event_id>",
	Short: "List RSVPs for an event.",
	Long: `List the "going" RSVPs recorded for the given event. A "not_going"
(including withdrawn) or "maybe" RSVP is never included in the response.

Hub owners/admins/moderators can always list; an ordinary active member can
list too when the host left the attendee list visible for this event.`,
	Example: `  mio events rsvps list evt_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, eventsRSVPsPath(hubID, args[0]), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

func init() {
	for _, cmd := range []*cobra.Command{eventsCreateCmd, eventsUpdateCmd} {
		cmd.Flags().String("title", "", "Event title.")
		cmd.Flags().String("starts-at", "", "Event start time, RFC3339 (e.g. 2026-09-01T18:00:00Z).")
		cmd.Flags().String("ends-at", "", "Event end time, RFC3339 (e.g. 2026-09-01T20:00:00Z).")
		cmd.Flags().String("timezone", "", "IANA timezone name (e.g. America/New_York).")
		cmd.Flags().String("location-type", "", "Location type: url or address.")
		cmd.Flags().String("description", "", "Event description.")
		cmd.Flags().String("cover-image-url", "", "Cover image URL; an empty value clears the cover URL.")
		cmd.Flags().String("cover-media-id", "", "Media id from a hub-scoped attachment upload session; binding is one-shot.")
		cmd.Flags().String("location-url", "", "Location URL (when --location-type=url).")
		cmd.Flags().String("location-address", "", "Location address (when --location-type=address).")
		cmd.Flags().Int("capacity", 0, "Maximum attendee capacity. A \"maybe\" RSVP does not consume a seat — only \"going\" counts against this limit.")
		cmd.Flags().String("visibility", "", "Visibility: all_members or segment.")
		cmd.Flags().String("segment-id", "", "Segment id to scope visibility to (when --visibility=segment).")
		cmd.Flags().String("rsvp-tag-id", "", "Tag id to apply to contacts who RSVP \"going\". Not applied for \"maybe\" or \"not_going\".")
		cmd.Flags().Bool("attendee-list-visible", false, "Whether the attendee list is visible to other attendees.")

		cmd.Flags().String("host-contact-id", "",
			"Contact id to record as the event's SOLE host, replacing the whole host set — shorthand for "+
				"--host-contact-ids with a single value. Must be a current, active member of this hub. "+
				"Mutually exclusive with --host-contact-ids. Default when both host flags are omitted: on "+
				"create, the calling contact hosts the event; on update, the existing hosts are left unchanged.")
		cmd.Flags().StringSlice("host-contact-ids", nil,
			fmt.Sprintf("The full, ordered set of contact ids to record as the event's hosts, REPLACING the "+
				"existing set rather than merging into it (position 0 is the primary host). Comma-separated "+
				"or repeatable. Must hold 1 to %d distinct, non-blank ids, each a current active member of "+
				"this hub — the count, duplicate and blank checks all run before any request is sent. "+
				"Mutually exclusive with --host-contact-id. Default when both host flags are omitted: on "+
				"create, the calling contact hosts the event; on update, the existing hosts are left unchanged.",
				eventsMaxHosts))
		cmd.MarkFlagsMutuallyExclusive("host-contact-id", "host-contact-ids")
	}
	eventsUpdateCmd.Flags().Bool("clear-cover-media", false, "Clear the media-backed cover (irreversible; requires confirmation).")
	eventsUpdateCmd.MarkFlagsMutuallyExclusive("clear-cover-media", "cover-media-id")
	eventsCancelCmd.Flags().String("message", "", "Organiser message to going/maybe attendees (max 280 characters).")
	markFlagsRequired(eventsCreateCmd, "title", "starts-at", "ends-at", "timezone", "location-type")

	addPaginationFlags(eventsListCmd)
	eventsListCmd.Flags().String("status", "", "Filter by status: upcoming or past.")
	eventsListCmd.Flags().String("sort", "", "Sort order: starts_at or -starts_at.")

	eventsRSVPSetCmd.Flags().String("status", "", "RSVP status: going, not_going, or maybe. Required.")
	markFlagsRequired(eventsRSVPSetCmd, "status")

	addPaginationFlags(eventsRSVPsListCmd)
}
