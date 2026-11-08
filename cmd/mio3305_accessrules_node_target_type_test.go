package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestContract_AccessRulesRulesCreateHelp_MentionsNodeTargetType(t *testing.T) {
	out := strings.ToLower(helpOutput(t, "access-rules", "rules", "create"))
	if !strings.Contains(out, "page-builder") {
		t.Errorf("access-rules rules create --help must document \"node\" as the page-builder tree-node gate --target-type value; got:\n%s", out)
	}
	if !strings.Contains(out, ", node") && !strings.Contains(out, "or node") {
		t.Errorf("access-rules rules create --help must list \"node\" as its own --target-type value (not just as a substring of \"content_node\"); got:\n%s", out)
	}
}

func TestWritePath_AccessRulesRulesCreate_TargetTypeNode_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalAccessRuleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"access-rules", "rules", "create",
			"--target-type", "node",
			"--target-id", "node_123",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "access_rules",
			"attributes": {
				"target_type": "node",
				"target_id": "node_123"
			}
		}
	}`)
}
