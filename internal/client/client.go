// Package client is the mio HTTP layer: a thin JSON:API v1.1 client plus the
// plain-JSON auth helpers. It owns request construction (auth header, content
// negotiation), response decoding, and the translation of non-2xx responses
// into typed *errs.CLIError values carrying the right exit code.
//
// Resource commands depend only on the high-level verbs (List/Retrieve/Create/
// Update/Delete/Action). They never build URLs by hand beyond the path; the
// client joins the path onto the configured base.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// Content types. JSON:API for resource routes; plain JSON for /api/auth/*.
const (
	contentTypeJSONAPI = "application/vnd.api+json"
	contentTypeJSON    = "application/json"
)

// Client is a configured mio API client. Construct it with New; it is safe for
// sequential CLI use (one process, one command).
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	debug   bool
}

// Option customizes a Client at construction time.
type Option func(*Client)

// WithHTTPClient overrides the underlying *http.Client (used in tests).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithDebug enables verbose request/response logging to stderr.
func WithDebug(debug bool) Option {
	return func(c *Client) { c.debug = debug }
}

// New builds a Client for the given API base URL and bearer key. The apiKey may
// be empty for unauthenticated calls (e.g. auth login); the verbs that need it
// will surface a 401 from the server.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// envelope wraps create/update attributes in a JSON:API resource document.
// Type is intentionally omitted: the mio backend infers it from the route, and
// leaving it out keeps resource commands from having to hardcode type names.
type envelope struct {
	Data envelopeData `json:"data"`
}

type envelopeData struct {
	Attributes map[string]any `json:"attributes"`
}

// List performs a GET against a collection route and decodes the JSON:API
// collection document, including the top-level meta (pagination cursors).
func (c *Client) List(ctx context.Context, path string, query url.Values) (*Collection, error) {
	body, err := c.do(ctx, http.MethodGet, path, query, nil, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	col, derr := DecodeCollection(body)
	if derr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, derr)
	}
	return col, nil
}

// Retrieve performs a GET against a single-resource route.
func (c *Client) Retrieve(ctx context.Context, path string) (*Resource, error) {
	body, err := c.do(ctx, http.MethodGet, path, nil, nil, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// Create performs a POST with the attributes wrapped in a JSON:API envelope.
func (c *Client) Create(ctx context.Context, path string, attrs map[string]any) (*Resource, error) {
	body, err := c.do(ctx, http.MethodPost, path, nil, envelope{Data: envelopeData{Attributes: attrs}}, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// Update performs a PATCH with the attributes wrapped in a JSON:API envelope.
// Per JSON:API, only the supplied fields are changed (partial update).
func (c *Client) Update(ctx context.Context, path string, attrs map[string]any) (*Resource, error) {
	body, err := c.do(ctx, http.MethodPatch, path, nil, envelope{Data: envelopeData{Attributes: attrs}}, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	return decodeResourceWrapped(body)
}

// Delete performs a DELETE and discards any body. A 204 (or any 2xx) is success.
func (c *Client) Delete(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, path, nil, nil, contentTypeJSONAPI)
	return err
}

// Action performs a custom action route (cancel, refund, replay, restore, …).
// body may be nil for actions that take no payload; when non-nil it is wrapped
// in the JSON:API envelope like create/update. The response, if any resource is
// returned, is decoded; an empty 2xx body yields a nil resource and nil error.
func (c *Client) Action(ctx context.Context, method, path string, body map[string]any) (*Resource, error) {
	var payload any
	if body != nil {
		payload = envelope{Data: envelopeData{Attributes: body}}
	}
	raw, err := c.do(ctx, method, path, nil, payload, contentTypeJSONAPI)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	return decodeResourceWrapped(raw)
}

// decodeResourceWrapped decodes a single resource and tags any decode failure
// with a generic exit code.
func decodeResourceWrapped(body []byte) (*Resource, error) {
	res, derr := DecodeResource(body)
	if derr != nil {
		return nil, errs.Wrap(errs.ExitGeneric, derr)
	}
	return res, nil
}

// do is the single request choke point. It builds the URL, sets auth and
// content negotiation headers, executes, and converts non-2xx responses into a
// typed *errs.CLIError. accept controls both the Accept and (for bodies) the
// Content-Type header, so auth helpers can request plain JSON.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, payload any, accept string) ([]byte, error) {
	u := c.baseURL + ensureLeadingSlash(path)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reqBody io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("encode request body: %w", err))
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("build request: %w", err))
	}
	req.Header.Set("Accept", accept)
	if reqBody != nil {
		req.Header.Set("Content-Type", accept)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	if c.debug {
		fmt.Fprintf(stderr(), "[debug] %s %s\n", method, u)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("%s %s: %w", method, u, err))
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, fmt.Errorf("read response: %w", err))
	}

	if c.debug {
		fmt.Fprintf(stderr(), "[debug] -> %d (%d bytes)\n", resp.StatusCode, len(respBody))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.errorForResponse(resp.StatusCode, respBody)
	}
	return respBody, nil
}

// errorForResponse maps a non-2xx response into a *errs.CLIError. If the body
// carries a JSON:API `errors` array, its joined message is used; otherwise a
// generic status-based message is returned. The exit code is derived from the
// HTTP status so it is correct regardless of body shape.
func (c *Client) errorForResponse(status int, body []byte) error {
	code := errs.ExitCodeForStatus(status)

	// Try to parse a JSON:API errors array for a precise message. We reuse the
	// collection decoder shape which already understands the errors array.
	var doc struct {
		Errors []apiError `json:"errors"`
	}
	if json.Unmarshal(body, &doc) == nil && len(doc.Errors) > 0 {
		return errs.Wrap(code, &apiErrorList{Errors: doc.Errors})
	}

	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return errs.New(code, "request failed (HTTP %d): %s", status, msg)
}

func ensureLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}
