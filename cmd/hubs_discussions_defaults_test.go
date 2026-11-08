package cmd

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestHubsCreate_DiscussionsDefaults(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create", "--name", "H",
			"--discussions-default-title", "Welcome!",
			"--discussions-default-description", "Say hi to the community.",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	if attrs["discussions_default_title"] != "Welcome!" {
		t.Errorf("discussions_default_title = %v, want Welcome!", attrs["discussions_default_title"])
	}
	if attrs["discussions_default_description"] != "Say hi to the community." {
		t.Errorf("discussions_default_description = %v, want the description", attrs["discussions_default_description"])
	}
	if _, ok := attrs["discussions-default-title"]; ok {
		t.Errorf("kebab key discussions-default-title must not be present; attrs=%v", attrs)
	}
}

func TestHubsUpdate_DiscussionsDefaultTitle(t *testing.T) {
	srv, gotMethod, _, gotBody := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_x", "--discussions-default-title", "Community Discussions",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", *gotMethod)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	if attrs["discussions_default_title"] != "Community Discussions" {
		t.Errorf("discussions_default_title = %v, want Community Discussions", attrs["discussions_default_title"])
	}
}

func TestHubsUpdate_DiscussionsDefaultClear(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_x", "--discussions-default-title", "",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	v, ok := attrs["discussions_default_title"]
	if !ok {
		t.Fatalf("discussions_default_title must be present (as null) to clear; attrs=%v", attrs)
	}
	if v != nil {
		t.Errorf("discussions_default_title = %#v, want null (cleared)", v)
	}
}
