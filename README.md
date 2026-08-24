# extractors

A plugin framework for media extractors, in pure Go.

A *host* program asks a question about a URL — *what is at this page, and what
can be downloaded from it?* — and a *plugin* answers it. Plugins are standalone
executables, one per site, spoken to over
[hashicorp/go-plugin](https://github.com/hashicorp/go-plugin)'s `net/rpc` (gob)
transport. A plugin returns metadata and a list of downloadable formats; it
never writes a file. Downloading stays in the host, so a plugin only has to
understand its site, and a new site is a new binary rather than a new release of
the host.

The framework knows nothing about any particular website, and nothing about any
particular host program either: everything application-specific is derived from
one string, the *tool name*.

```go
host, err := extractor.NewHost("example", nil, os.Stderr, "info")
if err != nil {
	return err
}
inst, err := host.Resolve("https://site.example/videos/1")
if err != nil {
	return err
}
defer inst.Close()

m, err := inst.Extract(ctx, extractor.Request{URL: url, HTTP: httpConfig})
if err != nil {
	return err
}
best, err := m.Select("best")
```

## The tool name

A shared library must not decide that plugins are called `example-plugin-*` or
that their index lives under `example/`. It is told. `extractor.Tool` is that
name, and the four things a host would otherwise hardcode are derived from it:

| derived from `Tool("example")` |                                       |
| ------------------------------ | ------------------------------------- |
| binary prefix                  | `example-plugin-`                     |
| search-path variable           | `$EXAMPLE_PLUGIN_PATH`                |
| per-user install directory     | `<user config dir>/example/plugins`   |
| plugin index cache             | `<user cache dir>/example/plugins.json` |

A dash is legal in a file name and not in an environment variable, so it is
spelled as an underscore there and nowhere else; nothing else about the name is
transformed. Two hosts with different names therefore never see each other's
plugins or each other's index.

The name becomes a file name, a directory name and an environment variable name
on three operating systems, so it is restricted to ASCII letters, digits, `-`
and `_`. Anything else — a path separator, a dot segment, a space, a non-ASCII
rune — is refused with `extractor.ErrToolName`, which is also what keeps a name
such as `..` from reaching `filepath.Join`. **An empty name is refused too,
rather than defaulted**: a framework has no application name to fall back on,
and silently picking one would put a caller's plugins and index somewhere it
never asked for. `NewHost` returns that error; `Tool.Validate` reports it on its
own.

The handshake is deliberately *not* derived from the tool name. It identifies
the protocol, so a plugin is written against this framework and can be installed
under any host's prefix; the tool name decides where plugins live and how they
are named, never whether two processes can talk to each other.

## Writing a plugin

A plugin is any executable named `<tool>-plugin-<site>` that serves an
`extractor.Extractor`:

```go
package main

import (
	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/media"
)

type mysite struct{}

func (mysite) Info() (extractor.Info, error) {
	return extractor.Info{
		Name:         "mysite",
		Version:      "1.0.0",
		Description:  "one line about the site",
		Hosts:        []string{"site.example"},
		HostPatterns: []string{`^(?:[a-z0-9-]+\.)*site\.example$`},
	}, nil
}

func (mysite) Match(rawURL string) (extractor.Claim, error) { … }
func (mysite) Extract(req extractor.Request) (*media.Media, error) { … }
func (mysite) List(req extractor.Request) (*extractor.Playlist, error) { … }

func main() { extractor.Serve(mysite{}) }
```

Most of what a plugin needs already exists here. `scrape` fetches a page under
the host's HTTP policy, pulls a JSON blob out of a script tag, reads OpenGraph
tags and turns what a site says about a file into `media.Format` fields;
`jspacker` unpacks a packed player script. A new plugin should be the part that
is specific to its site, and nothing else.

Install it next to the host binary, in `plugins/` beside it, in
`<user config dir>/<tool>/plugins`, or point `$<TOOL>_PLUGIN_PATH` at its
directory. Discovery deduplicates by base name and the first directory wins, so
a local build shadows an installed one. On Windows, only a `.exe` is discovered.

A plugin declares a **priority**: the highest claimant wins, one that knows a
site sits at zero, and a generic catch-all declares a negative one so it is only
used when nothing better answers. It also declares the **hosts it serves**
(`Info.HostPatterns`, regular expressions matched against the URL's host name).
Those are remembered in an index under the user cache directory, so the URL
alone usually names the plugin to start; `Host.List` fills that index for every
plugin at once. When nothing matches — an unknown host, a rebuilt binary — every
plugin is asked, which is also how the index gets built. The chosen plugin still
answers `Match`: the index is a filter, never a decision. A plugin that fails to
start never prevents another from serving the request.

Errors cross the RPC boundary as text, so compare them with
`extractor.MatchError` rather than `errors.Is`.

## The packages

| package    | what it is                                                                                                                                            |
| ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `extractor`| The plugin contract — `Extractor`, `Info`, `Claim`, `Request`, `Playlist`, the error sentinels — plus the host that discovers, launches and talks to plugin processes, the gob transport, and the on-disk plugin index. |
| `media`    | The site-agnostic description of an extracted item: `Media` and `Format`, the quality ranking, the format selectors (`best`, `worst`, `best-http`, `720p`, a format id) and the output-name templates. No dependencies. |
| `scrape`   | The scraping toolkit plugins share: page fetch under the host's HTTP policy, balanced JSON-blob extraction, OpenGraph and `<title>` reading, byte sizes, ISO durations and quality labels. |
| `jspacker` | Undoes the packed-JS substitution some players use, so the player configuration can be read out of an obfuscated script. |

`extractor` and `scrape` build on
[`go-streamkit/streamkit/httpx`](https://github.com/go-streamkit/streamkit) for
the HTTP policy — user agent, cookies, proxy, retries, rate limit — which the
host passes down so a plugin inherits the user's settings.

## Building

Pure Go, `CGO_ENABLED=0`, go 1.26.4 or later.

```
go test ./...
```

CI gates every change on `gofmt`, `go vet`, `-race`, and **100 % statement
coverage** across Linux, macOS and Windows, plus the six supported 64-bit
targets (amd64 and arm64 natively; riscv64, loong64, ppc64le and s390x under
qemu).

## Licence

BSD-3-Clause. See [LICENSE](LICENSE).
