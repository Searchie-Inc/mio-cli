package client

// upload.go — raw binary upload plumbing (MIO-2267 / MIO-2423). The media-upload
// flow streams bytes directly to a presigned S3 URL, which is NOT a mio route:
// it must NOT carry the mio Authorization header and its body is the raw file (or
// one multipart part), not a JSON:API envelope. The normal client request path
// can't express that, so these are standalone helpers on a bare HTTP client.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// uploadHTTPClient has no timeout: uploads can be large and are bounded by the
// caller's context instead.
var uploadHTTPClient = &http.Client{}

// putToURL streams body (of the given size) to url with a bare, unauthenticated
// HTTP PUT and returns the response ETag (quotes trimmed) — needed to complete a
// multipart upload. Content-Type is set only when non-empty.
func putToURL(ctx context.Context, url string, body io.Reader, size int64, contentType string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return "", err
	}
	// Fixed-length PUT (not chunked) so S3 accepts it.
	req.ContentLength = size
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := uploadHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload PUT: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("upload PUT failed: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`), nil
}

// PutFileToURL streams the whole file at path to url (single-part upload) and
// returns the S3 ETag. It sends NO Authorization header (the presigned URL is
// self-authorizing).
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
	return putToURL(ctx, url, f, fi.Size(), contentType)
}

// PutBytesToURL PUTs one in-memory chunk (a multipart part) to a presigned part
// URL and returns its S3 ETag.
func PutBytesToURL(ctx context.Context, url string, body []byte, contentType string) (string, error) {
	return putToURL(ctx, url, bytes.NewReader(body), int64(len(body)), contentType)
}
