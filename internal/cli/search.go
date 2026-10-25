package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"liki/internal/embed"
	"liki/internal/search"
	"liki/internal/store"
)

func runSearch(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	n := fs.Int("n", 10, "maximum number of results")
	words, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(words, " "))
	if query == "" {
		return errors.New("search: missing query")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := embed.New()

	// Links added or changed since the last search are indexed first.
	if count, err := indexPending(ctx, s, c, out); err != nil {
		return err
	} else if count > 0 {
		fmt.Fprintf(out, "indexed %d new links\n\n", count)
	}

	results, err := search.Search(ctx, s, c, query, *n)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintln(out, "no links yet - add one with 'liki add <url>'")
		return nil
	}
	for _, r := range results {
		fmt.Fprintf(out, "%.2f ", r.Score)
		printLink(out, r.Link)
		fmt.Fprintf(out, "          %s\n\n", snippet(r.Snippet, 160))
	}
	return nil
}

func runIndex(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	all := fs.Bool("all", false, "rebuild the index of every link")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := embed.New()

	links, err := s.Unindexed(c.Model)
	if *all {
		links, err = s.List()
	}
	if err != nil {
		return err
	}
	if len(links) == 0 {
		fmt.Fprintln(out, "index is up to date (use -all to rebuild)")
		return nil
	}
	for i, l := range links {
		if err := search.Index(ctx, s, c, l); err != nil {
			return fmt.Errorf("index #%d: %w", l.ID, err)
		}
		fmt.Fprintf(out, "[%d/%d] indexed #%d %s\n", i+1, len(links), l.ID, l.URL)
	}
	return nil
}

// indexPending indexes every link that has no index for the current model
// and returns how many were indexed.
func indexPending(ctx context.Context, s *store.Store, c *embed.Client, out io.Writer) (int, error) {
	links, err := s.Unindexed(c.Model)
	if err != nil {
		return 0, err
	}
	if len(links) > 1 {
		fmt.Fprintf(out, "indexing %d links for search...\n", len(links))
	}
	for i, l := range links {
		if err := search.Index(ctx, s, c, l); err != nil {
			return i, fmt.Errorf("index #%d: %w", l.ID, err)
		}
	}
	return len(links), nil
}

// indexAfterChange keeps the search index fresh after add, edit or fetch.
// A failure is only a warning: the link is saved and is indexed on the next search.
func indexAfterChange(s *store.Store, out io.Writer) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if _, err := indexPending(ctx, s, embed.New(), out); err != nil {
		fmt.Fprintf(out, "warning: search index not updated: %v\n", err)
	}
}

// parseInterspersed parses fs allowing flags between positional arguments,
// which it returns.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// snippet returns text on one line, shortened to about max characters.
func snippet(text string, max int) string {
	s := strings.Join(strings.Fields(text), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}
