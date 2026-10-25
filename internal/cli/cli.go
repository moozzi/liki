// Package cli implements liki subcommands.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"liki/internal/store"
)

const usage = `liki - local wiki for links

Usage:
  liki add <url> [-tags a,b] [-no-fetch] [-force]
                                             save a link and download its content;
                                             -force saves a link already saved
  liki list [page] [-n 20] [-tags a,b]       show saved links, newest first, a page
                                             at a time; -tags keeps links with any
                                             of the given tags
  liki edit <id> [-url ...] [-tags a,b]     change a link (only given fields;
                                             -tags replaces all tags)
  liki rm <id> [<id>...]                     remove links by id
  liki fetch [-all] [<id>...]                download article content and page
                                             snapshot; with no ids fetches links
                                             missing them (-all: re-fetch everything)
  liki show <id> [-html]                     read saved article text
  liki open <id> [-o file.html]              open the saved page, with its original
                                             styles, in the browser (offline)
  liki search <words...> [-n 10]             find links by meaning and keywords in
                                             tags, URL and article text
  liki related <id> [-n 10]                  find links on similar topics
  liki index [-all]                          build the search index for links
                                             missing it (-all: rebuild everything)
  liki help                                  show this help

Database: ~/.liki/liki.db (override with LIKI_DB)
Search uses a local Ollama server (OLLAMA_HOST, default localhost:11434)
with the embedding model from LIKI_EMBED_MODEL (default bge-m3).
`

// Command describes a subcommand, for help and completion in interactive mode.
type Command struct {
	Name    string
	Args    string // argument synopsis
	Summary string
}

var Commands = []Command{
	{"search", "<words...> [-n 10]", "find links by meaning and keywords"},
	{"add", "<url> [-tags a,b] [-no-fetch] [-force]", "save a link and download its content"},
	{"list", "[page] [-n 20] [-tags a,b]", "show saved links, newest first, a page at a time"},
	{"related", "<id> [-n 10]", "find links on similar topics"},
	{"show", "<id> [-html]", "read saved article text"},
	{"open", "<id> [-o file.html]", "open the saved page, with original styles, in the browser"},
	{"edit", "<id> [-url ...] [-tags a,b]", "change a link (-tags replaces all tags)"},
	{"rm", "<id> [<id>...]", "remove links"},
	{"fetch", "[-all] [<id>...]", "download content of links missing it"},
	{"index", "[-all]", "build the search index for links missing it"},
}

// Run executes the subcommand in args[0], writing its output to out.
// Long-running commands stop when ctx is cancelled.
func Run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	err := dispatch(ctx, args, out)
	// "-h" already printed the flag usage.
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func dispatch(ctx context.Context, args []string, out io.Writer) error {
	switch args[0] {
	case "add":
		return runAdd(ctx, args[1:], out)
	case "list", "ls":
		return runList(ctx, args[1:], out)
	case "edit":
		return runEdit(ctx, args[1:], out)
	case "rm", "remove", "delete":
		return runRemove(ctx, args[1:], out)
	case "fetch":
		return runFetch(ctx, args[1:], out)
	case "show", "read":
		return runShow(ctx, args[1:], out)
	case "open":
		return runOpen(ctx, args[1:], out)
	case "search", "find", "s":
		return runSearch(ctx, args[1:], out)
	case "related", "similar":
		return runRelated(ctx, args[1:], out)
	case "index":
		return runIndex(ctx, args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q (see 'liki help')", args[0])
	}
}

// newFlagSet returns a flag set that reports problems to out instead of stderr.
func newFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}

func openStore() (*store.Store, error) {
	path, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	return store.Open(path)
}

func runAdd(ctx context.Context, args []string, out io.Writer) error {
	fs := newFlagSet("add", out)
	tags := fs.String("tags", "", "comma-separated tags")
	noFetch := fs.Bool("no-fetch", false, "save only the link, without downloading its content")
	force := fs.Bool("force", false, "save the link even if it is already saved")

	raw, err := parseWithArg(fs, args)
	if err != nil {
		return err
	}
	if raw == "" {
		return errors.New("add: missing url")
	}
	u, err := parseURL(raw)
	if err != nil {
		return fmt.Errorf("add: %w", err)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if !*force {
		dups, err := s.FindURL(u)
		if err != nil {
			return err
		}
		if len(dups) > 0 {
			d := dups[0]
			return fmt.Errorf("add: already saved as #%d %s (use -force to save it again, or '%s' to change its tags)",
				d.ID, d.URL, commandHint(out, fmt.Sprintf("edit %d -tags ...", d.ID)))
		}
	}
	l, err := s.Add(u, splitTags(*tags))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "added #%d %s\n", l.ID, l.URL)
	if !*noFetch {
		// The link is already saved, so a failed download is only a warning.
		if err := fetchOne(ctx, s, l, out); err != nil {
			fmt.Fprintf(out, "warning: could not fetch content: %v (retry with 'liki fetch %d')\n", err, l.ID)
		}
	}
	indexAfterChange(ctx, s, out)
	return nil
}

func runList(ctx context.Context, args []string, out io.Writer) error {
	fs := newFlagSet("list", out)
	tags := fs.String("tags", "", "comma-separated tags; shows links with any of them")
	size := fs.Int("n", 20, "links per page")
	rawPage, err := parseWithArg(fs, args)
	if err != nil {
		return err
	}
	page := 1
	if rawPage != "" {
		if page, err = strconv.Atoi(rawPage); err != nil || page < 1 {
			return fmt.Errorf("list: invalid page %q", rawPage)
		}
	}
	if *size < 1 {
		return errors.New("list: -n must be at least 1")
	}
	filter := splitTags(*tags)

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	links, total, err := s.Page(filter, (page-1)*(*size), *size)
	if err != nil {
		return err
	}
	switch {
	case total == 0 && len(filter) > 0:
		fmt.Fprintln(out, "no links match the given tags")
		return nil
	case total == 0:
		fmt.Fprintf(out, "no links yet - add one with '%s'\n", commandHint(out, "add <url>"))
		return nil
	}
	pages := (total + *size - 1) / *size
	if page > pages {
		return fmt.Errorf("list: page %d does not exist, there are %d", page, pages)
	}

	// The command for the next page repeats the options of this one.
	next := fmt.Sprintf("list %d", page+1)
	if *tags != "" {
		next += " -tags " + strings.Join(filter, ",")
	}
	if *size != 20 {
		next += fmt.Sprintf(" -n %d", *size)
	}
	printLinks(out, links, listPage{
		first: (page-1)*(*size) + 1, total: total,
		page: page, pages: pages, next: commandHint(out, next),
	})
	return nil
}

func runEdit(ctx context.Context, args []string, out io.Writer) error {
	fs := newFlagSet("edit", out)
	newURL := fs.String("url", "", "new url")
	tags := fs.String("tags", "", "new comma-separated tags, replacing old ones (empty clears them)")

	rawID, err := parseWithArg(fs, args)
	if err != nil {
		return err
	}
	if rawID == "" {
		return errors.New("edit: missing id")
	}
	id, err := parseID(rawID)
	if err != nil {
		return fmt.Errorf("edit: %w", err)
	}

	// Only flags given explicitly are changed, so "-tags ''" can clear the tags.
	var p store.Patch
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "url":
			p.URL = newURL
		case "tags":
			t := splitTags(*tags)
			p.Tags = &t
		}
	})
	if p.URL == nil && p.Tags == nil {
		return errors.New("edit: nothing to change (use -url or -tags)")
	}
	if p.URL != nil {
		u, err := parseURL(*p.URL)
		if err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		p.URL = &u
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if p.URL != nil {
		dups, err := s.FindURL(*p.URL)
		if err != nil {
			return err
		}
		for _, d := range dups {
			if d.ID != id {
				return fmt.Errorf("edit #%d: #%d already has this url: %s", id, d.ID, d.URL)
			}
		}
	}
	l, err := s.Update(id, p)
	if err != nil {
		return fmt.Errorf("edit #%d: %w", id, err)
	}
	fmt.Fprintln(out, "updated:")
	printLink(out, l)
	indexAfterChange(ctx, s, out)
	return nil
}

func printLink(out io.Writer, l store.Link) {
	fmt.Fprintf(out, "#%-3d %s\n", l.ID, l.URL)
	if len(l.Tags) > 0 {
		fmt.Fprintf(out, "     [%s]\n", strings.Join(l.Tags, ", "))
	}
}

// parseWithArg parses fs and returns the single positional argument,
// which may come before or after the flags.
func parseWithArg(fs *flag.FlagSet, args []string) (string, error) {
	var arg string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		arg, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if arg == "" && fs.NArg() > 0 {
		arg = fs.Arg(0)
	}
	return arg, nil
}

func parseURL(raw string) (string, error) {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid url %q", raw)
	}
	return u.String(), nil
}

// parseID accepts "3" or "#3".
func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q", s)
	}
	return id, nil
}

func runRemove(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("rm: missing id")
	}
	// Validate all ids before touching the database.
	ids := make([]int64, 0, len(args))
	for _, a := range args {
		id, err := parseID(a)
		if err != nil {
			return fmt.Errorf("rm: %w", err)
		}
		ids = append(ids, id)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	var failed bool
	for _, id := range ids {
		if err := s.Delete(id); err != nil {
			fmt.Fprintf(out, "#%d: %v\n", id, err)
			failed = true
			continue
		}
		fmt.Fprintf(out, "removed #%d\n", id)
	}
	if failed {
		return errors.New("rm: some links were not removed")
	}
	return nil
}

func splitTags(s string) []string {
	var tags []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}
