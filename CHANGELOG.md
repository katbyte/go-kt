## Unreleased

- add `parallel`: runs a batch of jobs a few at a time and stops at the first error; extracted from abs-mcp and embyfin-mcp
- add `lock`: makes two edits of one record in one process take turns; extracted from abs-mcp and embyfin-mcp
- add `mcp/server`: serves an MCP server over stdio, or over HTTP behind a bearer token; extracted from the five MCP servers, with abs-mcp's fixes for sessions left open and a slow stop
- add `mcp/registry`: decides which tools an MCP session gets and what each tells a client; extracted from the five MCP servers
- chttp: a connection a Mac refuses with "no route to host" says what the system may be doing about it
- chttp: add what an API client needs around a request: credentials kept out of error messages and off other hosts on a redirect, and one error for an unexpected status

## v0.3.0 (2026-10-09)

- add `spelling`: says when two names are one thing spelled two ways, a typing slip apart, or one cut short; extracted from embyfin-mcp and abs-mcp
- add `whitespace`: finds the spaces out of place in a name or a file name, shows them, and gives the name put right; extracted from embyfin-mcp

## v0.2.0 (2026-09-13)

- add `pointer`: an optional value or its zero, and a pointer to a value; extracted from ghp-sync
- cout: add `Flags`, the silent, quiet and verbose flags as one set a tool embeds, replacing the switch every tool carried
- cout: add a JSON level between silent and quiet, for tools that print a JSON document

## v0.1.0 (2026-09-13)

- initial release: `clog`, `cout`, `chttp`, and `version`, extracted from the copies in tctest, koi, ghp-sync, tf-provider-profile, and friends
