package cmd

// products_prices_trial_test.go — MIO-4159: `mio products prices create`
// carries --trial-period-days to the wire, and `prices update` deliberately
// does not have it.
//
// Backend contract (mio-backend origin/main, app/products/schemas.py):
//
//	PriceCreateAttributes.trial_period_days: int | None = Field(default=None, ge=0, le=2147483647)
//
// with no type-dependent rule, and PriceUpdateAttributes (extra="forbid")
// omits it: it is one of the six fields the service treats as immutable
// (_PRICE_IMMUTABLE_FIELDS). The CLI is a conduit, so the oracle here is the
// exact request body: the key is present, as a JSON number, exactly when the
// flag was given, and the CLI adds no bound or type rule of its own.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const priceTrialResponseBody = `{"data":{"id":"price_1","type":"prices","attributes":{"product_id":"prod_1","amount":999,"currency":"usd","type":"recurring","trial_period_days":7}}}`

type recordedPriceRequest struct {
	Method string
	Path   string
	Body   []byte
}

// newPriceRecordingServer records every request (method, path, body) and
// answers each with the given status and a canned price document.
func newPriceRecordingServer(t *testing.T, status int) (*httptest.Server, *[]recordedPriceRequest) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []recordedPriceRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, recordedPriceRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(priceTrialResponseBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// TestPricesCreate_TrialPeriodDays_ExactBody pins the whole request body of
// `prices create` with and without --trial-period-days.
//
//   - set → "trial_period_days" is present as a JSON number (a string "7"
//     would not deep-equal the number 7 in assertExactBody);
//   - unset → the key is ABSENT, not 0 or null: the backend default is None,
//     and a stored 0 is a different row;
//   - 0 → sent as 0. The backend accepts 0 (ge=0), so a CLI that dropped it, or
//     tested `v > 0` instead of Changed(), would silently diverge from the API;
//   - -1 → sent. Rejecting it is the API's call (ge=0 → 422), not the CLI's;
//   - one_time → sent. The API has no type-dependent rule for the field.
func TestPricesCreate_TrialPeriodDays_ExactBody(t *testing.T) {
	recurring := []string{"--amount", "999", "--currency", "usd", "--type", "recurring", "--interval", "month", "--interval-count", "1"}

	cases := []struct {
		name  string
		args  []string
		attrs string // the complete "attributes" object expected on the wire
	}{
		{
			name:  "set on a recurring price",
			args:  append(append([]string{}, recurring...), "--trial-period-days", "7"),
			attrs: `{"amount":999,"currency":"usd","type":"recurring","interval":"month","interval_count":1,"trial_period_days":7}`,
		},
		{
			name:  "unset is absent",
			args:  recurring,
			attrs: `{"amount":999,"currency":"usd","type":"recurring","interval":"month","interval_count":1}`,
		},
		{
			name:  "zero is sent, not dropped",
			args:  append(append([]string{}, recurring...), "--trial-period-days", "0"),
			attrs: `{"amount":999,"currency":"usd","type":"recurring","interval":"month","interval_count":1,"trial_period_days":0}`,
		},
		{
			name:  "negative is the API's to reject",
			args:  append(append([]string{}, recurring...), "--trial-period-days", "-1"),
			attrs: `{"amount":999,"currency":"usd","type":"recurring","interval":"month","interval_count":1,"trial_period_days":-1}`,
		},
		{
			name:  "one_time is not blocked client-side",
			args:  []string{"--amount", "4999", "--currency", "usd", "--type", "one_time", "--trial-period-days", "7"},
			attrs: `{"amount":4999,"currency":"usd","type":"one_time","trial_period_days":7}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := newPriceRecordingServer(t, http.StatusCreated)

			args := append([]string{"products", "prices", "create", "prod_1"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
			}
			if len(*got) != 1 {
				t.Fatalf("server saw %d requests, want exactly 1: %+v", len(*got), *got)
			}
			req := (*got)[0]
			if req.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", req.Method)
			}
			if want := "/api/v1/teams/t_team1/products/prod_1/prices"; strings.TrimSuffix(req.Path, "/") != want {
				t.Errorf("path = %s, want %s", req.Path, want)
			}
			assertExactBody(t, req.Body, `{"data":{"type":"prices","attributes":`+tc.attrs+`}}`)
		})
	}
}

// TestPricesUpdate_HasNoTrialPeriodDays pins the triage decision on MIO-4159:
// trial length is immutable server-side (PriceUpdateAttributes forbids it and
// the service lists it in _PRICE_IMMUTABLE_FIELDS), so `prices update` must not
// offer a flag that can only ever produce a 422. The error must NAME the flag —
// codeForExecuteErr maps every non-CLIError to ExitUsage, so an exit-code-only
// check would pass on any usage failure.
func TestPricesUpdate_HasNoTrialPeriodDays(t *testing.T) {
	srv, got := newPriceRecordingServer(t, http.StatusOK)

	err := executeCLI(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "prices", "update", "prod_1", "price_1",
			"--trial-period-days", "14")...)
	if err == nil {
		t.Fatal("prices update --trial-period-days succeeded; want an unknown-flag usage error")
	}
	if code := codeForExecuteErr(err); code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", code, errs.ExitUsage)
	}
	if !strings.Contains(err.Error(), "unknown flag: --trial-period-days") {
		t.Errorf("error = %q, want it to name the unknown flag --trial-period-days", err.Error())
	}
	if len(*got) != 0 {
		t.Errorf("server saw %d request(s), want 0: %+v", len(*got), *got)
	}
}
