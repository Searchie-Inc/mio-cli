package cmd

// verifieddomains.go implements the `mio verified-domains` command group.
//
// Routes (team-owner admin surface, External Login v2 / verified-domain SSO):
//
//	create   POST   /api/teams/{team_id}/verified-domains
//	list     GET    /api/teams/{team_id}/verified-domains
//	retrieve GET    /api/teams/{team_id}/verified-domains/{id}
//	delete   DELETE /api/teams/{team_id}/verified-domains/{id}
//	verify   POST   /api/teams/{team_id}/verified-domains/{id}/verify
//
// JSON:API resource type: "verified_domains"
//
// A verified domain lets a hub trust users who authenticate with an email on
// that domain. Creating one returns a verification_token and the DNS TXT record
// (host + value) the admin must publish; the verify action then triggers the
// backend DNS-TXT check (200 verified / 422 not-yet / 409 if another hub already
// owns the domain).

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	verifiedDomainsCmd.AddCommand(
		verifiedDomainsCreateCmd,
		verifiedDomainsListCmd,
		verifiedDomainsRetrieveCmd,
		verifiedDomainsDeleteCmd,
		verifiedDomainsVerifyCmd,
	)
	rootCmd.AddCommand(verifiedDomainsCmd)
}

// ── path helper ───────────────────────────────────────────────────────────────

// verifiedDomainsPath returns /api/teams/{team_id}/verified-domains[/{id}].
func verifiedDomainsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/verified-domains", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ── group ─────────────────────────────────────────────────────────────────────

var verifiedDomainsCmd = &cobra.Command{
	Use:   "verified-domains",
	Short: "Manage verified domains for the active team.",
	Long: `Create, list, retrieve, verify, and delete verified domains for the
active team.

A verified domain lets a hub trust members who sign in with an email address on
that domain (External Login v2 / verified-domain SSO). Creating a domain returns
a verification_token and a DNS TXT record (host + value) to publish; once the
record is live, run 'mio verified-domains verify <id>' to trigger the backend
DNS check and activate the domain.`,
}

// ── create ────────────────────────────────────────────────────────────────────

var verifiedDomainsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create (claim) a verified domain for a hub.",
	Long: `Claim a domain for a hub. The backend returns a verification_token and the
DNS TXT record (host + value) you must publish to prove ownership.

After publishing the TXT record at your DNS provider, run
'mio verified-domains verify <id>' to trigger the DNS check and activate the
domain.`,
	Example: `  mio verified-domains create --hub-id hub_abc123 --domain example.com`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "hub-id")
		setStringFlag(cmd, attrs, "domain")

		// --hub-id and --domain are required client-side.
		if v, _ := attrs["hub_id"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--hub-id is required")
		}
		if v, _ := attrs["domain"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--domain is required")
		}

		res, err := c.client.Create(c.ctx, verifiedDomainsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}

		// Surface the verification token + DNS TXT record on STDERR (TTY only) so
		// the admin knows exactly what to publish, while stdout stays machine-clean
		// (the rendered resource is the sole stdout payload).
		printVerifiedDomainSetupHint(cmd, res)

		return c.render(cmd, res)
	},
}

// ── list ──────────────────────────────────────────────────────────────────────

var verifiedDomainsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List verified domains for the active team.",
	Long:  "List all verified domains belonging to the active team.",
	Example: `  mio verified-domains list
  mio verified-domains list --limit=20
  mio verified-domains list --after=<cursor>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, verifiedDomainsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ── retrieve ──────────────────────────────────────────────────────────────────

var verifiedDomainsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a verified domain by id.",
	Long:    "Retrieve a single verified domain, including its verification status and DNS TXT record.",
	Example: `  mio verified-domains retrieve vd_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, verifiedDomainsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ── delete ────────────────────────────────────────────────────────────────────

var verifiedDomainsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a verified domain by id.",
	Long: `Permanently delete (release) a verified domain. Members will no longer be
trusted by virtue of an email on this domain, and the domain becomes available
for another hub to claim.

This action is irreversible. Pass --yes to skip the confirmation prompt in
non-interactive environments.`,
	Example: `  # Interactive — prompts for confirmation
  mio verified-domains delete vd_abc123

  # Non-interactive (CI) — skip the prompt
  mio verified-domains delete vd_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete verified domain %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, verifiedDomainsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted verified domain %s.\n", args[0])
		return nil
	},
}

// ── verify ────────────────────────────────────────────────────────────────────

var verifiedDomainsVerifyCmd = &cobra.Command{
	Use:   "verify <id>",
	Short: "Trigger the DNS-TXT ownership check for a verified domain.",
	Long: `Trigger the backend DNS-TXT ownership check for a verified domain. Publish
the TXT record returned by 'create' (or shown by 'retrieve') BEFORE running this.

On success the domain becomes verified (HTTP 200) and the updated row is
returned. If the TXT record is not yet visible the backend returns 422, and if
another hub already owns the domain it returns 409 — both surface as a usage
error (exit 2).`,
	Example: `  mio verified-domains verify vd_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		path := verifiedDomainsPath(teamID, args[0]) + "/verify"
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Verification triggered for domain %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ── flag registration ─────────────────────────────────────────────────────────

func init() {
	// create flags
	verifiedDomainsCreateCmd.Flags().String("hub-id", "", "Hub id to claim the domain for. (required)")
	verifiedDomainsCreateCmd.Flags().String("domain", "", "Domain to verify, e.g. example.com. (required)")

	// list pagination
	addPaginationFlags(verifiedDomainsListCmd)
}

// ── local helpers ─────────────────────────────────────────────────────────────

// printVerifiedDomainSetupHint writes a TTY-only stderr hint after a successful
// create, surfacing the verification token and the DNS TXT record (host + value)
// the admin must publish. It is best-effort: only fields actually present in the
// resource are printed, so it stays correct even if the backend renames a field.
// Nothing is written when stderr is not a TTY, keeping stdout (the rendered
// resource) the sole machine-readable channel.
func printVerifiedDomainSetupHint(cmd *cobra.Command, res *client.Resource) {
	if res == nil || !isTTY(cmd.ErrOrStderr()) {
		return
	}
	attrs := res.Attributes
	if attrs == nil {
		return
	}

	w := cmd.ErrOrStderr()
	fmt.Fprintln(w, "Domain claimed. Publish this DNS TXT record to prove ownership:")
	if tok := stringAttr(attrs, "verification_token"); tok != "" {
		fmt.Fprintf(w, "  verification token: %s\n", tok)
	}
	// Print any TXT-record / DNS host + value fields the backend returned, so the
	// admin sees the exact record regardless of the precise attribute names.
	for _, k := range []string{
		"txt_record_host", "txt_record_name", "dns_record_host", "dns_record_name",
		"txt_record_value", "dns_record_value", "txt_record", "dns_txt_record",
	} {
		if v := stringAttr(attrs, k); v != "" {
			fmt.Fprintf(w, "  %s: %s\n", k, v)
		}
	}
	fmt.Fprintf(w, "Then run: mio verified-domains verify %s\n", res.ID)
}

// stringAttr returns attrs[key] as a trimmed string when it is a non-empty
// string, else "".
func stringAttr(attrs map[string]any, key string) string {
	if v, ok := attrs[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
