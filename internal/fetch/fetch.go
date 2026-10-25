// Package fetch downloads web pages, extracts their readable content and
// builds self-contained snapshots of them.
package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html/charset"
)

// maxBody caps how much of a page is read, to avoid huge downloads.
const maxBody = 10 << 20

const userAgent = "liki/0.1 (+local link wiki)"

var client = &http.Client{Timeout: 30 * time.Second}

type Article struct {
	Title string
	Text  string
	HTML  string
	// Snapshot is the whole original page as a single HTML file with
	// styles, images and fonts embedded, viewable offline.
	Snapshot []byte
}

// Fetch downloads rawURL, extracts its main content and builds a snapshot.
func Fetch(ctx context.Context, rawURL string) (Article, error) {
	body, pageURL, ctype, err := get(ctx, rawURL, "text/html,application/xhtml+xml", maxBody)
	if err != nil {
		return Article{}, err
	}
	if mt, _, _ := mime.ParseMediaType(ctype); mt != "" && !strings.Contains(mt, "html") {
		return Article{}, fmt.Errorf("unsupported content type %q", mt)
	}
	// Convert to UTF-8 so the extractor and the snapshot both see correct text.
	r, err := charset.NewReader(bytes.NewReader(body), ctype)
	if err != nil {
		return Article{}, fmt.Errorf("decode page: %w", err)
	}
	if body, err = io.ReadAll(r); err != nil {
		return Article{}, fmt.Errorf("decode page: %w", err)
	}

	art, err := readability.FromReader(bytes.NewReader(body), pageURL)
	if err != nil {
		return Article{}, fmt.Errorf("extract content: %w", err)
	}
	if art.Node == nil {
		return Article{}, fmt.Errorf("no readable content found")
	}
	var text, html strings.Builder
	if err := art.RenderText(&text); err != nil {
		return Article{}, err
	}
	if err := art.RenderHTML(&html); err != nil {
		return Article{}, err
	}

	snap, err := Snapshot(ctx, body, pageURL)
	if err != nil {
		return Article{}, fmt.Errorf("snapshot: %w", err)
	}
	return Article{
		Title:    strings.TrimSpace(art.Title()),
		Text:     strings.TrimSpace(text.String()),
		HTML:     html.String(),
		Snapshot: snap,
	}, nil
}

// get downloads rawURL and returns its body, the final URL after redirects
// and the response content type. Bodies larger than limit are rejected.
func get(ctx context.Context, rawURL, accept string, limit int64) ([]byte, *url.URL, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, "", fmt.Errorf("http %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, "", err
	}
	if int64(len(body)) > limit {
		return nil, nil, "", fmt.Errorf("response larger than %d MB", limit>>20)
	}
	return body, resp.Request.URL, resp.Header.Get("Content-Type"), nil
}
