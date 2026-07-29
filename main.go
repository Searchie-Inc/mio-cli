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
	"strconv"

	"golang.org/x/term"

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

// writeErrorEnvelope renders the error to stderr. The rendering is TTY-aware:
//
//   - When stderr is a real terminal (a human is watching), it prints a
//     friendly one-line `Error: <detail>` followed by a dimmed `(exit code N)`,
//     so interactive use isn't a wall of JSON.
//   - When stderr is NOT a terminal (piped to a file, another process, or an
//     agent), it emits the EXACT JSON:API error envelope. This shape is a
//     machine contract that agents and CI parse — it must never change.
func writeErrorEnvelope(err error, code int) {
	if term.IsTerminal(int(os.Stderr.Fd())) {
		writeFriendlyError(err, code)
		return
	}
	writeJSONErrorEnvelope(err, code)
}

// writeFriendlyError prints a human-readable, single-line error for interactive
// terminals. The exit-code hint is dimmed with ANSI so it reads as secondary.
func writeFriendlyError(err error, code int) {
	const (
		dim   = "\x1b[2m"
		reset = "\x1b[0m"
	)
	fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
	fmt.Fprintf(os.Stderr, "%s(exit code %d)%s\n", dim, code, reset)
}

// writeJSONErrorEnvelope prints the error to stderr as a JSON:API-style error
// document so agents can parse failures uniformly. This is the non-TTY contract
// path: the KEYS (`errors[].status`, `errors[].detail`, `errors[].meta.exit_code`)
// must never change.
func writeJSONErrorEnvelope(err error, code int) {
	doc := map[string]any{
		"errors": []map[string]any{
			{
				"status": statusForEnvelope(err, code),
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

// statusForEnvelope resolves the value of the envelope's `status` member.
//
// When the failure came from the API, the client recorded the status the API
// actually answered with (see client.errorForResponse) and we report THAT
// verbatim. Previously this member was reconstructed from the exit code, which
// is lossy by construction because errs.ExitCodeForStatus is many-to-one:
// 401/403 both become ExitAuth and 400/409/422 all become ExitUsage. The CLI
// consequently told agents "401" when the API had said 403, and "400" when it
// had said 409 or 422 — the three mis-mappings MIO-2656 reports. Exit codes
// stay deliberately coarse (that contract is unchanged); `status` becomes exact.
//
// When there is NO HTTP status — a genuinely local failure such as a bad flag,
// an unreadable file, or no API key at all — we keep the historical
// exit-code-derived value rather than omitting or zeroing the member. That is
// both backward compatible and defensible on its own terms: "no API key found"
// really is a 401-class condition, and a rejected flag really is a 400-class
// one. Inventing a more specific status for a failure that never reached the
// network would be worse than the honest approximation.
func statusForEnvelope(err error, code int) string {
	if status := errs.HTTPStatusOf(err); status > 0 {
		return strconv.Itoa(status)
	}
	return exitToStatus(code)
}

// exitToStatus maps an exit code to an HTTP-ish status string for the error
// envelope's `status` member. This is the FALLBACK for local errors that never
// produced an HTTP response; API failures report their real status via
// statusForEnvelope. Best-effort and informational only.
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
