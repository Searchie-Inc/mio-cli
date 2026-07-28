package cmd

// checkout_onboarding_link_test.go — contract test for MIO-2717.
//
// mio-backend PR #578 (MIO-2655) makes POST …/payment-accounts/onboarding-link
// reject API-key principals (403, JWT-only): a leaked team API key must not be
// able to attach an attacker's Stripe payout account. The mio CLI authenticates
// exclusively via team API keys (see login.go), so `checkout accounts
// onboarding-link` can never satisfy that requirement and now fails fast,
// client-side, with a clear error instead of sending the request and
// surfacing a raw 403.
//
// CONTRACT: `checkout accounts onboarding-link` → ExitAuth (3), NO HTTP
// request sent, regardless of flags or auth/team context.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestCheckoutAccountsOnboardingLink_WebJWTOnlyErrorsFast verifies the command
// exits with ExitAuth and fires zero HTTP requests even when fully-formed
// flags and a valid API key / team context are supplied.
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

// TestCheckoutAccountsOnboardingLink_NoContextStillErrorsFast verifies the
// guard fires unconditionally — even with no flags, no API key, and no team
// context — since the operation can never succeed from this CLI regardless of
// auth state. This also confirms it never depends on requireAuth/requireTeam
// succeeding first.
func TestCheckoutAccountsOnboardingLink_NoContextStillErrorsFast(t *testing.T) {
	res := runContract(t, nil, "checkout", "accounts", "onboarding-link")

	if res.Code != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth); stderr=%q", res.Code, errs.ExitAuth, res.Stderr)
	}
}
