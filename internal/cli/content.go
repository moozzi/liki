package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"liki/internal/fetch"
	"liki/internal/store"
)

// fetchWorkers is how many pages are downloaded at once.
const fetchWorkers = 4

func runFetch(ctx context.Context, args []string, out io.Writer) error {
	fs := newFlagSet("fetch", out)
	all := fs.Bool("all", false, "re-fetch every link, including already fetched ones")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	var links []store.Link
	if fs.NArg() > 0 {
		for _, a := range fs.Args() {
			id, err := parseID(a)
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			l, err := s.Get(id)
			if err != nil {
				return fmt.Errorf("fetch #%d: %w", id, err)
			}
			links = append(links, l)
		}
	} else {
		ls, err := s.List()
		if err != nil {
			return err
		}
		for _, l := range ls {
			if *all || l.FetchedAt.IsZero() || !l.HasSnapshot {
				links = append(links, l)
			}
		}
	}
	if len(links) == 0 {
		fmt.Fprintln(out, "nothing to fetch - all links have content (use -all to re-fetch)")
		return nil
	}

	f := fetch.NewFetcher()
	defer f.Close()

	type result struct {
		link store.Link
		art  fetch.Article
		err  error
	}
	jobs := make(chan store.Link)
	results := make(chan result)
	var wg sync.WaitGroup
	for range min(fetchWorkers, len(links)) {
		wg.Go(func() {
			for l := range jobs {
				art, err := f.Fetch(ctx, l.URL)
				results <- result{l, art, err}
			}
		})
	}
	go func() {
		defer close(jobs)
		for _, l := range links {
			select {
			case jobs <- l:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	// Results are saved here, one at a time, so only one goroutine writes to the database.
	var failed int
	var renderErr error
	for r := range results {
		if r.err == nil {
			r.err = s.SaveContent(r.link.ID, r.art.Title, r.art.Text, r.art.HTML, r.art.Snapshot)
		}
		if r.err != nil {
			failed++
			fmt.Fprintf(out, "#%d %s: %v\n", r.link.ID, r.link.URL, r.err)
			continue
		}
		fmt.Fprintf(out, "fetched #%d %s (%s)\n", r.link.ID, r.link.URL, summary(r.art))
		if renderErr == nil {
			renderErr = r.art.RenderErr
		}
	}
	if ctx.Err() != nil {
		return errors.New("fetch: interrupted")
	}
	if renderErr != nil {
		fmt.Fprintf(out, "note: some pages were saved as served, without running their scripts in Chrome: %v\n", renderErr)
	}
	indexAfterChange(ctx, s, out)
	if failed > 0 {
		return fmt.Errorf("fetch: %d of %d links failed", failed, len(links))
	}
	return nil
}

// fetchOne downloads and saves content for a single link, reporting the outcome.
func fetchOne(ctx context.Context, s *store.Store, l store.Link, out io.Writer) error {
	f := fetch.NewFetcher()
	defer f.Close()
	art, err := f.Fetch(ctx, l.URL)
	if err == nil {
		err = s.SaveContent(l.ID, art.Title, art.Text, art.HTML, art.Snapshot)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "fetched content (%s)\n", summary(art))
	if art.RenderErr != nil {
		fmt.Fprintf(out, "note: saved as served, without running its scripts in Chrome: %v\n", art.RenderErr)
	}
	return nil
}

func runOpen(_ context.Context, args []string, out io.Writer) error {
	fs := newFlagSet("open", out)
	outFile := fs.String("o", "", "write the snapshot to this file instead of opening it")
	raw, err := parseWithArg(fs, args)
	if err != nil {
		return err
	}
	if raw == "" {
		return errors.New("open: missing id")
	}
	id, err := parseID(raw)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	snap, err := s.GetSnapshot(id)
	if errors.Is(err, store.ErrNoContent) {
		if _, gerr := s.Get(id); gerr != nil {
			return fmt.Errorf("open #%d: %w", id, gerr)
		}
		return fmt.Errorf("open #%d: no snapshot saved - run 'liki fetch %d'", id, id)
	}
	if err != nil {
		return err
	}

	if *outFile != "" {
		if err := os.WriteFile(*outFile, snap, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "saved #%d to %s\n", id, *outFile)
		return nil
	}
	// Reuse one file per link so repeated opens don't pile up temp files.
	path := filepath.Join(os.TempDir(), "liki", fmt.Sprintf("%d.html", id))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, snap, 0o644); err != nil {
		return err
	}
	return openInBrowser(path)
}

func openInBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("open browser: %w (file: %s)", err, path)
	}
	return nil
}

func runShow(_ context.Context, args []string, out io.Writer) error {
	fs := newFlagSet("show", out)
	asHTML := fs.Bool("html", false, "print cleaned-up HTML instead of plain text")
	raw, err := parseWithArg(fs, args)
	if err != nil {
		return err
	}
	if raw == "" {
		return errors.New("show: missing id")
	}
	id, err := parseID(raw)
	if err != nil {
		return fmt.Errorf("show: %w", err)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	l, err := s.Get(id)
	if err != nil {
		return fmt.Errorf("show #%d: %w", id, err)
	}
	c, err := s.GetContent(id)
	if errors.Is(err, store.ErrNoContent) {
		return fmt.Errorf("show #%d: %w - run 'liki fetch %d'", id, err, id)
	}
	if err != nil {
		return err
	}

	if *asHTML {
		fmt.Fprintln(out, c.HTML)
		return nil
	}
	if c.Title != "" {
		fmt.Fprintln(out, c.Title)
	}
	printLink(out, l)
	fmt.Fprintf(out, "     fetched %s\n\n", c.FetchedAt.Format("2006-01-02 15:04"))
	fmt.Fprintln(out, c.Text)
	return nil
}

func summary(a fetch.Article) string {
	s := fmt.Sprintf("%d words, snapshot %.1f MB", len(strings.Fields(a.Text)), float64(len(a.Snapshot))/(1<<20))
	if a.RenderErr != nil {
		s += ", without browser"
	}
	return s
}
