package cmd

// contactid.go — MIO-2504 shared helper for the contact-id namespace trap.
//
// Two server-side id namespaces exist and are indistinguishable by shape:
//
//   - the TEAM-contact id: routes bind {team_contact_id} (contacts,
//     contact-attributes, tags). `mio contacts` surfaces it as .id.
//   - the GLOBAL contact id: routes bind {contact_id} (hub-memberships,
//     activity, community members, email enrollments). The API sends it as
//     the contact_id attribute, which `mio contacts` flattens to a top-level
//     .contact_id (.data.attributes.contact_id only under --raw).
//
// Piping the .id from `mio contacts` into a member-shaped verb 404s for a
// perfectly live contact, because the verb looked up a GLOBAL contact id and
// found nothing. hintGlobalContactID makes that failure self-explaining.

import "github.com/Searchie-Inc/mio-cli/internal/errs"

// globalContactIDHint is the actionable redirect appended to a not-found error
// from a member-shaped verb. It names the field NOT to use (.id) and hands over
// a capture that works as written (MIO-3413: it used to name
// .attributes.contact_id, which yields null in every output mode, and a bare
// --jq JSON-quotes the string, so -o plain is part of the capture).
const globalContactIDHint = "this verb needs the GLOBAL contact id, NOT the .id from `mio contacts` (that is the team-contact id): capture it with `mio contacts retrieve <team-contact-id> -o plain --jq .contact_id`"

// hintGlobalContactID appends globalContactIDHint to a not-found (exit 4) error
// so the user is redirected to the correct id namespace. It keys off the exit
// code (errs.ExitNotFound), NOT a brittle server message string, so it survives
// the divergent backend 404 messages across these surfaces. The original exit
// code is preserved, so the exit-code contract is unchanged. Any other error
// (or nil) passes through untouched.
func hintGlobalContactID(err error) error {
	if err == nil || errs.CodeOf(err) != errs.ExitNotFound {
		return err
	}
	return errs.New(errs.ExitNotFound, "%s\nhint: %s", err.Error(), globalContactIDHint)
}
