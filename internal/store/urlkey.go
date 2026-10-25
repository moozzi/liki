package store

import (
	"net/url"
	"strings"
)

// trackingParams are query parameters that only track where a visitor came
// from; they never change which page a URL points to.
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "msclkid": true, "yclid": true,
	"igshid": true, "mc_cid": true, "mc_eid": true, "_ga": true, "_gl": true,
}

// URLKey reduces a URL to the form used to detect duplicate links, so that
// variants of one page compare equal: the scheme, "www.", a default port, a
// trailing slash, the fragment and tracking parameters are dropped, the host
// is lowercased and the remaining query parameters are sorted.
func URLKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		host += ":" + port
	}

	q := u.Query()
	for k := range q {
		if trackingParams[strings.ToLower(k)] || strings.HasPrefix(strings.ToLower(k), "utm_") {
			q.Del(k)
		}
	}
	key := host + strings.TrimRight(u.EscapedPath(), "/")
	if len(q) > 0 {
		key += "?" + q.Encode() // Encode sorts by key
	}
	// Fragments are usually anchors within a page, but "#/" and "#!" are
	// routes of single-page apps that pick a different page.
	if f := u.EscapedFragment(); strings.HasPrefix(f, "/") || strings.HasPrefix(f, "!") {
		key += "#" + f
	}
	return key
}
