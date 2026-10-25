package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"

	"liki/internal/search"
	"liki/internal/store"
	"liki/internal/theme"
)

const (
	barCells = 10
	// Similarities between these bounds fill the bar from empty to full;
	// bge-m3 rarely scores unrelated text above barLow.
	barLow, barHigh = 0.3, 0.8
)

// printResults prints one line per link: its id, URL (host highlighted) and
// the scores in a column on the right edge of the terminal, or right after
// the longest URL when the width is unknown.
func printResults(out io.Writer, results []search.Result) {
	noun := "results"
	if len(results) == 1 {
		noun = "result"
	}
	fmt.Fprintf(out, "%s %s\n", theme.Strong.Render(fmt.Sprint(len(results))), theme.Dim.Render(noun))

	wordsWidth := 0
	for _, r := range results {
		wordsWidth = max(wordsWidth, len(wordsText(r)))
	}
	links := make([]store.Link, len(results))
	scores := make([]string, len(results))
	for i, r := range results {
		links[i], scores[i] = r.Link, scoreColumn(r, wordsWidth)
	}
	printRows(out, links, scores)
}

// listPage describes which part of all matching links a list shows.
type listPage struct {
	first, total int // 1-based position of the first shown link, all matching links
	page, pages  int
	next         string // command that shows the next page
}

// maxTagsWidth keeps long tag lists from squeezing the URLs.
const maxTagsWidth = 30

// printLinks prints a page of links like search results, with tags and the
// date added on the right, between a "1–20 of 134 links" header and a footer
// pointing to the next page.
func printLinks(out io.Writer, links []store.Link, p listPage) {
	fmt.Fprintf(out, "%s %s\n",
		theme.Strong.Render(fmt.Sprintf("%d–%d", p.first, p.first+len(links)-1)),
		theme.Dim.Render(fmt.Sprintf("of %d links", p.total)))

	tags := make([]string, len(links))
	tagsWidth := 0
	for i, l := range links {
		tags[i] = truncate(strings.Join(l.Tags, ", "), maxTagsWidth)
		tagsWidth = max(tagsWidth, lipgloss.Width(tags[i]))
	}
	right := make([]string, len(links))
	for i, l := range links {
		right[i] = theme.Accent.Render(tags[i]) + strings.Repeat(" ", tagsWidth-lipgloss.Width(tags[i])) +
			"   " + theme.Dim.Render(l.AddedAt.Format("2006-01-02"))
	}
	printRows(out, links, right)

	switch {
	case p.page < p.pages:
		fmt.Fprintf(out, "%s %s\n", theme.Dim.Render(fmt.Sprintf("page %d/%d · next:", p.page, p.pages)), theme.Accent.Render(p.next))
	case p.pages > 1:
		fmt.Fprintln(out, theme.Dim.Render(fmt.Sprintf("page %d/%d", p.page, p.pages)))
	}
}

// printRows prints one line per link: its id, URL (host highlighted) and a
// right column, aligned to the right edge of the terminal, or right after
// the longest URL when the width is unknown.
func printRows(out io.Writer, links []store.Link, right []string) {
	rightWidth, urlWidth := 0, 0
	for i, l := range links {
		rightWidth = max(rightWidth, lipgloss.Width(right[i]))
		urlWidth = max(urlWidth, lipgloss.Width(displayURL(l.URL)))
	}
	const idWidth, gap = 5, 3 // "#123 " and space before the right column
	if w := outputWidth(out); w > 0 {
		urlWidth = max(10, w-idWidth-gap-rightWidth)
	}
	for i, l := range links {
		id := theme.Accent.Render(fmt.Sprintf("#%-3d", l.ID))
		u := truncate(displayURL(l.URL), urlWidth)
		pad := strings.Repeat(" ", urlWidth-lipgloss.Width(u)+gap)
		fmt.Fprintf(out, "%s %s%s%s\n", id, styleURL(u), pad, right[i])
	}
}

// interactive is implemented by outputs of liki's interactive mode, where
// commands are typed as /name instead of liki name.
type interactive interface{ Interactive() bool }

// commandHint returns how to type cmd (e.g. "list 2") where out is shown.
func commandHint(out io.Writer, cmd string) string {
	if i, ok := out.(interactive); ok && i.Interactive() {
		return "/" + cmd
	}
	return "liki " + cmd
}

// scoreColumn renders "meaning ━━━━━━──── 0.67   words 2/2" with fixed widths,
// so columns line up; the words part is padded to wordsWidth.
func scoreColumn(r search.Result, wordsWidth int) string {
	fill := (float64(r.Similarity) - barLow) / (barHigh - barLow)
	cells := int(math.Round(math.Max(0, math.Min(1, fill)) * barCells))

	valueStyle := theme.Dim
	switch {
	case r.Similarity >= 0.6:
		valueStyle = theme.Strong
	case r.Similarity >= 0.5:
		valueStyle = theme.Accent
	}
	s := theme.Dim.Render("meaning ") +
		theme.Accent.Render(strings.Repeat("━", cells)) +
		theme.Weak.Render(strings.Repeat("─", barCells-cells)) + " " +
		valueStyle.Render(fmt.Sprintf("%.2f", r.Similarity))

	if wordsWidth == 0 {
		return s
	}
	// Links matched by meaning only leave the column blank.
	words := wordsText(r)
	if words != "" {
		words = theme.Dim.Render("words ") + theme.Accent.Render(strings.TrimPrefix(words, "words "))
	}
	return s + "   " + words + strings.Repeat(" ", wordsWidth-len(wordsText(r)))
}

// wordsText returns "words 2/3", or "" when no query words matched.
func wordsText(r search.Result) string {
	if r.Words == 0 {
		return ""
	}
	return fmt.Sprintf("words %d/%d", r.Words, r.TotalWords)
}

// displayURL drops the scheme, which adds length but no information.
func displayURL(raw string) string {
	return strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
}

// styleURL highlights the host and dims the rest of the address.
func styleURL(u string) string {
	host, rest, _ := strings.Cut(u, "/")
	if rest != "" || strings.HasSuffix(u, "/") {
		rest = "/" + rest
	}
	return lipgloss.NewStyle().Foreground(theme.PaleBlue).Bold(true).Render(host) + theme.Dim.Render(rest)
}

// widther is implemented by outputs that know their width in columns,
// like the interactive mode.
type widther interface{ Width() int }

// outputWidth returns how many columns out can show, or 0 if unknown.
func outputWidth(out io.Writer) int {
	if w, ok := out.(widther); ok {
		return w.Width()
	}
	if f, ok := out.(*os.File); ok {
		if w, _, err := term.GetSize(f.Fd()); err == nil {
			// Writing up to the last column makes some terminals wrap.
			return w - 1
		}
	}
	return 0
}

// truncate shortens s to at most n characters, marking the cut with "…".
func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
