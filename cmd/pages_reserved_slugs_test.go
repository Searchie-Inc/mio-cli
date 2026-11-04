package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestPagesCreate_ReservedSlugConduit(t *testing.T) {
	fired := false
	const detail = "The page slug login is reserved by the API."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fired = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"status":"422","code":"page_slug_reserved","detail":"` + detail + `"}]}`))
	}))
	t.Cleanup(srv.Close)
	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--hub", "hub_123", "pages", "create", "--title", "Login", "--slug", "login")...)
	if !fired {
		t.Error("reserved slug login was not sent to the API")
	}
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want %d; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, detail) {
		t.Errorf("stderr = %q, want detail %q verbatim", res.Stderr, detail)
	}
}

func TestPagesCreate_ReservedSlugHelp(t *testing.T) {
	for _, verb := range []string{"create", "update"} {
		t.Run(verb, func(t *testing.T) {
			res := runContract(t, nil, "pages", verb, "--help")
			if res.Code != errs.ExitOK {
				t.Fatalf("help exit = %d: %s", res.Code, res.Stderr)
			}
			for _, slug := range catalog.ReservedPageSlugs() {
				if !strings.Contains(res.Stdout, slug) {
					t.Errorf("help missing reserved slug %q", slug)
				}
			}
			if !strings.Contains(res.Stdout, "page_slug_reserved") {
				t.Error("help missing page_slug_reserved")
			}
		})
	}
}
