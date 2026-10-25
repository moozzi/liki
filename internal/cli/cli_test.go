package cli

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// setup gives each test its own database. Ollama is unreachable, so commands
// only warn that the search index was not updated.
func setup(t *testing.T) {
	t.Helper()
	t.Setenv("LIKI_DB", filepath.Join(t.TempDir(), "liki.db"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_HOST", ln.Addr().String())
	ln.Close()
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// run runs a liki command line and returns its output without colors.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(context.Background(), args, &out)
	return ansi.ReplaceAllString(out.String(), ""), err
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := run(t, args...)
	if err != nil {
		t.Fatalf("liki %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func wantErr(t *testing.T, contains string, args ...string) {
	t.Helper()
	if _, err := run(t, args...); err == nil || !strings.Contains(err.Error(), contains) {
		t.Errorf("liki %s: err = %v, want it to mention %q", strings.Join(args, " "), err, contains)
	}
}

func TestAddRefusesDuplicates(t *testing.T) {
	setup(t)
	mustRun(t, "add", "https://go.dev/blog/maps", "-tags", "go", "-no-fetch")
	wantErr(t, "already saved as #1 https://go.dev/blog/maps", "add", "http://www.go.dev/blog/maps/?utm_source=x", "-no-fetch")
	if out := mustRun(t, "add", "-force", "-no-fetch", "http://www.go.dev/blog/maps/"); !strings.Contains(out, "added #2") {
		t.Errorf("add -force: %s", out)
	}
	wantErr(t, "invalid url", "add", "go.dev/blog/maps")
}

func TestFlagsAroundArgument(t *testing.T) {
	setup(t)
	// Flags before the URL used to stop flag parsing at it, dropping -tags.
	mustRun(t, "add", "-no-fetch", "https://a.example", "-tags", "go,maps")
	if out := mustRun(t, "list"); !strings.Contains(out, "go, maps") {
		t.Errorf("tags after the URL were dropped:\n%s", out)
	}
	if out := mustRun(t, "edit", "-url", "https://b.example", "1", "-tags", "x"); !strings.Contains(out, "https://b.example") || !strings.Contains(out, "[x]") {
		t.Errorf("edit with flags on both sides of the id:\n%s", out)
	}
	wantErr(t, `unexpected argument "https://c.example"`, "add", "https://b.example", "https://c.example")
	wantErr(t, "flag provided but not defined: -x", "related", "-n", "3", "1", "-x")
}

func TestEdit(t *testing.T) {
	setup(t)
	mustRun(t, "add", "https://a.example", "-tags", "x,y", "-no-fetch")
	mustRun(t, "add", "https://b.example", "-no-fetch")

	// The id may come after the flags.
	if out := mustRun(t, "edit", "-tags", "go, maps ,", "#1"); !strings.Contains(out, "[go, maps]") {
		t.Errorf("edit -tags: %s", out)
	}
	if out := mustRun(t, "edit", "1", "-tags", ""); strings.Contains(out, "[") {
		t.Errorf("edit -tags '' kept tags: %s", out)
	}
	wantErr(t, "#2 already has this url", "edit", "1", "-url", "https://www.b.example/")
	wantErr(t, "nothing to change", "edit", "1")
	wantErr(t, "link not found", "edit", "7", "-tags", "a")
}

func TestList(t *testing.T) {
	setup(t)
	if out := mustRun(t, "list"); !strings.Contains(out, "no links yet - add one with 'liki add <url>'") {
		t.Errorf("empty list: %s", out)
	}
	for _, u := range []string{"https://a.example", "https://b.example", "https://c.example"} {
		mustRun(t, "add", u, "-tags", "go", "-no-fetch")
	}
	mustRun(t, "add", "https://d.example", "-tags", "bread", "-no-fetch")

	out := mustRun(t, "list", "-tags", "go", "-n", "2")
	for _, want := range []string{"1–2 of 3 links", "#3", "#2", "page 1/2 · next: liki list 2 -tags go -n 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("first page lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "d.example") {
		t.Errorf("tag filter let through d.example:\n%s", out)
	}
	// The next-page command from the footer works as printed.
	out = mustRun(t, "list", "2", "-tags", "go", "-n", "2")
	if !strings.Contains(out, "3–3 of 3 links") || !strings.Contains(out, "a.example") || strings.Contains(out, "next:") {
		t.Errorf("last page:\n%s", out)
	}
	wantErr(t, "page 3 does not exist, there are 2", "list", "3", "-tags", "go", "-n", "2")
	if out := mustRun(t, "list", "-tags", "nope"); !strings.Contains(out, "no links match") {
		t.Errorf("unknown tag: %s", out)
	}
}

func TestRemoveChecksAllIDsFirst(t *testing.T) {
	setup(t)
	mustRun(t, "add", "https://a.example", "-no-fetch")
	wantErr(t, `invalid id "x"`, "rm", "1", "x")
	if out := mustRun(t, "list"); !strings.Contains(out, "a.example") {
		t.Errorf("link removed although the command failed:\n%s", out)
	}

	out, err := run(t, "rm", "1", "5")
	if err == nil || !strings.Contains(out, "removed #1") || !strings.Contains(out, "#5: link not found") {
		t.Errorf("rm 1 5: %v\n%s", err, out)
	}
}

func TestShowAndOpenWithoutContent(t *testing.T) {
	setup(t)
	mustRun(t, "add", "https://a.example", "-no-fetch")
	wantErr(t, "run 'liki fetch 1'", "show", "1")
	wantErr(t, "run 'liki fetch 1'", "open", "1")
	wantErr(t, "link not found", "open", "2")
}

func TestUnknownCommand(t *testing.T) {
	wantErr(t, `unknown command "frobnicate"`, "frobnicate")
}
