// Package cli implements liki subcommands.
package cli

import (
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
  liki add <url> [-title "..."] [-tags a,b]   save a link
  liki list                                  show saved links
  liki edit <id> [-url ...] [-title ...] [-tags a,b]
                                             change a link (only given fields;
                                             -tags replaces all tags)
  liki rm <id> [<id>...]                     remove links by id
  liki help                                  show this help

Database: ~/.liki/liki.db (override with LIKI_DB)
`

func Run(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	switch args[0] {
	case "add":
		return runAdd(args[1:], out)
	case "list", "ls":
		return runList(out)
	case "edit":
		return runEdit(args[1:], out)
	case "rm", "remove", "delete":
		return runRemove(args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q (see 'liki help')", args[0])
	}
}

func openStore() (*store.Store, error) {
	path, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	return store.Open(path)
}

func runAdd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	title := fs.String("title", "", "link title")
	tags := fs.String("tags", "", "comma-separated tags")

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
	l, err := s.Add(u, *title, splitTags(*tags))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "added #%d %s\n", l.ID, l.URL)
	return nil
}

func runList(out io.Writer) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	links, err := s.List()
	if err != nil {
		return err
	}
	if len(links) == 0 {
		fmt.Fprintln(out, "no links yet - add one with 'liki add <url>'")
		return nil
	}
	for _, l := range links {
		printLink(out, l)
	}
	return nil
}

func runEdit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	newURL := fs.String("url", "", "new url")
	title := fs.String("title", "", "new title (empty clears it)")
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

	// Only flags given explicitly are changed, so "-title ''" can clear a title.
	var p store.Patch
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "url":
			p.URL = newURL
		case "title":
			p.Title = title
		case "tags":
			t := splitTags(*tags)
			p.Tags = &t
		}
	})
	if p.URL == nil && p.Title == nil && p.Tags == nil {
		return errors.New("edit: nothing to change (use -url, -title or -tags)")
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
	l, err := s.Update(id, p)
	if err != nil {
		return fmt.Errorf("edit #%d: %w", id, err)
	}
	fmt.Fprintln(out, "updated:")
	printLink(out, l)
	return nil
}

func printLink(out io.Writer, l store.Link) {
	fmt.Fprintf(out, "#%-3d %s\n", l.ID, l.URL)
	if l.Title != "" {
		fmt.Fprintf(out, "     %s\n", l.Title)
	}
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

func runRemove(args []string, out io.Writer) error {
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
