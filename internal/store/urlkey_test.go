package store

import "testing"

func TestURLKey(t *testing.T) {
	same := [][]string{
		{"https://go.dev/blog/maps", "http://go.dev/blog/maps/", "https://WWW.Go.dev/blog/maps#intro", "https://go.dev:443/blog/maps"},
		{"https://x.y/a?b=2&a=1", "https://x.y/a?a=1&b=2&utm_source=hn&fbclid=abc"},
		{"https://x.y", "https://x.y/", "https://www.x.y/?utm_medium=rss"},
	}
	for _, urls := range same {
		for _, u := range urls[1:] {
			if URLKey(u) != URLKey(urls[0]) {
				t.Errorf("URLKey(%q) = %q, want %q", u, URLKey(u), URLKey(urls[0]))
			}
		}
	}

	different := [][2]string{
		{"https://x.y/a", "https://x.y/A"},           // paths are case-sensitive
		{"https://x.y/a?id=1", "https://x.y/a?id=2"}, // real parameters stay
		{"https://x.y/#/a", "https://x.y/#/b"},       // single-page app routes
		{"https://x.y/a", "https://x.y:8080/a"},
		{"https://blog.x.y/a", "https://x.y/a"},
	}
	for _, d := range different {
		if URLKey(d[0]) == URLKey(d[1]) {
			t.Errorf("URLKey(%q) == URLKey(%q) = %q, want different", d[0], d[1], URLKey(d[0]))
		}
	}
}
