package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestSetFlags_SnakeCaseKeys verifies that kebab-case flag names are written to
// the attributes map under snake_case keys (the JSON:API backend uses
// snake_case attribute names), while the user-facing flag name stays kebab.
func TestSetFlags_SnakeCaseKeys(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("user-id", "", "")
	cmd.Flags().String("first-name", "", "")
	cmd.Flags().Int("interval-count", 0, "")
	cmd.Flags().Bool("is-free-tier", false, "")
	cmd.Flags().String("name", "", "") // no hyphen — must remain unchanged

	if err := cmd.Flags().Set("user-id", "usr_123"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("first-name", "Ada"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("interval-count", "3"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("is-free-tier", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("name", "Pro"); err != nil {
		t.Fatal(err)
	}

	attrs := map[string]any{}
	setStringFlag(cmd, attrs, "user-id")
	setStringFlag(cmd, attrs, "first-name")
	setIntFlag(cmd, attrs, "interval-count")
	setBoolFlag(cmd, attrs, "is-free-tier")
	setStringFlag(cmd, attrs, "name")

	want := map[string]any{
		"user_id":        "usr_123",
		"first_name":     "Ada",
		"interval_count": 3,
		"is_free_tier":   true,
		"name":           "Pro",
	}
	for k, wantV := range want {
		gotV, ok := attrs[k]
		if !ok {
			t.Errorf("attrs missing snake_case key %q; got keys %v", k, keysOf(attrs))
			continue
		}
		if gotV != wantV {
			t.Errorf("attrs[%q] = %v, want %v", k, gotV, wantV)
		}
	}
	// Ensure no kebab-case keys leaked through.
	for _, kebab := range []string{"user-id", "first-name", "interval-count", "is-free-tier"} {
		if _, ok := attrs[kebab]; ok {
			t.Errorf("attrs unexpectedly contains kebab key %q", kebab)
		}
	}
}

// TestAttrKey is a focused unit check on the kebab→snake conversion.
func TestAttrKey(t *testing.T) {
	cases := map[string]string{
		"user-id":        "user_id",
		"first-name":     "first_name",
		"name":           "name",
		"interval-count": "interval_count",
		"a-b-c":          "a_b_c",
	}
	for in, want := range cases {
		if got := attrKey(in); got != want {
			t.Errorf("attrKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
