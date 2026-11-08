package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestPlainJQStream_OneBareValuePerLine_ThroughTheCLI(t *testing.T) {
	cases := []struct {
		name    string
		filter  string
		atLeast int
		exactly int
	}{
		{"many page templates", `.[] | select(.kind=="page") | .id`, 2, -1},
		{"exactly one", `.[] | select(.id=="page-login") | .id`, 1, 1},
		{"none", `.[] | select(.id=="no-such-template") | .id`, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jsonRes := runContract(t, offlineEnv(),
				"pages", "catalog", "templates", "--offline", "-o", "json", "--jq", "["+tc.filter+"]")
			if jsonRes.Code != errs.ExitOK {
				t.Fatalf("json oracle exit = %d; stderr=%q", jsonRes.Code, jsonRes.Stderr)
			}
			var want []string
			if err := json.Unmarshal([]byte(jsonRes.Stdout), &want); err != nil {
				t.Fatalf("json oracle did not parse as a string array: %v\n%s", err, jsonRes.Stdout)
			}
			if len(want) < tc.atLeast || (tc.exactly >= 0 && len(want) != tc.exactly) {
				t.Fatalf("the embedded catalog yields %d values for %s; this case needs at least %d (exactly %d) — pick another filter",
					len(want), tc.filter, tc.atLeast, tc.exactly)
			}

			res := runContract(t, offlineEnv(),
				"pages", "catalog", "templates", "--offline", "-o", "plain", "--jq", tc.filter)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			wantOut := ""
			for _, v := range want {
				wantOut += v + "\n"
			}
			if res.Stdout != wantOut {
				t.Errorf("-o plain --jq %s (%d values)\n got: %q\nwant: %q — one bare value per line, no value= prefix, no blank lines, nothing at all for zero values",
					tc.filter, len(want), res.Stdout, wantOut)
			}
			if strings.Contains(res.Stdout, "value=") {
				t.Errorf("a scalar stream printed value= records: %q", res.Stdout)
			}
		})
	}
}
