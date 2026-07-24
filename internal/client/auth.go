package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// claimsNamespace is the custom claim namespace used by the mio backend JWT
// issuer. Claims are nested under this key in the token payload.
// See app/infrastructure/security/jwt.py: CLAIMS_NAMESPACE.
const claimsNamespace = "https://membership.io/claims"

// TeamIDFromAccessToken decodes the payload segment of a JWT access token
// (without verifying the signature — the token was just received over TLS from
// our own backend) and extracts the team_id from the namespaced claims object.
//
// Returns "" if the token is malformed or the claim is absent.
func TeamIDFromAccessToken(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	// Base64url decode with padding tolerance.
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}
	ns, ok := claims[claimsNamespace].(map[string]any)
	if !ok {
		return ""
	}
	teamID, _ := ns["team_id"].(string)
	return teamID
}

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

// RegisterResult is the decoded response of POST /api/auth/register. The backend
// answers 201 with the SAME token payload as login (verified against
// mio-backend: both routes return TokenResponse), so it aliases LoginResult —
// the register command can feed the resulting access token straight into the
// mint flow without a second login round-trip.
type RegisterResult = LoginResult

// Register creates a new account via the plain-JSON auth route and returns the
// resulting tokens. first_name/last_name are sent ONLY when non-empty (the
// backend fields are optional). Like Login, this uses application/json rather
// than the JSON:API content type, and requires no bearer credentials.
//
// Failures are returned unchanged so the backend's precise detail survives: a
// 409 ("Email '…' is already registered.") or a 422 ("String should have at
// least 8 characters") both map to ExitUsage via errorForResponse and already
// carry a user-facing message — masking them with a generic string would only
// hide why registration was rejected.
func (c *Client) Register(ctx context.Context, email, password, firstName, lastName string) (*RegisterResult, error) {
	payload := map[string]string{"email": email, "password": password}
	if firstName != "" {
		payload["first_name"] = firstName
	}
	if lastName != "" {
		payload["last_name"] = lastName
	}
	body, err := c.do(ctx, http.MethodPost, "/api/auth/register", nil, payload, contentTypeJSON)
	if err != nil {
		return nil, err
	}
	var res RegisterResult
	if uerr := json.Unmarshal(body, &res); uerr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("decode register response: %w", uerr))
	}
	if res.AccessToken == "" {
		return nil, errs.New(errs.ExitGeneric, "registration succeeded but no access token was returned")
	}
	return &res, nil
}

// MintAPIKey creates a new API key for the given team using a bearer access
// token obtained from Login. It returns the minted Resource; the full secret is
// in attributes["secret"] and is only ever returned once by the backend.
//
// The caller is authenticated with a JWT access token rather than an API key
// (the backend requires JWT to manage keys), so we issue the request with a
// temporary client carrying that token. The api-keys create endpoint binds a
// FLAT pydantic model (ApiKeyCreateRequest = {name, scopes?, expires_at?}), NOT
// a JSON:API envelope — so the body MUST be sent flat (StyleFlat). Sending an
// envelope here lands `name` under `data` and 422s, breaking `mio login`'s
// password→mint flow.
func (c *Client) MintAPIKey(ctx context.Context, accessToken, teamID, name string) (*Resource, error) {
	if teamID == "" {
		return nil, errs.New(errs.ExitUsage, "cannot mint API key: no team id resolved (set --team or `mio config set current_team <id>`)")
	}
	tokenClient := New(c.baseURL, accessToken, WithHTTPClient(c.http), WithDebug(c.debug))
	path := fmt.Sprintf("/api/teams/%s/api-keys", teamID)
	return tokenClient.CreateWith(ctx, StyleFlat, path, map[string]any{"name": name})
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

// TeamInfo is a minimal representation of a team returned by GET /api/teams.
type TeamInfo struct {
	ID   string
	Name string
}

// ListTeams fetches the teams visible to the bearer access token (JWT) and
// returns them as a slice of TeamInfo. It uses the JSON:API collection endpoint
// GET /api/teams, authenticated with the access token as a JWT bearer.
func (c *Client) ListTeams(ctx context.Context, accessToken string) ([]TeamInfo, error) {
	tokenClient := New(c.baseURL, accessToken, WithHTTPClient(c.http), WithDebug(c.debug))
	col, err := tokenClient.List(ctx, "/api/teams", nil)
	if err != nil {
		return nil, err
	}
	teams := make([]TeamInfo, 0, len(col.Data))
	for _, r := range col.Data {
		name, _ := r.Attributes["name"].(string)
		teams = append(teams, TeamInfo{ID: r.ID, Name: name})
	}
	return teams, nil
}
