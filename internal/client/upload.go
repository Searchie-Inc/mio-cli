package client

// upload.go — raw binary upload plumbing (MIO-2267). The media-upload flow
// streams file bytes directly to a presigned S3 URL, which is NOT a mio route:
// it must NOT carry the mio Authorization header and its body is the raw file,
// not a JSON:API envelope. The normal client request path can't express that,
// so PutFileToURL is a standalone helper on a bare HTTP client.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// uploadHTTPClient has no timeout: uploads can be large and are bounded by the
// caller's context instead. Redirects are left at the default policy.
var uploadHTTPClient = &http.Client{}

// PutFileToURL streams the file at path to url with an HTTP PUT and returns the
// S3 ETag from the response (quotes trimmed) — needed to complete a multipart
// upload. It deliberately sends NO Authorization header (the presigned URL is
// self-authorizing) and sets Content-Type only when contentType is non-empty
// (the single-part presign does not sign Content-Type, so it is optional).
func PutFileToURL(ctx context.Context, url, path, contentType string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, f)
	if err != nil {
		return "", err
	}
	// Set ContentLength explicitly so S3 gets a fixed-length PUT (not chunked).
	req.ContentLength = fi.Size()
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := uploadHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload PUT: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("upload PUT failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`), nil
}
