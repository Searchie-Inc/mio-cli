package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func attrKey(flagName string) string {
	return strings.ReplaceAll(flagName, "-", "_")
}

func setStringFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetString(name); err == nil {
		attrs[attrKey(name)] = v
	}
}

func setNullableMappedString(cmd *cobra.Command, attrs map[string]any, name, attrName string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		return
	}
	if v == "" {
		attrs[attrName] = nil
	} else {
		attrs[attrName] = v
	}
}

func setIntFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetInt(name); err == nil {
		attrs[attrKey(name)] = v
	}
}

func setBoolFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetBool(name); err == nil {
		attrs[attrKey(name)] = v
	}
}

func flagValue(cmd *cobra.Command, name string) string {
	if cmd.Flags().Lookup(name) == nil {
		return ""
	}
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func stringArg(arg string) string {
	return strings.TrimSpace(arg)
}

func setMappedString(cmd *cobra.Command, attrs map[string]any, name, key string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetString(name); err == nil {
		attrs[key] = v
	}
}

func setMappedInt(cmd *cobra.Command, attrs map[string]any, name, key string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetInt(name); err == nil {
		attrs[key] = v
	}
}

func setMappedBool(cmd *cobra.Command, attrs map[string]any, name, key string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetBool(name); err == nil {
		attrs[key] = v
	}
}

func setMappedBoolInverted(cmd *cobra.Command, attrs map[string]any, name, key string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetBool(name); err == nil {
		attrs[key] = !v
	}
}

func changedString(cmd *cobra.Command, name string) *string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		return nil
	}
	return &v
}

func changedBool(cmd *cobra.Command, name string) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v, err := cmd.Flags().GetBool(name)
	if err != nil {
		return nil
	}
	return &v
}

func changedInt(cmd *cobra.Command, name string) *int {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v, err := cmd.Flags().GetInt(name)
	if err != nil {
		return nil
	}
	return &v
}

func parseJSONFlag(raw string) (any, error) {
	data := []byte(strings.TrimSpace(raw))
	if after, ok := strings.CutPrefix(string(data), "@"); ok {
		b, err := os.ReadFile(after)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", after, err)
		}
		data = b
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func setMappedJSONObjectFlag(cmd *cobra.Command, attrs map[string]any, name, key string) error {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	raw, err := cmd.Flags().GetString(name)
	if err != nil {
		return errs.New(errs.ExitUsage, "--%s: %s", name, err)
	}
	v, err := parseJSONFlag(raw)
	if err != nil {
		return errs.New(errs.ExitUsage, "--%s must be valid JSON: %s", name, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return errs.New(errs.ExitUsage, "--%s must be a JSON object", name)
	}
	attrs[key] = obj
	return nil
}

func addPaginationFlags(cmd *cobra.Command) {
	cmd.Flags().Int("limit", 0, "Page size (page[size]). Omit for the API's default, usually 20; the API caps it, usually at 100.")
	cmd.Flags().String("after", "", "Pagination cursor for the next page (page[after]). When more rows exist, a note on stderr prints it.")
}

func addPageFlags(cmd *cobra.Command, query url.Values) {
	if cmd.Flags().Changed("limit") {
		if limit, err := cmd.Flags().GetInt("limit"); err == nil && limit > 0 {
			query.Set("page[size]", itoa(limit))
		}
	}
	if cmd.Flags().Changed("after") {
		if after, err := cmd.Flags().GetString("after"); err == nil && after != "" {
			query.Set("page[after]", after)
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
