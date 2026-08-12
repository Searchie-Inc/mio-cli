package cmd

// events.go implements the `mio events` command group for hub events (Events
// v1 API, MIO-3173). Every sub-command is hub-scoped ONLY.
//
// UNLIKE every other resource in this CLI, the events base route does NOT sit
// under /api/teams/{team_id}/... — the backend route (mio-backend
// app/hub_events/router.py) is /api/hubs/{hub_id}/events, with no team_id
// segment at all. This is the first resource with that shape. The command
// still resolves and requires a team (via requireTeam, called inside
// eventsContext) for context/UX consistency with every other hub-scoped
// resource — but the resolved team id is never interpolated into the path.
// See eventsPath.
//
// Routes (mio-backend app/hub_events/router.py):
//
//	create        POST    /api/hubs/{hub_id}/events
//	list          GET     /api/hubs/{hub_id}/events
//	retrieve      GET     /api/hubs/{hub_id}/events/{id}
//	update        PATCH   /api/hubs/{hub_id}/events/{id}
//	cancel        POST    /api/hubs/{hub_id}/events/{id}/cancel
//	rsvp set      PUT     /api/hubs/{hub_id}/events/{id}/rsvp
//	rsvp withdraw DELETE  /api/hubs/{hub_id}/events/{id}/rsvp
//	rsvps list    GET     /api/hubs/{hub_id}/events/{id}/rsvps
//
// create/update/cancel require hub owner/admin/moderator permissions. The
// rsvp set/withdraw verbs act as the AUTHENTICATED MEMBER CONTACT (login-based
// auth) — they operate on the calling contact's own RSVP, not an arbitrary one,
// and do NOT require hub owner/admin/mod permissions.
//
// AUTH (Codex round 1, MIO-3173): every Events v1 route — including
// create/update/cancel — is gated by a contact-identity dependency
// (require_hub_elevated_contact / member deps on the backend). A team API key
// authenticates as the TEAM, not an individual contact, so it cannot satisfy
// that dependency: every command in this file 401s under a plain team key.
//
// `mio login` (see login.go mintAndStore) mints and stores ONLY a team-scoped
// API key today — the JWT obtained from POST /api/auth/login is used once to
// mint that key and then discarded; the CLI has no existing mechanism that
// persists a contact/member access token, and no other command consumes one.
// Until the CLI grows a dedicated member-login flow (tracked as a follow-up),
// eventsContext below reads a member access token from MIO_CONTACT_TOKEN —
// the minimal, precedent-setting escape hatch, mirroring the MIO_API_KEY /
// MIO_EMAIL / MIO_PASSWORD env-var pattern already used by config.EnvAPIKey
// and login.go — and swaps it in as the bearer for every events request. With
// only a team API key configured, eventsContext fails fast with an actionable
// error rather than letting every command round-trip to a guaranteed 401.
//
// OUT OF SCOPE (deferred to a follow-up ticket): GET .../events/{id}/calendar.ics
// — a raw text/calendar download that needs a new raw-response client method
// distinct from the JSON:API decoders used everywhere else in this file.
//
// See also internal/client/client.go: resourceTypeFromPath's typeOverrides
// table carries two entries this command group depends on —
// {"hubs/events": "hub_events"} and {"events/rsvp": "event_rsvps"} — and
// knownCollections gained "rsvp". Without those, every events/rsvp write 422s
// (the backend's Literal-typed write schemas reject the wrong envelope type).

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// eventsContactTokenEnv is the environment variable holding a member/contact
// access token (a JWT minted by POST /api/auth/login for an account that
// resolves to a contact — the same login route login.go's `mio login` calls,
// just without discarding the token afterward). Events v1 requires a contact
// identity on every route; see the package doc comment above for why a team
// API key cannot be used instead.
const eventsContactTokenEnv = "MIO_CONTACT_TOKEN"

func init() {
	// events <action>
	eventsCmd.AddCommand(
		eventsCreateCmd,
		eventsListCmd,
		eventsRetrieveCmd,
		eventsUpdateCmd,
		eventsCancelCmd,
	)

	// events rsvp <action>  (nested sub-resource: the caller's own RSVP)
	eventsRSVPCmd.AddCommand(
		eventsRSVPSetCmd,
		eventsRSVPWithdrawCmd,
	)
	eventsCmd.AddCommand(eventsRSVPCmd)

	// events rsvps <action>  (nested sub-resource: read all RSVPs on an event)
	eventsRSVPsCmd.AddCommand(eventsRSVPsListCmd)
	eventsCmd.AddCommand(eventsRSVPsCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(eventsCmd)
}

// ---- events group -----------------------------------------------------------

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Manage hub events.",
	Long: `Create, list, retrieve, update, and cancel events within a hub.

Hub-scoped: --hub is required (or a configured current_hub). Unlike most
resources in this CLI, events has NO team_id segment in its API path — only
--hub matters for routing, though a team context is still resolved for
consistency with the rest of the CLI.

create/update/cancel require hub owner/admin/moderator permissions. See
'mio events rsvp --help' for the member-facing RSVP actions.`,
}

// eventsPath returns /api/hubs/{hub_id}/events[/{id}]. NOTE: no team_id
// segment — see the package doc comment above.
func eventsPath(hubID, id string) string {
	base := fmt.Sprintf("/api/hubs/%s/events", hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// eventsRSVPPath returns /api/hubs/{hub_id}/events/{event_id}/rsvp — the
// caller's own RSVP on the event.
func eventsRSVPPath(hubID, eventID string) string {
	return eventsPath(hubID, eventID) + "/rsvp"
}

// eventsRSVPsPath returns /api/hubs/{hub_id}/events/{event_id}/rsvps — the
// full list of RSVPs on the event.
func eventsRSVPsPath(hubID, eventID string) string {
	return eventsPath(hubID, eventID) + "/rsvps"
}

// eventsContext is the shared boilerplate for events sub-commands: builds the
// context, resolves both team id (for context/UX consistency — see the
// package doc comment) and hub id, and — critically for this resource —
// swaps the HTTP client's bearer to a member/contact access token when one is
// configured via MIO_CONTACT_TOKEN. Only hubID is returned; the events API
// path never interpolates a team id.
//
// Every Events v1 route requires a contact identity (see the package doc
// comment); a team API key cannot satisfy it. So unlike every other
// eventsContext-shaped helper in this CLI, this one does NOT call
// c.requireAuth() (which only checks for a team API key) — it has its own
// three-way gate: a configured contact token wins, --anonymous is honoured
// as a deliberate unauthenticated probe (mirroring requireAuth's own
// precedent, MIO-2694), and anything else (an API key alone, or nothing at
// all) fails fast with an actionable error instead of round-tripping to a
// guaranteed 401.
func eventsContext(cmd *cobra.Command) (*cmdContext, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", err
	}

	contactToken := strings.TrimSpace(os.Getenv(eventsContactTokenEnv))
	switch {
	case contactToken != "":
		c.client = client.New(c.resolved.APIBase, contactToken, client.WithDebug(flags.debug))
	case c.resolved.Anonymous:
		// Deliberate unauthenticated probe — let it through so the caller can
		// observe the API's own answer, exactly like requireAuth's ordering
		// contract for every other resource (MIO-2694).
	default:
		// Do NOT tell the user to "run `mio login`" here — that command mints
		// and stores only a team API key (see the package doc comment above),
		// never a contact token, so that advice would be actively misleading.
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
	return c, hubID, nil
}

// eventsAttrFlags is the set of create/update flags, shared so both commands
// stay in lockstep. attrKey (see flags.go) translates each kebab-case flag
// name to its snake_case attribute key (e.g. --starts-at -> starts_at).
var eventsAttrFlags = []string{
	"title", "starts-at", "ends-at", "timezone", "location-type",
	"description", "cover-image-url", "location-url", "location-address",
	"visibility", "segment-id", "rsvp-tag-id",
}

// setEventAttrs copies every changed create/update flag into attrs, applying
// the correct getter per field. Only flags the user actually set are copied
// (setStringFlag/setIntFlag/setBoolFlag all no-op on an unset flag), so a
// PATCH stays a partial update and no field is ever sent as an explicit null.
func setEventAttrs(cmd *cobra.Command, attrs map[string]any) {
	for _, f := range eventsAttrFlags {
		setStringFlag(cmd, attrs, f)
	}
	setIntFlag(cmd, attrs, "capacity")
	setBoolFlag(cmd, attrs, "attendee-list-visible")
}

// ---- create -------------------------------------------------------------------

var eventsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a hub event.",
	Long: `Create a new event in the active hub.

--title, --starts-at, --ends-at, --timezone, and --location-type are required.

--starts-at and --ends-at accept an RFC3339 datetime string (e.g.
2026-09-01T18:00:00Z), passed through to the API as-is.

Allowed values for --location-type: url, address.
Allowed values for --visibility:     all_members, segment (requires --segment-id).

Requires hub owner/admin/moderator permissions.`,
	Example: `  mio events create --hub hub_123 --title "Community Meetup" \
    --starts-at 2026-09-01T18:00:00Z --ends-at 2026-09-01T20:00:00Z \
    --timezone America/New_York --location-type url --location-url https://zoom.us/j/123

  mio events create --hub hub_123 --title "Members-Only Session" \
    --starts-at 2026-09-01T18:00:00Z --ends-at 2026-09-01T20:00:00Z \
    --timezone America/New_York --location-type address --location-address "123 Main St" \
    --visibility segment --segment-id seg_abc123 --capacity 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		// --title, --starts-at, --ends-at, --timezone, and --location-type are
		// required by the backend HubEventCreateAttributes schema; validate
		// client-side so a partial-required body never reaches the API.
		var missing []string
		for _, f := range []string{"title", "starts-at", "ends-at", "timezone", "location-type"} {
			if !cmd.Flags().Changed(f) {
				missing = append(missing, "--"+f)
			}
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flag(s): %s", strings.Join(missing, ", "))
		}

		attrs := map[string]any{}
		setEventAttrs(cmd, attrs)

		res, err := c.client.Create(c.ctx, eventsPath(hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- list -------------------------------------------------------------------

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

		// Validate --status/--sort client-side: the backend silently mistreats
		// unrecognised values (Codex round 1, MIO-3173) rather than rejecting
		// them, so a typo would otherwise return an unexpectedly-unfiltered/
		// unsorted result instead of a clear error.
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

// ---- retrieve ---------------------------------------------------------------

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

// ---- update -----------------------------------------------------------------

var eventsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an event by id.",
	Long: `Partially update an event. Only the flags you supply are changed (PATCH
semantics) — an unset flag is never sent, so it can never be null'd out by
accident.

Allowed values for --location-type (when provided): url, address.
Allowed values for --visibility (when provided):     all_members, segment.

Requires hub owner/admin/moderator permissions.`,
	Example: `  mio events update evt_abc123 --hub hub_123 --title "New Title"
  mio events update evt_abc123 --hub hub_123 --capacity 100 --visibility all_members`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setEventAttrs(cmd, attrs)

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, eventsPath(hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- cancel -------------------------------------------------------------------

var eventsCancelCmd = &cobra.Command{
	Use:   "cancel <id>",
	Short: "Cancel an event.",
	Long: `Cancel an event. This is an action verb (not a delete) — the event record is
retained with a cancelled status.

Requires hub owner/admin/moderator permissions. Pass --yes to skip the
confirmation prompt in non-interactive environments (scripts, CI, AI agents).`,
	Example: `  mio events cancel evt_abc123 --hub hub_123
  mio events cancel evt_abc123 --hub hub_123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Cancel event %s?", args[0])); err != nil {
			return err
		}

		path := eventsPath(hubID, args[0]) + "/cancel"
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Cancelled event %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- events rsvp sub-resource (the caller's own RSVP) ------------------------

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

--status is required: going or not_going.

Acts as the authenticated member contact (login-based auth); does not require
hub owner/admin/moderator permissions.`,
	Example: `  mio events rsvp set evt_abc123 --hub hub_123 --status going
  mio events rsvp set evt_abc123 --hub hub_123 --status not_going`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := eventsContext(cmd)
		if err != nil {
			return err
		}

		if !cmd.Flags().Changed("status") {
			return errs.New(errs.ExitUsage, "missing required flag: --status")
		}
		status, ferr := cmd.Flags().GetString("status")
		if ferr != nil {
			return errs.Wrap(errs.ExitGeneric, ferr)
		}
		switch status {
		case "going", "not_going":
		default:
			return errs.New(errs.ExitUsage, "--status must be one of: going, not_going (got %q)", status)
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

		// The API returns 200 with the withdrawn RSVP resource in the body (NOT
		// 204 No Content), so this MUST go through client.Action (which decodes
		// and renders a resource body) rather than client.Delete (which discards
		// any response body unconditionally).
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

// ---- events rsvps sub-resource (read all RSVPs on an event) -----------------

var eventsRSVPsCmd = &cobra.Command{
	Use:   "rsvps",
	Short: "Read RSVPs recorded for an event.",
	Long: `List RSVPs recorded for an event. Only "going" RSVPs are returned — a
withdrawn or "not_going" RSVP never appears here.

Hub owners/admins/moderators can always list. An ordinary active member can
list too when the host left the attendee list visible for this event
(--attendee-list-visible on create/update) — this is not an admin-only read.`,
}

var eventsRSVPsListCmd = &cobra.Command{
	Use:   "list <event_id>",
	Short: "List RSVPs for an event.",
	Long: `List the "going" RSVPs recorded for the given event. Withdrawn and
"not_going" RSVPs are never included in the response.

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

// ---- flag registration -------------------------------------------------------

func init() {
	// Attribute flags for events create/update.
	for _, cmd := range []*cobra.Command{eventsCreateCmd, eventsUpdateCmd} {
		cmd.Flags().String("title", "", "Event title.")
		cmd.Flags().String("starts-at", "", "Event start time, RFC3339 (e.g. 2026-09-01T18:00:00Z).")
		cmd.Flags().String("ends-at", "", "Event end time, RFC3339 (e.g. 2026-09-01T20:00:00Z).")
		cmd.Flags().String("timezone", "", "IANA timezone name (e.g. America/New_York).")
		cmd.Flags().String("location-type", "", "Location type: url or address.")
		cmd.Flags().String("description", "", "Event description.")
		cmd.Flags().String("cover-image-url", "", "Cover image URL.")
		cmd.Flags().String("location-url", "", "Location URL (when --location-type=url).")
		cmd.Flags().String("location-address", "", "Location address (when --location-type=address).")
		cmd.Flags().Int("capacity", 0, "Maximum attendee capacity.")
		cmd.Flags().String("visibility", "", "Visibility: all_members or segment.")
		cmd.Flags().String("segment-id", "", "Segment id to scope visibility to (when --visibility=segment).")
		cmd.Flags().String("rsvp-tag-id", "", "Tag id to apply to contacts who RSVP.")
		cmd.Flags().Bool("attendee-list-visible", false, "Whether the attendee list is visible to other attendees.")
	}

	// Pagination + filter/sort on list.
	addPaginationFlags(eventsListCmd)
	eventsListCmd.Flags().String("status", "", "Filter by status: upcoming or past.")
	eventsListCmd.Flags().String("sort", "", "Sort order: starts_at or -starts_at.")

	// rsvp set flags.
	eventsRSVPSetCmd.Flags().String("status", "", "RSVP status: going or not_going. Required.")

	// Pagination on rsvps list.
	addPaginationFlags(eventsRSVPsListCmd)
}
