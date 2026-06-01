// Command mio is the Membership.io command-line interface.
//
// main is intentionally tiny: it runs the cobra command tree and translates the
// returned error into a process exit code via the errs package. All real logic
// lives under cmd/ and internal/. Errors are rendered to stderr as a JSON:API
// error envelope so agents get a parseable failure with a stable exit code.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/Searchie-Inc/mio-cli/cmd"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func main() {
	err := cmd.Execute()
	if err == nil {
		os.Exit(errs.ExitOK)
	}

	code := exitCodeFor(err)
	writeErrorEnvelope(err, code)
	os.Exit(code)
}

// exitCodeFor maps the error returned by Execute() to a process exit code.
//
// Every command's RunE returns a *errs.CLIError on every failure path (verified
// by the audit in round 4 and guarded by cmd/root_test.go), and the flag-parse
// hook (SetFlagErrorFunc) wraps flag errors in a *CLIError too. So any error that
// reaches here WITHOUT being a *CLIError is, by elimination, a Cobra usage error
// that Cobra returns directly from Execute() — required-flag-not-set, wrong arg
// count, or unknown command/subcommand. Those are usage errors → ExitUsage (2),
// not ExitGeneric (1). A *CLIError keeps its own carried code.
func exitCodeFor(err error) int {
	var ce *errs.CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	// Non-*CLIError from Execute() ⇒ Cobra usage error.
	return errs.ExitUsage
}

// writeErrorEnvelope prints the error to stderr as a JSON:API-style error
// document so agents can parse failures uniformly. The HTTP-ish status mirrors
// the exit code's class for convenience.
func writeErrorEnvelope(err error, code int) {
	doc := map[string]any{
		"errors": []map[string]any{
			{
				"status": exitToStatus(code),
				"detail": err.Error(),
				"meta":   map[string]any{"exit_code": code},
			},
		},
	}
	enc := json.NewEncoder(os.Stderr)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if encErr := enc.Encode(doc); encErr != nil {
		// Last-resort fallback if JSON encoding itself fails.
		fmt.Fprintln(os.Stderr, err.Error())
	}
}

// exitToStatus maps an exit code to an HTTP-ish status string for the error
// envelope's `status` member. Best-effort and informational only.
func exitToStatus(code int) string {
	switch code {
	case errs.ExitAuth:
		return "401"
	case errs.ExitNotFound:
		return "404"
	case errs.ExitNeedsConfir:
		return "412"
	case errs.ExitRateLimited:
		return "429"
	case errs.ExitServer:
		return "500"
	case errs.ExitUsage:
		return "400"
	default:
		return "500"
	}
}
