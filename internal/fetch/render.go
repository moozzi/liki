package fetch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// renderTimeout limits how long one page may take to load and settle.
const renderTimeout = 30 * time.Second

var errBrowserOff = errors.New("browser rendering turned off (LIKI_BROWSER=off)")

// settleJS runs in the loaded page and returns its final HTML. It scrolls
// through the page so lazy content loads, waits until scripts stop changing
// the DOM, and writes into the DOM what outerHTML would otherwise miss.
const settleJS = `(async () => {
	const sleep = ms => new Promise(r => setTimeout(r, ms));
	const root = document.documentElement;
	for (let i = 0; i < 30 && scrollY + innerHeight < root.scrollHeight; i++) {
		scrollBy(0, innerHeight);
		await sleep(100);
	}
	scrollTo(0, 0);

	// Done when the HTML stays the same for 250 ms, after at most 5 s.
	let last = -1;
	for (let i = 0; i < 20; i++) {
		const size = root.outerHTML.length;
		if (size === last) break;
		last = size;
		await sleep(250);
	}

	// Styles added through the CSSOM (CSS-in-JS) are not in the <style> text.
	const cssText = sheet => Array.from(sheet.cssRules).map(r => r.cssText).join("\n");
	for (const s of document.querySelectorAll("style")) {
		try {
			const css = cssText(s.sheet);
			if (css.length > s.textContent.length) s.textContent = css;
		} catch (e) {}
	}
	for (const sheet of document.adoptedStyleSheets || []) {
		try {
			const s = document.createElement("style");
			s.textContent = cssText(sheet);
			document.head.appendChild(s);
		} catch (e) {}
	}
	// Keep the image the browser actually chose and loaded.
	for (const img of document.images) {
		if (img.currentSrc && !img.currentSrc.startsWith("data:")) {
			img.setAttribute("src", img.currentSrc);
			img.removeAttribute("srcset");
		}
	}
	return "<!DOCTYPE html>" + root.outerHTML;
})()`

// browser renders pages in headless Chrome, so content that scripts draw
// is captured. Chrome starts on first use and is shared by all pages.
type browser struct {
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	err    error
}

// start launches Chrome, honoring LIKI_BROWSER: "off" disables rendering,
// any other value is the path to the Chrome binary.
func (b *browser) start() error {
	b.once.Do(func() {
		env := os.Getenv("LIKI_BROWSER")
		if env == "off" {
			b.err = errBrowserOff
			return
		}
		opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(1280, 1024))
		if env != "" {
			opts = append(opts, chromedp.ExecPath(env))
		}
		allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
		// chromedp logs to stderr by default, which would break the interactive UI.
		quiet := func(string, ...any) {}
		ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(quiet), chromedp.WithErrorf(quiet))
		// The browser lives as long as ctx, so it starts without a timeout.
		if err := chromedp.Do(ctx); err != nil {
			cancel()
			cancelAlloc()
			b.err = fmt.Errorf("start Chrome: %w", err)
			return
		}
		b.ctx = ctx
		b.cancel = func() { cancel(); cancelAlloc() }
	})
	return b.err
}

// render loads rawURL in a new tab and returns the page's HTML after its
// scripts ran, and the URL it ended up at.
func (b *browser) render(ctx context.Context, rawURL string) (string, *url.URL, error) {
	if err := b.start(); err != nil {
		return "", nil, err
	}
	tabCtx, cancel := context.WithTimeout(b.ctx, renderTimeout)
	defer cancel()
	tabCtx, cancelTab := chromedp.NewContext(tabCtx)
	defer cancelTab()
	// Closing the tab also stops the render when the caller gives up.
	defer context.AfterFunc(ctx, cancelTab)()

	if err := chromedp.Do(tabCtx, chromedp.Navigate(rawURL)); err != nil {
		return "", nil, errors.Join(ctx.Err(), fmt.Errorf("render: %w", err))
	}
	html, err := chromedp.Run(tabCtx, chromedp.Evaluate[string](settleJS, chromedp.EvalAwaitPromise))
	if err != nil {
		return "", nil, errors.Join(ctx.Err(), fmt.Errorf("render: %w", err))
	}
	loc, err := chromedp.Run(tabCtx, chromedp.Location())
	if err != nil {
		return "", nil, errors.Join(ctx.Err(), fmt.Errorf("render: %w", err))
	}
	final, err := url.Parse(loc)
	if err != nil {
		return "", nil, err
	}
	return html, final, nil
}

func (b *browser) close() {
	if b.cancel != nil {
		b.cancel()
	}
}
