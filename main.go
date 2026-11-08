package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	writeErrorEnvelope(err, code, cmd.RawRequested())
	os.Exit(code)
}

func exitCodeFor(err error) int {
	var ce *errs.CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return errs.ExitUsage
}

func writeErrorEnvelope(err error, code int, raw bool) {
	if term.IsTerminal(int(os.Stderr.Fd())) {
		writeFriendlyError(err, code)
		return
	}
	writeJSONErrorEnvelope(os.Stderr, err, code, raw)
}

func writeFriendlyError(err error, code int) {
	const (
		dim   = "\x1b[2m"
		reset = "\x1b[0m"
	)
	fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
	fmt.Fprintf(os.Stderr, "%s(exit code %d)%s\n", dim, code, reset)
}

func writeJSONErrorEnvelope(w io.Writer, err error, code int, raw bool) {
	if apiDoc := errs.APIErrorDocumentOf(err); apiDoc != nil {
		if out, ok := apiErrorEnvelope(apiDoc, statusForEnvelope(err, code), err.Error(), code, raw); ok {
			_, _ = w.Write(out)
			return
		}
	}
	doc := map[string]any{
		"errors": []map[string]any{
			{
				"status": statusForEnvelope(err, code),
				"detail": err.Error(),
				"meta":   map[string]any{"exit_code": code},
			},
		},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if encErr := enc.Encode(doc); encErr != nil {
		fmt.Fprintln(w, err.Error())
	}
}

func statusForEnvelope(err error, code int) string {
	if status := errs.HTTPStatusOf(err); status > 0 {
		return strconv.Itoa(status)
	}
	return exitToStatus(code)
}

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
