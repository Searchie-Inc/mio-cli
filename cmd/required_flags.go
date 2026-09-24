package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// markFlagsRequired declares flags a command cannot run without — to COBRA.
//
// Declare a required flag here, never as a `!cmd.Flags().Changed(...)` check
// inside RunE. cobra then refuses the command before RunE runs (exit 2,
// ExitUsage, before any credential lookup or request), and — the reason this
// helper exists — the requirement becomes visible to the doc-example guard
// (doc_examples_test.go), which resolves every documented `mio …` invocation
// against cobra. A requirement enforced only inside RunE is one no example can
// be checked against: README documented `products create` without --type for
// exactly that reason (MIO-4154).
//
// A requirement that is CONDITIONAL on another flag's value (--interval when
// --type=recurring) is not expressible here. The CLI does not check that one
// at all: `products prices create` sends the price as given, and the API
// rejects a recurring price without interval and interval_count (422, "recurring
// prices require both interval and interval_count"; exit 2). The doc-example
// guard cannot see that requirement either.
//
// Older RunE checks still exist: MIO-4154 moved the `missing required flag(s)`
// ones, except `hubs policies gate --enabled` (kept for its usage hint), and the
// `--x is required[: hint]` ones remain (the grep that lists them is in
// doc_examples_test.go, under WHAT IT DOES NOT SEE). Do not copy them.
//
// It panics on a name the command does not define, so a typo fails at init
// rather than silently leaving the flag optional.
func markFlagsRequired(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("markFlagsRequired(%q, %q): %v", cmd.Name(), name, err))
		}
	}
}
