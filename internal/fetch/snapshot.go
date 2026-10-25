package fetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	maxResource     = 10 << 20 // single stylesheet, image or font
	maxSnapshot     = 50 << 20 // all embedded resources of one page
	maxImportDepth  = 5
	resourceWorkers = 8
)

var (
	cssURLRe     = regexp.MustCompile(`url\(\s*(?:"([^"]*)"|'([^']*)'|([^'")\s]*))\s*\)`)
	cssImportRe  = regexp.MustCompile(`@import\s+(?:url\(\s*(?:"([^"]*)"|'([^']*)'|([^'")\s]*))\s*\)|"([^"]*)"|'([^']*)')\s*([^;]*);`)
	cssCharsetRe = regexp.MustCompile(`@charset\s+[^;]*;`)
	srcsetSepRe  = regexp.MustCompile(`,\s+`)
)

// Snapshot turns page (UTF-8 HTML fetched from pageURL) into a single
// self-contained HTML document: stylesheets, images, icons and fonts are
// embedded as data: URIs, scripts are removed and remaining links are made
// absolute. Resources that fail to download keep their absolute URL.
func Snapshot(ctx context.Context, page []byte, pageURL *url.URL) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, err
	}
	s := &snapshotter{
		ctx:   ctx,
		base:  pageURL,
		sem:   make(chan struct{}, resourceWorkers),
		cache: map[string]*resource{},
	}

	var elems []*html.Node
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode {
			elems = append(elems, n)
		}
	}
	// <base href> changes how relative URLs resolve; apply it, then drop the tag.
	for _, n := range elems {
		if n.DataAtom == atom.Base {
			if h := getAttr(n, "href"); h != "" {
				if u, err := pageURL.Parse(h); err == nil {
					s.base = u
				}
			}
			break
		}
	}

	var kept []*html.Node
	for _, n := range elems {
		if strip(n) {
			n.Parent.RemoveChild(n)
			continue
		}
		kept = append(kept, n)
	}
	// Start every download first so they run in parallel, then embed them.
	for _, n := range kept {
		s.prepare(n)
	}
	for _, n := range kept {
		s.embed(n)
	}

	addHeader(doc, pageURL)
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), ctx.Err()
}

// strip removes attributes that run code or would break the snapshot and
// reports whether the whole element should be dropped.
func strip(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Noscript, atom.Base:
		return true
	case atom.Meta:
		equiv := strings.ToLower(getAttr(n, "http-equiv"))
		// The snapshot is always UTF-8; addHeader declares that.
		if hasAttr(n, "charset") || equiv == "content-type" || equiv == "refresh" || equiv == "content-security-policy" {
			return true
		}
	case atom.Link:
		for _, rel := range strings.Fields(strings.ToLower(getAttr(n, "rel"))) {
			switch rel {
			case "preload", "modulepreload", "prefetch", "dns-prefetch", "preconnect", "manifest":
				return true
			}
		}
	case atom.Source:
		// Inside <picture> the <img> fallback is embedded instead.
		if n.Parent != nil && n.Parent.DataAtom == atom.Picture {
			return true
		}
	}
	n.Attr = slices.DeleteFunc(n.Attr, func(a html.Attribute) bool {
		k := strings.ToLower(a.Key)
		return strings.HasPrefix(k, "on") || k == "integrity" || k == "nonce" || k == "crossorigin"
	})
	return false
}

// addHeader declares UTF-8 and records where the snapshot came from.
func addHeader(doc *html.Node, pageURL *url.URL) {
	for n := range doc.Descendants() {
		if n.DataAtom == atom.Head {
			meta := &html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta,
				Attr: []html.Attribute{{Key: "charset", Val: "utf-8"}}}
			n.InsertBefore(meta, n.FirstChild)
			break
		}
	}
	for n := range doc.ChildNodes() {
		if n.DataAtom == atom.Html {
			c := &html.Node{Type: html.CommentNode,
				Data: " saved by liki from " + pageURL.String() + " at " + time.Now().Format(time.RFC3339) + " "}
			doc.InsertBefore(c, n)
			break
		}
	}
}

type resource struct {
	done  chan struct{}
	data  []byte
	mime  string
	final *url.URL
	err   error
}

type snapshotter struct {
	ctx   context.Context
	base  *url.URL
	sem   chan struct{}
	total atomic.Int64

	mu    sync.Mutex
	cache map[string]*resource
}

// prepare makes the element's URLs absolute and starts downloading
// everything that will be embedded.
func (s *snapshotter) prepare(n *html.Node) {
	switch n.DataAtom {
	case atom.Img:
		src := getAttr(n, "src")
		// Lazy-loaded images keep the real URL in data-* or srcset.
		if src == "" || strings.HasPrefix(src, "data:") {
			if alt := firstAttr(n, "data-src", "data-lazy-src", "data-original"); alt != "" {
				src = alt
			} else if set := firstAttr(n, "srcset", "data-srcset"); set != "" {
				src = largestCandidate(set)
			}
		}
		for _, k := range []string{"srcset", "data-srcset", "sizes", "loading", "data-src", "data-lazy-src", "data-original"} {
			delAttr(n, k)
		}
		if src != "" {
			s.startAttr(n, "src", src)
		}
	case atom.Link:
		rel := strings.ToLower(getAttr(n, "rel"))
		if strings.Contains(rel, "stylesheet") || strings.Contains(rel, "icon") {
			s.startAttr(n, "href", getAttr(n, "href"))
		} else {
			s.absAttr(n, "href")
		}
	case atom.Video:
		s.startAttr(n, "poster", getAttr(n, "poster"))
		s.absAttr(n, "src")
	case atom.Input:
		if strings.EqualFold(getAttr(n, "type"), "image") {
			s.startAttr(n, "src", getAttr(n, "src"))
		}
	case atom.Style:
		if c := n.FirstChild; c != nil && c.Type == html.TextNode {
			s.startCSS(c.Data, s.base)
		}
	case atom.A, atom.Area:
		s.absAttr(n, "href")
	case atom.Iframe, atom.Audio, atom.Source, atom.Track, atom.Embed:
		s.absAttr(n, "src")
	case atom.Form:
		s.absAttr(n, "action")
	case atom.Object:
		s.absAttr(n, "data")
	}
	if st := getAttr(n, "style"); st != "" {
		s.startCSS(st, s.base)
	}
}

// embed replaces the element's prepared URLs with data: URIs.
func (s *snapshotter) embed(n *html.Node) {
	switch n.DataAtom {
	case atom.Img, atom.Input:
		s.embedAttr(n, "src")
	case atom.Video:
		s.embedAttr(n, "poster")
	case atom.Link:
		rel := strings.ToLower(getAttr(n, "rel"))
		switch {
		case strings.Contains(rel, "stylesheet"):
			s.embedStylesheet(n)
		case strings.Contains(rel, "icon"):
			s.embedAttr(n, "href")
		}
	case atom.Style:
		if c := n.FirstChild; c != nil && c.Type == html.TextNode {
			c.Data = styleText(s.inlineCSS(c.Data, s.base, 0))
		}
	}
	if st := getAttr(n, "style"); st != "" {
		setAttr(n, "style", s.inlineCSS(st, s.base, 0))
	}
}

// embedStylesheet replaces <link rel=stylesheet> with an equivalent <style>.
func (s *snapshotter) embedStylesheet(n *html.Node) {
	href := getAttr(n, "href")
	if href == "" {
		return
	}
	r := s.wait(href)
	if r.err != nil {
		return
	}
	style := &html.Node{Type: html.ElementNode, Data: "style", DataAtom: atom.Style}
	if m := getAttr(n, "media"); m != "" {
		style.Attr = []html.Attribute{{Key: "media", Val: m}}
	}
	style.AppendChild(&html.Node{Type: html.TextNode, Data: styleText(s.inlineCSS(string(r.data), r.final, 0))})
	n.Parent.InsertBefore(style, n)
	n.Parent.RemoveChild(n)
}

// inlineCSS inlines @import rules and replaces url(...) references with data: URIs.
func (s *snapshotter) inlineCSS(css string, base *url.URL, depth int) string {
	css = cssCharsetRe.ReplaceAllString(css, "")
	s.startCSS(css, base)
	css = cssImportRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssImportRe.FindStringSubmatch(m)
		u := resolve(base, firstNonEmpty(sub[1:6]...))
		if u == "" {
			return m
		}
		media := strings.TrimSpace(sub[6])
		r := s.wait(u)
		if r.err != nil || depth >= maxImportDepth {
			return `@import url("` + u + `") ` + media + ";"
		}
		inner := s.inlineCSS(string(r.data), r.final, depth+1)
		if media != "" {
			return "@media " + media + " {\n" + inner + "\n}"
		}
		return inner
	})
	return cssURLRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssURLRe.FindStringSubmatch(m)
		ref := firstNonEmpty(sub[1:4]...)
		if strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
			return m
		}
		u := resolve(base, ref)
		if u == "" {
			return m
		}
		return `url("` + s.dataURI(u) + `")`
	})
}

// startCSS starts downloading everything css refers to.
func (s *snapshotter) startCSS(css string, base *url.URL) {
	for _, sub := range cssImportRe.FindAllStringSubmatch(css, -1) {
		if u := resolve(base, firstNonEmpty(sub[1:6]...)); u != "" {
			s.start(u)
		}
	}
	for _, sub := range cssURLRe.FindAllStringSubmatch(css, -1) {
		ref := firstNonEmpty(sub[1:4]...)
		if strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
			continue
		}
		if u := resolve(base, ref); u != "" {
			s.start(u)
		}
	}
}

// startAttr stores ref as an absolute URL in attribute key and starts downloading it.
func (s *snapshotter) startAttr(n *html.Node, key, ref string) {
	if u := resolve(s.base, ref); u != "" {
		setAttr(n, key, u)
		s.start(u)
	}
}

func (s *snapshotter) absAttr(n *html.Node, key string) {
	v := getAttr(n, key)
	if v == "" || strings.HasPrefix(v, "#") {
		return
	}
	if u, err := s.base.Parse(strings.TrimSpace(v)); err == nil {
		setAttr(n, key, u.String())
	}
}

func (s *snapshotter) embedAttr(n *html.Node, key string) {
	if v := getAttr(n, key); v != "" && !strings.HasPrefix(v, "data:") {
		setAttr(n, key, s.dataURI(v))
	}
}

// start begins downloading u in the background, once per URL.
func (s *snapshotter) start(u string) *resource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.cache[u]; ok {
		return r
	}
	r := &resource{done: make(chan struct{})}
	s.cache[u] = r
	go func() {
		defer close(r.done)
		select {
		case s.sem <- struct{}{}:
		case <-s.ctx.Done():
			r.err = s.ctx.Err()
			return
		}
		defer func() { <-s.sem }()

		body, final, ctype, err := get(s.ctx, u, "*/*", maxResource)
		if err != nil {
			r.err = err
			return
		}
		if s.total.Add(int64(len(body))) > maxSnapshot {
			r.err = errors.New("snapshot size limit reached")
			return
		}
		r.data, r.final, r.mime = body, final, mediaType(ctype, final.Path, body)
	}()
	return r
}

func (s *snapshotter) wait(u string) *resource {
	r := s.start(u)
	<-r.done
	return r
}

// dataURI returns u embedded as a data: URI, or u itself if the download failed.
func (s *snapshotter) dataURI(u string) string {
	r := s.wait(u)
	if r.err != nil {
		return u
	}
	return "data:" + r.mime + ";base64," + base64.StdEncoding.EncodeToString(r.data)
}

// mediaType picks a MIME type from the header, the file extension or the content.
func mediaType(header, p string, body []byte) string {
	if mt, _, err := mime.ParseMediaType(header); err == nil && mt != "application/octet-stream" && mt != "text/plain" {
		return mt
	}
	if mt, _, err := mime.ParseMediaType(mime.TypeByExtension(path.Ext(p))); err == nil {
		return mt
	}
	mt, _, _ := mime.ParseMediaType(http.DetectContentType(body))
	return mt
}

// resolve returns ref as an absolute http(s) URL without fragment, or "" if it is not one.
func resolve(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// largestCandidate returns the URL with the biggest width or density descriptor from a srcset.
func largestCandidate(srcset string) string {
	var best string
	var bestSize float64 = -1
	for _, c := range srcsetSepRe.Split(strings.TrimSpace(srcset), -1) {
		f := strings.Fields(c)
		if len(f) == 0 {
			continue
		}
		size := 1.0
		if len(f) > 1 {
			if v, err := strconv.ParseFloat(strings.TrimRight(f[1], "wxh"), 64); err == nil {
				size = v
			}
		}
		if size > bestSize {
			best, bestSize = f[0], size
		}
	}
	return best
}

// styleText keeps CSS from closing its <style> element early.
func styleText(css string) string {
	return strings.ReplaceAll(css, "</style", `<\/style`)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	return slices.ContainsFunc(n.Attr, func(a html.Attribute) bool { return strings.EqualFold(a.Key, key) })
}

func firstAttr(n *html.Node, keys ...string) string {
	for _, k := range keys {
		if v := getAttr(n, k); v != "" {
			return v
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func delAttr(n *html.Node, key string) {
	n.Attr = slices.DeleteFunc(n.Attr, func(a html.Attribute) bool { return strings.EqualFold(a.Key, key) })
}
