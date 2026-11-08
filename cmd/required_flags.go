package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func markFlagsRequired(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("markFlagsRequired(%q, %q): %v", cmd.Name(), name, err))
		}
	}
}
