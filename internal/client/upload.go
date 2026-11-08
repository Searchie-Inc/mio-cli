package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

var uploadHTTPClient = &http.Client{}

func putToURL(ctx context.Context, url string, body io.Reader, size int64, contentType string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return "", err
	}
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

func PutBytesToURL(ctx context.Context, url string, body []byte, contentType string) (string, error) {
	return putToURL(ctx, url, bytes.NewReader(body), int64(len(body)), contentType)
}
