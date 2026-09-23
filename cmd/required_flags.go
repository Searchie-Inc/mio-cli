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
// --type=recurring) is not expressible here and stays in RunE.
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
