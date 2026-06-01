package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// stderr is indirected so tests can capture debug output if needed. It defaults
// to the process stderr.
var stderr = func() *os.File { return os.Stderr }

// LoginResult is the decoded payload of a successful POST /api/auth/login. The
// auth routes are PLAIN JSON (not JSON:API), so this is a flat struct rather
// than a Resource. Field names mirror the backend's token response.
type LoginResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	// User holds whatever the backend returns about the authenticated user;
	// kept generic because the CLI only needs the access token to mint a key.
	User map[string]any `json:"user"`
}

// Login authenticates with email + password against the plain-JSON auth route
// and returns the resulting tokens. It does NOT use the JSON:API content type.
//
// This is special-cased here (rather than via do's JSON:API path) because the
// /api/auth/* family speaks plain JSON in both directions.
func (c *Client) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	payload := map[string]string{"email": email, "password": password}
	body, err := c.do(ctx, http.MethodPost, "/api/auth/login", nil, payload, contentTypeJSON)
	if err != nil {
		// A 401 here means bad credentials → auth exit code, with a clearer hint.
		if errs.CodeOf(err) == errs.ExitAuth {
			return nil, errs.New(errs.ExitAuth, "login failed: invalid email or password")
		}
		return nil, err
	}
	var res LoginResult
	if uerr := json.Unmarshal(body, &res); uerr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("decode login response: %w", uerr))
	}
	if res.AccessToken == "" {
		return nil, errs.New(errs.ExitGeneric, "login succeeded but no access token was returned")
	}
	return &res, nil
}

// MintAPIKey creates a new API key for the given team using a bearer access
// token obtained from Login. It returns the minted Resource; the full secret is
// in attributes["secret"] and is only ever returned once by the backend.
//
// The api-keys route IS JSON:API, but the caller is authenticated with a JWT
// access token rather than an API key — so we issue the request with a
// temporary client carrying that token.
func (c *Client) MintAPIKey(ctx context.Context, accessToken, teamID, name string) (*Resource, error) {
	if teamID == "" {
		return nil, errs.New(errs.ExitUsage, "cannot mint API key: no team id resolved (set --team or `mio config set team <id>`)")
	}
	tokenClient := New(c.baseURL, accessToken, WithHTTPClient(c.http), WithDebug(c.debug))
	path := fmt.Sprintf("/api/teams/%s/api-keys", teamID)
	return tokenClient.Create(ctx, path, map[string]any{"name": name})
}

// Me fetches the authenticated principal via GET /api/auth/me. Used by `mio
// login` to validate a pasted or env-provided key. Auth routes are plain JSON.
func (c *Client) Me(ctx context.Context) (map[string]any, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/auth/me", nil, nil, contentTypeJSON)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if uerr := json.Unmarshal(body, &out); uerr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("decode /api/auth/me: %w", uerr))
	}
	return out, nil
}
