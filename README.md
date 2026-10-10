# go-kt

[![GitHub release](https://img.shields.io/github/v/release/katbyte/go-kt?color=blueviolet)](https://github.com/katbyte/go-kt/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/go-kt?color=00ADD8)](https://github.com/katbyte/go-kt/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/go-kt?color=blue)](https://github.com/katbyte/go-kt/blob/main/LICENSE)
![build](https://github.com/katbyte/go-kt/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/go-kt/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/go-kt/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/go-kt/badges/coverage.json)](https://github.com/katbyte/go-kt/actions/workflows/coverage.yaml)

The small packages every katbyte command-line tool needs, kept in one place instead of copy-pasted into each repo.

| package | what it is for |
|---|---|
| [`clog`](clog) | the shared logrus logger: stderr, timestamps, level from the tool's own env var |
| [`cout`](cout) | verbosity-levelled, coloured console output (`--quiet`, `--verbose`, `--silent`) |
| [`chttp`](chttp) | an `http.Client` with per-attempt timeouts, retries that never re-send a mutation whose fate is unknown, and a trace of every exchange for the logger it is handed; and what an API client needs around one: an answer read whole, credentials kept out of errors, traces and other hosts, and one error for an unexpected status. Standard library only, so an SDK can be built on it |
| [`version`](version) | the tool's version, stamped at build time or taken from module build info |
| [`pointer`](pointer) | `From` and `To` for optional API fields: dereference-or-zero, and pointer-to-value in expression position |
| [`spelling`](spelling) | one name spelled two ways: a key that folds case, accents, separators and punctuation, and the tests for a slip of the keyboard, a name cut short or a first name reduced to its initial |
| [`whitespace`](whitespace) | the spaces out of place in a name - doubled, at an end, odd, before a colon or a file extension - named, made visible and put right |
| [`parallel`](parallel) | a batch of jobs run a few at a time, stopped at the first error |
| [`lock`](lock) | a lock on a record, a named piece of one or a plain string, any mix in one call, so two edits of one thing in one process take turns; one set for the process, or a set of your own |
| [`mcp/server`](mcp/server) | an MCP server served over stdio, or over HTTP behind a bearer token with a health probe |
| [`mcp/registry`](mcp/registry) | which tools an MCP session gets: read, write and delete kinds, toolsets, an allow list that adds tools by name and a deny list that takes them out |
| [`mcp/acctest`](mcp/acctest) | a test suite that drives an MCP server as a client does, and fails when a tool was never seen to work |
| [`test/replayproxy`](test/replayproxy) | what a server under test fetches from the internet, recorded once and replayed in CI |
| [`test/env`](test/env) | a live suite's surroundings: its environment variables, its replay proxy, and files laid out where the server's container reads them |

## Usage

```go
import (
    "github.com/katbyte/go-kt/clog"
    "github.com/katbyte/go-kt/cout"
    "github.com/katbyte/go-kt/chttp"
    "github.com/katbyte/go-kt/version"
)

func init() {
    clog.SetLevelFromEnv("MYTOOL_LOG") // "debug", "trace", ... default WARN
}

func run(silent, quiet, verbose bool) {
    cout.SetLevelFromFlags(silent, quiet, verbose) // or embed cout.Flags in your flag struct and call Apply()

    cout.Printf("<green>mytool</> %s\n", version.Version)
    cout.Verbosef("only with -v\n")
    cout.Quietf("%s\n", "the one line a script parses; printed in quiet mode and above")
    cout.Errorf("<red>error:</> %v\n", err) // stderr, every mode except silent

    client := chttp.New(chttp.Options{Name: "MyAPI", Log: clog.Log}) // traces every exchange at TRACE, retries a 429, a 502-504 and a dropped connection
    req = chttp.MarkRetrySafe(req)                                    // a POST that is really a read may be sent again too
    resp, body, err := client.Fetch(req, 16<<20)                      // the whole answer, asked for again if it stops part way
}
```

### Stamping the version

Point the linker at this module's variables instead of a local `lib/version`:

```yaml
# .goreleaser.yaml
ldflags:
  - -X github.com/katbyte/go-kt/version.Version={{ .Tag }}
  - -X github.com/katbyte/go-kt/version.GitCommit={{ .ShortCommit }}
```

Tools installed with `go install ...@version` report the module version from build info; plain local builds report `dev`.

## Development

```bash
make tools      # build the pinned dev tools into .tools/bin
make check-all  # build + test + lint + actionlint + yamllint + shellcheck + typos + depscheck
make cover      # tests with coverage; the coverage workflow publishes the total as the README badge
```

Nothing is vendored (consumers resolve dependencies through go.sum); tool versions are pinned in `.tools/go.mod` and dependabot keeps both current.
