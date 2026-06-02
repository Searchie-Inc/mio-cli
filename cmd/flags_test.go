package cmd

import (
	"os"
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

// TestSetMappedFlags verifies that mapped helpers write to an EXPLICIT backend
// key (not the snake_cased flag name) only when the user set the flag.
func TestSetMappedFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("from-email", "", "")
	cmd.Flags().Int("mail-port", 0, "")
	cmd.Flags().String("reply-to", "", "") // left unset

	if err := cmd.Flags().Set("from-email", "hi@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("mail-port", "587"); err != nil {
		t.Fatal(err)
	}

	attrs := map[string]any{}
	setMappedString(cmd, attrs, "from-email", "mail_from_email")
	setMappedInt(cmd, attrs, "mail-port", "mail_port")
	setMappedString(cmd, attrs, "reply-to", "reply_to") // unset → omitted

	if attrs["mail_from_email"] != "hi@example.com" {
		t.Errorf("mail_from_email = %v, want hi@example.com", attrs["mail_from_email"])
	}
	if attrs["mail_port"] != 587 {
		t.Errorf("mail_port = %v, want 587", attrs["mail_port"])
	}
	if _, ok := attrs["reply_to"]; ok {
		t.Errorf("reply_to should be omitted when the flag is unset; got %v", attrs["reply_to"])
	}
	// The snake_cased flag name must NOT leak as a key.
	if _, ok := attrs["from_email"]; ok {
		t.Errorf("attrs unexpectedly contains the flag-derived key from_email")
	}
}

// TestParseJSONFlag verifies inline JSON and @file parsing for structured flags.
func TestParseJSONFlag(t *testing.T) {
	// Inline object.
	v, err := parseJSONFlag(`{"version":1,"groups":[]}`)
	if err != nil {
		t.Fatalf("inline parse error: %v", err)
	}
	obj, ok := v.(map[string]any)
	if !ok || obj["version"] != float64(1) {
		t.Errorf("inline parse = %#v, want object with version=1", v)
	}

	// Invalid JSON surfaces an error.
	if _, err := parseJSONFlag(`{not json`); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}

	// @file form.
	dir := t.TempDir()
	fp := dir + "/conds.json"
	if err := os.WriteFile(fp, []byte(`{"version":1,"groups":[{"logic":"AND","conditions":[]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fv, err := parseJSONFlag("@" + fp)
	if err != nil {
		t.Fatalf("@file parse error: %v", err)
	}
	fobj, ok := fv.(map[string]any)
	if !ok || fobj["version"] != float64(1) {
		t.Errorf("@file parse = %#v, want object with version=1", fv)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
