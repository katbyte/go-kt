# go-kt

[![GitHub release](https://img.shields.io/github/v/release/katbyte/go-kt?color=blueviolet)](https://github.com/katbyte/go-kt/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/go-kt?color=00ADD8)](https://github.com/katbyte/go-kt/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/go-kt?color=blue)](https://github.com/katbyte/go-kt/blob/main/LICENSE)
![build](https://github.com/katbyte/go-kt/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/go-kt/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/go-kt/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/go-kt/badges/coverage.json)](https://github.com/katbyte/go-kt/actions/workflows/coverage.yaml)

The small packages every katbyte tool needs, kept in one place instead of copied into each repo.

| package | what it is for |
|---|---|
| [`clog`](clog) | the shared logger, with its level set from the tool's own environment variable |
| [`cout`](cout) | console output with colour and quiet, verbose and silent modes |
| [`chttp`](chttp) | an HTTP client that retries what is safe to retry and keeps credentials out of logs and errors |
| [`version`](version) | the tool's version, set at build time |
| [`pointer`](pointer) | the value behind a pointer, and a pointer to a value |
| [`spelling`](spelling) | tells whether two names are one name spelled two ways |
| [`whitespace`](whitespace) | finds and fixes stray spaces in a name |
| [`outside`](outside) | cleans text that came from outside before a model reads it |
| [`parallel`](parallel) | runs a batch of jobs a few at a time and stops at the first error |
| [`lock`](lock) | makes two edits of the same record take turns |
| [`mcp/server`](mcp/server) | runs an MCP server over stdio, or over HTTP behind a token |
| [`mcp/registry`](mcp/registry) | chooses which tools a session gets, logs each write, and keeps credentials out of logs and errors |
| [`mcp/acctest`](mcp/acctest) | tests an MCP server as a client uses it, and fails if a tool was never seen to work |
| [`prometheus/metrics`](prometheus/metrics) | a tool's Prometheus metrics page, with a health check |
| [`test/replayproxy`](test/replayproxy) | records what a server under test fetches from the internet, and replays it in CI |
| [`test/env`](test/env) | sets up a live test's surroundings: its variables, proxy and files |
| [`test/services/arrserver`](test/services/arrserver) | stands in for the servers Sonarr, Radarr and Prowlarr call home to |
| [`test/services/newznab`](test/services/newznab) | stand-in Newznab and Torznab indexers that a test controls |
| [`test/services/sabnzbd`](test/services/sabnzbd) | a stand-in SABnzbd that downloads nothing, whose jobs a test controls |

The detail for each is on [pkg.go.dev](https://pkg.go.dev/github.com/katbyte/go-kt).

## Install

```bash
go get github.com/katbyte/go-kt@latest
```

It needs Go 1.27 or newer.

## Usage

```go
import (
    "github.com/katbyte/go-kt/clog"
    "github.com/katbyte/go-kt/cout"
    "github.com/katbyte/go-kt/chttp"
    "github.com/katbyte/go-kt/version"
)

func init() {
    clog.SetLevelFromEnv("MYTOOL_LOG") // warnings and errors unless the variable says otherwise
}

func run(silent, quiet, verbose bool) {
    cout.SetLevelFromFlags(silent, quiet, verbose)

    cout.Printf("<green>mytool</> %s\n", version.Version)
    cout.Verbosef("only when verbose\n")
    cout.Quietf("still printed when quiet\n")
    cout.Errorf("<red>error:</> %v\n", err) // every mode but silent

    client := chttp.New(chttp.Options{Name: "MyAPI", Log: clog.Log})
    resp, body, err := client.Fetch(req, 16<<20) // the whole answer, up to 16 MB
}
```

### Stamping the version

Point the linker at this module's variables:

```yaml
# .goreleaser.yaml
ldflags:
  - -X github.com/katbyte/go-kt/version.Version={{ .Tag }}
  - -X github.com/katbyte/go-kt/version.GitCommit={{ .ShortCommit }}
```

A tool installed with `go install` reports its module version. A local build reports `dev`.

## Used by

- **MCP servers:** [abs-mcp](https://github.com/katbyte/abs-mcp), [dockhand-mcp](https://github.com/katbyte/dockhand-mcp), [embyfin-mcp](https://github.com/katbyte/embyfin-mcp), [prowlarr-mcp](https://github.com/katbyte/prowlarr-mcp), [radarr-mcp](https://github.com/katbyte/radarr-mcp), [sonarr-mcp](https://github.com/katbyte/sonarr-mcp), [technitium-mcp](https://github.com/katbyte/technitium-mcp), [truenas-mcp](https://github.com/katbyte/truenas-mcp)
- **Metrics:** [embyfin-metrics](https://github.com/katbyte/embyfin-metrics), [leontp-metrics](https://github.com/katbyte/leontp-metrics), [truenas-metrics](https://github.com/katbyte/truenas-metrics)
- **Libraries:** [pandorest](https://github.com/katbyte/pandorest)
- **Other tools:** [acorn-vfd](https://github.com/katbyte/acorn-vfd), [ghp-sync](https://github.com/katbyte/ghp-sync), [go-ingest-media](https://github.com/katbyte/go-ingest-media), [gogo-jira-stats](https://github.com/katbyte/gogo-jira-stats), [gogo-repo-stats](https://github.com/katbyte/gogo-repo-stats), [koi](https://github.com/katbyte/koi), [nanoleaf-aurora-taproot](https://github.com/katbyte/nanoleaf-aurora-taproot), [prawn](https://github.com/katbyte/prawn), [tf-provider-profile](https://github.com/katbyte/tf-provider-profile), [tf-schema-extract](https://github.com/katbyte/tf-schema-extract)

## Development

```bash
make tools      # install the pinned dev tools
make check-all  # build, test and every lint
make cover      # tests with coverage
```

Nothing is vendored. Tool versions are pinned in the makefile and `.tools/go.mod`.

How releases are cut is in [CONTRIBUTING.md](CONTRIBUTING.md).
