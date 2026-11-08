package cmd

import (
	"github.com/spf13/cobra"
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
	res, err := runResume(t, baseEnv(srv.URL), withTeam("t_team1", "--hub", "hub_123", "pages", "create", "--title", "Login", "--slug", "login")...)
	if !fired {
		t.Error("reserved slug login was not sent to the API")
	}
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want %d; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if err == nil || !strings.Contains(err.Error(), detail) {
		t.Errorf("error = %v, want detail %q verbatim", err, detail)
	}
}

func TestPagesCreate_ReservedSlugHelp(t *testing.T) {
	for _, cmd := range []*cobra.Command{pagesCreateCmd, pagesUpdateCmd} {
		t.Run(cmd.Name(), func(t *testing.T) {
			f := cmd.Flags().Lookup("slug")
			if f == nil {
				t.Fatal("no --slug flag")
			}
			for _, slug := range catalog.ReservedPageSlugs() {
				if !strings.Contains(f.Usage, slug) {
					t.Errorf("--slug usage missing reserved slug %q: %s", slug, f.Usage)
				}
			}
			if !strings.Contains(f.Usage, "page_slug_reserved") {
				t.Errorf("--slug usage missing page_slug_reserved: %s", f.Usage)
			}
		})
	}
}
