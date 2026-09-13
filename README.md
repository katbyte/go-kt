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
| [`chttp`](chttp) | an `http.Client` with trace logging, per-attempt timeouts, and retries that never re-send a mutation whose fate is unknown |
| [`version`](version) | the tool's version, stamped at build time or taken from module build info |
| [`pointer`](pointer) | `From` and `To` for optional API fields: dereference-or-zero, and pointer-to-value in expression position |

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

func run(quiet, verbose bool) {
    switch {
    case quiet:
        cout.Level = cout.VerbosityQuiet
    case verbose:
        cout.Level = cout.VerbosityVerbose
    }

    cout.Printf("<green>mytool</> %s\n", version.Version)
    cout.Verbosef("only with -v\n")
    cout.Quietf("%s\n", "the one line a script parses; printed in quiet mode and above")
    cout.Errorf("<red>error:</> %v\n", err) // stderr, every mode except silent

    client := chttp.NewHTTPClient("MyAPI") // logs every exchange at TRACE, retries 429/5xx
    req = chttp.MarkRetrySafe(req)         // a POST that is really a read may be retried on 5xx too
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
