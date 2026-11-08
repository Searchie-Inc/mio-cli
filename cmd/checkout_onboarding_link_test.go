package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestCheckoutAccountsOnboardingLink_WebJWTOnlyErrorsFast(t *testing.T) {
	requestFired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestFired = true
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"checkout", "accounts", "onboarding-link",
			"--hub-id", "3fa85f64-5717-4562-b3fc-2c963f66afa6",
			"--return-url", "https://app.example.com/return",
			"--refresh-url", "https://app.example.com/refresh",
		)...)

	if res.Code != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth); stderr=%q", res.Code, errs.ExitAuth, res.Stderr)
	}
	if requestFired {
		t.Error("onboarding-link must NOT send an HTTP request — it always fails client-side (web/JWT-only, MIO-2655/MIO-2717)")
	}
}

func TestCheckoutAccountsOnboardingLink_NoContextStillErrorsFast(t *testing.T) {
	res := runContract(t, nil, "checkout", "accounts", "onboarding-link")

	if res.Code != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth); stderr=%q", res.Code, errs.ExitAuth, res.Stderr)
	}
}
