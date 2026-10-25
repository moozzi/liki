<img src="logo.svg" alt="liki logo" width="160">

**Your local link wiki.** liki saves links to articles you want to remember, downloads their content and a full offline copy of each page, and lets you find them later by meaning, not just by exact title, tag or URL. Type `enum postgres` and you get every saved article about Postgres enums.

Everything stays on your machine: links, article text and page snapshots live in a single SQLite file, and search runs on a local embedding model.

## Dependencies

- **[Go](https://go.dev/dl/) 1.27.3 or newer** to build liki.
- **[Ollama](https://ollama.com)** with the **`bge-m3`** embedding model, for search. It understands English and Polish (and many other languages), so a Polish query finds English articles. Everything except search works without it.

  ```sh
  brew install ollama
  brew services start ollama
  ollama pull bge-m3
  ```

Go libraries (fetched automatically by `go build`):

| Library | Used for |
|---|---|
| [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) | database, pure Go (no cgo), with FTS5 full-text search |
| [codeberg.org/readeck/go-readability](https://codeberg.org/readeck/go-readability) | extracting readable article text from pages |
| [golang.org/x/net/html](https://pkg.go.dev/golang.org/x/net/html) | building self-contained page snapshots |
| [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Bubbles](https://github.com/charmbracelet/bubbles), [Lip Gloss](https://github.com/charmbracelet/lipgloss) | interactive mode and styling |

## Install

```sh
git clone <repo-url> liki
cd liki
go build -o liki .
```

Move the `liki` binary somewhere on your `PATH`, or use `go install .`.

## Usage

liki works in two ways:

- **Interactive mode:** run `liki` with no arguments. Type any text to search, or `/` to pick a command. Tab completes, ↑↓ browses history, ctrl+c cancels a running command or exits.
- **One-off commands:** `liki <command> ...`, handy in scripts. In interactive mode the same commands are typed as `/command`.

```
$ liki

  █  ▀ █▄▀ ▀   your local link wiki
  █▄ █ █ █ █   47 links · 7 tags · last added 4h ago

> enum postgres
  ⎿  3 results
     #10  www.crunchydata.com/blog/enums-vs-check-constraints-in-postgres   meaning ━━━━━━━─── 0.67   words 2/2
     #9   www.postgresql.org/docs/current/datatype-enum.html                meaning ━━━━━━──── 0.60   words 1/2
     #12  en.wikipedia.org/wiki/PostgreSQL                                  meaning ━━━━────── 0.52   words 2/2
```

### Commands

| Command | What it does |
|---|---|
| `add <url> [-title ...] [-tags a,b] [-no-fetch]` | Save a link. Downloads the article text and an offline snapshot right away unless `-no-fetch` is given. |
| `search <words...> [-n 10]` | Find links by meaning and keywords in their title, tags, URL and article text. In interactive mode, just type the words. |
| `list [page] [-n 20] [-tags a,b]` | Show saved links, newest first, one page at a time. `-tags` keeps links with any of the tags. |
| `show <id> [-html]` | Print the saved article text (or its cleaned-up HTML). |
| `open <id> [-o file.html]` | Open the saved page, with its original styles and images, in the browser. Works offline. `-o` saves it to a file instead. |
| `edit <id> [-url ...] [-title ...] [-tags a,b]` | Change a link. Only the given fields change; `-tags` replaces all tags, `-title ''` clears the title. |
| `rm <id> [<id>...]` | Remove links with their content. |
| `fetch [-all] [<id>...]` | Download content for links missing it, the given links, or all of them with `-all`. |
| `index [-all]` | Build the search index for links missing it, or rebuild it with `-all`. Usually not needed: liki indexes links when they are added, changed or searched. |

Interactive mode also has `/help`, `/clear` and `/exit`.

### Examples

```sh
# Save articles, with tags
liki add https://www.postgresql.org/docs/current/datatype-enum.html -tags postgres,sql
liki add https://go.dev/blog/maps -tags go

# Find them later without remembering titles or tags
liki search enum postgres
liki search "how do maps work in go"
liki search typ wyliczeniowy w bazie danych   # Polish query, English articles

# Browse
liki list                    # newest 20 links
liki list 2 -n 50            # second page of 50
liki list -tags go,postgres  # links tagged go or postgres

# Read
liki show 3                  # article text in the terminal
liki open 3                  # original page in the browser, offline

# Maintain
liki edit 3 -title "Postgres enums" -tags postgres,enum
liki rm 3 4
liki fetch                   # fetch content that failed before
```

## How search works

Each article is split into pieces of about 1200 characters, and each piece is stored twice:

- as an **embedding vector** from `bge-m3`, which matches by meaning ("enumerated type" finds "enum", a Polish query finds English text);
- in an **SQLite FTS5** full-text index, which matches exact words and names like `pg_enum`.

A search ranks links both ways and merges the two rankings (reciprocal rank fusion). Results show how well each link matched:

- **meaning**: similarity of the best matching piece, 0 to 1. Links matching only by meaning below 0.45 are hidden as noise.
- **words**: how many of the query words the link contains. A link needs at least half of them to count as a keyword match.

Searching compares stored vectors only; article text is never re-read at search time.

## Configuration

| Variable | Default | |
|---|---|---|
| `LIKI_DB` | `~/.liki/liki.db` | database file |
| `OLLAMA_HOST` | `localhost:11434` | Ollama server |
| `LIKI_EMBED_MODEL` | `bge-m3` | embedding model; changing it re-indexes links on the next search |

Interactive mode keeps its input history in `history` next to the database.

## Limitations

- Pages rendered entirely by JavaScript (some single-page apps) may save little or no content. Paywalled pages and PDFs are not supported.
- Snapshots don't include content a page loads with scripts after it opens, and embedded iframes (like videos) work only online.
- Keyword search stems English words only.
