// Package cli implements liki subcommands.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"

	"liki/internal/store"
)

const usage = `liki - local wiki for links

Usage:
  liki add <url> [-title "..."] [-tags a,b]   save a link
  liki list                                  show saved links
  liki help                                  show this help

Data file: ~/.liki/links.json (override with LIKI_DB)
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
	l := s.Add(u.String(), *title, splitTags(*tags))
	if err := s.Save(); err != nil {
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
	if len(s.Links) == 0 {
		fmt.Fprintln(out, "no links yet - add one with 'liki add <url>'")
		return nil
	}
	for _, l := range s.Links {
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

func splitTags(s string) []string {
	var tags []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}
