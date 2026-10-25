package fetch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const article = `<html><head><title>%s</title></head><body>
<nav>Home | About | Contact</nav>
<article><h1>Tables</h1>
<p>Zażółć gęślą jaźń. A paragraph long enough to look like an article to the extractor, with several sentences of text.</p>
<p>Another paragraph about the same subject, so there is more readable content than navigation on this page.</p>
<p>And a third one, because readability scores pages by the amount of text in their paragraphs.</p>
</article></body></html>`

// latin2 encodes the Polish letters used in article as ISO-8859-2.
var latin2 = strings.NewReplacer("ż", "\xbf", "ó", "\xf3", "ł", "\xb3", "ć", "\xe6", "ę", "\xea", "ś", "\xb6", "ą", "\xb1", "ź", "\xbc", "ń", "\xf1")

func TestFetch(t *testing.T) {
	t.Setenv("LIKI_BROWSER", "off")
	page := strings.Replace(article, "%s", "Zażółć", 1)
	srv := site(t, map[string][2]string{
		"/latin2": {latin2.Replace(page), "text/html; charset=iso-8859-2"},
		"/pdf":    {"%PDF-1.4", "application/pdf"},
	})
	f := NewFetcher()
	defer f.Close()

	art, err := f.Fetch(context.Background(), srv.URL+"/latin2")
	if err != nil {
		t.Fatal(err)
	}
	// Text in a legacy charset ends up as UTF-8.
	if art.Title != "Zażółć" || !strings.Contains(art.Text, "Zażółć gęślą jaźń.") {
		t.Errorf("title %q, text %q; want decoded Polish text", art.Title, art.Text)
	}
	if strings.Contains(art.Text, "Contact") {
		t.Errorf("text includes navigation: %q", art.Text)
	}
	if !strings.Contains(string(art.Snapshot), "Zażółć gęślą jaźń.") {
		t.Error("snapshot is not UTF-8")
	}
	if !errors.Is(art.RenderErr, errBrowserOff) {
		t.Errorf("RenderErr = %v, want errBrowserOff", art.RenderErr)
	}

	for path, want := range map[string]string{
		"/pdf":     `unsupported content type "application/pdf"`,
		"/missing": "404",
	} {
		if _, err := f.Fetch(context.Background(), srv.URL+path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Fetch(%s): err = %v, want %q", path, err, want)
		}
	}
}
