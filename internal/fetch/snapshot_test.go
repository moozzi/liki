package fetch

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// site serves files by path, each with an optional Content-Type.
func site(t *testing.T, files map[string][2]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if f[1] != "" {
			w.Header().Set("Content-Type", f[1])
		}
		w.Write([]byte(f[0]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dataURI(mime, data string) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString([]byte(data))
}

const png = "\x89PNG\r\n\x1a\nfake image"

func TestSnapshot(t *testing.T) {
	srv := site(t, map[string][2]string{
		"/article/style.css": {`@import "print.css" print;
body { background: url(img/bg.png) }
.x::after { content: "</style>" }`, "text/css"},
		"/article/print.css":  {`@font-face { src: url('/font.woff2') }`, "text/css"},
		"/article/img/bg.png": {png, ""},              // type detected from the content
		"/font.woff2":         {"FONT", "text/plain"}, // wrong type, the extension decides
		"/article/lazy.jpg":   {"LAZY", "image/jpeg"},
		"/article/small.jpg":  {"SMALL", "image/jpeg"},
		"/article/big.jpg":    {"BIG", "image/jpeg"},
	})
	page := `<!DOCTYPE html><html><head>
<meta http-equiv="Content-Security-Policy" content="default-src 'self'">
<meta charset="iso-8859-2">
<link rel="stylesheet" href="style.css" integrity="sha384-x">
<link rel="preload" href="app.js" as="script">
<script>document.write("tracking")</script>
</head><body onload="init()">
<a href="/docs#intro" onclick="go()">docs</a> <a href="#top">top</a>
<img src="data:image/gif;base64,R0lGODlh" data-src="lazy.jpg">
<img srcset="small.jpg 300w, big.jpg 1200w" sizes="50vw">
<img src="missing.png">
<div style="background: url('img/bg.png')"></div>
<noscript><img src="pixel.gif"></noscript>
</body></html>`
	base, _ := url.Parse(srv.URL + "/article/page.html")
	out, err := Snapshot(context.Background(), []byte(page), base)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	for _, want := range []string{
		`<meta charset="utf-8"/>`,
		// The stylesheet is inlined, with its import, image and font.
		"@media print {",
		`url("` + dataURI("font/woff2", "FONT") + `")`,
		`background: url("` + dataURI("image/png", png) + `")`,
		// Text in CSS must not close the <style> element.
		`content: "<\/style>"`,
		// Lazy image: the real URL from data-src, not the placeholder.
		`src="` + dataURI("image/jpeg", "LAZY") + `"`,
		// srcset: the largest candidate.
		`src="` + dataURI("image/jpeg", "BIG") + `"`,
		// A failed download stays as an absolute URL.
		`src="` + srv.URL + `/article/missing.png"`,
		// Links are absolute; anchors within the page stay.
		`href="` + srv.URL + `/docs#intro"`,
		`href="#top"`,
		`style="background: url(&#34;` + dataURI("image/png", png) + `&#34;)"`,
		"<!-- saved by liki from " + base.String(),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("snapshot lacks %s", want)
		}
	}
	for _, unwanted := range []string{
		"<script", "tracking", "<noscript", "pixel.gif", "onload", "onclick", "integrity",
		"Content-Security-Policy", "iso-8859-2", "preload", "app.js",
		"<link", "data-src", "srcset", "sizes=",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("snapshot still contains %q", unwanted)
		}
	}
}

func TestSnapshotBaseHref(t *testing.T) {
	srv := site(t, map[string][2]string{"/assets/a.png": {png, "image/png"}})
	base, _ := url.Parse(srv.URL + "/blog/post")
	page := `<html><head><base href="/assets/"></head><body><img src="a.png"></body></html>`
	out, err := Snapshot(context.Background(), []byte(page), base)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, dataURI("image/png", png)) || strings.Contains(got, "<base") {
		t.Errorf("image not resolved against <base href>, or <base> kept:\n%s", got)
	}
}

func TestLargestCandidate(t *testing.T) {
	for _, tt := range []struct{ srcset, want string }{
		{"a.jpg", "a.jpg"},
		{"a.jpg 1x, b.jpg 2x", "b.jpg"},
		{"a.jpg 300w, b.jpg 1200w, c.jpg 800w", "b.jpg"},
		// Commas inside URLs are not separators.
		{"img.jpg?crop=1,2 1000w, small.jpg 100w", "img.jpg?crop=1,2"},
	} {
		if got := largestCandidate(tt.srcset); got != tt.want {
			t.Errorf("largestCandidate(%q) = %q, want %q", tt.srcset, got, tt.want)
		}
	}
}
