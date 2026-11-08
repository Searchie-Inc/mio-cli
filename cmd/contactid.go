package cmd

import "github.com/Searchie-Inc/mio-cli/internal/errs"

const globalContactIDHint = "this verb needs the GLOBAL contact id, NOT the .id from `mio contacts` (that is the team-contact id): capture it with `mio contacts retrieve <team-contact-id> -o plain --jq .contact_id`"

func hintGlobalContactID(err error) error {
	if err == nil || errs.CodeOf(err) != errs.ExitNotFound {
		return err
	}
	return errs.New(errs.ExitNotFound, "%w\nhint: %s", err, globalContactIDHint)
}
