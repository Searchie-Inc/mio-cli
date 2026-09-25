package cmd

// auth_password_test.go — `mio auth forgot-password` / `mio auth
// reset-password` (MIO-4286). Every wire assertion here reads what the
// httptest server RECEIVED (method, path, body, headers), never a second copy
// of the expected request.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// authPasswordCapture is what the mock backend saw on the one request the
// command is expected to send.
type authPasswordCapture struct {
	Method, Path, Authorization, ContentType string
	Body                                     map[string]any
	Count                                    int
}

// authPasswordServer answers every request with status and body, recording
// the request. A 429 carries Retry-After: 0 so the client's retries do not
// sleep.
func authPasswordServer(t *testing.T, status int, body string) (*httptest.Server, *authPasswordCapture) {
	t.Helper()
	cap := &authPasswordCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.Count++
		cap.Method = r.Method
		cap.Path = r.URL.Path
		cap.Authorization = r.Header.Get("Authorization")
		cap.ContentType = r.Header.Get("Content-Type")
		cap.Body = nil
		_ = json.NewDecoder(r.Body).Decode(&cap.Body)
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "0")
		}
		if body != "" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// runAuthPassword drives the command tree like runContract but also returns
// the error itself: the hints these commands add live in the error's message,
// which main.go renders after os.Exit, out of runContract's reach. It pins a
// STALE stored/exported key in the environment so every test also proves the
// request goes out without it.
func runAuthPassword(t *testing.T, srvURL string, extraEnv []string, args ...string) (contractResult, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	env := append([]string{
		"MIO_API_BASE_URL=" + srvURL,
		"MIO_API_KEY=mio_sk_stale_revoked_key",
		"MIO_PASSWORD=",
	}, extraEnv...)
	restore := overlayEnv(t, env)
	defer restore()
	resetGlobalFlags()

	root := RootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	// stdin is a PIPE with content, never a terminal: a command that decides
	// to prompt anyway would read these lines and send them, which is what
	// the off-a-terminal tests must be able to see. (With an empty stdin the
	// prompt would read EOF and exit 2 on its own, and a missing TTY guard
	// would pass unnoticed.)
	root.SetIn(strings.NewReader("piped@test.member.dev\npiped-password\npiped-password\n"))
	defer root.SetIn(nil)
	root.SetArgs(args)
	defer root.SetArgs(nil)
	err := root.Execute()
	return contractResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: codeForExecuteErr(err)}, err
}

// ─── forgot-password ─────────────────────────────────────────────────────────

// TestAuthForgotPassword_PostsFlatBodyWithoutAKey pins the wire: one POST to
// /api/v1/auth/forgot, a flat plain-JSON {email} body (no JSON:API envelope,
// no other key), application/json, and NO Authorization header even though a
// key is exported — the route is unauthenticated and the caller may hold
// exactly the revoked key they are recovering from. The API's empty 202 is
// success, and stdout is the neutral message naming the next command.
func TestAuthForgotPassword_PostsFlatBodyWithoutAKey(t *testing.T) {
	srv, cap := authPasswordServer(t, http.StatusAccepted, "")

	res, _ := runAuthPassword(t, srv.URL, nil, "auth", "forgot-password", "--email", "lost@test.member.dev")

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if cap.Count != 1 {
		t.Fatalf("requests = %d, want exactly 1", cap.Count)
	}
	if cap.Method != http.MethodPost || cap.Path != "/api/v1/auth/forgot" {
		t.Errorf("request = %s %s, want POST /api/v1/auth/forgot", cap.Method, cap.Path)
	}
	if cap.Authorization != "" {
		t.Errorf("Authorization = %q, want none: forgot-password must not send the (possibly revoked) key", cap.Authorization)
	}
	if cap.ContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (the /api/auth/* family is plain JSON)", cap.ContentType)
	}
	if want := (map[string]any{"email": "lost@test.member.dev"}); !mapsEqualJSON(cap.Body, want) {
		t.Errorf("body = %#v, want exactly %#v", cap.Body, want)
	}
	if !strings.Contains(res.Stdout, "lost@test.member.dev") || !strings.Contains(res.Stdout, "If an account exists") {
		t.Errorf("stdout %q must be the neutral 'if an account exists' message naming the address", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "mio auth reset-password") {
		t.Errorf("stdout %q must name the next command", res.Stdout)
	}
}

// TestAuthForgotPassword_RequiresEmailOffATerminal pins exit 2 with NO request
// when --email is absent and there is no terminal to prompt on.
func TestAuthForgotPassword_RequiresEmailOffATerminal(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "forgot-password"},
		{"auth", "forgot-password", "--email", "   "},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			srv, cap := authPasswordServer(t, http.StatusAccepted, "")
			res, err := runAuthPassword(t, srv.URL, nil, args...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want 2; err=%v", res.Code, err)
			}
			if cap.Count != 0 {
				t.Errorf("%d request(s) fired; a usage error must fire none", cap.Count)
			}
		})
	}
}

// TestAuthForgotPassword_SurfacesTheAPIsVerdict pins the pass-throughs: a 429
// is exit 6 with the API's document kept, a 422 exit 2, and the two answers a
// user cannot act on from the status alone — a 404 from a backend without the
// route, and the route's by-design 500 — carry the hint that says what they
// mean. Each failure keeps the API's real status on the error.
func TestAuthForgotPassword_SurfacesTheAPIsVerdict(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int
		wantHint string
	}{
		{"429 rate limited", http.StatusTooManyRequests,
			`{"errors":[{"status":"429","title":"Too Many Requests","detail":"Too many requests. Try again later."}]}`,
			errs.ExitRateLimited, ""},
		{"422 not an email", http.StatusUnprocessableEntity,
			`{"errors":[{"status":"422","detail":"value is not a valid email address","source":{"pointer":"/email"}}]}`,
			errs.ExitUsage, ""},
		{"404 route absent", http.StatusNotFound, `{"detail":"Not Found"}`, errs.ExitNotFound, "MIO-3571"},
		{"405 route absent", http.StatusMethodNotAllowed, `{"detail":"Method Not Allowed"}`, errs.ExitGeneric, "MIO-3571"},
		{"500 not configured", http.StatusInternalServerError,
			`{"errors":[{"status":"500","title":"Internal Server Error","detail":"Internal server error"}]}`,
			errs.ExitServer, "not configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := authPasswordServer(t, tc.status, tc.body)
			res, err := runAuthPassword(t, srv.URL, nil, "auth", "forgot-password", "--email", "lost@test.member.dev")
			if res.Code != tc.wantCode {
				t.Errorf("exit code = %d, want %d; err=%v", res.Code, tc.wantCode, err)
			}
			if got := errs.HTTPStatusOf(err); got != tc.status {
				t.Errorf("HTTPStatusOf = %d, want %d (the envelope's status must be the API's)", got, tc.status)
			}
			if tc.wantHint != "" && !strings.Contains(err.Error(), tc.wantHint) {
				t.Errorf("error %q must carry the hint mentioning %q", err.Error(), tc.wantHint)
			}
			if strings.HasPrefix(tc.body, `{"errors"`) && errs.APIErrorDocumentOf(err) == nil {
				t.Errorf("the API's error document was dropped from the error chain (wrap with %%w)")
			}
			if cap.Authorization != "" {
				t.Errorf("Authorization = %q, want none", cap.Authorization)
			}
			if res.Stdout != "" {
				t.Errorf("a failure must print nothing on stdout; got %q", res.Stdout)
			}
		})
	}
}

// ─── reset-password ──────────────────────────────────────────────────────────

// TestAuthResetPassword_PostsTokenAndPasswordFromEnvWithoutAKey pins the wire:
// one POST to /api/v1/auth/reset with a flat {token, password} body, the
// password taken from MIO_PASSWORD (never argv), application/json, and no
// Authorization header. The API's empty 204 is success; stdout says nothing
// was minted and names `mio login` as the next step.
func TestAuthResetPassword_PostsTokenAndPasswordFromEnvWithoutAKey(t *testing.T) {
	srv, cap := authPasswordServer(t, http.StatusNoContent, "")

	res, _ := runAuthPassword(t, srv.URL, []string{"MIO_PASSWORD=a-new-password"},
		"auth", "reset-password", "--token", "tok_abc123")

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if cap.Count != 1 {
		t.Fatalf("requests = %d, want exactly 1", cap.Count)
	}
	if cap.Method != http.MethodPost || cap.Path != "/api/v1/auth/reset" {
		t.Errorf("request = %s %s, want POST /api/v1/auth/reset", cap.Method, cap.Path)
	}
	if cap.Authorization != "" {
		t.Errorf("Authorization = %q, want none", cap.Authorization)
	}
	if cap.ContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", cap.ContentType)
	}
	if want := (map[string]any{"token": "tok_abc123", "password": "a-new-password"}); !mapsEqualJSON(cap.Body, want) {
		t.Errorf("body = %#v, want exactly %#v", cap.Body, want)
	}
	if !strings.Contains(res.Stdout, "Password reset") || !strings.Contains(res.Stdout, "Next: mio login") {
		t.Errorf("stdout %q must confirm the reset and name `mio login` as the next step", res.Stdout)
	}
}

// TestAuthResetPassword_TakesTheWholeLink pins that --token accepts the
// emailed link and sends only its fragment: the token after `#`, with a query
// string before it left alone and surrounding whitespace dropped.
func TestAuthResetPassword_TakesTheWholeLink(t *testing.T) {
	for _, tc := range []struct{ arg, want string }{
		{"https://admin.example.com/reset-password#tok_abc123", "tok_abc123"},
		{"http://localhost:3000/reset-password?utm=x#tok_with-dash_and.dot", "tok_with-dash_and.dot"},
		{"  tok_bare  \n", "tok_bare"},
		{"#tok_only_fragment", "tok_only_fragment"},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			srv, cap := authPasswordServer(t, http.StatusNoContent, "")
			res, err := runAuthPassword(t, srv.URL, []string{"MIO_PASSWORD=a-new-password"},
				"auth", "reset-password", "--token", tc.arg)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit code = %d, want 0; err=%v", res.Code, err)
			}
			if got := cap.Body["token"]; got != tc.want {
				t.Errorf("sent token = %#v, want %q", got, tc.want)
			}
		})
	}
}

// TestAuthResetPassword_RejectsAnUnusableTokenBeforeAnyRequest pins exit 2
// with no request for a missing token, a link whose fragment was stripped,
// and a link with an empty fragment: sending those would earn the same 410 a
// wrong token earns and send the user back to forgot-password for nothing.
func TestAuthResetPassword_RejectsAnUnusableTokenBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no --token", nil},
		{"blank token", []string{"--token", "  "}},
		{"link without fragment", []string{"--token", "https://admin.example.com/reset-password"}},
		{"link with empty fragment", []string{"--token", "https://admin.example.com/reset-password#"}},
		{"lone hash", []string{"--token", "#"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := authPasswordServer(t, http.StatusNoContent, "")
			args := append([]string{"auth", "reset-password"}, tc.args...)
			res, err := runAuthPassword(t, srv.URL, []string{"MIO_PASSWORD=a-new-password"}, args...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want 2; err=%v", res.Code, err)
			}
			if cap.Count != 0 {
				t.Errorf("%d request(s) fired; a usage error must fire none", cap.Count)
			}
		})
	}
}

// TestAuthResetPassword_PasswordIsNeverOnArgv pins the ticket's contract
// literally: there is no --password flag on reset-password (a stray one is
// the usual `unknown flag` exit 2 with no request), and with neither a
// terminal nor MIO_PASSWORD the command exits 2 naming MIO_PASSWORD, again
// before any request.
func TestAuthResetPassword_PasswordIsNeverOnArgv(t *testing.T) {
	if f := authResetPasswordCmd.Flags().Lookup("password"); f != nil {
		t.Fatalf("reset-password has a --password flag; the new password must never be taken on argv")
	}

	t.Run("--password is unknown", func(t *testing.T) {
		srv, cap := authPasswordServer(t, http.StatusNoContent, "")
		res, err := runAuthPassword(t, srv.URL, nil, "auth", "reset-password", "--token", "tok", "--password", "x")
		if res.Code != errs.ExitUsage {
			t.Errorf("exit code = %d, want 2; err=%v", res.Code, err)
		}
		if cap.Count != 0 {
			t.Errorf("%d request(s) fired; want none", cap.Count)
		}
	})
	t.Run("no terminal and no MIO_PASSWORD", func(t *testing.T) {
		srv, cap := authPasswordServer(t, http.StatusNoContent, "")
		res, err := runAuthPassword(t, srv.URL, nil, "auth", "reset-password", "--token", "tok")
		if res.Code != errs.ExitUsage {
			t.Errorf("exit code = %d, want 2; err=%v", res.Code, err)
		}
		if err == nil || !strings.Contains(err.Error(), "MIO_PASSWORD") {
			t.Errorf("error %v must name MIO_PASSWORD as the headless way in", err)
		}
		if cap.Count != 0 {
			t.Errorf("%d request(s) fired; want none", cap.Count)
		}
	})
}

// TestAuthResetPassword_ExplainsTheAPIsVerdict pins the plain-words hints on
// the answers the ticket names — 410 (expired/used/unknown → run
// forgot-password again; envelope keeps code password_reset_expired), 422
// (too short → exit 2) — and the missing-route 404, each with the API's real
// status and document kept on the error.
func TestAuthResetPassword_ExplainsTheAPIsVerdict(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int
		wantHint string
		wantAPI  string // a member of the API's document that must survive
	}{
		{"410 expired", http.StatusGone,
			`{"errors":[{"status":"410","code":"password_reset_expired","title":"Gone","detail":"Password reset link expired.","meta":{"request_id":"req_1"}}]}`,
			errs.ExitGeneric, "mio auth forgot-password", "password_reset_expired"},
		{"422 too short", http.StatusUnprocessableEntity,
			`{"errors":[{"status":"422","title":"Unprocessable Entity","detail":"String should have at least 8 characters","source":{"pointer":"/password"}}]}`,
			errs.ExitUsage, "must be at least 8 characters", "/password"}, // the API says "should have"; only the hint says "must be"
		{"404 route absent", http.StatusNotFound, `{"detail":"Not Found"}`, errs.ExitNotFound, "MIO-3571", ""},
		{"429 rate limited", http.StatusTooManyRequests,
			`{"errors":[{"status":"429","title":"Too Many Requests","detail":"Too many requests. Try again later."}]}`,
			errs.ExitRateLimited, "", "Too many requests"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := authPasswordServer(t, tc.status, tc.body)
			res, err := runAuthPassword(t, srv.URL, []string{"MIO_PASSWORD=short"},
				"auth", "reset-password", "--token", "tok")
			if res.Code != tc.wantCode {
				t.Errorf("exit code = %d, want %d; err=%v", res.Code, tc.wantCode, err)
			}
			if got := errs.HTTPStatusOf(err); got != tc.status {
				t.Errorf("HTTPStatusOf = %d, want %d", got, tc.status)
			}
			if tc.wantHint != "" && !strings.Contains(err.Error(), tc.wantHint) {
				t.Errorf("error %q must carry a hint mentioning %q", err.Error(), tc.wantHint)
			}
			if tc.wantAPI != "" && !strings.Contains(string(errs.APIErrorDocumentOf(err)), tc.wantAPI) {
				t.Errorf("the API's document (with %q) must survive on the error; got %q", tc.wantAPI, errs.APIErrorDocumentOf(err))
			}
			if cap.Authorization != "" {
				t.Errorf("Authorization = %q, want none", cap.Authorization)
			}
			if res.Stdout != "" {
				t.Errorf("a failure must print nothing on stdout; got %q", res.Stdout)
			}
		})
	}
}

// TestAuthResetPassword_PromptReadsTwiceFromOneReader drives the terminal
// prompt with a scripted stdin: both reads come from ONE shared reader (a
// fresh bufio per read would buffer and lose the confirmation line), a match
// is accepted, and a mismatch or an empty entry is exit 2.
func TestAuthResetPassword_PromptReadsTwiceFromOneReader(t *testing.T) {
	prompt := func(t *testing.T, input string) (string, error) {
		t.Helper()
		cmd := &cobra.Command{}
		cmd.SetIn(strings.NewReader(input))
		var errBuf bytes.Buffer
		cmd.SetErr(&errBuf)
		got, err := promptNewPassword(cmd, bufio.NewReader(cmd.InOrStdin()))
		if !strings.Contains(errBuf.String(), "New password:") {
			t.Errorf("prompt text %q must ask for the new password on stderr", errBuf.String())
		}
		return got, err
	}

	if got, err := prompt(t, "a-new-password\na-new-password\n"); err != nil || got != "a-new-password" {
		t.Errorf("matching entries: got %q, %v; want the password and no error", got, err)
	}
	if _, err := prompt(t, "a-new-password\nDIFFERENT\n"); errs.CodeOf(err) != errs.ExitUsage || !strings.Contains(errString(err), "do not match") {
		t.Errorf("mismatch: err = %v; want exit 2 saying the passwords do not match", err)
	}
	if _, err := prompt(t, "\n\n"); errs.CodeOf(err) != errs.ExitUsage {
		t.Errorf("empty entry: err = %v; want exit 2", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// mapsEqualJSON compares two decoded JSON objects by their JSON encoding.
func mapsEqualJSON(got, want map[string]any) bool {
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return bytes.Equal(g, w)
}
