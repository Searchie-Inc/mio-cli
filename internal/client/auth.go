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

const claimsNamespace = "https://membership.io/claims"

func decodeAccessTokenClaims(accessToken string) map[string]any {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil
	}
	return claims
}

func TeamIDFromAccessToken(accessToken string) string {
	claims := decodeAccessTokenClaims(accessToken)
	if claims == nil {
		return ""
	}
	ns, ok := claims[claimsNamespace].(map[string]any)
	if !ok {
		return ""
	}
	teamID, _ := ns["team_id"].(string)
	return teamID
}

func SubjectFromAccessToken(accessToken string) string {
	claims := decodeAccessTokenClaims(accessToken)
	if claims == nil {
		return ""
	}
	sub, _ := claims["sub"].(string)
	return sub
}

var stderr = func() *os.File { return os.Stderr }

type LoginResult struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	TokenType    string         `json:"token_type"`
	User         map[string]any `json:"user"`
}

func (c *Client) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	payload := map[string]string{"email": email, "password": password}
	body, err := c.do(ctx, http.MethodPost, "/api/auth/login", nil, payload, contentTypeJSON)
	if err != nil {
		if errs.HTTPStatusOf(err) == http.StatusUnauthorized {
			return nil, errs.NewHTTP(http.StatusUnauthorized, "login failed: invalid email or password")
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

type RegisterResult = LoginResult

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

func (c *Client) MintAPIKey(ctx context.Context, accessToken, teamID, name string) (*Resource, error) {
	if teamID == "" {
		return nil, errs.New(errs.ExitUsage, "cannot mint API key: no team id resolved (set --team or `mio config set current_team <id>`)")
	}
	tokenClient := New(c.baseURL, accessToken, WithHTTPClient(c.http), WithDebug(c.debug))
	path := fmt.Sprintf("/api/teams/%s/api-keys", teamID)
	return tokenClient.CreateWith(ctx, StyleFlat, path, map[string]any{"name": name})
}

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

type TeamInfo struct {
	ID      string
	Name    string
	OwnerID string
}

func (c *Client) ListTeams(ctx context.Context, accessToken string) ([]TeamInfo, error) {
	tokenClient := New(c.baseURL, accessToken, WithHTTPClient(c.http), WithDebug(c.debug))
	col, err := tokenClient.List(ctx, "/api/teams", nil)
	if err != nil {
		return nil, err
	}
	teams := make([]TeamInfo, 0, len(col.Data))
	for _, r := range col.Data {
		name, _ := r.Attributes["name"].(string)
		ownerID, _ := r.Attributes["owner_id"].(string)
		teams = append(teams, TeamInfo{ID: r.ID, Name: name, OwnerID: ownerID})
	}
	return teams, nil
}

func (c *Client) ForgotPassword(ctx context.Context, email string) error {
	payload := map[string]string{"email": email}
	_, err := c.do(ctx, http.MethodPost, "/api/auth/forgot", nil, payload, contentTypeJSON)
	return err
}

func (c *Client) ResetPassword(ctx context.Context, token, password string) error {
	payload := map[string]string{"token": token, "password": password}
	_, err := c.do(ctx, http.MethodPost, "/api/auth/reset", nil, payload, contentTypeJSON)
	return err
}
