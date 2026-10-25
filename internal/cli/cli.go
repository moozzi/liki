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

	// Allow the URL before or after flags.
	var raw string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		raw, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if raw == "" && fs.NArg() > 0 {
		raw = fs.Arg(0)
	}
	if raw == "" {
		return errors.New("add: missing url")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("add: invalid url %q", raw)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	l, err := s.Add(u.String(), *title, splitTags(*tags))
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
		fmt.Fprintf(out, "#%-3d %s\n", l.ID, l.URL)
		if l.Title != "" {
			fmt.Fprintf(out, "     %s\n", l.Title)
		}
		if len(l.Tags) > 0 {
			fmt.Fprintf(out, "     [%s]\n", strings.Join(l.Tags, ", "))
		}
	}
	return nil
}

func runRemove(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("rm: missing id")
	}
	// Validate all ids before touching the database.
	ids := make([]int64, 0, len(args))
	for _, a := range args {
		id, err := strconv.ParseInt(strings.TrimPrefix(a, "#"), 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("rm: invalid id %q", a)
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
